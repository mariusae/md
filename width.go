package md

import "github.com/mattn/go-runewidth"

// terminalWidth returns the number of terminal cells occupied by text.
// This differs from its rune count for wide glyphs such as CJK characters
// and emoji, and for zero-width combining characters.
func terminalWidth(text string) int {
	return runewidth.StringWidth(text)
}

func terminalRuneWidth(r rune) int {
	width := runewidth.RuneWidth(r)
	if width < 0 {
		return 0
	}
	return width
}
