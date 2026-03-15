package md

import (
	"bytes"
	"strings"
	"testing"
)

func renderOpts(markdown string, width int, osc8 bool) string {
	var buf bytes.Buffer
	Render([]byte(markdown), &buf, width, osc8)
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
	if !strings.Contains(out, "    foo") {
		t.Error("code block should be indented by 4 spaces")
	}
	if !strings.Contains(out, "    bar") {
		t.Error("code block should be indented by 4 spaces")
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

func TestTaskCheckBoxUnchecked(t *testing.T) {
	out := render("- [ ] todo item\n")
	if !strings.Contains(out, "\u2610") {
		t.Error("unchecked checkbox should use ☐ character")
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
	if !strings.Contains(out, "done item") {
		t.Error("checkbox text missing")
	}
	if strings.Contains(out, "<input") {
		t.Error("checkbox should not render as HTML input")
	}
}

func TestBlockquote(t *testing.T) {
	out := render("> hello world\n")
	if !strings.Contains(out, "█") {
		t.Error("blockquote should contain block bar character")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("blockquote text missing")
	}
	if !strings.Contains(out, Dim) {
		t.Error("blockquote bar should be dimmed")
	}
}

func TestBlockquoteWrapping(t *testing.T) {
	out := renderOpts("> one two three four five six seven eight\n", 20, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if !strings.Contains(line, "█") {
			t.Errorf("wrapped blockquote line should contain bar: %q", line)
		}
	}
}

func TestNestedBlockquote(t *testing.T) {
	out := render("> > nested\n")
	if strings.Count(out, "█") < 2 {
		t.Error("nested blockquote should have two bar characters")
	}
	if !strings.Contains(out, "nested") {
		t.Error("nested blockquote text missing")
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
