package md

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type Heading struct {
	Level int
	Text  string
	Line  int
}

type RenderResult struct {
	Output       string
	Headings     []Heading
	lineMappings []renderLineMapping
}

type sourceSpan struct {
	start int
	end   int
}

func (s sourceSpan) valid() bool {
	return s.end > s.start
}

type renderLineMapping struct {
	spans []sourceSpan
}

// Render converts markdown source to ANSI-formatted text written to w.
// width is the terminal width for word wrapping; osc8 enables OSC-8 hyperlinks.
func Render(source []byte, w io.Writer, width int, osc8 bool) error {
	return RenderWithStyle(source, w, width, osc8, RenderStyle{})
}

func RenderWithStyle(source []byte, w io.Writer, width int, osc8 bool, style RenderStyle) error {
	result, err := RenderDocumentWithStyle(source, width, osc8, style)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, result.Output)
	return err
}

func RenderDocument(source []byte, width int, osc8 bool) (RenderResult, error) {
	return RenderDocumentWithStyle(source, width, osc8, RenderStyle{})
}

func RenderDocumentWithStyle(source []byte, width int, osc8 bool, style RenderStyle) (RenderResult, error) {
	ansiRenderer := NewAnsiRenderer(width, osc8, style)
	gm := goldmark.New(
		goldmark.WithExtensions(extension.GFM, MarkExtension),
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(
					util.Prioritized(ansiRenderer, 1),
				),
			),
		),
	)
	var buf bytes.Buffer
	if err := gm.Convert(source, &buf); err != nil {
		return RenderResult{}, err
	}
	return RenderResult{
		Output:       buf.String(),
		Headings:     append([]Heading(nil), ansiRenderer.headings...),
		lineMappings: append([]renderLineMapping(nil), ansiRenderer.lineMappings...),
	}, nil
}

func ExtractHeadings(source []byte) ([]Heading, error) {
	gm := goldmark.New(goldmark.WithExtensions(extension.GFM, MarkExtension))
	doc := gm.Parser().Parse(text.NewReader(source))
	var headings []Heading
	if err := ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Kind() != ast.KindHeading {
			return ast.WalkContinue, nil
		}
		n := node.(*ast.Heading)
		headings = append(headings, Heading{
			Level: n.Level,
			Text:  strings.TrimSpace(extractText(node, source)),
			Line:  -1,
		})
		return ast.WalkContinue, nil
	}); err != nil {
		return nil, err
	}
	return headings, nil
}

type style struct {
	bold       bool
	italic     bool
	underline  bool
	color      string
	background string
}

type AnsiRenderer struct {
	styles          []style
	listDepth       int
	orderedIndex    []int
	indentStack     []int // saved indent levels for nested lists
	line            int
	width           int  // terminal width for word wrapping
	col             int  // current column position
	indent          int  // current indentation level (in characters)
	blockquoteDepth int  // nesting depth of blockquotes
	osc8            bool // emit OSC-8 hyperlink sequences
	headings        []Heading
	renderStyle     RenderStyle
	lineMappings    []renderLineMapping
	spanStack       []sourceSpan
}

func NewAnsiRenderer(width int, osc8 bool, style RenderStyle) *AnsiRenderer {
	return &AnsiRenderer{width: width, osc8: osc8, renderStyle: style}
}

func (r *AnsiRenderer) pushStyle(s style, w util.BufWriter) {
	r.styles = append(r.styles, s)
	r.applyCurrentStyle(w)
}

func (r *AnsiRenderer) popStyle(w util.BufWriter) {
	if len(r.styles) > 0 {
		r.styles = r.styles[:len(r.styles)-1]
	}
	r.writeString(w, Reset)
	r.applyCurrentStyle(w)
}

func (r *AnsiRenderer) pushSourceSpan(span sourceSpan) {
	r.spanStack = append(r.spanStack, span)
}

func (r *AnsiRenderer) popSourceSpan() {
	if len(r.spanStack) == 0 {
		return
	}
	r.spanStack = r.spanStack[:len(r.spanStack)-1]
}

func (r *AnsiRenderer) currentSourceSpan() sourceSpan {
	for i := len(r.spanStack) - 1; i >= 0; i-- {
		if r.spanStack[i].valid() {
			return r.spanStack[i]
		}
	}
	return sourceSpan{}
}

func (r *AnsiRenderer) applyCurrentStyle(w util.BufWriter) {
	var bold, italic, underline bool
	var color, background string
	for _, s := range r.styles {
		if s.bold {
			bold = true
		}
		if s.italic {
			italic = true
		}
		if s.underline {
			underline = true
		}
		if s.color != "" {
			color = s.color
		}
		if s.background != "" {
			background = s.background
		}
	}
	if bold {
		r.writeString(w, Bold)
	}
	if italic {
		r.writeString(w, Italic)
	}
	if underline {
		r.writeString(w, Underline)
	}
	if color != "" {
		r.writeString(w, color)
	}
	if background != "" {
		r.writeString(w, background)
	}
}

// writeWrapped writes text with word wrapping at the terminal width.
// It respects the current indentation level and column position.
func (r *AnsiRenderer) writeWrapped(w util.BufWriter, text string) {
	if r.width <= 0 {
		r.writeString(w, text)
		return
	}

	words := splitWords(text)
	for _, word := range words {
		wlen := len(word)
		if wlen == 0 {
			continue
		}

		isSpace := len(word) > 0 && unicode.IsSpace([]rune(word)[0])

		// Emit indent (with blockquote bars) at the start of a new line.
		if r.col == 0 && r.indent > 0 {
			r.writeIndent(w)
			r.applyCurrentStyle(w)
		}

		// If this word would exceed the line, wrap.
		if r.col > r.indent && r.col+wlen > r.width {
			r.writeString(w, Reset)
			r.writeString(w, "\n")
			r.col = 0
			r.writeIndent(w)
			r.applyCurrentStyle(w)
			// Skip whitespace at the start of a wrapped line.
			if isSpace {
				continue
			}
		}

		// Don't emit whitespace at the very start of a line (after indent).
		if isSpace && r.col == r.indent {
			continue
		}

		r.writeString(w, word)
		r.col += wlen
	}
}

// splitWords splits text into tokens preserving whitespace as separate tokens.
func splitWords(text string) []string {
	var tokens []string
	i := 0
	runes := []rune(text)
	for i < len(runes) {
		if unicode.IsSpace(runes[i]) {
			j := i
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		} else {
			j := i
			for j < len(runes) && !unicode.IsSpace(runes[j]) {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		}
	}
	return tokens
}

func (r *AnsiRenderer) writeNewline(w util.BufWriter) {
	r.writeString(w, "\n")
	r.col = 0
}

func (r *AnsiRenderer) writeIndent(w util.BufWriter) {
	if r.blockquoteDepth > 0 {
		for i := 0; i < r.blockquoteDepth; i++ {
			if r.renderStyle.BlockquoteBG != "" {
				r.writeString(w, r.renderStyle.BlockquoteBG)
				r.writeString(w, " ")
				r.writeString(w, Reset)
			} else {
				r.writeString(w, " ")
			}
		}
		remaining := r.indent - r.blockquoteDepth
		if remaining > 0 {
			r.writeString(w, strings.Repeat(" ", remaining))
		}
		r.col = r.indent
	} else if r.indent > 0 {
		r.writeString(w, strings.Repeat(" ", r.indent))
		r.col = r.indent
	}
}

func (r *AnsiRenderer) writeString(w util.BufWriter, s string) {
	w.WriteString(s)
	r.recordOutput(s, nil)
}

func (r *AnsiRenderer) writeBytes(w util.BufWriter, b []byte) {
	w.Write(b)
	r.recordOutput(string(b), nil)
}

func (r *AnsiRenderer) writeMappedString(w util.BufWriter, s string, spans []sourceSpan) {
	w.WriteString(s)
	r.recordOutput(s, spans)
}

func (r *AnsiRenderer) recordOutput(s string, spans []sourceSpan) {
	if len(r.lineMappings) == 0 {
		r.lineMappings = append(r.lineMappings, renderLineMapping{})
	}

	spanIdx := 0
	defaultSpan := r.currentSourceSpan()
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			seq, _, next := consumeEscapeSequence(s, i)
			if seq == "" || next <= i {
				break
			}
			i = next
			continue
		}

		rn, size := utf8.DecodeRuneInString(s[i:])
		if rn == '\n' {
			r.line++
			r.lineMappings = append(r.lineMappings, renderLineMapping{})
			i += size
			continue
		}

		span := defaultSpan
		if spanIdx < len(spans) {
			span = spans[spanIdx]
		}
		r.lineMappings[len(r.lineMappings)-1].spans = append(r.lineMappings[len(r.lineMappings)-1].spans, span)
		spanIdx++
		i += size
	}
}

func (r *AnsiRenderer) writeWrappedMapped(w util.BufWriter, text string, spans []sourceSpan) {
	if r.width <= 0 {
		r.writeMappedString(w, text, spans)
		return
	}

	tokens := splitMappedTokens(text, spans)
	for _, token := range tokens {
		if token.width == 0 {
			continue
		}

		if r.col == 0 && r.indent > 0 {
			r.writeIndent(w)
			r.applyCurrentStyle(w)
		}

		if r.col > r.indent && r.col+token.width > r.width {
			r.writeString(w, Reset)
			r.writeString(w, "\n")
			r.col = 0
			r.writeIndent(w)
			r.applyCurrentStyle(w)
			if token.space {
				continue
			}
		}

		if token.space && r.col == r.indent {
			continue
		}

		r.writeMappedString(w, token.text, token.spans)
		r.col += token.width
	}
}

type mappedToken struct {
	text  string
	spans []sourceSpan
	space bool
	width int
}

func splitMappedTokens(text string, spans []sourceSpan) []mappedToken {
	var tokens []mappedToken
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	for i := 0; i < len(runes); {
		space := unicode.IsSpace(runes[i])
		j := i + 1
		for j < len(runes) && unicode.IsSpace(runes[j]) == space {
			j++
		}
		token := mappedToken{
			text:  string(runes[i:j]),
			space: space,
			width: j - i,
		}
		if i < len(spans) {
			end := min(j, len(spans))
			token.spans = append(token.spans, spans[i:end]...)
		}
		tokens = append(tokens, token)
		i = j
	}
	return tokens
}

func segmentRuneSpans(segment text.Segment, source []byte) []sourceSpan {
	data := segment.Value(source)
	spans := make([]sourceSpan, 0, utf8.RuneCount(data))
	offset := segment.Start
	for len(data) > 0 {
		_, size := utf8.DecodeRune(data)
		spans = append(spans, sourceSpan{start: offset, end: offset + size})
		offset += size
		data = data[size:]
	}
	return spans
}

func blockSourceSpan(source []byte, node ast.Node) sourceSpan {
	span := sourceSpan{}
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Type() != ast.TypeBlock && n.Type() != ast.TypeDocument {
			return ast.WalkContinue, nil
		}
		lines := n.Lines()
		if lines == nil || lines.Len() == 0 {
			return ast.WalkContinue, nil
		}
		first := lines.At(0)
		last := lines.At(lines.Len() - 1)
		start := first.Start
		for start > 0 && source[start-1] != '\n' {
			start--
		}
		if !span.valid() || start < span.start {
			span.start = start
		}
		if last.Stop > span.end {
			span.end = last.Stop
		}
		return ast.WalkContinue, nil
	})
	return span
}

func inlineTextBounds(node ast.Node) sourceSpan {
	span := sourceSpan{}
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		textNode, ok := n.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		seg := textNode.Segment
		if !span.valid() || seg.Start < span.start {
			span.start = seg.Start
		}
		if seg.Stop > span.end {
			span.end = seg.Stop
		}
		return ast.WalkContinue, nil
	})
	return span
}

func expandDelimitedSpan(source []byte, span sourceSpan, markers string) sourceSpan {
	if !span.valid() {
		return span
	}
	for span.start > 0 && strings.ContainsRune(markers, rune(source[span.start-1])) {
		span.start--
	}
	for span.end < len(source) && strings.ContainsRune(markers, rune(source[span.end])) {
		span.end++
	}
	return span
}

func expandAngleSpan(source []byte, span sourceSpan) sourceSpan {
	if !span.valid() {
		return span
	}
	if span.start > 0 && source[span.start-1] == '<' {
		span.start--
	}
	if span.end < len(source) && source[span.end] == '>' {
		span.end++
	}
	return span
}

func expandLinkSpan(source []byte, span sourceSpan, image bool) sourceSpan {
	if !span.valid() {
		return span
	}

	start := span.start
	for i := span.start - 1; i >= 0 && source[i] != '\n'; i-- {
		if source[i] == '[' {
			start = i
			if image && i > 0 && source[i-1] == '!' {
				start = i - 1
			}
			break
		}
	}

	end := span.end
	for i := span.end; i < len(source) && source[i] != '\n'; i++ {
		if source[i] != ']' {
			continue
		}
		end = i + 1
		if i+1 >= len(source) {
			break
		}
		switch source[i+1] {
		case '(':
			depth := 1
			for j := i + 2; j < len(source); j++ {
				switch source[j] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						end = j + 1
						return sourceSpan{start: start, end: end}
					}
				case '\n':
					return sourceSpan{start: start, end: end}
				}
			}
		case '[':
			for j := i + 2; j < len(source); j++ {
				if source[j] == ']' {
					end = j + 1
					return sourceSpan{start: start, end: end}
				}
				if source[j] == '\n' {
					return sourceSpan{start: start, end: end}
				}
			}
		}
		return sourceSpan{start: start, end: end}
	}

	return sourceSpan{start: start, end: end}
}

func inlineSourceSpan(source []byte, node ast.Node) sourceSpan {
	span := inlineTextBounds(node)
	if !span.valid() {
		return span
	}

	switch node.Kind() {
	case ast.KindEmphasis:
		return expandDelimitedSpan(source, span, "*_")
	case KindMark:
		return expandDelimitedSpan(source, span, "=")
	case ast.KindCodeSpan:
		return expandDelimitedSpan(source, span, "`")
	case ast.KindLink:
		return expandLinkSpan(source, span, false)
	case ast.KindImage:
		return expandLinkSpan(source, span, true)
	case ast.KindAutoLink:
		return expandAngleSpan(source, span)
	default:
		return span
	}
}

func (r *AnsiRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	// Block nodes
	reg.Register(ast.KindDocument, r.renderDocument)
	reg.Register(ast.KindHeading, r.renderHeading)
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindList, r.renderList)
	reg.Register(ast.KindListItem, r.renderListItem)
	reg.Register(ast.KindThematicBreak, r.renderThematicBreak)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(ast.KindTextBlock, r.renderTextBlock)

	// Inline nodes
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(KindMark, r.renderMark)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)

	// Extension nodes
	reg.Register(east.KindTable, r.renderTable)
	reg.Register(east.KindTableHeader, r.renderTableHeader)
	reg.Register(east.KindTableRow, r.renderTableRow)
	reg.Register(east.KindTableCell, r.renderTableCell)
	reg.Register(east.KindTaskCheckBox, r.renderTaskCheckBox)
}

func (r *AnsiRenderer) renderDocument(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderHeading(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.Heading)
		r.pushSourceSpan(blockSourceSpan(source, node))
		r.headings = append(r.headings, Heading{
			Level: n.Level,
			Text:  strings.TrimSpace(extractText(node, source)),
			Line:  r.line,
		})
		r.pushStyle(style{bold: true}, w)
	} else {
		r.popStyle(w)
		r.popSourceSpan()
		r.writeNewline(w)
		r.writeNewline(w)
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderParagraph(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		r.writeNewline(w)
		if r.blockquoteDepth > 0 && node.NextSibling() != nil {
			r.writeIndent(w)
		}
		r.writeNewline(w)
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		lines := node.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			r.writeString(w, "    ")
			r.writeBytes(w, line.Value(source))
		}
		r.popSourceSpan()
		r.writeNewline(w)
		r.col = 0
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		lines := node.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			r.writeString(w, "    ")
			r.writeBytes(w, line.Value(source))
		}
		r.popSourceSpan()
		r.writeNewline(w)
		r.col = 0
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderBlockquote(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		r.blockquoteDepth++
		r.indent += 2
	} else {
		r.blockquoteDepth--
		r.indent -= 2
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderList(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.List)
	if entering {
		r.listDepth++
		if n.IsOrdered() {
			r.orderedIndex = append(r.orderedIndex, n.Start)
		} else {
			r.orderedIndex = append(r.orderedIndex, -1)
		}
	} else {
		r.listDepth--
		if len(r.orderedIndex) > 0 {
			r.orderedIndex = r.orderedIndex[:len(r.orderedIndex)-1]
		}
		if r.listDepth == 0 {
			r.writeNewline(w)
		}
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderListItem(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		r.indentStack = append(r.indentStack, r.indent)
		indent := strings.Repeat("  ", r.listDepth-1)
		idx := r.orderedIndex[len(r.orderedIndex)-1]
		if idx < 0 {
			prefix := indent + "  \u2022 "
			if isTaskListItem(node) {
				prefix = indent + "    "
			}
			r.writeString(w, prefix)
			r.col = len([]rune(prefix))
			r.indent = len([]rune(prefix))
		} else {
			prefix := fmt.Sprintf("%s  %d. ", indent, idx)
			r.writeString(w, prefix)
			r.col = len(prefix)
			r.indent = len(prefix)
			r.orderedIndex[len(r.orderedIndex)-1] = idx + 1
		}
	} else {
		if len(r.indentStack) > 0 {
			r.indent = r.indentStack[len(r.indentStack)-1]
			r.indentStack = r.indentStack[:len(r.indentStack)-1]
		} else {
			r.indent = 0
		}
		r.popSourceSpan()
		r.writeNewline(w)
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderTaskCheckBox(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*east.TaskCheckBox)
		if n.IsChecked {
			r.writeWrapped(w, "\u2611 ") // ☑
		} else {
			r.writeWrapped(w, "\u2610 ") // ☐
		}
		r.indent += 2
	}
	return ast.WalkContinue, nil
}

func isTaskListItem(node ast.Node) bool {
	textBlock := node.FirstChild()
	if textBlock == nil || textBlock.Kind() != ast.KindTextBlock {
		return false
	}
	firstInline := textBlock.FirstChild()
	return firstInline != nil && firstInline.Kind() == east.KindTaskCheckBox
}

func (r *AnsiRenderer) renderThematicBreak(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		hrWidth := r.width
		if hrWidth <= 0 {
			hrWidth = 40
		}
		r.writeString(w, strings.Repeat("\u2500", hrWidth))
		r.popSourceSpan()
		r.writeNewline(w)
		r.writeNewline(w)
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(blockSourceSpan(source, node))
		lines := node.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			r.writeBytes(w, line.Value(source))
		}
		r.popSourceSpan()
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderTextBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		// If this text block has a next sibling (e.g. a nested list),
		// we need a newline between them.
		if node.NextSibling() != nil {
			r.writeNewline(w)
		}
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Text)
	text := string(n.Value(source))
	if span := r.currentSourceSpan(); span.valid() {
		r.writeWrapped(w, text)
	} else {
		r.writeWrappedMapped(w, text, segmentRuneSpans(n.Segment, source))
	}
	if n.HardLineBreak() {
		r.writeNewline(w)
		r.writeIndent(w)
	} else if n.SoftLineBreak() {
		breakSpan := sourceSpan{start: n.Segment.Stop, end: min(len(source), n.Segment.Stop+1)}
		r.writeWrappedMapped(w, " ", []sourceSpan{breakSpan})
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderString(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.String)
	r.writeWrapped(w, string(n.Value))
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderCodeSpan(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		r.pushStyle(style{color: FgBlue}, w)
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				r.writeWrapped(w, string(t.Value(source)))
			}
		}
		r.popStyle(w)
		r.popSourceSpan()
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderEmphasis(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Emphasis)
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		if n.Level == 2 {
			r.pushStyle(style{bold: true}, w)
		} else {
			r.pushStyle(style{italic: true}, w)
		}
	} else {
		r.popStyle(w)
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderMark(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		r.pushStyle(style{background: r.renderStyle.HighlightBG}, w)
	} else {
		r.popStyle(w)
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		if r.osc8 {
			r.writeString(w, OSC8Start(string(n.Destination)))
			r.pushStyle(style{color: FgBlue, underline: true}, w)
		} else {
			r.pushStyle(style{color: FgBlue}, w)
		}
	} else {
		if r.osc8 {
			r.popStyle(w)
			r.writeString(w, OSC8End)
		} else {
			r.writeWrapped(w, " (")
			r.pushStyle(style{underline: true}, w)
			r.writeWrapped(w, string(n.Destination))
			r.popStyle(w)
			r.writeWrapped(w, ")")
			r.popStyle(w)
		}
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.AutoLink)
		url := string(n.URL(source))
		r.pushSourceSpan(inlineSourceSpan(source, node))
		if r.osc8 {
			r.writeString(w, OSC8Start(url))
			r.pushStyle(style{color: FgBlue, underline: true}, w)
			r.writeWrapped(w, url)
			r.popStyle(w)
			r.writeString(w, OSC8End)
		} else {
			r.writeWrapped(w, url)
		}
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		r.writeString(w, "[image: ")
	} else {
		r.writeString(w, "]")
		r.popSourceSpan()
	}
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.pushSourceSpan(inlineSourceSpan(source, node))
		n := node.(*ast.RawHTML)
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			r.writeBytes(w, seg.Value(source))
		}
		r.popSourceSpan()
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}
