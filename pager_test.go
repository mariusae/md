package md

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseOSCColorRGB(t *testing.T) {
	color, ok := parseOSCColor("rgb:ffff/8000/0000")
	if !ok {
		t.Fatal("expected rgb payload to parse")
	}
	if color != (rgbColor{r: 255, g: 128, b: 0}) {
		t.Fatalf("unexpected color: %#v", color)
	}
}

func TestParseOSCColorRGBA(t *testing.T) {
	color, ok := parseOSCColor("rgba:0000/0000/ffff/ffff")
	if !ok {
		t.Fatal("expected rgba payload to parse")
	}
	if color != (rgbColor{r: 0, g: 0, b: 255}) {
		t.Fatalf("unexpected color: %#v", color)
	}
}

func TestCodeBlockTintUsesIonHUDAlphaValues(t *testing.T) {
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-256color")
	tests := []struct {
		name string
		bg   rgbColor
		want float64
	}{
		{name: "light", bg: rgbColor{r: 255, g: 255, b: 255}, want: 0.04},
		{name: "dark", bg: rgbColor{}, want: 0.12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subtleTintAlpha(tt.bg); got != tt.want {
				t.Fatalf("subtleTintAlpha(%#v) = %v, want %v", tt.bg, got, tt.want)
			}
			theme := deriveTintTheme(tt.bg)
			if theme.codeBlockBG != theme.statusBG {
				t.Fatalf("code block tint %q does not match subtle HUD tint %q", theme.codeBlockBG, theme.statusBG)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	input := Bold + "hello" + Reset + " " + OSC8Start("https://example.com") + "world" + OSC8End
	if got := stripANSI(input); got != "hello world" {
		t.Fatalf("stripANSI() = %q, want %q", got, "hello world")
	}
}

func TestSearchStateTracksNearestMatch(t *testing.T) {
	p := &pager{
		plainLines:  []string{"alpha", "beta alpha", "gamma"},
		searchQuery: "alpha",
		topLine:     1,
		searchIndex: -1,
	}

	p.refreshSearchAround(1)

	if len(p.searchMatches) != 2 {
		t.Fatalf("unexpected match count: %d", len(p.searchMatches))
	}
	if p.searchIndex != 1 {
		t.Fatalf("searchIndex = %d, want 1", p.searchIndex)
	}
}

func TestWatchFilesWithoutPaths(t *testing.T) {
	reloadCh, errCh, closeFn, err := watchFiles(nil)
	if err != nil {
		t.Fatalf("watchFiles(nil) returned error: %v", err)
	}
	if reloadCh != nil {
		t.Fatal("expected nil reload channel when no paths are watched")
	}
	if errCh != nil {
		t.Fatal("expected nil error channel when no paths are watched")
	}
	closeFn()
}

func TestQuestionMarkTogglesHelpScreen(t *testing.T) {
	p := &pager{outline: outlineState{active: true}}

	p.handleKey(keyEvent{kind: keyRune, ch: '?'})
	if !p.helpActive {
		t.Fatal("? did not open help")
	}
	if p.outline.filter != "" {
		t.Fatal("? was inserted into the outline filter")
	}

	p.handleKey(keyEvent{kind: keyRune, ch: '?'})
	if p.helpActive {
		t.Fatal("? did not close help")
	}
}

func TestFlowModeCapsAndCentersWideContent(t *testing.T) {
	p := &pager{width: 140, height: 24}

	p.handleKey(keyEvent{kind: keyRune, ch: 'f'})

	if !p.flow {
		t.Fatal("f did not enable flow mode")
	}
	if got := p.renderWidth(); got != 100 {
		t.Fatalf("renderWidth() = %d, want 100", got)
	}
	if got := p.contentLeft(); got != 21 {
		t.Fatalf("contentLeft() = %d, want 21", got)
	}
	if !strings.Contains(p.statusBarLeft(), "flow") {
		t.Fatalf("status bar does not show flow mode: %q", p.statusBarLeft())
	}
}

func TestFlowModeDoesNotNarrowSmallScreens(t *testing.T) {
	p := &pager{width: 80, flow: true}

	if got := p.renderWidth(); got != 80 {
		t.Fatalf("renderWidth() = %d, want 80", got)
	}
	if got := p.contentLeft(); got != 1 {
		t.Fatalf("contentLeft() = %d, want 1", got)
	}
}

func TestHelpFitsStandardTerminalHeight(t *testing.T) {
	if got := len(pagerHelpLines()); got > 24 {
		t.Fatalf("help has %d rows; want at most 24", got)
	}
}

func TestChangedRenderedRangesTracksEditedAndInsertedText(t *testing.T) {
	oldLines := []string{"# Title", "hello world", "tail"}
	newLines := []string{"# Title", "hello brave world", "new line", "tail"}

	got := changedRenderedRanges(oldLines, newLines)
	assertMatchRange(t, got[1], matchRange{start: 6, end: 12})
	assertMatchRange(t, got[2], matchRange{start: 0, end: 8})
	if _, ok := got[3]; ok {
		t.Fatal("unchanged tail line should not flash")
	}
}

func TestChangedRenderedRangesMarksNeighborForDeletion(t *testing.T) {
	got := changedRenderedRanges(
		[]string{"before", "removed", "after"},
		[]string{"before", "after"},
	)

	assertMatchRange(t, got[1], matchRange{start: 0, end: 5})
}

func TestReloadFlashesChangedRenderedText(t *testing.T) {
	path := t.TempDir() + "/document.md"
	if err := os.WriteFile(path, []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &pager{cfg: PagerConfig{Paths: []string{path}}, width: 80, height: 24}
	if err := p.reload(true); err != nil {
		t.Fatal(err)
	}
	if len(p.changeFlash) != 0 {
		t.Fatal("initial render should not flash")
	}

	if err := os.WriteFile(path, []byte("hello brave world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.reload(false); err != nil {
		t.Fatal(err)
	}
	if p.changeFlashUntil.IsZero() {
		t.Fatal("changed render did not start a flash")
	}
	if rendered := p.renderLine(0); !strings.Contains(rendered, p.highlightStart()+"brave") {
		t.Fatalf("changed text was not highlighted: %q", rendered)
	}

	p.clearExpiredChangeFlash(p.changeFlashUntil)
	if len(p.changeFlash) != 0 || !p.changeFlashUntil.IsZero() {
		t.Fatal("expired flash was not cleared")
	}
}

func assertMatchRange(t *testing.T, got []matchRange, want matchRange) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ranges = %#v, want %#v", got, []matchRange{want})
	}
}

func TestPromptControlUDeletesToStart(t *testing.T) {
	p := &pager{
		promptActive: true,
		promptValue:  "alpha beta",
		promptCursor: 6,
	}

	p.handlePromptKey(keyEvent{ch: 21})

	if p.promptValue != "beta" {
		t.Fatalf("promptValue = %q, want %q", p.promptValue, "beta")
	}
	if p.promptCursor != 0 {
		t.Fatalf("promptCursor = %d, want 0", p.promptCursor)
	}
}

func TestPromptControlKDeletesToEnd(t *testing.T) {
	p := &pager{
		promptActive: true,
		promptValue:  "alpha beta",
		promptCursor: 5,
	}

	p.handlePromptKey(keyEvent{ch: 11})

	if p.promptValue != "alpha" {
		t.Fatalf("promptValue = %q, want %q", p.promptValue, "alpha")
	}
	if p.promptCursor != 5 {
		t.Fatalf("promptCursor = %d, want 5", p.promptCursor)
	}
}

func TestPromptControlWDeletesPreviousWord(t *testing.T) {
	p := &pager{
		promptActive: true,
		promptValue:  "alpha beta gamma",
		promptCursor: len([]rune("alpha beta ")),
	}

	p.handlePromptKey(keyEvent{ch: 23})

	if p.promptValue != "alpha gamma" {
		t.Fatalf("promptValue = %q, want %q", p.promptValue, "alpha gamma")
	}
	if p.promptCursor != len([]rune("alpha ")) {
		t.Fatalf("promptCursor = %d", p.promptCursor)
	}
}

func TestPromptCursorMovementAndInsertion(t *testing.T) {
	p := &pager{
		promptActive: true,
		promptValue:  "abef",
		promptCursor: 2,
	}

	p.handlePromptKey(keyEvent{ch: 2})
	p.handlePromptKey(keyEvent{kind: keyRune, ch: 'Z'})
	p.handlePromptKey(keyEvent{ch: 6})
	p.handlePromptKey(keyEvent{kind: keyRune, ch: 'Y'})

	if p.promptValue != "aZbYef" {
		t.Fatalf("promptValue = %q, want %q", p.promptValue, "aZbYef")
	}
	if p.promptCursor != 4 {
		t.Fatalf("promptCursor = %d, want 4", p.promptCursor)
	}
}

func TestPromptDisplayKeepsCursorVisible(t *testing.T) {
	p := &pager{
		width:        8,
		promptValue:  "abcdefghij",
		promptCursor: 9,
	}

	display, cursorCol := p.promptDisplay()

	if display != "/cdefghi" {
		t.Fatalf("display = %q, want %q", display, "/cdefghi")
	}
	if cursorCol != 8 {
		t.Fatalf("cursorCol = %d, want 8", cursorCol)
	}
}

func TestFindSearchMatches(t *testing.T) {
	got := findSearchMatches("alpha beta alpha", "alpha")
	want := []matchRange{{start: 0, end: 5}, {start: 11, end: 16}}
	if len(got) != len(want) {
		t.Fatalf("unexpected match count: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestHighlightSearchMatchesPlain(t *testing.T) {
	startSeq := "\033[48;2;1;2;3m\033[1m"
	got := highlightSearchMatches("alpha beta alpha", "alpha beta alpha", "alpha", startSeq)
	if strings.Count(got, startSeq) != 2 {
		t.Fatalf("expected two highlighted matches, got %q", got)
	}
	if stripANSI(got) != "alpha beta alpha" {
		t.Fatalf("stripANSI() = %q", stripANSI(got))
	}
}

func TestHighlightSearchMatchesRestoresExistingStyles(t *testing.T) {
	startSeq := "\033[48;2;1;2;3m\033[1m"
	rendered := Bold + "alpha" + Reset + " beta"
	got := highlightSearchMatches(rendered, "alpha beta", "alpha", startSeq)
	if !strings.Contains(got, Bold+startSeq+"alpha"+Reset+Bold+Reset+" beta") {
		t.Fatalf("highlighted output did not preserve style reset ordering: %q", got)
	}
}

func TestHighlightSearchMatchesPreservesLinks(t *testing.T) {
	startSeq := "\033[48;2;1;2;3m\033[1m"
	rendered := OSC8Start("https://example.com") + FgBlue + Underline + "alpha" + Reset + OSC8End
	got := highlightSearchMatches(rendered, "alpha", "alpha", startSeq)
	if !strings.Contains(got, OSC8Start("https://example.com")) || !strings.Contains(got, OSC8End) {
		t.Fatalf("expected OSC-8 escapes to be preserved: %q", got)
	}
	if !strings.Contains(got, startSeq) {
		t.Fatalf("expected highlighted match: %q", got)
	}
}

func TestHumanizeRelativeTime(t *testing.T) {
	now := time.Date(2026, time.March, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		then time.Time
		want string
	}{
		{name: "just now", then: now.Add(-20 * time.Second), want: "just now"},
		{name: "minutes", then: now.Add(-2 * time.Minute), want: "2m"},
		{name: "hours", then: now.Add(-3 * time.Hour), want: "3h"},
		{name: "yesterday", then: now.Add(-30 * time.Hour), want: "yesterday"},
		{name: "days", then: now.Add(-6 * 24 * time.Hour), want: "6d"},
		{name: "last month", then: now.Add(-45 * 24 * time.Hour), want: "last month"},
		{name: "last year", then: now.Add(-400 * 24 * time.Hour), want: "last year"},
		{name: "years", then: now.Add(-800 * 24 * time.Hour), want: "2y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := humanizeRelativeTime(tt.then, now); got != tt.want {
				t.Fatalf("humanizeRelativeTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractHeadings(t *testing.T) {
	source := []byte("# One\n\n## Two\ntext\n")
	headings, err := ExtractHeadings(source)
	if err != nil {
		t.Fatalf("ExtractHeadings() returned error: %v", err)
	}
	if len(headings) != 2 {
		t.Fatalf("unexpected heading count: %d", len(headings))
	}
	if headings[0].Text != "One" || headings[0].Level != 1 {
		t.Fatalf("unexpected heading[0]: %#v", headings[0])
	}
	if headings[1].Text != "Two" || headings[1].Level != 2 {
		t.Fatalf("unexpected heading[1]: %#v", headings[1])
	}
}

func TestOutlineRefreshSelectsCurrentHeading(t *testing.T) {
	p := &pager{
		topLine: 5,
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 4},
			{Level: 2, Text: "Three", Line: 9},
		},
		outline: outlineState{selected: -1},
	}

	p.refreshOutline()

	if len(p.outline.filtered) != 3 {
		t.Fatalf("unexpected filtered count: %d", len(p.outline.filtered))
	}
	if p.outline.selected != 1 {
		t.Fatalf("outline.selected = %d, want 1", p.outline.selected)
	}
}

func TestOutlineFilterNarrowsHeadings(t *testing.T) {
	p := &pager{
		headings: []Heading{
			{Level: 1, Text: "Alpha", Line: 0},
			{Level: 2, Text: "Beta", Line: 2},
			{Level: 2, Text: "Alphabet", Line: 4},
		},
		outline: outlineState{
			filter: "alp",
			cursor: 3,
		},
	}

	p.refreshOutline()

	if len(p.outline.filtered) != 2 {
		t.Fatalf("unexpected filtered count: %d", len(p.outline.filtered))
	}
	if p.outline.filtered[0] != 0 || p.outline.filtered[1] != 2 {
		t.Fatalf("unexpected filtered values: %#v", p.outline.filtered)
	}
}

func TestInsertOutlineRuneRejectsZeroMatchFilter(t *testing.T) {
	p := &pager{
		headings: []Heading{
			{Level: 1, Text: "Alpha", Line: 0},
			{Level: 2, Text: "Alphabet", Line: 4},
		},
		outline: outlineState{
			filter: "alp",
			cursor: 3,
		},
	}

	p.refreshOutline()
	p.insertOutlineRune('z')

	if p.outline.filter != "alp" {
		t.Fatalf("outline.filter = %q, want %q", p.outline.filter, "alp")
	}
	if p.outline.cursor != 3 {
		t.Fatalf("outline.cursor = %d, want 3", p.outline.cursor)
	}
	if len(p.outline.filtered) != 2 {
		t.Fatalf("unexpected filtered count: %d", len(p.outline.filtered))
	}
}

func TestInsertOutlineRuneNavigatesToFilteredSelection(t *testing.T) {
	p := &pager{
		height: 8,
		headings: []Heading{
			{Level: 1, Text: "Intro", Line: 0},
			{Level: 2, Text: "Alpha", Line: 4},
			{Level: 2, Text: "Beta", Line: 9},
		},
		lines: make([]string, 20),
		outline: outlineState{
			filter:   "",
			cursor:   0,
			selected: 0,
			filtered: []int{0, 1, 2},
		},
	}

	p.insertOutlineRune('b')

	if p.outline.filter != "b" {
		t.Fatalf("outline.filter = %q, want %q", p.outline.filter, "b")
	}
	if p.outline.selected != 2 {
		t.Fatalf("outline.selected = %d, want 2", p.outline.selected)
	}
	if p.topLine != 9 {
		t.Fatalf("topLine = %d, want 9", p.topLine)
	}
}

func TestMoveOutlineSelectionNavigates(t *testing.T) {
	p := &pager{
		height: 8,
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 4},
			{Level: 2, Text: "Three", Line: 9},
		},
		outline: outlineState{
			filtered: []int{0, 1, 2},
			selected: 0,
		},
		lines: make([]string, 20),
	}

	p.moveOutlineSelection(1)

	if p.outline.selected != 1 {
		t.Fatalf("outline.selected = %d, want 1", p.outline.selected)
	}
	if p.topLine != 4 {
		t.Fatalf("topLine = %d, want 4", p.topLine)
	}
}

func TestOutlineEscapeClosesOverlay(t *testing.T) {
	p := &pager{
		outline: outlineState{
			active:   true,
			filter:   "alp",
			selected: 1,
			filtered: []int{1},
		},
	}

	quit := p.handleOutlineKey(keyEvent{kind: keyEscape})

	if quit {
		t.Fatal("handleOutlineKey() should not quit on escape")
	}
	if p.outline.active {
		t.Fatal("outline should be inactive after escape")
	}
}

func TestRenderOutlineEntryUsesRowBackgroundForSelectionWhenTinted(t *testing.T) {
	p := &pager{
		theme: tintTheme{
			highlightBG: "\033[48;2;1;2;3m",
		},
	}

	got := p.renderOutlineEntry(Heading{Level: 2, Text: "Alpha"}, true, 20)

	if strings.Contains(got, p.theme.highlightBG) {
		t.Fatalf("selected outline entry should not embed highlight background when row tint is available: %q", got)
	}
	if !strings.Contains(got, Bold) {
		t.Fatalf("selected outline entry should remain bold: %q", got)
	}
}

func TestParseSGRMouseEventScrollUp(t *testing.T) {
	ev, err := parseSGRMouseEvent("<64;12;5", 'M')
	if err != nil {
		t.Fatalf("parseSGRMouseEvent() returned error: %v", err)
	}
	mouse, ok := ev.(mouseEvent)
	if !ok {
		t.Fatalf("expected mouseEvent, got %#v", ev)
	}
	if dir, ok := mouse.verticalWheelDirection(); !ok || dir != -1 || mouse.col != 12 || mouse.row != 5 || !mouse.pressed {
		t.Fatalf("unexpected mouse event: %#v", mouse)
	}
}

func TestParseSGRMouseEventDetectsOptionModifier(t *testing.T) {
	ev, err := parseSGRMouseEvent("<8;12;5", 'M')
	if err != nil {
		t.Fatalf("parseSGRMouseEvent() returned error: %v", err)
	}
	mouse, ok := ev.(mouseEvent)
	if !ok {
		t.Fatalf("expected mouseEvent, got %#v", ev)
	}
	if !mouse.optionModified() || mouse.baseButton() != 0 {
		t.Fatalf("unexpected option-click event: %#v", mouse)
	}
}

func TestHandleMouseScrollsPager(t *testing.T) {
	p := &pager{
		topLine: 10,
		height:  8,
		lines:   make([]string, 50),
	}

	p.handleMouse(mouseEvent{button: 65, row: 1, col: 1, pressed: true})

	if p.topLine != 13 {
		t.Fatalf("topLine = %d, want 13", p.topLine)
	}
}

func TestHandleMouseScrollsOutlineSelection(t *testing.T) {
	p := &pager{
		width:  40,
		height: 12,
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 4},
			{Level: 2, Text: "Three", Line: 8},
		},
		lines: make([]string, 40),
		outline: outlineState{
			active:   true,
			filtered: []int{0, 1, 2},
			selected: 0,
		},
	}

	top, _, _, _ := p.outlinePanelRect()
	p.handleMouse(mouseEvent{button: 65, row: top, col: 1, pressed: true})

	if p.outline.selected != 1 {
		t.Fatalf("outline.selected = %d, want 1", p.outline.selected)
	}
}

func TestRenderLineAddsCopyIconToCodeBlockLeftGutter(t *testing.T) {
	bg := "\033[48;2;1;2;3m"
	p := &pager{
		lines:      []string{bg + "    foo" + Reset},
		plainLines: []string{"    foo"},
		codeBlocks: []renderCodeBlock{{line: 0, text: []byte("foo\n")}},
		theme:      tintTheme{codeBlockBG: bg},
	}

	got := p.renderLine(0)
	if stripANSI(got) != "⎘   foo" {
		t.Fatalf("stripANSI(renderLine(0)) = %q", stripANSI(got))
	}
	if !strings.Contains(got, bg+Dim+"⎘"+Reset+bg+"   foo") {
		t.Fatalf("copy icon or tinted background is misplaced: %q", got)
	}
}

func TestRenderLineKeepsSelectionHighlightAfterCopyIcon(t *testing.T) {
	p := &pager{
		lines:      []string{"    foo"},
		plainLines: []string{"    foo"},
		codeBlocks: []renderCodeBlock{{line: 0, text: []byte("foo\n")}},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 1},
			current: selectionCell{line: 0, col: 7},
		},
	}

	got := p.renderLine(0)
	if strings.Count(got, p.selectionHighlightStart()) < 2 {
		t.Fatalf("selection highlight was not restored after icon styling: %q", got)
	}
}

func TestCodeBlockButtonAccountsForScrollPosition(t *testing.T) {
	p := &pager{
		height:     8,
		topLine:    10,
		lines:      make([]string, 30),
		codeBlocks: []renderCodeBlock{{line: 12, text: []byte("foo\n")}},
	}

	block, ok := p.codeBlockButton(3, 1)
	if !ok || string(block.text) != "foo\n" {
		t.Fatalf("codeBlockButton() = %#v, %v", block, ok)
	}
	if _, ok := p.codeBlockButton(3, 2); ok {
		t.Fatal("codeBlockButton() matched outside the icon column")
	}
}

func TestHandleMouseCopiesCodeBlockWithOSC52(t *testing.T) {
	tty, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()

	p := &pager{
		tty:        tty,
		height:     8,
		lines:      []string{"    foo"},
		codeBlocks: []renderCodeBlock{{line: 0, text: []byte("foo\n")}},
	}
	p.handleMouse(mouseEvent{button: 0, row: 1, col: 1, pressed: true})

	if _, err := tty.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(tty)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;Zm9vCg==\x07"
	if string(got) != want {
		t.Fatalf("clipboard sequence = %q, want %q", got, want)
	}
	if p.notice != "Copied code block" || p.noticeIsError {
		t.Fatalf("notice = %q, error = %v", p.notice, p.noticeIsError)
	}
}

func TestFileLinkAtFindsRenderedFileLink(t *testing.T) {
	p := &pager{
		height: 8,
		lines: []string{
			"before " + OSC8Start("src/main.go:42") + "main" + fileLinkIcon + OSC8End + " after",
		},
	}

	if got, ok := p.fileLinkAt(1, 9); !ok || got != "src/main.go:42" {
		t.Fatalf("fileLinkAt() = %q, %v", got, ok)
	}
	if _, ok := p.fileLinkAt(1, 7); ok {
		t.Fatal("fileLinkAt() matched text outside the link")
	}
	if got, ok := p.fileLinkAt(1, 14); !ok || got != "src/main.go:42" {
		t.Fatalf("fileLinkAt(icon) = %q, %v", got, ok)
	}
}

func TestFileLinkAtFindsIconWrappedOntoNextLine(t *testing.T) {
	p := &pager{
		height: 8,
		lines: []string{
			OSC8Start("src/main.go") + "source ",
			"📄" + OSC8End,
		},
	}

	if got, ok := p.fileLinkAt(2, 1); !ok || got != "src/main.go" {
		t.Fatalf("fileLinkAt(wrapped icon) = %q, %v", got, ok)
	}
}

func TestFileLinkClickOpensWithIonInBackground(t *testing.T) {
	original := openFileInBackground
	defer func() { openFileInBackground = original }()
	var opened string
	openFileInBackground = func(target string) error {
		opened = target
		return nil
	}

	p := &pager{
		height: 8,
		lines:  []string{OSC8Start("README.md:12") + "README" + OSC8End},
	}
	p.handleMouse(mouseEvent{button: 0, row: 1, col: 2, pressed: true})
	p.handleMouse(mouseEvent{button: 0, row: 1, col: 5, pressed: false})

	if opened != "README.md:12" {
		t.Fatalf("opened %q, want README.md:12", opened)
	}
	if p.notice != "Opened README.md:12" || p.noticeIsError {
		t.Fatalf("notice = %q, error = %v", p.notice, p.noticeIsError)
	}
}

func TestHTTPLinkClickDoesNotOpenIon(t *testing.T) {
	original := openFileInBackground
	defer func() { openFileInBackground = original }()
	called := false
	openFileInBackground = func(string) error {
		called = true
		return nil
	}

	p := &pager{
		height: 8,
		lines:  []string{OSC8Start("https://example.com") + "example" + OSC8End},
	}
	p.handleMouse(mouseEvent{button: 0, row: 1, col: 2, pressed: true})
	p.handleMouse(mouseEvent{button: 0, row: 1, col: 2, pressed: false})

	if called {
		t.Fatal("HTTP link unexpectedly opened with ion")
	}
}

func TestSelectionBoundsNormalizesReverseDrag(t *testing.T) {
	p := &pager{
		plainLines: []string{"alpha", "beta"},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 1, col: 4},
			current: selectionCell{line: 0, col: 2},
		},
	}

	start, end, ok := p.selectionBounds()
	if !ok {
		t.Fatal("selectionBounds() ok = false, want true")
	}
	if start != (selectionPoint{line: 0, col: 1}) {
		t.Fatalf("start = %#v", start)
	}
	if end != (selectionPoint{line: 1, col: 4}) {
		t.Fatalf("end = %#v", end)
	}
}

func TestOptionMouseSelectionEnablesQuotedCopy(t *testing.T) {
	p := &pager{plainLines: []string{"alpha"}}

	p.handleSelectionMouse(mouseEvent{button: 8, row: 1, col: 1, pressed: true})

	if !p.selection.quoted {
		t.Fatal("Option-click did not enable quoted selection")
	}
}

func TestQuoteMarkdownPrefixesEveryLine(t *testing.T) {
	got := string(quoteMarkdown([]byte("alpha\n\nbeta\n")))
	want := "> alpha\n> \n> beta\n> "
	if got != want {
		t.Fatalf("quoteMarkdown() = %q, want %q", got, want)
	}
}

func TestSelectionMarkdownCopiesUnderlyingLinkMarkdown(t *testing.T) {
	source := []byte("[alpha](https://example.com)")
	p := &pager{
		source:     source,
		plainLines: []string{"alpha"},
		lineMappings: []renderLineMapping{{
			spans: []sourceSpan{
				{start: 0, end: len(source)},
				{start: 0, end: len(source)},
				{start: 0, end: len(source)},
				{start: 0, end: len(source)},
				{start: 0, end: len(source)},
			},
		}},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 2},
			current: selectionCell{line: 0, col: 4},
		},
	}

	if got := string(p.selectionMarkdown()); got != string(source) {
		t.Fatalf("selectionMarkdown() = %q, want %q", got, string(source))
	}
}

func TestSelectionMarkdownUsesRenderedLineMappingsForLink(t *testing.T) {
	source := []byte("[alpha](https://example.com)\n")
	result, err := RenderDocumentWithStyle(source, 80, true, RenderStyle{})
	if err != nil {
		t.Fatalf("RenderDocumentWithStyle() error = %v", err)
	}

	text := strings.TrimSuffix(result.Output, "\n")
	p := &pager{
		source:       source,
		lines:        strings.Split(text, "\n"),
		lineMappings: result.lineMappings,
		plainLines:   []string{stripANSI(text)},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 2},
			current: selectionCell{line: 0, col: 4},
		},
	}

	if got := string(p.selectionMarkdown()); got != "[alpha](https://example.com)" {
		t.Fatalf("selectionMarkdown() = %q", got)
	}
}

func TestSelectionMarkdownUsesRenderedLineMappingsForListItem(t *testing.T) {
	source := []byte("- [alpha](https://example.com)\n")
	result, err := RenderDocumentWithStyle(source, 80, true, RenderStyle{})
	if err != nil {
		t.Fatalf("RenderDocumentWithStyle() error = %v", err)
	}

	text := strings.TrimSuffix(result.Output, "\n")
	p := &pager{
		source:       source,
		lines:        strings.Split(text, "\n"),
		lineMappings: result.lineMappings,
		plainLines:   []string{stripANSI(text)},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 1},
			current: selectionCell{line: 0, col: 9},
		},
	}

	if got := string(p.selectionMarkdown()); got != "- [alpha](https://example.com)" {
		t.Fatalf("selectionMarkdown() = %q", got)
	}
}

func TestSelectionMarkdownPreservesSourceNewlinesBetweenLines(t *testing.T) {
	source := []byte("- alpha\n- beta\n")
	result, err := RenderDocumentWithStyle(source, 80, true, RenderStyle{})
	if err != nil {
		t.Fatalf("RenderDocumentWithStyle() error = %v", err)
	}

	text := strings.TrimSuffix(result.Output, "\n")
	p := &pager{
		source:       source,
		lines:        strings.Split(text, "\n"),
		lineMappings: result.lineMappings,
		plainLines:   []string{"  • alpha", "  • beta"},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 1},
			current: selectionCell{line: 1, col: 9},
		},
	}

	if got := string(p.selectionMarkdown()); got != "- alpha\n- beta" {
		t.Fatalf("selectionMarkdown() = %q", got)
	}
}

func TestSelectionMarkdownFallsBackToPlainText(t *testing.T) {
	p := &pager{
		plainLines: []string{"alpha beta"},
		lineMappings: []renderLineMapping{{
			spans: make([]sourceSpan, len([]rune("alpha beta"))),
		}},
		selection: selectionState{
			active:  true,
			anchor:  selectionCell{line: 0, col: 1},
			current: selectionCell{line: 0, col: 5},
		},
	}

	if got := string(p.selectionMarkdown()); got != "alpha" {
		t.Fatalf("selectionMarkdown() = %q, want %q", got, "alpha")
	}
}

func TestCurrentHeadingPath(t *testing.T) {
	p := &pager{
		topLine: 7,
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 3},
			{Level: 3, Text: "Three", Line: 6},
			{Level: 2, Text: "Four", Line: 10},
		},
	}

	got := p.currentHeadingPath()
	if len(got) != 3 {
		t.Fatalf("unexpected path length: %d", len(got))
	}
	if got[0].Text != "One" || got[1].Text != "Two" || got[2].Text != "Three" {
		t.Fatalf("unexpected path: %#v", got)
	}
}

func TestStatusSectionPath(t *testing.T) {
	p := &pager{
		cfg:     PagerConfig{Label: "test.md"},
		topLine: 7,
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 3},
			{Level: 3, Text: "Three", Line: 6},
		},
	}

	got := p.statusSectionPath()

	if !strings.Contains(got, "test.md: One › Two › ") {
		t.Fatalf("missing section path prefix: %q", got)
	}
	if !strings.Contains(got, Bold+"Three"+Reset) {
		t.Fatalf("current section should be bold: %q", got)
	}
}

func TestStatusSectionPathFittedTruncatesFromLeft(t *testing.T) {
	p := &pager{
		cfg: PagerConfig{Label: "very-long-file-name.md"},
		headings: []Heading{
			{Level: 1, Text: "Top", Line: 0},
			{Level: 2, Text: "Middle", Line: 3},
			{Level: 3, Text: "Innermost", Line: 6},
		},
		topLine: 7,
	}

	got := p.statusSectionPathFitted(18)

	if stripANSI(got) != "...dle › Innermost" {
		t.Fatalf("stripANSI(statusSectionPathFitted()) = %q", stripANSI(got))
	}
	if !strings.Contains(got, Bold+"Innermost"+Reset) {
		t.Fatalf("expected innermost heading to remain bold: %q", got)
	}
}

func TestStatusBarLeftFittedPreservesInnermostBreadcrumbWithSearch(t *testing.T) {
	p := &pager{
		cfg: PagerConfig{Label: "very-long-file-name.md"},
		headings: []Heading{
			{Level: 1, Text: "Top", Line: 0},
			{Level: 2, Text: "Middle", Line: 3},
			{Level: 3, Text: "Innermost", Line: 6},
		},
		topLine:       7,
		searchQuery:   "needle",
		searchMatches: []int{1, 5},
		searchIndex:   1,
	}

	got := p.statusBarLeftFitted(28)

	if !strings.Contains(stripANSI(got), "Innermost") {
		t.Fatalf("expected fitted left status to keep innermost breadcrumb: %q", stripANSI(got))
	}
	if !strings.Contains(stripANSI(got), "/needle 2/2") {
		t.Fatalf("expected fitted left status to keep search info: %q", stripANSI(got))
	}
}

func TestRenderStatusBarRightAlignsMetaAndOmitsLineNumbers(t *testing.T) {
	oldTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, time.March, 28, 12, 0, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = oldTimeNow
	}()

	p := &pager{
		width:         40,
		height:        8,
		topLine:       10,
		sourceModTime: time.Date(2026, time.March, 28, 11, 58, 0, 0, time.UTC),
		lines:         make([]string, 50),
		cfg:           PagerConfig{Label: "test.md"},
		headings: []Heading{
			{Level: 1, Text: "One", Line: 0},
			{Level: 2, Text: "Two", Line: 8},
		},
	}

	got := stripANSI(p.renderStatusBar())

	if strings.Contains(got, "/50") || strings.Contains(got, "11-") {
		t.Fatalf("status bar should not contain line numbers: %q", got)
	}
	if !strings.HasSuffix(got, "2m  23%") {
		t.Fatalf("status bar right side should be right aligned with mod time and percent, got %q", got)
	}
}

func TestFitToWidthPreservesANSI(t *testing.T) {
	input := "test.md: " + Bold + "VeryLongHeading" + Reset
	got := fitToWidth(input, 15)
	if visibleWidth(got) != 15 {
		t.Fatalf("visibleWidth(fitToWidth(...)) = %d, want 15", visibleWidth(got))
	}
	if !strings.Contains(got, Bold) {
		t.Fatalf("expected ANSI styling to be preserved: %q", got)
	}
	if !strings.HasSuffix(stripANSI(got), "...") {
		t.Fatalf("expected ellipsis in truncated output: %q", stripANSI(got))
	}
}

func TestVisibleWidthCountsButterflyAsTwoCells(t *testing.T) {
	if got := visibleWidth("Monarch 🦋"); got != 10 {
		t.Fatalf("visibleWidth() = %d, want 10", got)
	}
}

func TestFitToWidthAccountsForWideGlyphs(t *testing.T) {
	got := fitToWidth("123🦋", 4)
	if got != "1..." {
		t.Fatalf("fitToWidth() = %q, want %q", got, "1...")
	}
	if width := visibleWidth(got); width != 4 {
		t.Fatalf("fitted width = %d, want 4", width)
	}
}

func TestStatusBarWithWideHeadingDoesNotOverflow(t *testing.T) {
	p := &pager{
		width:  24,
		height: 8,
		lines:  []string{"heading"},
		cfg:    PagerConfig{Label: "README.md"},
		headings: []Heading{
			{Level: 1, Text: "Monarch 🦋", Line: 0},
		},
	}

	got := p.renderStatusBar()
	if width := visibleWidth(got); width != p.width {
		t.Fatalf("status bar width = %d, want %d: %q", width, p.width, stripANSI(got))
	}
}

func TestRenderTintedBlockPadsStyledTextToFullWidth(t *testing.T) {
	got := renderTintedBlock(Bold+"abc"+Reset, "", 5)
	if stripANSI(got) != "abc  " {
		t.Fatalf("stripANSI(renderTintedBlock(...)) = %q, want %q", stripANSI(got), "abc  ")
	}
}

func TestRenderTintedBlockReappliesTintAfterInnerReset(t *testing.T) {
	bg := "\033[48;2;1;2;3m"
	got := renderTintedBlock(Bold+"abc"+Reset, bg, 5)
	if !strings.Contains(got, Reset+bg+"  ") {
		t.Fatalf("expected background to be restored before padding, got %q", got)
	}
}
