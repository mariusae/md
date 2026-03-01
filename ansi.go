package md

const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Italic    = "\033[3m"
	Underline = "\033[4m"

	FgBlue = "\033[34m"

	// OSC8End closes an OSC-8 hyperlink.
	OSC8End = "\033]8;;\033\\"
)

// OSC8Start returns an OSC-8 escape sequence that begins a hyperlink to url.
func OSC8Start(url string) string {
	return "\033]8;;" + url + "\033\\"
}
