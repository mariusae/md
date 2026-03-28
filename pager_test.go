package md

import "testing"

func TestParseOSCColorRGB(t *testing.T) {
	color, ok := parseOSCColor("rgb:ffff/8000/0000")
	if !ok {
		t.Fatal("expected rgb payload to parse")
	}
	if color != (rgbColor{r: 255, g: 128, b: 0}) {
		t.Fatalf("unexpected color: %#v", color)
	}
}

func TestParseOSCColorRGBA(t *testing.T) {
	color, ok := parseOSCColor("rgba:0000/0000/ffff/ffff")
	if !ok {
		t.Fatal("expected rgba payload to parse")
	}
	if color != (rgbColor{r: 0, g: 0, b: 255}) {
		t.Fatalf("unexpected color: %#v", color)
	}
}

func TestStripANSI(t *testing.T) {
	input := Bold + "hello" + Reset + " " + OSC8Start("https://example.com") + "world" + OSC8End
	if got := stripANSI(input); got != "hello world" {
		t.Fatalf("stripANSI() = %q, want %q", got, "hello world")
	}
}

func TestSearchStateTracksNearestMatch(t *testing.T) {
	p := &pager{
		plainLines:  []string{"alpha", "beta alpha", "gamma"},
		searchQuery: "alpha",
		topLine:     1,
		searchIndex: -1,
	}

	p.refreshSearchAround(1)

	if len(p.searchMatches) != 2 {
		t.Fatalf("unexpected match count: %d", len(p.searchMatches))
	}
	if p.searchIndex != 1 {
		t.Fatalf("searchIndex = %d, want 1", p.searchIndex)
	}
}

func TestWatchFilesWithoutPaths(t *testing.T) {
	reloadCh, errCh, closeFn, err := watchFiles(nil)
	if err != nil {
		t.Fatalf("watchFiles(nil) returned error: %v", err)
	}
	if reloadCh != nil {
		t.Fatal("expected nil reload channel when no paths are watched")
	}
	if errCh != nil {
		t.Fatal("expected nil error channel when no paths are watched")
	}
	closeFn()
}
