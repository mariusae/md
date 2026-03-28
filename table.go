package md

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// renderTable handles the entire table by manually walking the AST subtree.
// It returns WalkSkipChildren so goldmark doesn't call per-row/cell renderers.
func (r *AnsiRenderer) renderTable(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	table := node.(*east.Table)
	alignments := table.Alignments

	// Extract all rows as text.
	var rows [][]string
	var headerIdx int = -1
	rowIdx := 0
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		var row []string
		for cell := child.FirstChild(); cell != nil; cell = cell.NextSibling() {
			row = append(row, extractText(cell, source))
		}
		rows = append(rows, row)
		if _, ok := child.(*east.TableHeader); ok {
			headerIdx = rowIdx
		}
		rowIdx++
	}

	if len(rows) == 0 {
		return ast.WalkSkipChildren, nil
	}

	// Determine number of columns and compute widths.
	numCols := len(alignments)
	if numCols == 0 {
		for _, row := range rows {
			if len(row) > numCols {
				numCols = len(row)
			}
		}
	}
	widths := make([]int, numCols)
	for _, row := range rows {
		for i, cell := range row {
			if i < numCols && utf8.RuneCountInString(cell) > widths[i] {
				widths[i] = utf8.RuneCountInString(cell)
			}
		}
	}
	// Ensure minimum column width of 3.
	for i := range widths {
		if widths[i] < 3 {
			widths[i] = 3
		}
	}

	r.writeSeparator(w, widths)
	for i, row := range rows {
		r.writeRow(w, row, widths, alignments)
		if i == headerIdx || i == len(rows)-1 {
			r.writeSeparator(w, widths)
		}
	}
	r.writeString(w, "\n")
	r.col = 0

	return ast.WalkSkipChildren, nil
}

func (r *AnsiRenderer) writeSeparator(w util.BufWriter, widths []int) {
	r.writeString(w, "+")
	for _, cw := range widths {
		r.writeString(w, strings.Repeat("-", cw+2))
		r.writeString(w, "+")
	}
	r.writeString(w, "\n")
}

func (r *AnsiRenderer) writeRow(w util.BufWriter, row []string, widths []int, alignments []east.Alignment) {
	r.writeString(w, "|")
	for i, cw := range widths {
		var cell string
		if i < len(row) {
			cell = row[i]
		}
		var align east.Alignment
		if i < len(alignments) {
			align = alignments[i]
		}
		r.writeString(w, " ")
		r.writeString(w, alignCell(cell, cw, align))
		r.writeString(w, " |")
	}
	r.writeString(w, "\n")
}

func alignCell(text string, width int, align east.Alignment) string {
	textLen := utf8.RuneCountInString(text)
	pad := width - textLen
	if pad < 0 {
		pad = 0
	}
	switch align {
	case east.AlignRight:
		return fmt.Sprintf("%s%s", strings.Repeat(" ", pad), text)
	case east.AlignCenter:
		left := pad / 2
		right := pad - left
		return fmt.Sprintf("%s%s%s", strings.Repeat(" ", left), text, strings.Repeat(" ", right))
	default: // AlignLeft, AlignNone
		return fmt.Sprintf("%s%s", text, strings.Repeat(" ", pad))
	}
}

func extractText(node ast.Node, source []byte) string {
	var buf strings.Builder
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Text:
			buf.Write(v.Value(source))
		case *ast.String:
			buf.Write(v.Value)
		case *ast.CodeSpan:
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					buf.Write(t.Value(source))
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return buf.String()
}

// These are no-ops because renderTable handles everything via WalkSkipChildren.
func (r *AnsiRenderer) renderTableHeader(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderTableRow(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *AnsiRenderer) renderTableCell(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

// Ensure AnsiRenderer implements NodeRenderer.
var _ renderer.NodeRenderer = (*AnsiRenderer)(nil)
