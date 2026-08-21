// Package grokmermaid renders Mermaid source as Unicode box-drawing art.
//
// The embedded renderer is the Apache-2.0-licensed grok-mermaid WebAssembly
// build extracted from xai-org/grok-build by Simon Willison. Keeping the
// original renderer intact preserves its parsing, layout, width limits, and
// fallback behavior exactly. Node.js supplies the WebAssembly host because
// the Go standard library does not include a native WebAssembly runtime.
package grokmermaid

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"html"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

//go:embed renderer.wasm
var rendererWASM []byte

type Class string

const (
	ClassNone      Class = ""
	ClassBorder    Class = "b"
	ClassNodeText  Class = "n"
	ClassEdge      Class = "e"
	ClassEdgeLabel Class = "el"
	ClassTitle     Class = "t"
)

type Span struct {
	Text   string
	Class  Class
	Italic bool
}

type Line struct {
	Spans []Span
}

type Art struct {
	Lines []Line
}

func (a *Art) Plain() string {
	if a == nil {
		return ""
	}
	var out strings.Builder
	for i, line := range a.Lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		for _, span := range line.Spans {
			out.WriteString(span.Text)
		}
	}
	return out.String()
}

type cacheKey struct {
	source   string
	maxWidth int
}

var renderCache = struct {
	sync.Mutex
	entries map[cacheKey]*Art
}{entries: make(map[cacheKey]*Art)}

// Render renders Mermaid source. maxWidth <= 0 means unlimited width. Blank
// input returns (nil, nil), matching grok-mermaid's render_plain API.
func Render(source string, maxWidth int) (*Art, error) {
	key := cacheKey{source: source, maxWidth: maxWidth}
	renderCache.Lock()
	defer renderCache.Unlock()
	if art, ok := renderCache.entries[key]; ok {
		return cloneArt(art), nil
	}

	htmlOutput, err := renderHTML(source, maxWidth)
	if err != nil {
		return nil, err
	}
	if htmlOutput == "" {
		return nil, nil
	}
	art, err := parseHTML(htmlOutput)
	if err != nil {
		return nil, err
	}
	renderCache.entries[key] = art
	return cloneArt(art), nil
}

func cloneArt(art *Art) *Art {
	if art == nil {
		return nil
	}
	clone := &Art{Lines: make([]Line, len(art.Lines))}
	for i, line := range art.Lines {
		clone.Lines[i].Spans = append([]Span(nil), line.Spans...)
	}
	return clone
}

func renderHTML(source string, maxWidth int) (string, error) {
	var input bytes.Buffer
	if err := binary.Write(&input, binary.LittleEndian, uint32(len(rendererWASM))); err != nil {
		return "", err
	}
	input.Write(rendererWASM)
	input.WriteString(source)

	cmd := exec.Command("node", "-e", nodeRenderer, strconv.Itoa(maxWidth))
	cmd.Stdin = &input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("running embedded grok-mermaid renderer: %w: %s", err, message)
		}
		return "", fmt.Errorf("running embedded grok-mermaid renderer: %w", err)
	}
	return stdout.String(), nil
}

const nodeRenderer = `
const fs = require('fs');
(async () => {
  const input = fs.readFileSync(0);
  const wasmLength = input.readUInt32LE(0);
  const wasm = input.subarray(4, 4 + wasmLength);
  const source = input.subarray(4 + wasmLength).toString('utf8');
  const width = Number(process.argv[1]);
  const { instance } = await WebAssembly.instantiate(wasm, {});
  const { memory, wasm_alloc, wasm_render_html, wasm_result_ptr } = instance.exports;
  const bytes = Buffer.from(source, 'utf8');
  const ptr = wasm_alloc(bytes.length);
  new Uint8Array(memory.buffer, ptr, bytes.length).set(bytes);
  const length = wasm_render_html(ptr, bytes.length, width);
  const result = Buffer.from(instance.exports.memory.buffer, wasm_result_ptr(), length);
  process.stdout.write(result);
})().catch(error => {
  console.error(error && error.stack || String(error));
  process.exit(1);
});
`

func parseHTML(input string) (*Art, error) {
	var (
		art     Art
		line    Line
		class   Class
		italic  bool
		content strings.Builder
	)
	flushContent := func() {
		if content.Len() == 0 {
			return
		}
		line.Spans = append(line.Spans, Span{
			Text:   html.UnescapeString(content.String()),
			Class:  class,
			Italic: italic,
		})
		content.Reset()
	}
	flushLine := func() {
		flushContent()
		art.Lines = append(art.Lines, line)
		line = Line{}
	}

	for len(input) > 0 {
		switch {
		case strings.HasPrefix(input, `<span class="`):
			flushContent()
			end := strings.Index(input, `">`)
			if end < 0 {
				return nil, fmt.Errorf("invalid grok-mermaid span: missing class terminator")
			}
			classes := strings.Fields(input[len(`<span class="`):end])
			class, italic = ClassNone, false
			for _, name := range classes {
				if name == "i" {
					italic = true
				} else {
					class = Class(name)
				}
			}
			input = input[end+2:]
		case strings.HasPrefix(input, `</span>`):
			flushContent()
			class, italic = ClassNone, false
			input = input[len(`</span>`):]
		case input[0] == '\n':
			flushLine()
			input = input[1:]
		default:
			next := len(input)
			if tag := strings.IndexByte(input, '<'); tag >= 0 && tag < next {
				next = tag
			}
			if newline := strings.IndexByte(input, '\n'); newline >= 0 && newline < next {
				next = newline
			}
			if next == 0 {
				return nil, fmt.Errorf("invalid grok-mermaid HTML near %q", input[:min(len(input), 32)])
			}
			content.WriteString(input[:next])
			input = input[next:]
		}
	}
	flushContent()
	if len(line.Spans) > 0 {
		art.Lines = append(art.Lines, line)
	}
	return &art, nil
}
