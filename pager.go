package md

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	enterAltScreen        = "\033[?1049h"
	exitAltScreen         = "\033[?1049l"
	hideCursor            = "\033[?25l"
	showCursor            = "\033[?25h"
	clearScreen           = "\033[2J"
	cursorHome            = "\033[H"
	enableFocusReporting  = "\033[?1004h"
	disableFocusReporting = "\033[?1004l"
	enableMouseReporting  = "\033[?1000h\033[?1006h"
	disableMouseReporting = "\033[?1006l\033[?1000l"
	queryBackgroundColor  = "\033]11;?\033\\"
)

type PagerConfig struct {
	Paths         []string
	InitialSource []byte
	Label         string
}

type pager struct {
	tty           *os.File
	cfg           PagerConfig
	width         int
	height        int
	source        []byte
	sourceModTime time.Time
	headings      []Heading
	lines         []string
	plainLines    []string
	topLine       int
	searchQuery   string
	searchMatches []int
	searchIndex   int
	promptActive  bool
	promptValue   string
	promptCursor  int
	notice        string
	noticeIsError bool
	theme         tintTheme
	outline       outlineState
}

type tintTheme struct {
	statusBG     string
	promptBG     string
	highlightBG  string
	blockquoteBG string
	markBG       string
}

type outlineState struct {
	active   bool
	filter   string
	cursor   int
	filtered []int
	selected int
	scroll   int
}

type inputEvent interface{}

type keyEvent struct {
	kind keyKind
	ch   rune
	alt  bool
}

type keyKind int

const (
	keyRune keyKind = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyPageUp
	keyPageDown
	keyHome
	keyEnd
	keyEscape
	keyEnter
	keyBackspace
	keyDelete
)

type focusEvent struct {
	gained bool
}

type bgColorEvent struct {
	color rgbColor
}

type inputErrorEvent struct {
	err error
}

type mouseEvent struct {
	kind mouseEventKind
	row  int
	col  int
}

type mouseEventKind int

const (
	mouseScrollUp mouseEventKind = iota
	mouseScrollDown
)

type rgbColor struct {
	r uint8
	g uint8
	b uint8
}

var timeNow = time.Now

func RunPager(cfg PagerConfig) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening tty: %w", err)
	}
	defer tty.Close()

	if !term.IsTerminal(int(tty.Fd())) {
		return errors.New("pager requires a terminal")
	}

	state, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return fmt.Errorf("enabling raw mode: %w", err)
	}
	defer term.Restore(int(tty.Fd()), state)

	p := &pager{
		tty:         tty,
		cfg:         cfg,
		searchIndex: -1,
	}
	if cfg.Label == "" {
		p.cfg.Label = pagerLabel(cfg.Paths)
	}

	width, height, err := term.GetSize(int(tty.Fd()))
	if err != nil {
		return fmt.Errorf("getting terminal size: %w", err)
	}
	p.resize(width, height)

	if err := p.reload(true); err != nil {
		return err
	}

	fmt.Fprint(tty, enterAltScreen, clearScreen, cursorHome, hideCursor, enableFocusReporting, enableMouseReporting)
	defer fmt.Fprint(tty, Reset, showCursor, disableMouseReporting, disableFocusReporting, exitAltScreen)

	events := make(chan inputEvent, 128)
	go readTerminalEvents(tty, events)

	reloadCh, watchErrCh, closeWatcher, err := watchFiles(cfg.Paths)
	if err != nil {
		return err
	}
	defer closeWatcher()

	resizeCh := make(chan os.Signal, 1)
	signal.Notify(resizeCh, syscall.SIGWINCH)
	defer signal.Stop(resizeCh)

	timeTicker := time.NewTicker(15 * time.Second)
	defer timeTicker.Stop()

	p.requestBackground()
	if err := p.draw(); err != nil {
		return err
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			quit, err := p.handleEvent(ev)
			if err != nil {
				return err
			}
			if quit {
				return nil
			}
		case <-reloadCh:
			_ = p.reload(false)
		case err, ok := <-watchErrCh:
			if ok && err != nil {
				p.setNotice(err.Error(), true)
			}
		case <-resizeCh:
			width, height, err := term.GetSize(int(tty.Fd()))
			if err == nil {
				p.resize(width, height)
				if err := p.rebuild(); err != nil {
					p.setNotice(err.Error(), true)
				}
			}
		case <-timeTicker.C:
		}

		if err := p.draw(); err != nil {
			return err
		}
	}
}

func (p *pager) handleEvent(ev inputEvent) (bool, error) {
	switch v := ev.(type) {
	case keyEvent:
		return p.handleKey(v), nil
	case focusEvent:
		if v.gained {
			p.requestBackground()
		}
	case bgColorEvent:
		nextTheme := deriveTintTheme(v.color)
		if p.theme != nextTheme {
			p.theme = nextTheme
			if err := p.rebuild(); err != nil {
				return false, err
			}
		}
	case mouseEvent:
		p.handleMouse(v)
	case inputErrorEvent:
		if v.err != nil && !errors.Is(v.err, io.EOF) {
			return false, v.err
		}
	}
	return false, nil
}

func (p *pager) handleMouse(ev mouseEvent) {
	if p.outline.active && p.mouseInOutline(ev.row, ev.col) {
		switch ev.kind {
		case mouseScrollUp:
			p.moveOutlineSelection(-1)
		case mouseScrollDown:
			p.moveOutlineSelection(1)
		}
		return
	}

	switch ev.kind {
	case mouseScrollUp:
		p.scrollBy(-3)
	case mouseScrollDown:
		p.scrollBy(3)
	}
}

func (p *pager) handleKey(ev keyEvent) bool {
	if p.outline.active {
		return p.handleOutlineKey(ev)
	}
	if p.promptActive {
		return p.handlePromptKey(ev)
	}

	switch {
	case ev.kind == keyEnter || ev.ch == 'j' || ev.ch == 14 || ev.kind == keyDown:
		p.scrollBy(1)
	case ev.ch == 'k' || ev.ch == 16 || ev.kind == keyUp:
		p.scrollBy(-1)
	case ev.ch == 'd':
		p.scrollBy(max(1, p.viewHeight()/2))
	case ev.ch == 'u':
		p.scrollBy(-max(1, p.viewHeight()/2))
	case ev.ch == ' ' || ev.ch == 'f' || ev.ch == 22 || ev.kind == keyPageDown:
		p.scrollBy(p.viewHeight())
	case ev.ch == 'b' || (ev.alt && unicode.ToLower(ev.ch) == 'v') || ev.kind == keyPageUp:
		p.scrollBy(-p.viewHeight())
	case ev.ch == 'g' || ev.kind == keyHome || (ev.alt && ev.ch == '<'):
		p.topLine = 0
	case ev.ch == 'G' || ev.kind == keyEnd || (ev.alt && ev.ch == '>'):
		p.topLine = p.maxTopLine()
	case ev.ch == '/':
		p.promptActive = true
		p.promptValue = p.searchQuery
		p.promptCursor = utf8.RuneCountInString(p.promptValue)
	case ev.ch == 18:
		p.openOutline()
	case ev.ch == 'n':
		p.searchNext()
	case ev.ch == 'N':
		p.searchPrev()
	case ev.ch == 'r' || ev.ch == 12:
		_ = p.reload(false)
	case ev.ch == 'q' || ev.ch == 3:
		return true
	}

	p.topLine = clamp(p.topLine, 0, p.maxTopLine())
	return false
}

func (p *pager) handleOutlineKey(ev keyEvent) bool {
	switch {
	case ev.kind == keyEscape || ev.ch == 18:
		p.closeOutline()
	case ev.kind == keyEnter:
		p.closeOutline()
	case ev.ch == 7:
		p.closeOutline()
	case ev.ch == 3:
		return true
	case ev.kind == keyDown || ev.ch == 14 || ev.ch == 'j':
		p.moveOutlineSelection(1)
	case ev.kind == keyUp || ev.ch == 16 || ev.ch == 'k':
		p.moveOutlineSelection(-1)
	case ev.kind == keyPageDown || ev.ch == 22:
		p.moveOutlineSelection(max(1, p.outlineListRows()-1))
	case ev.kind == keyPageUp || ev.ch == 'b' || (ev.alt && unicode.ToLower(ev.ch) == 'v'):
		p.moveOutlineSelection(-max(1, p.outlineListRows()-1))
	case ev.kind == keyHome || ev.ch == 'g' || ev.ch == 1:
		p.moveOutlineSelectionTo(0)
	case ev.kind == keyEnd || ev.ch == 'G' || ev.ch == 5:
		p.moveOutlineSelectionTo(len(p.outline.filtered) - 1)
	case ev.kind == keyBackspace || ev.ch == 8:
		p.deleteOutlineBeforeCursor()
	case ev.kind == keyDelete || ev.ch == 4:
		p.deleteOutlineAtCursor()
	case ev.kind == keyLeft || ev.ch == 2:
		p.moveOutlineCursor(-1)
	case ev.kind == keyRight || ev.ch == 6:
		p.moveOutlineCursor(1)
	case ev.ch == 21:
		p.killOutlineToStart()
	case ev.ch == 11:
		p.killOutlineToEnd()
	case ev.ch == 23:
		p.deleteOutlineBackwardWord()
	case ev.alt && (ev.ch == 'b' || ev.ch == 'B'):
		p.moveOutlineBackwardWord()
	case ev.alt && (ev.ch == 'f' || ev.ch == 'F'):
		p.moveOutlineForwardWord()
	case ev.kind == keyRune && unicode.IsPrint(ev.ch):
		p.insertOutlineRune(ev.ch)
	}
	return false
}

func (p *pager) handlePromptKey(ev keyEvent) bool {
	switch {
	case ev.kind == keyEnter:
		p.promptActive = false
		p.searchQuery = p.promptValue
		p.promptCursor = utf8.RuneCountInString(p.promptValue)
		p.refreshSearchState()
		if p.searchQuery == "" {
			p.clearNotice()
			return false
		}
		if !p.jumpToFirstMatchAtOrAfter(p.topLine) {
			p.setNotice(fmt.Sprintf("pattern not found: /%s", p.searchQuery), true)
		} else {
			p.clearNotice()
		}
	case ev.kind == keyBackspace || ev.ch == 8:
		p.deletePromptBeforeCursor()
	case ev.kind == keyDelete || ev.ch == 4:
		p.deletePromptAtCursor()
	case ev.kind == keyLeft || ev.ch == 2:
		p.movePromptCursor(-1)
	case ev.kind == keyRight || ev.ch == 6:
		p.movePromptCursor(1)
	case ev.kind == keyHome || ev.ch == 1:
		p.promptCursor = 0
	case ev.kind == keyEnd || ev.ch == 5:
		p.promptCursor = utf8.RuneCountInString(p.promptValue)
	case ev.ch == 21:
		p.killPromptToStart()
	case ev.ch == 11:
		p.killPromptToEnd()
	case ev.ch == 23:
		p.deletePromptBackwardWord()
	case ev.alt && (ev.ch == 'b' || ev.ch == 'B'):
		p.movePromptBackwardWord()
	case ev.alt && (ev.ch == 'f' || ev.ch == 'F'):
		p.movePromptForwardWord()
	case ev.ch == 7:
		p.promptActive = false
		p.promptValue = p.searchQuery
		p.promptCursor = utf8.RuneCountInString(p.promptValue)
	case ev.ch == 3:
		return true
	case ev.kind == keyRune && unicode.IsPrint(ev.ch):
		p.insertPromptRune(ev.ch)
	}
	return false
}

func (p *pager) requestBackground() {
	fmt.Fprint(p.tty, queryBackgroundColor)
}

func (p *pager) resize(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	p.width = width
	p.height = height
}

func (p *pager) reload(initial bool) error {
	source, modTime, err := p.loadSource()
	if err != nil {
		if initial {
			return err
		}
		p.setNotice(err.Error(), true)
		return nil
	}

	p.source = source
	p.sourceModTime = modTime
	if err := p.rebuild(); err != nil {
		if initial {
			return err
		}
		p.setNotice(err.Error(), true)
		return nil
	}

	if p.noticeIsError {
		p.clearNotice()
	}
	return nil
}

func (p *pager) loadSource() ([]byte, time.Time, error) {
	if len(p.cfg.Paths) == 0 {
		return append([]byte(nil), p.cfg.InitialSource...), time.Time{}, nil
	}

	var all []byte
	var latestModTime time.Time
	for _, path := range p.cfg.Paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("reading %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("stating %s: %w", path, err)
		}
		if info.ModTime().After(latestModTime) {
			latestModTime = info.ModTime()
		}
		all = append(all, data...)
	}
	return all, latestModTime, nil
}

func (p *pager) rebuild() error {
	result, err := RenderDocumentWithStyle(p.source, p.width, true, p.theme.renderStyle())
	if err != nil {
		return err
	}

	anchor := p.topLine
	if p.searchIndex >= 0 && p.searchIndex < len(p.searchMatches) {
		anchor = p.searchMatches[p.searchIndex]
	}

	p.headings = append([]Heading(nil), result.Headings...)
	text := strings.TrimSuffix(result.Output, "\n")
	if text == "" {
		p.lines = nil
		p.plainLines = nil
		p.headings = nil
		p.topLine = 0
		p.refreshSearchState()
		p.refreshOutline()
		return nil
	}

	p.lines = strings.Split(text, "\n")
	p.plainLines = make([]string, len(p.lines))
	for i, line := range p.lines {
		p.plainLines[i] = stripANSI(line)
	}

	p.topLine = clamp(p.topLine, 0, p.maxTopLine())
	p.refreshSearchAround(anchor)
	p.refreshOutline()
	return nil
}

func (p *pager) draw() error {
	var out strings.Builder
	viewHeight := p.viewHeight()

	for row := 0; row < viewHeight; row++ {
		out.WriteString(cursorTo(row+1, 1))
		out.WriteString("\033[2K")
		lineIdx := p.topLine + row
		if lineIdx < len(p.lines) {
			out.WriteString(p.renderLine(lineIdx))
		}
	}

	if p.outline.active {
		out.WriteString(p.drawOutline())
	}

	statusRow := max(1, p.height)
	out.WriteString(cursorTo(statusRow, 1))
	out.WriteString("\033[2K")
	switch {
	case p.promptActive:
		prompt, cursorCol := p.promptDisplay()
		out.WriteString(p.renderBar(prompt, true))
		out.WriteString(cursorTo(statusRow, max(1, cursorCol)))
		out.WriteString(showCursor)
	case p.outline.active:
		out.WriteString(hideCursor)
		out.WriteString(p.renderStatusBar())
		row, col := p.outlineCursorPosition()
		out.WriteString(cursorTo(row, col))
		out.WriteString(showCursor)
	default:
		out.WriteString(hideCursor)
		out.WriteString(p.renderStatusBar())
	}

	_, err := io.WriteString(p.tty, out.String())
	return err
}

func (p *pager) renderBar(text string, prompt bool) string {
	text = fitToWidth(text, p.width)
	padding := p.width - visibleWidth(text)
	if padding < 0 {
		padding = 0
	}
	bg := p.theme.statusBG
	if prompt {
		bg = p.theme.promptBG
	}
	if bg == "" {
		return text + strings.Repeat(" ", padding)
	}
	return bg + text + strings.Repeat(" ", padding) + Reset
}

func (p *pager) renderStatusBar() string {
	left := p.statusBarLeft()
	right := p.statusBarRight()

	if p.width <= 0 {
		return ""
	}

	right = fitToWidth(right, p.width)
	rightWidth := visibleWidth(right)
	if rightWidth >= p.width {
		return renderTintedBlock(right, p.theme.statusBG, p.width)
	}

	availableLeft := p.width - rightWidth
	if left != "" && right != "" {
		availableLeft -= 2
	}
	if availableLeft < 0 {
		availableLeft = 0
	}

	left = fitToWidth(left, availableLeft)
	leftWidth := visibleWidth(left)
	gapWidth := p.width - leftWidth - rightWidth
	if left != "" && right != "" && gapWidth >= 2 {
		left += strings.Repeat(" ", 2)
		gapWidth -= 2
	}

	return renderTintedBlock(left+strings.Repeat(" ", gapWidth)+right, p.theme.statusBG, p.width)
}

func (p *pager) drawOutline() string {
	if len(p.outline.filtered) == 0 {
		return ""
	}

	panelWidth := p.outlinePanelWidth()
	listRows := p.outlineListRows()
	panelHeight := listRows + 1
	panelTop := max(1, p.height-panelHeight)
	selectedPos := p.outlineSelectedPosition()
	p.outline.scroll = clamp(p.outline.scroll, 0, max(0, len(p.outline.filtered)-listRows))
	if selectedPos >= 0 {
		if selectedPos < p.outline.scroll {
			p.outline.scroll = selectedPos
		}
		if selectedPos >= p.outline.scroll+listRows {
			p.outline.scroll = selectedPos - listRows + 1
		}
	}

	var out strings.Builder
	bg := p.theme.statusBG
	promptBG := p.theme.promptBG
	if promptBG == "" {
		promptBG = bg
	}

	for row := 0; row < listRows; row++ {
		out.WriteString(cursorTo(panelTop+row, 1))
		idx := p.outline.scroll + row
		line := ""
		rowBG := bg
		if idx < len(p.outline.filtered) {
			heading := p.headings[p.outline.filtered[idx]]
			selected := idx == selectedPos
			line = p.renderOutlineEntry(heading, selected, panelWidth)
			if selected {
				rowBG = p.outlineSelectionBG()
			}
		}
		out.WriteString(renderTintedBlock(line, rowBG, panelWidth))
	}

	promptRow := panelTop + listRows
	out.WriteString(cursorTo(promptRow, 1))
	out.WriteString(renderTintedBlock(p.outlinePromptText(panelWidth), promptBG, panelWidth))
	return out.String()
}

func (p *pager) renderOutlineEntry(heading Heading, selected bool, width int) string {
	label := strings.Repeat("  ", max(0, heading.Level-1)) + heading.Text
	label = fitToWidth(label, width)
	if selected {
		if p.theme.highlightBG == "" {
			return Reverse + Bold + label + Reset
		}
		return Bold + label + Reset
	}
	return Bold + label + Reset
}

func (p *pager) outlineSelectionBG() string {
	if p.theme.highlightBG != "" {
		return p.theme.highlightBG
	}
	return ""
}

func renderTintedBlock(text, bg string, width int) string {
	if bg != "" {
		text = strings.ReplaceAll(text, Reset, Reset+bg)
	}
	padding := width - visibleWidth(text)
	if padding < 0 {
		padding = 0
	}
	if bg == "" {
		return text + strings.Repeat(" ", padding)
	}
	return bg + text + strings.Repeat(" ", padding) + Reset
}

func (p *pager) promptDisplay() (string, int) {
	if p.width <= 0 {
		return "", 1
	}

	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	available := p.width - 1
	if available <= 0 {
		return "/", 1
	}

	start := 0
	if cursor > available {
		start = cursor - available
	}
	end := start + available
	if end > len(value) {
		end = len(value)
		if end-available > 0 {
			start = end - available
		} else {
			start = 0
		}
	}

	display := "/" + string(value[start:end])
	return display, min(p.width, 2+cursor-start)
}

func (p *pager) outlinePromptText(width int) string {
	if width <= 0 {
		return ""
	}
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	prefix := "› "
	available := width - len([]rune(prefix))
	if available <= 0 {
		return fitToWidth(prefix, width)
	}

	start := 0
	if cursor > available {
		start = cursor - available
	}
	end := start + available
	if end > len(value) {
		end = len(value)
		if end-available > 0 {
			start = end - available
		} else {
			start = 0
		}
	}
	return prefix + string(value[start:end])
}

func (p *pager) outlineCursorPosition() (int, int) {
	panelWidth := p.outlinePanelWidth()
	listRows := p.outlineListRows()
	panelHeight := listRows + 1
	panelTop := max(1, p.height-panelHeight)
	prefixRunes := len([]rune("› "))
	cursor := clamp(p.outline.cursor, 0, utf8.RuneCountInString(p.outline.filter))
	available := panelWidth - prefixRunes
	start := 0
	if available > 0 && cursor > available {
		start = cursor - available
	}
	return panelTop + listRows, min(panelWidth, prefixRunes+1+cursor-start)
}

func (p *pager) renderLine(lineIdx int) string {
	line := p.lines[lineIdx]
	if p.searchQuery == "" {
		return line
	}
	return highlightSearchMatches(line, p.plainLines[lineIdx], p.searchQuery, p.highlightStart())
}

func (p *pager) highlightStart() string {
	if p.theme.highlightBG != "" {
		return p.theme.highlightBG + Bold
	}
	return Reverse + Bold
}

func (p *pager) statusBarLeft() string {
	parts := []string{p.statusSectionPath()}
	if p.searchQuery != "" {
		if len(p.searchMatches) == 0 {
			parts = append(parts, fmt.Sprintf("/%s 0", p.searchQuery))
		} else {
			current := p.searchIndex + 1
			if current < 1 {
				current = 1
			}
			parts = append(parts, fmt.Sprintf("/%s %d/%d", p.searchQuery, current, len(p.searchMatches)))
		}
	}
	if p.notice != "" {
		parts = append(parts, p.notice)
	}
	return strings.Join(parts, "  ")
}

func (p *pager) statusBarRight() string {
	var parts []string
	percent := 100
	if len(p.lines) == 0 {
		percent = 0
	} else if maxTop := p.maxTopLine(); maxTop > 0 {
		percent = int(math.Round(float64(p.topLine) / float64(maxTop) * 100))
	}
	if !p.sourceModTime.IsZero() {
		parts = append(parts, humanizeRelativeTime(p.sourceModTime, timeNow()))
	}
	parts = append(parts, fmt.Sprintf("%d%%", percent))
	return strings.Join(parts, "  ")
}

func (p *pager) statusSectionPath() string {
	path := p.currentHeadingPath()
	if len(path) == 0 {
		return p.cfg.Label
	}

	parts := make([]string, 0, len(path))
	for i, heading := range path {
		text := heading.Text
		if i == len(path)-1 {
			text = Bold + text + Reset
		}
		parts = append(parts, text)
	}
	return p.cfg.Label + ": " + strings.Join(parts, " › ")
}

func (p *pager) openOutline() {
	p.outline.active = true
	p.outline.filter = ""
	p.outline.cursor = 0
	p.outline.selected = -1
	p.outline.scroll = 0
	p.refreshOutline()
}

func (p *pager) closeOutline() {
	p.outline.active = false
	p.outline.filter = ""
	p.outline.cursor = 0
	p.outline.filtered = nil
	p.outline.selected = -1
	p.outline.scroll = 0
}

func (p *pager) setNotice(msg string, isError bool) {
	p.notice = msg
	p.noticeIsError = isError
}

func (p *pager) clearNotice() {
	p.notice = ""
	p.noticeIsError = false
}

func (p *pager) refreshSearchState() {
	p.refreshSearchAround(p.topLine)
}

func (p *pager) refreshOutline() {
	p.outline.filtered = p.matchingOutlineIndices(p.outline.filter, p.outline.filtered)
	if len(p.outline.filtered) == 0 {
		p.outline.selected = -1
		p.outline.scroll = 0
		return
	}

	current := p.currentHeadingIndex()
	if containsInt(p.outline.filtered, p.outline.selected) {
		p.syncOutlineTopLine()
		return
	}
	if containsInt(p.outline.filtered, current) {
		p.outline.selected = current
		p.syncOutlineTopLine()
		return
	}
	p.outline.selected = p.outline.filtered[0]
	p.syncOutlineTopLine()
}

func (p *pager) matchingOutlineIndices(filter string, dst []int) []int {
	filter = strings.ToLower(filter)
	dst = dst[:0]
	for i, heading := range p.headings {
		if filter == "" || strings.Contains(strings.ToLower(heading.Text), filter) {
			dst = append(dst, i)
		}
	}
	return dst
}

func (p *pager) currentHeadingIndex() int {
	if len(p.headings) == 0 {
		return -1
	}
	idx := sort.Search(len(p.headings), func(i int) bool {
		return p.headings[i].Line > p.topLine
	}) - 1
	if idx < 0 {
		return 0
	}
	return idx
}

func (p *pager) currentHeadingPath() []Heading {
	current := p.currentHeadingIndex()
	if current < 0 || current >= len(p.headings) {
		return nil
	}

	stack := make([]Heading, 0, p.headings[current].Level)
	for i := 0; i <= current; i++ {
		heading := p.headings[i]
		for len(stack) > 0 && stack[len(stack)-1].Level >= heading.Level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, heading)
	}
	return append([]Heading(nil), stack...)
}

func (p *pager) moveOutlineSelection(delta int) {
	pos := p.outlineSelectedPosition()
	if pos < 0 {
		if len(p.outline.filtered) == 0 {
			return
		}
		p.outline.selected = p.outline.filtered[0]
		p.topLine = clamp(p.headings[p.outline.selected].Line, 0, p.maxTopLine())
		return
	}
	p.moveOutlineSelectionTo(pos + delta)
}

func (p *pager) moveOutlineSelectionTo(pos int) {
	if len(p.outline.filtered) == 0 {
		p.outline.selected = -1
		return
	}
	pos = clamp(pos, 0, len(p.outline.filtered)-1)
	p.outline.selected = p.outline.filtered[pos]
	p.syncOutlineTopLine()
}

func (p *pager) syncOutlineTopLine() {
	if p.outline.selected < 0 || p.outline.selected >= len(p.headings) {
		return
	}
	p.topLine = clamp(p.headings[p.outline.selected].Line, 0, p.maxTopLine())
}

func (p *pager) outlineSelectedPosition() int {
	for i, idx := range p.outline.filtered {
		if idx == p.outline.selected {
			return i
		}
	}
	return -1
}

func (p *pager) outlinePanelWidth() int {
	if p.width <= 0 {
		return 0
	}
	maxWidth := min(p.width, max(24, p.width*2/3))
	width := 24
	for _, idx := range p.outline.filtered {
		heading := p.headings[idx]
		candidate := 4 + (heading.Level-1)*2 + utf8.RuneCountInString(heading.Text)
		if candidate > width {
			width = candidate
		}
	}
	promptWidth := 4 + utf8.RuneCountInString(p.outline.filter)
	if promptWidth > width {
		width = promptWidth
	}
	return min(maxWidth, width)
}

func (p *pager) outlineListRows() int {
	if p.height <= 2 {
		return 1
	}
	return min(max(3, p.height/3), max(1, p.height-2))
}

func (p *pager) outlinePanelRect() (top, left, bottom, right int) {
	panelWidth := p.outlinePanelWidth()
	listRows := p.outlineListRows()
	panelHeight := listRows + 1
	panelTop := max(1, p.height-panelHeight)
	return panelTop, 1, panelTop + panelHeight - 1, panelWidth
}

func (p *pager) mouseInOutline(row, col int) bool {
	top, left, bottom, right := p.outlinePanelRect()
	return row >= top && row <= bottom && col >= left && col <= right
}

func (p *pager) refreshSearchAround(anchor int) {
	p.searchMatches = p.searchMatches[:0]
	p.searchIndex = -1
	if p.searchQuery == "" {
		return
	}

	needle := strings.ToLower(p.searchQuery)
	for i, line := range p.plainLines {
		if strings.Contains(strings.ToLower(line), needle) {
			p.searchMatches = append(p.searchMatches, i)
		}
	}
	if len(p.searchMatches) == 0 {
		return
	}

	idx := sort.SearchInts(p.searchMatches, anchor)
	switch {
	case idx < len(p.searchMatches) && p.searchMatches[idx] == anchor:
		p.searchIndex = idx
	case idx > 0:
		p.searchIndex = idx - 1
	default:
		p.searchIndex = 0
	}
}

func (p *pager) jumpToFirstMatchAtOrAfter(anchor int) bool {
	if len(p.searchMatches) == 0 {
		return false
	}
	idx := sort.Search(len(p.searchMatches), func(i int) bool {
		return p.searchMatches[i] >= anchor
	})
	if idx >= len(p.searchMatches) {
		return false
	}
	p.searchIndex = idx
	p.topLine = clamp(p.searchMatches[idx], 0, p.maxTopLine())
	return true
}

func (p *pager) searchNext() {
	if len(p.searchMatches) == 0 {
		if p.searchQuery != "" {
			p.setNotice(fmt.Sprintf("pattern not found: /%s", p.searchQuery), true)
		}
		return
	}
	start := p.topLine - 1
	if p.searchIndex >= 0 && p.searchIndex < len(p.searchMatches) {
		start = p.searchMatches[p.searchIndex]
	}
	idx := sort.Search(len(p.searchMatches), func(i int) bool {
		return p.searchMatches[i] > start
	})
	if idx >= len(p.searchMatches) {
		p.setNotice(fmt.Sprintf("no later match for /%s", p.searchQuery), true)
		return
	}
	p.searchIndex = idx
	p.topLine = clamp(p.searchMatches[idx], 0, p.maxTopLine())
	p.clearNotice()
}

func (p *pager) searchPrev() {
	if len(p.searchMatches) == 0 {
		if p.searchQuery != "" {
			p.setNotice(fmt.Sprintf("pattern not found: /%s", p.searchQuery), true)
		}
		return
	}
	start := p.topLine
	if p.searchIndex >= 0 && p.searchIndex < len(p.searchMatches) {
		start = p.searchMatches[p.searchIndex]
	}
	idx := sort.Search(len(p.searchMatches), func(i int) bool {
		return p.searchMatches[i] >= start
	}) - 1
	if idx < 0 {
		p.setNotice(fmt.Sprintf("no earlier match for /%s", p.searchQuery), true)
		return
	}
	p.searchIndex = idx
	p.topLine = clamp(p.searchMatches[idx], 0, p.maxTopLine())
	p.clearNotice()
}

func (p *pager) scrollBy(delta int) {
	p.topLine = clamp(p.topLine+delta, 0, p.maxTopLine())
}

func (p *pager) insertPromptRune(r rune) {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	value = append(value[:cursor], append([]rune{r}, value[cursor:]...)...)
	p.promptValue = string(value)
	p.promptCursor = cursor + 1
}

func (p *pager) movePromptCursor(delta int) {
	p.promptCursor = clamp(p.promptCursor+delta, 0, utf8.RuneCountInString(p.promptValue))
}

func (p *pager) deletePromptBeforeCursor() {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	if cursor == 0 {
		return
	}
	value = append(value[:cursor-1], value[cursor:]...)
	p.promptValue = string(value)
	p.promptCursor = cursor - 1
}

func (p *pager) deletePromptAtCursor() {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	if cursor >= len(value) {
		return
	}
	value = append(value[:cursor], value[cursor+1:]...)
	p.promptValue = string(value)
	p.promptCursor = cursor
}

func (p *pager) killPromptToStart() {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	p.promptValue = string(value[cursor:])
	p.promptCursor = 0
}

func (p *pager) killPromptToEnd() {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	p.promptValue = string(value[:cursor])
}

func (p *pager) deletePromptBackwardWord() {
	value := []rune(p.promptValue)
	cursor := clamp(p.promptCursor, 0, len(value))
	start := promptBackwardWordBoundary(value, cursor)
	if start == cursor {
		return
	}
	p.promptValue = string(append(value[:start], value[cursor:]...))
	p.promptCursor = start
}

func (p *pager) movePromptBackwardWord() {
	value := []rune(p.promptValue)
	p.promptCursor = promptBackwardWordBoundary(value, clamp(p.promptCursor, 0, len(value)))
}

func (p *pager) movePromptForwardWord() {
	value := []rune(p.promptValue)
	p.promptCursor = promptForwardWordBoundary(value, clamp(p.promptCursor, 0, len(value)))
}

func (p *pager) insertOutlineRune(r rune) {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	value = append(value[:cursor], append([]rune{r}, value[cursor:]...)...)
	if len(p.matchingOutlineIndices(string(value), nil)) == 0 {
		return
	}
	p.outline.filter = string(value)
	p.outline.cursor = cursor + 1
	p.refreshOutline()
}

func (p *pager) moveOutlineCursor(delta int) {
	p.outline.cursor = clamp(p.outline.cursor+delta, 0, utf8.RuneCountInString(p.outline.filter))
}

func (p *pager) deleteOutlineBeforeCursor() {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	if cursor == 0 {
		return
	}
	value = append(value[:cursor-1], value[cursor:]...)
	p.outline.filter = string(value)
	p.outline.cursor = cursor - 1
	p.refreshOutline()
}

func (p *pager) deleteOutlineAtCursor() {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	if cursor >= len(value) {
		return
	}
	value = append(value[:cursor], value[cursor+1:]...)
	p.outline.filter = string(value)
	p.outline.cursor = cursor
	p.refreshOutline()
}

func (p *pager) killOutlineToStart() {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	p.outline.filter = string(value[cursor:])
	p.outline.cursor = 0
	p.refreshOutline()
}

func (p *pager) killOutlineToEnd() {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	p.outline.filter = string(value[:cursor])
	p.refreshOutline()
}

func (p *pager) deleteOutlineBackwardWord() {
	value := []rune(p.outline.filter)
	cursor := clamp(p.outline.cursor, 0, len(value))
	start := promptBackwardWordBoundary(value, cursor)
	if start == cursor {
		return
	}
	p.outline.filter = string(append(value[:start], value[cursor:]...))
	p.outline.cursor = start
	p.refreshOutline()
}

func (p *pager) moveOutlineBackwardWord() {
	value := []rune(p.outline.filter)
	p.outline.cursor = promptBackwardWordBoundary(value, clamp(p.outline.cursor, 0, len(value)))
}

func (p *pager) moveOutlineForwardWord() {
	value := []rune(p.outline.filter)
	p.outline.cursor = promptForwardWordBoundary(value, clamp(p.outline.cursor, 0, len(value)))
}

func promptBackwardWordBoundary(value []rune, cursor int) int {
	for cursor > 0 && unicode.IsSpace(value[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(value[cursor-1]) {
		cursor--
	}
	return cursor
}

func promptForwardWordBoundary(value []rune, cursor int) int {
	for cursor < len(value) && unicode.IsSpace(value[cursor]) {
		cursor++
	}
	for cursor < len(value) && !unicode.IsSpace(value[cursor]) {
		cursor++
	}
	return cursor
}

func (p *pager) viewHeight() int {
	if p.height <= 1 {
		return 1
	}
	return p.height - 1
}

func (p *pager) maxTopLine() int {
	maxTop := len(p.lines) - p.viewHeight()
	if maxTop < 0 {
		return 0
	}
	return maxTop
}

func readTerminalEvents(tty *os.File, out chan<- inputEvent) {
	defer close(out)

	reader := bufio.NewReader(tty)
	for {
		ev, err := readTerminalEvent(reader, tty)
		if err != nil {
			out <- inputErrorEvent{err: err}
			return
		}
		if ev == nil {
			continue
		}
		out <- ev
	}
}

func readTerminalEvent(reader *bufio.Reader, tty *os.File) (inputEvent, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}

	switch b {
	case 0x1b:
		return readEscapeEvent(reader, tty)
	case '\r', '\n':
		return keyEvent{kind: keyEnter}, nil
	case 0x7f:
		return keyEvent{kind: keyBackspace}, nil
	case 0x08:
		return keyEvent{ch: 8}, nil
	default:
		if b < utf8.RuneSelf {
			return keyEvent{kind: keyRune, ch: rune(b)}, nil
		}
		if err := reader.UnreadByte(); err != nil {
			return nil, err
		}
		r, _, err := reader.ReadRune()
		if err != nil {
			return nil, err
		}
		return keyEvent{kind: keyRune, ch: r}, nil
	}
}

func readEscapeEvent(reader *bufio.Reader, tty *os.File) (inputEvent, error) {
	if reader.Buffered() == 0 {
		ready, err := waitForInput(tty, 35*time.Millisecond)
		if err != nil {
			return nil, err
		}
		if !ready {
			return keyEvent{kind: keyEscape}, nil
		}
	}

	b, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}

	switch b {
	case '[':
		return readCSIEvent(reader)
	case ']':
		return readOSCEvent(reader)
	default:
		if b < utf8.RuneSelf {
			return keyEvent{kind: keyRune, ch: rune(b), alt: true}, nil
		}
		if err := reader.UnreadByte(); err != nil {
			return nil, err
		}
		r, _, err := reader.ReadRune()
		if err != nil {
			return nil, err
		}
		return keyEvent{kind: keyRune, ch: r, alt: true}, nil
	}
}

func readCSIEvent(reader *bufio.Reader) (inputEvent, error) {
	var seq []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		seq = append(seq, b)
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}

	if len(seq) == 0 {
		return nil, nil
	}

	final := seq[len(seq)-1]
	params := string(seq[:len(seq)-1])

	switch final {
	case 'A':
		return keyEvent{kind: keyUp}, nil
	case 'B':
		return keyEvent{kind: keyDown}, nil
	case 'C':
		return keyEvent{kind: keyRight}, nil
	case 'D':
		return keyEvent{kind: keyLeft}, nil
	case 'H':
		return keyEvent{kind: keyHome}, nil
	case 'F':
		return keyEvent{kind: keyEnd}, nil
	case 'I':
		return focusEvent{gained: true}, nil
	case 'O':
		return focusEvent{gained: false}, nil
	case 'M', 'm':
		if strings.HasPrefix(params, "<") {
			return parseSGRMouseEvent(params, final)
		}
	case '~':
		param := firstCSIParam(params)
		switch param {
		case "1", "7":
			return keyEvent{kind: keyHome}, nil
		case "3":
			return keyEvent{kind: keyDelete}, nil
		case "4", "8":
			return keyEvent{kind: keyEnd}, nil
		case "5":
			return keyEvent{kind: keyPageUp}, nil
		case "6":
			return keyEvent{kind: keyPageDown}, nil
		}
	}

	return nil, nil
}

func parseSGRMouseEvent(params string, final byte) (inputEvent, error) {
	if final != 'M' {
		return nil, nil
	}

	fields := strings.Split(strings.TrimPrefix(params, "<"), ";")
	if len(fields) != 3 {
		return nil, nil
	}

	var button, col, row int
	if _, err := fmt.Sscanf(fields[0], "%d", &button); err != nil {
		return nil, nil
	}
	if _, err := fmt.Sscanf(fields[1], "%d", &col); err != nil {
		return nil, nil
	}
	if _, err := fmt.Sscanf(fields[2], "%d", &row); err != nil {
		return nil, nil
	}

	switch button & 0x43 {
	case 64:
		return mouseEvent{kind: mouseScrollUp, row: row, col: col}, nil
	case 65:
		return mouseEvent{kind: mouseScrollDown, row: row, col: col}, nil
	default:
		return nil, nil
	}
}

func waitForInput(tty *os.File, timeout time.Duration) (bool, error) {
	pollFds := []unix.PollFd{{
		Fd:     int32(tty.Fd()),
		Events: unix.POLLIN,
	}}
	n, err := unix.Poll(pollFds, int(timeout.Milliseconds()))
	if err != nil {
		return false, err
	}
	return n > 0 && pollFds[0].Revents&unix.POLLIN != 0, nil
}

func firstCSIParam(params string) string {
	if idx := strings.IndexByte(params, ';'); idx >= 0 {
		return params[:idx]
	}
	return params
}

func readOSCEvent(reader *bufio.Reader) (inputEvent, error) {
	var data []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		switch b {
		case 0x07:
			return parseOSCEvent(string(data)), nil
		case 0x1b:
			next, err := reader.ReadByte()
			if err != nil {
				return nil, err
			}
			if next == '\\' {
				return parseOSCEvent(string(data)), nil
			}
			data = append(data, b, next)
		default:
			data = append(data, b)
		}
	}
}

func parseOSCEvent(payload string) inputEvent {
	if !strings.HasPrefix(payload, "11;") {
		return nil
	}
	color, ok := parseOSCColor(strings.TrimPrefix(payload, "11;"))
	if !ok {
		return nil
	}
	return bgColorEvent{color: color}
}

func parseOSCColor(payload string) (rgbColor, bool) {
	switch {
	case strings.HasPrefix(payload, "rgb:"):
		parts := strings.Split(strings.TrimPrefix(payload, "rgb:"), "/")
		if len(parts) != 3 {
			return rgbColor{}, false
		}
		r, ok := parseColorComponent(parts[0])
		if !ok {
			return rgbColor{}, false
		}
		g, ok := parseColorComponent(parts[1])
		if !ok {
			return rgbColor{}, false
		}
		b, ok := parseColorComponent(parts[2])
		if !ok {
			return rgbColor{}, false
		}
		return rgbColor{r: r, g: g, b: b}, true
	case strings.HasPrefix(payload, "rgba:"):
		parts := strings.Split(strings.TrimPrefix(payload, "rgba:"), "/")
		if len(parts) != 4 {
			return rgbColor{}, false
		}
		r, ok := parseColorComponent(parts[0])
		if !ok {
			return rgbColor{}, false
		}
		g, ok := parseColorComponent(parts[1])
		if !ok {
			return rgbColor{}, false
		}
		b, ok := parseColorComponent(parts[2])
		if !ok {
			return rgbColor{}, false
		}
		if _, ok := parseColorComponent(parts[3]); !ok {
			return rgbColor{}, false
		}
		return rgbColor{r: r, g: g, b: b}, true
	default:
		return rgbColor{}, false
	}
}

func parseColorComponent(part string) (uint8, bool) {
	switch len(part) {
	case 2:
		var v uint8
		_, err := fmt.Sscanf(part, "%02x", &v)
		return v, err == nil
	case 4:
		var v uint16
		_, err := fmt.Sscanf(part, "%04x", &v)
		if err != nil {
			return 0, false
		}
		return uint8((uint32(v) + 128) / 257), true
	default:
		return 0, false
	}
}

func deriveTintTheme(bg rgbColor) tintTheme {
	status := tintedBackground(bg, subtleTintAlpha(bg))
	prompt := tintedBackground(bg, promptTintAlpha(bg))
	return tintTheme{
		statusBG:     status,
		promptBG:     prompt,
		highlightBG:  prompt,
		blockquoteBG: tintedBackground(bg, 0.16),
		markBG:       prompt,
	}
}

func subtleTintAlpha(bg rgbColor) float64 {
	if isLight(bg) {
		return 0.04
	}
	return 0.12
}

func promptTintAlpha(bg rgbColor) float64 {
	if isLight(bg) {
		return 0.10
	}
	return 0.20
}

func isLight(bg rgbColor) bool {
	y := 0.299*float64(bg.r) + 0.587*float64(bg.g) + 0.114*float64(bg.b)
	return y > 128.0
}

func tintedBackground(bg rgbColor, alpha float64) string {
	mode := detectColorMode()
	if mode == colorModeNone {
		return ""
	}

	overlay := rgbColor{}
	if !isLight(bg) {
		overlay = rgbColor{r: 255, g: 255, b: 255}
	}
	blended := blendColor(bg, overlay, alpha)

	switch mode {
	case colorModeTrueColor:
		return fmt.Sprintf("\033[48;2;%d;%d;%dm", blended.r, blended.g, blended.b)
	case colorModeANSI256:
		return fmt.Sprintf("\033[48;5;%dm", nearestANSI256(blended))
	default:
		return ""
	}
}

type colorMode int

const (
	colorModeNone colorMode = iota
	colorModeANSI256
	colorModeTrueColor
)

func detectColorMode() colorMode {
	colorterm := strings.ToLower(os.Getenv("COLORTERM"))
	if strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return colorModeTrueColor
	}
	if strings.Contains(strings.ToLower(os.Getenv("TERM")), "256color") {
		return colorModeANSI256
	}
	return colorModeNone
}

func blendColor(bg, overlay rgbColor, alpha float64) rgbColor {
	blend := func(base, top uint8) uint8 {
		value := math.Floor(float64(top)*alpha + float64(base)*(1-alpha))
		return uint8(clamp(int(value), 0, 255))
	}
	return rgbColor{
		r: blend(bg.r, overlay.r),
		g: blend(bg.g, overlay.g),
		b: blend(bg.b, overlay.b),
	}
}

func nearestANSI256(c rgbColor) int {
	bestIdx := 0
	bestDist := math.MaxFloat64
	for idx, candidate := range ansi256Palette {
		dr := float64(c.r) - float64(candidate.r)
		dg := float64(c.g) - float64(candidate.g)
		db := float64(c.b) - float64(candidate.b)
		dist := 0.299*dr*dr + 0.587*dg*dg + 0.114*db*db
		if dist < bestDist {
			bestDist = dist
			bestIdx = idx
		}
	}
	return bestIdx
}

var ansi256Palette = buildANSI256Palette()

func buildANSI256Palette() []rgbColor {
	palette := make([]rgbColor, 256)
	base := []rgbColor{
		{0, 0, 0},
		{128, 0, 0},
		{0, 128, 0},
		{128, 128, 0},
		{0, 0, 128},
		{128, 0, 128},
		{0, 128, 128},
		{192, 192, 192},
		{128, 128, 128},
		{255, 0, 0},
		{0, 255, 0},
		{255, 255, 0},
		{0, 0, 255},
		{255, 0, 255},
		{0, 255, 255},
		{255, 255, 255},
	}
	copy(palette, base)

	steps := []uint8{0, 95, 135, 175, 215, 255}
	index := 16
	for _, r := range steps {
		for _, g := range steps {
			for _, b := range steps {
				palette[index] = rgbColor{r: r, g: g, b: b}
				index++
			}
		}
	}

	for i := 0; i < 24; i++ {
		v := uint8(8 + i*10)
		palette[232+i] = rgbColor{r: v, g: v, b: v}
	}
	return palette
}

func watchFiles(paths []string) (<-chan struct{}, <-chan error, func(), error) {
	if len(paths) == 0 {
		return nil, nil, func() {}, nil
	}

	reloadCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating file watcher: %w", err)
	}

	watched := make(map[string]struct{}, len(paths))
	dirs := make(map[string]struct{})
	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			watcher.Close()
			return nil, nil, nil, fmt.Errorf("resolving %s: %w", path, err)
		}
		absPath = filepath.Clean(absPath)
		watched[absPath] = struct{}{}
		dir := filepath.Dir(absPath)
		if _, seen := dirs[dir]; seen {
			continue
		}
		dirs[dir] = struct{}{}
		if err := watcher.Add(dir); err != nil {
			watcher.Close()
			return nil, nil, nil, fmt.Errorf("watching %s: %w", dir, err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(reloadCh)
		defer close(errCh)

		var (
			timer  *time.Timer
			timerC <-chan time.Time
		)
		for {
			select {
			case <-done:
				if timer != nil {
					timer.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if !matchesWatchedPath(event, watched) {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(75 * time.Millisecond)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(75 * time.Millisecond)
				}
				timerC = timer.C
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				select {
				case errCh <- err:
				default:
				}
			case <-timerC:
				timerC = nil
				select {
				case reloadCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	closeFn := func() {
		close(done)
		watcher.Close()
	}
	return reloadCh, errCh, closeFn, nil
}

func matchesWatchedPath(event fsnotify.Event, watched map[string]struct{}) bool {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	name := filepath.Clean(event.Name)
	_, ok := watched[name]
	return ok
}

func pagerLabel(paths []string) string {
	switch len(paths) {
	case 0:
		return "stdin"
	case 1:
		return paths[0]
	default:
		return fmt.Sprintf("%s +%d more", paths[0], len(paths)-1)
	}
}

func humanizeRelativeTime(then, now time.Time) string {
	if then.IsZero() {
		return ""
	}

	if now.Before(then) {
		now = then
	}
	delta := now.Sub(then)

	switch {
	case delta < 30*time.Second:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 48*time.Hour:
		return "yesterday"
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	case delta < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(delta/(7*24*time.Hour)))
	case delta < 365*24*time.Hour:
		months := int(delta / (30 * 24 * time.Hour))
		if months <= 1 {
			return "last month"
		}
		return fmt.Sprintf("%dmo", months)
	case delta < 2*365*24*time.Hour:
		return "last year"
	default:
		return fmt.Sprintf("%dy", int(delta/(365*24*time.Hour)))
	}
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type matchRange struct {
	start int
	end   int
}

func highlightSearchMatches(rendered, plain, query, startSeq string) string {
	matches := findSearchMatches(plain, query)
	if len(matches) == 0 || startSeq == "" {
		return rendered
	}

	var (
		out        strings.Builder
		runePos    int
		matchIdx   int
		active     bool
		activeEnd  int
		currentSGR string
	)

	for i := 0; i < len(rendered); {
		if !active && matchIdx < len(matches) && runePos == matches[matchIdx].start {
			out.WriteString(startSeq)
			active = true
			activeEnd = matches[matchIdx].end
		}
		if active && runePos == activeEnd {
			out.WriteString(Reset)
			out.WriteString(currentSGR)
			active = false
			matchIdx++
			continue
		}

		if rendered[i] == 0x1b {
			seq, seqKind, next := consumeEscapeSequence(rendered, i)
			if seq == "" {
				break
			}
			out.WriteString(seq)
			if seqKind == escapeSGR {
				currentSGR = updateCurrentSGR(currentSGR, seq)
				if active {
					out.WriteString(startSeq)
				}
			}
			i = next
			continue
		}

		r, size := utf8.DecodeRuneInString(rendered[i:])
		out.WriteRune(r)
		i += size
		runePos++
	}

	if active {
		out.WriteString(Reset)
		out.WriteString(currentSGR)
	}
	return out.String()
}

func findSearchMatches(plain, query string) []matchRange {
	if query == "" {
		return nil
	}

	haystack := []rune(strings.ToLower(plain))
	needle := []rune(strings.ToLower(query))
	if len(needle) == 0 || len(haystack) < len(needle) {
		return nil
	}

	var matches []matchRange
	for i := 0; i <= len(haystack)-len(needle); {
		if runesEqual(haystack[i:i+len(needle)], needle) {
			matches = append(matches, matchRange{start: i, end: i + len(needle)})
			i += len(needle)
			continue
		}
		i++
	}
	return matches
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type escapeKind int

const (
	escapeOther escapeKind = iota
	escapeSGR
)

func consumeEscapeSequence(s string, start int) (string, escapeKind, int) {
	if start+1 >= len(s) || s[start] != 0x1b {
		return "", escapeOther, start
	}

	switch s[start+1] {
	case '[':
		i := start + 2
		for i < len(s) {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				kind := escapeOther
				if s[i] == 'm' {
					kind = escapeSGR
				}
				return s[start : i+1], kind, i + 1
			}
			i++
		}
	case ']':
		i := start + 2
		for i < len(s) {
			if s[i] == 0x07 {
				return s[start : i+1], escapeOther, i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return s[start : i+2], escapeOther, i + 2
			}
			i++
		}
	}

	return s[start:], escapeOther, len(s)
}

func updateCurrentSGR(current, seq string) string {
	if !strings.HasPrefix(seq, "\033[") || !strings.HasSuffix(seq, "m") {
		return current
	}

	params := strings.TrimSuffix(strings.TrimPrefix(seq, "\033["), "m")
	if params == "" {
		return ""
	}
	for _, part := range strings.Split(params, ";") {
		if part == "" || part == "0" {
			return ""
		}
	}
	return current + seq
}

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			out.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[':
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
		case ']':
			i += 2
			for i < len(s) {
				if s[i] == 0x07 {
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return out.String()
}

func fitToWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if visibleWidth(text) <= width {
		return text
	}
	if width <= 3 {
		return truncateVisible(text, width)
	}
	return truncateVisible(text, width-3) + "..."
}

func visibleWidth(text string) int {
	return utf8.RuneCountInString(stripANSI(text))
}

func truncateVisible(text string, width int) string {
	if width <= 0 {
		return ""
	}

	var (
		out        strings.Builder
		visible    int
		currentSGR string
	)
	for i := 0; i < len(text) && visible < width; {
		if text[i] == 0x1b {
			seq, seqKind, next := consumeEscapeSequence(text, i)
			if seq == "" {
				break
			}
			out.WriteString(seq)
			if seqKind == escapeSGR {
				currentSGR = updateCurrentSGR(currentSGR, seq)
			}
			i = next
			continue
		}

		r, size := utf8.DecodeRuneInString(text[i:])
		out.WriteRune(r)
		visible++
		i += size
	}

	if currentSGR != "" {
		out.WriteString(Reset)
	}
	return out.String()
}

func trimLastRune(text string) string {
	if text == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}

func cursorTo(row, col int) string {
	return fmt.Sprintf("\033[%d;%dH", row, col)
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
