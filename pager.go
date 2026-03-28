package md

import (
	"bufio"
	"bytes"
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
	lines         []string
	plainLines    []string
	topLine       int
	searchQuery   string
	searchMatches []int
	searchIndex   int
	promptActive  bool
	promptValue   string
	notice        string
	noticeIsError bool
	theme         tintTheme
	live          bool
}

type tintTheme struct {
	statusBG string
	promptBG string
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
	keyPageUp
	keyPageDown
	keyHome
	keyEnd
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

type rgbColor struct {
	r uint8
	g uint8
	b uint8
}

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
		live:        len(cfg.Paths) > 0,
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

	fmt.Fprint(tty, enterAltScreen, clearScreen, cursorHome, hideCursor, enableFocusReporting)
	defer fmt.Fprint(tty, Reset, showCursor, disableFocusReporting, exitAltScreen)

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
		p.theme = deriveTintTheme(v.color)
	case inputErrorEvent:
		if v.err != nil && !errors.Is(v.err, io.EOF) {
			return false, v.err
		}
	}
	return false, nil
}

func (p *pager) handleKey(ev keyEvent) bool {
	if p.promptActive {
		return p.handlePromptKey(ev)
	}

	switch {
	case ev.kind == keyEnter || ev.ch == 'j' || ev.ch == 14 || ev.kind == keyDown:
		p.scrollBy(1)
	case ev.ch == 'k' || ev.ch == 16 || ev.kind == keyUp:
		p.scrollBy(-1)
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

func (p *pager) handlePromptKey(ev keyEvent) bool {
	switch {
	case ev.kind == keyEnter:
		p.promptActive = false
		p.searchQuery = p.promptValue
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
		p.promptValue = trimLastRune(p.promptValue)
	case ev.kind == keyDelete:
	case ev.ch == 7:
		p.promptActive = false
		p.promptValue = p.searchQuery
	case ev.ch == 3:
		return true
	case ev.kind == keyRune && unicode.IsPrint(ev.ch):
		p.promptValue += string(ev.ch)
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
	source, err := p.loadSource()
	if err != nil {
		if initial {
			return err
		}
		p.setNotice(err.Error(), true)
		return nil
	}

	p.source = source
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

func (p *pager) loadSource() ([]byte, error) {
	if len(p.cfg.Paths) == 0 {
		return append([]byte(nil), p.cfg.InitialSource...), nil
	}

	var all []byte
	for _, path := range p.cfg.Paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		all = append(all, data...)
	}
	return all, nil
}

func (p *pager) rebuild() error {
	var buf bytes.Buffer
	if err := Render(p.source, &buf, p.width, true); err != nil {
		return err
	}

	anchor := p.topLine
	if p.searchIndex >= 0 && p.searchIndex < len(p.searchMatches) {
		anchor = p.searchMatches[p.searchIndex]
	}

	text := strings.TrimSuffix(buf.String(), "\n")
	if text == "" {
		p.lines = nil
		p.plainLines = nil
		p.topLine = 0
		p.refreshSearchState()
		return nil
	}

	p.lines = strings.Split(text, "\n")
	p.plainLines = make([]string, len(p.lines))
	for i, line := range p.lines {
		p.plainLines[i] = stripANSI(line)
	}

	p.topLine = clamp(p.topLine, 0, p.maxTopLine())
	p.refreshSearchAround(anchor)
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
			out.WriteString(p.lines[lineIdx])
		}
	}

	statusRow := max(1, p.height)
	out.WriteString(cursorTo(statusRow, 1))
	out.WriteString("\033[2K")
	if p.promptActive {
		prompt := fitToWidth("/"+p.promptValue, p.width)
		out.WriteString(p.renderBar(prompt, true))
		cursorCol := min(p.width, visibleWidth(prompt)+1)
		out.WriteString(cursorTo(statusRow, max(1, cursorCol)))
		out.WriteString(showCursor)
	} else {
		out.WriteString(hideCursor)
		out.WriteString(p.renderBar(p.statusLine(), false))
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

func (p *pager) statusLine() string {
	viewHeight := p.viewHeight()
	bottom := min(len(p.lines), p.topLine+viewHeight)
	lineSummary := "0/0"
	if len(p.lines) > 0 {
		lineSummary = fmt.Sprintf("%d-%d/%d", p.topLine+1, bottom, len(p.lines))
	}

	percent := 100
	if len(p.lines) == 0 {
		percent = 0
	} else if maxTop := p.maxTopLine(); maxTop > 0 {
		percent = int(math.Round(float64(p.topLine) / float64(maxTop) * 100))
	}

	parts := []string{p.cfg.Label, lineSummary, fmt.Sprintf("%d%%", percent)}
	if p.live {
		parts = append(parts, "live")
	}
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
	return fitToWidth(strings.Join(parts, "  "), p.width)
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

func readTerminalEvents(r io.Reader, out chan<- inputEvent) {
	defer close(out)

	reader := bufio.NewReader(r)
	for {
		ev, err := readTerminalEvent(reader)
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

func readTerminalEvent(reader *bufio.Reader) (inputEvent, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}

	switch b {
	case 0x1b:
		return readEscapeEvent(reader)
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

func readEscapeEvent(reader *bufio.Reader) (inputEvent, error) {
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
	case 'H':
		return keyEvent{kind: keyHome}, nil
	case 'F':
		return keyEvent{kind: keyEnd}, nil
	case 'I':
		return focusEvent{gained: true}, nil
	case 'O':
		return focusEvent{gained: false}, nil
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
		statusBG: status,
		promptBG: prompt,
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
		return string([]rune(text)[:width])
	}
	runes := []rune(text)
	if len(runes) > width-3 {
		runes = runes[:width-3]
	}
	return string(runes) + "..."
}

func visibleWidth(text string) int {
	return utf8.RuneCountInString(text)
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
