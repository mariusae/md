package md

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type Mark struct {
	ast.BaseInline
}

var KindMark = ast.NewNodeKind("Mark")

func (n *Mark) Kind() ast.NodeKind {
	return KindMark
}

func (n *Mark) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func NewMark() *Mark {
	return &Mark{}
}

type markDelimiterProcessor struct{}

func (p *markDelimiterProcessor) IsDelimiter(b byte) bool {
	return b == '='
}

func (p *markDelimiterProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (p *markDelimiterProcessor) OnMatch(consumes int) ast.Node {
	return NewMark()
}

var defaultMarkDelimiterProcessor = &markDelimiterProcessor{}

type markParser struct{}

func (p *markParser) Trigger() []byte {
	return []byte{'='}
}

func (p *markParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := parser.ScanDelimiter(line, before, 1, defaultMarkDelimiterProcessor)
	if node == nil || node.OriginalLength != 2 || before == '=' {
		return nil
	}

	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

func (p *markParser) CloseBlock(parent ast.Node, pc parser.Context) {}

type markExtender struct{}

var MarkExtension = &markExtender{}

func (e *markExtender) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(&markParser{}, 500),
	))
}
