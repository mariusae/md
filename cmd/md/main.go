package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mariusae/md"

	"golang.org/x/term"
)

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			printUsage(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, "md: %v\n", err)
		os.Exit(1)
	}
	if opts.help {
		printUsage(os.Stdout)
		return
	}

	if opts.pager {
		cfg := md.PagerConfig{
			Paths: opts.paths,
			Width: opts.width,
		}
		if len(opts.paths) == 0 {
			source, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "md: %v\n", err)
				os.Exit(1)
			}
			cfg.InitialSource = source
			cfg.Label = "stdin"
		}
		if err := md.RunPager(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "md: %v\n", err)
			os.Exit(1)
		}
		return
	}

	source, err := readInput(opts.paths)
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
	if opts.width > 0 {
		width = opts.width
	}

	// Keep one-shot rendering free of terminal protocol side effects.
	// The pager owns the terminal session and can safely probe for tint colors.
	if err := md.RenderWithStyle(source, os.Stdout, width, isTTY, md.RenderStyle{}); err != nil {
		fmt.Fprintf(os.Stderr, "md: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	pager bool
	help  bool
	width int
	paths []string
}

func parseArgs(args []string) (options, error) {
	fs := flag.NewFlagSet("md", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var opts options
	fs.BoolVar(&opts.pager, "P", false, "launch the built-in pager")
	fs.BoolVar(&opts.help, "help", false, "show help")
	fs.IntVar(&opts.width, "w", 0, "render width in characters")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if opts.help {
		return opts, nil
	}
	widthSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "w" {
			widthSet = true
		}
	})
	if widthSet && opts.width <= 0 {
		return options{}, fmt.Errorf("-w must be a positive integer")
	}
	opts.paths = fs.Args()
	return opts, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: md [options] [file ...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  -P          launch the built-in pager")
	fmt.Fprintln(w, "  -w width    render width in characters")
	fmt.Fprintln(w, "  -help       show help")
}

func readInput(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return io.ReadAll(os.Stdin)
	}

	var all []byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		all = append(all, data...)
	}
	return all, nil
}
