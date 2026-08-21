package md

import (
	"bytes"
	"strings"
	"testing"
)

var testRenderStyle = RenderStyle{
	BlockquoteBG: "\033[48;5;238m",
	CodeBlockBG:  "\033[48;5;237m",
	HighlightBG:  "\033[48;5;250m",
}

func renderOpts(markdown string, width int, osc8 bool) string {
	var buf bytes.Buffer
	Render([]byte(markdown), &buf, width, osc8)
	return buf.String()
}

func renderStyledOpts(markdown string, width int, osc8 bool, style RenderStyle) string {
	var buf bytes.Buffer
	RenderWithStyle([]byte(markdown), &buf, width, osc8, style)
	return buf.String()
}

func render(markdown string) string {
	return renderOpts(markdown, 80, false)
}

func TestHeadingBold(t *testing.T) {
	out := render("# Hello World\n")
	if !strings.Contains(out, Bold) {
		t.Error("heading should contain bold escape")
	}
	if !strings.Contains(out, "Hello World") {
		t.Error("heading text missing")
	}
	if !strings.Contains(out, Reset) {
		t.Error("heading should reset after")
	}
}

func TestWideGlyphWrapsAtTerminalCellWidth(t *testing.T) {
	out := stripANSI(renderOpts("Monarch 🦋\n", 9, false))
	if !strings.Contains(out, "Monarch \n🦋") {
		t.Fatalf("wide glyph did not wrap at terminal width: %q", out)
	}
}

func TestBold(t *testing.T) {
	out := render("**bold text**\n")
	if !strings.Contains(out, Bold) {
		t.Error("bold text should contain bold escape")
	}
	if !strings.Contains(out, "bold text") {
		t.Error("bold text missing")
	}
}

func TestItalic(t *testing.T) {
	out := render("*italic text*\n")
	if !strings.Contains(out, Italic) {
		t.Error("italic text should contain italic escape")
	}
	if !strings.Contains(out, "italic text") {
		t.Error("italic text missing")
	}
}

func TestInlineCode(t *testing.T) {
	out := render("some `code here` text\n")
	if !strings.Contains(out, FgBlue) {
		t.Error("inline code should be blue")
	}
	if !strings.Contains(out, "code here") {
		t.Error("inline code text missing")
	}
}

func TestFencedCodeBlock(t *testing.T) {
	out := render("```\nfoo\nbar\n```\n")
	if strings.Contains(out, codeBlockCopyIcon) {
		t.Error("one-shot rendering should not contain the pager copy icon")
	}
	if !strings.Contains(out, "    foo") {
		t.Error("code block should be indented by 4 spaces")
	}
	if !strings.Contains(out, "    bar") {
		t.Error("code block should be indented by 4 spaces")
	}
}

func TestFencedCodeBlockTintsFullPageWidth(t *testing.T) {
	out := renderStyledOpts("```\nfoo\nbar\n```\n", 12, false, testRenderStyle)
	lines := strings.Split(out, "\n")
	for i, line := range lines[:2] {
		if !strings.HasPrefix(line, testRenderStyle.CodeBlockBG+"    ") {
			t.Fatalf("code line %d does not start with tint: %q", i, line)
		}
		if got := stripANSI(line); got != "    "+[]string{"foo", "bar"}[i]+"     " {
			t.Fatalf("plain code line %d = %q", i, got)
		}
	}
}

func TestFencedCodeBlockRecordsPagerCopyPayload(t *testing.T) {
	result, err := RenderDocument([]byte("```go\nfoo()\nbar()\n```\n"), 80, false)
	if err != nil {
		t.Fatalf("RenderDocument() error = %v", err)
	}
	if len(result.codeBlocks) != 1 {
		t.Fatalf("codeBlocks = %#v, want one block", result.codeBlocks)
	}
	if result.codeBlocks[0].line != 0 {
		t.Fatalf("code block line = %d, want 0", result.codeBlocks[0].line)
	}
	if got := string(result.codeBlocks[0].text); got != "foo()\nbar()\n" {
		t.Fatalf("code block text = %q", got)
	}
}

func TestFencedCodeBlockPlacesPagerCopyIconOnFirstLine(t *testing.T) {
	result, err := RenderDocument([]byte("```\none\ntwo\nthree\n```\n"), 80, false)
	if err != nil {
		t.Fatalf("RenderDocument() error = %v", err)
	}
	if len(result.codeBlocks) != 1 {
		t.Fatalf("codeBlocks = %#v, want one block", result.codeBlocks)
	}
	if result.codeBlocks[0].line != 0 {
		t.Fatalf("code block icon line = %d, want 0", result.codeBlocks[0].line)
	}
}

func TestFencedCodeBlockPagerLineAccountsForFrontMatter(t *testing.T) {
	result, err := RenderDocument([]byte("---\ntitle: Example\n---\n```\nfoo\n```\n"), 80, false)
	if err != nil {
		t.Fatalf("RenderDocument() error = %v", err)
	}
	if len(result.codeBlocks) != 1 {
		t.Fatalf("codeBlocks = %#v, want one block", result.codeBlocks)
	}
	if result.codeBlocks[0].line != 3 {
		t.Fatalf("code block line = %d, want 3", result.codeBlocks[0].line)
	}
}

func TestUnorderedList(t *testing.T) {
	out := render("- one\n- two\n- three\n")
	if !strings.Contains(out, "\u2022") {
		t.Error("unordered list should use bullet character")
	}
	if !strings.Contains(out, "one") {
		t.Error("list item text missing")
	}
}

func TestOrderedList(t *testing.T) {
	out := render("1. first\n2. second\n3. third\n")
	if !strings.Contains(out, "1.") {
		t.Error("ordered list should have numbered items")
	}
	if !strings.Contains(out, "2.") {
		t.Error("ordered list should have numbered items")
	}
}

func TestTable(t *testing.T) {
	md := `| Name | Age |
| ---- | --- |
| Alice | 30 |
| Bob | 25 |
`
	out := render(md)
	if !strings.Contains(out, "+") {
		t.Error("table should contain + corners")
	}
	if !strings.Contains(out, "|") {
		t.Error("table should contain | borders")
	}
	if !strings.Contains(out, "Alice") {
		t.Error("table cell text missing")
	}
}

func TestLink(t *testing.T) {
	out := render("[click here](https://example.com)\n")
	if !strings.Contains(out, "click here") {
		t.Error("link text missing")
	}
	if !strings.Contains(out, "https://example.com") {
		t.Error("link URL missing")
	}
	if !strings.Contains(out, FgBlue) {
		t.Error("link should be rendered in blue")
	}
	if !strings.Contains(out, Underline) {
		t.Error("link URL should be underlined")
	}
}

func TestThematicBreak(t *testing.T) {
	out := render("---\n")
	if !strings.Contains(out, "\u2500") {
		t.Error("thematic break should use horizontal line character")
	}
}

func TestFrontMatterRendersRawBeforeContent(t *testing.T) {
	out := render("---\ntitle: **Raw**\ntags:\n  - go\n---\n# Body\n")
	wantPrefix := "title: **Raw**\ntags:\n  - go\n" + Dim + strings.Repeat("\u2500", 80) + Reset + "\n\n"
	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("frontmatter prefix = %q, want prefix %q", out, wantPrefix)
	}
	if strings.Contains(out[:len(wantPrefix)], Bold) {
		t.Fatalf("frontmatter should not be markdown rendered: %q", out[:len(wantPrefix)])
	}
	if !strings.Contains(out[len(wantPrefix):], Bold+"Body") {
		t.Fatalf("markdown content after frontmatter should render normally: %q", out)
	}
}

func TestFrontMatterOffsetsHeadingLines(t *testing.T) {
	result, err := RenderDocument([]byte("---\ntitle: Raw\n---\n# Body\n"), 12, false)
	if err != nil {
		t.Fatalf("RenderDocument returned error: %v", err)
	}
	if len(result.Headings) != 1 {
		t.Fatalf("headings = %#v, want one heading", result.Headings)
	}
	if result.Headings[0].Line != 3 {
		t.Fatalf("heading line = %d, want 3", result.Headings[0].Line)
	}
}

func TestExtractHeadingsSkipsFrontMatter(t *testing.T) {
	headings, err := ExtractHeadings([]byte("---\ntitle: # Not Heading\n---\n# Body\n"))
	if err != nil {
		t.Fatalf("ExtractHeadings returned error: %v", err)
	}
	if len(headings) != 1 || headings[0].Text != "Body" {
		t.Fatalf("headings = %#v, want Body", headings)
	}
}

func TestNestedEmphasisInHeading(t *testing.T) {
	out := render("# Hello *world*\n")
	// After italic ends, bold should be restored
	if !strings.Contains(out, Bold) {
		t.Error("heading should be bold")
	}
	if !strings.Contains(out, Italic) {
		t.Error("italic within heading should be italic")
	}
	if !strings.Contains(out, "world") {
		t.Error("italic text missing")
	}
}

func TestWordWrapping(t *testing.T) {
	out := renderOpts("one two three four five six seven eight\n", 20, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("expected word wrapping to produce multiple lines, got %d lines: %q", len(lines), out)
	}
}

func TestLinkOSC8(t *testing.T) {
	out := renderOpts("[click here](https://example.com)\n", 80, true)
	if !strings.Contains(out, "click here") {
		t.Error("link text missing")
	}
	if !strings.Contains(out, OSC8Start("https://example.com")) {
		t.Error("OSC-8 start sequence missing")
	}
	if !strings.Contains(out, OSC8End) {
		t.Error("OSC-8 end sequence missing")
	}
	if strings.Contains(out, " (https://example.com)") {
		t.Error("OSC-8 link should not show URL in parentheses")
	}
	if !strings.Contains(out, Underline) {
		t.Error("OSC-8 link text should be underlined")
	}
	if !strings.Contains(out, FgBlue) {
		t.Error("OSC-8 link text should be blue")
	}
}

func TestFileLinkOSC8IncludesClickableFileIcon(t *testing.T) {
	out := renderOpts("[source](src/main.go:42)\n", 80, true)
	if !strings.Contains(stripANSI(out), "source"+fileLinkIcon) {
		t.Fatalf("file link icon missing: %q", out)
	}
	icon := strings.Index(out, strings.TrimSpace(fileLinkIcon))
	end := strings.Index(out, OSC8End)
	if icon < 0 || end < 0 || icon > end {
		t.Fatalf("file icon is not inside the OSC-8 link: %q", out)
	}
}

func TestWebLinkOSC8DoesNotIncludeFileIcon(t *testing.T) {
	out := renderOpts("[website](https://example.com)\n", 80, true)
	if strings.Contains(out, strings.TrimSpace(fileLinkIcon)) {
		t.Fatalf("web link unexpectedly includes file icon: %q", out)
	}
}

func TestTaskCheckBoxUnchecked(t *testing.T) {
	out := render("- [ ] todo item\n")
	if !strings.Contains(out, "\u2610") {
		t.Error("unchecked checkbox should use ☐ character")
	}
	if strings.Contains(out, "\u2022") {
		t.Error("task list item should not render a bullet marker")
	}
	if !strings.Contains(out, "todo item") {
		t.Error("checkbox text missing")
	}
	if strings.Contains(out, "<input") {
		t.Error("checkbox should not render as HTML input")
	}
}

func TestTaskCheckBoxChecked(t *testing.T) {
	out := render("- [x] done item\n")
	if !strings.Contains(out, "\u2611") {
		t.Error("checked checkbox should use ☑ character")
	}
	if strings.Contains(out, "\u2022") {
		t.Error("task list item should not render a bullet marker")
	}
	if !strings.Contains(out, "done item") {
		t.Error("checkbox text missing")
	}
	if strings.Contains(out, "<input") {
		t.Error("checkbox should not render as HTML input")
	}
}

func TestBlockquote(t *testing.T) {
	out := renderStyledOpts("> hello world\n", 80, false, testRenderStyle)
	if strings.Contains(out, "█") {
		t.Error("blockquote should not contain block bar character")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("blockquote text missing")
	}
	if !strings.Contains(out, testRenderStyle.BlockquoteBG+" "+Reset+" ") {
		t.Error("blockquote should use tinted whitespace")
	}
}

func TestBlockquoteWrapping(t *testing.T) {
	out := renderStyledOpts("> one two three four five six seven eight\n", 20, false, testRenderStyle)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if !strings.Contains(line, testRenderStyle.BlockquoteBG+" "+Reset+" ") {
			t.Errorf("wrapped blockquote line should contain tinted indent: %q", line)
		}
	}
}

func TestNestedBlockquote(t *testing.T) {
	out := renderStyledOpts("> > nested\n", 80, false, testRenderStyle)
	if strings.Count(out, testRenderStyle.BlockquoteBG+" "+Reset) < 2 {
		t.Error("nested blockquote should have two tinted indent segments")
	}
	if !strings.Contains(out, "nested") {
		t.Error("nested blockquote text missing")
	}
}

func TestMarkHighlight(t *testing.T) {
	out := renderStyledOpts("some ==important== text\n", 80, false, testRenderStyle)
	if !strings.Contains(out, testRenderStyle.HighlightBG) {
		t.Error("highlight should contain background tint")
	}
	if !strings.Contains(out, "important") {
		t.Error("highlight text missing")
	}
}

func TestAutoLinkOSC8(t *testing.T) {
	out := renderOpts("<https://example.com>\n", 80, true)
	if !strings.Contains(out, "https://example.com") {
		t.Error("autolink URL missing")
	}
	if !strings.Contains(out, OSC8Start("https://example.com")) {
		t.Error("OSC-8 start sequence missing")
	}
	if !strings.Contains(out, OSC8End) {
		t.Error("OSC-8 end sequence missing")
	}
	if !strings.Contains(out, Underline) {
		t.Error("OSC-8 autolink should be underlined")
	}
	if !strings.Contains(out, FgBlue) {
		t.Error("OSC-8 autolink should be blue")
	}
}
