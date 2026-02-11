package main

import (
	"fmt"
	"io"
	"os"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	"golang.org/x/term"
)

func main() {
	source, err := readInput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "md: %v\n", err)
		os.Exit(1)
	}

	width := 80
	isTTY := false
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
		isTTY = true
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(
					util.Prioritized(NewAnsiRenderer(width, isTTY), 1),
				),
			),
		),
	)

	if err := md.Convert(source, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "md: %v\n", err)
		os.Exit(1)
	}
}

func readInput() ([]byte, error) {
	args := os.Args[1:]

	if len(args) == 0 {
		return io.ReadAll(os.Stdin)
	}

	var all []byte
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		all = append(all, data...)
	}
	return all, nil
}
