package main

import "testing"

func TestParseArgsWidth(t *testing.T) {
	opts, err := parseArgs([]string{"-w", "42", "test.md"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}

	if opts.width != 42 {
		t.Fatalf("width = %d, want 42", opts.width)
	}
	if len(opts.paths) != 1 || opts.paths[0] != "test.md" {
		t.Fatalf("paths = %#v, want [test.md]", opts.paths)
	}
}

func TestParseArgsPagerWidth(t *testing.T) {
	opts, err := parseArgs([]string{"-P", "-w", "42", "test.md"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}

	if !opts.pager {
		t.Fatal("pager = false, want true")
	}
	if opts.width != 42 {
		t.Fatalf("width = %d, want 42", opts.width)
	}
	if len(opts.paths) != 1 || opts.paths[0] != "test.md" {
		t.Fatalf("paths = %#v, want [test.md]", opts.paths)
	}
}

func TestParseArgsHelp(t *testing.T) {
	opts, err := parseArgs([]string{"-help"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}

	if !opts.help {
		t.Fatal("help = false, want true")
	}
}

func TestParseArgsRejectsNonPositiveWidth(t *testing.T) {
	for _, args := range [][]string{
		{"-w", "0"},
		{"-w", "-1"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("parseArgs(%v) returned nil error", args)
		}
	}
}
