package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

func renderOpts(markdown string, width int, osc8 bool) string {
	var buf bytes.Buffer
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(
					util.Prioritized(NewAnsiRenderer(width, osc8), 1),
				),
			),
		),
	)
	md.Convert([]byte(markdown), &buf)
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
