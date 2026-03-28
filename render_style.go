package md

import (
	"bytes"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type RenderStyle struct {
	BlockquoteBG string
	HighlightBG  string
}

func DetectRenderStyle() (RenderStyle, error) {
	bg, ok, err := queryTerminalBackground()
	if err != nil {
		return RenderStyle{}, err
	}
	if !ok {
		return RenderStyle{}, nil
	}
	return deriveTintTheme(bg).renderStyle(), nil
}

func (t tintTheme) renderStyle() RenderStyle {
	return RenderStyle{
		BlockquoteBG: t.blockquoteBG,
		HighlightBG:  t.markBG,
	}
}

func queryTerminalBackground() (rgbColor, bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return rgbColor{}, false, nil
		}
		return rgbColor{}, false, err
	}
	defer tty.Close()

	if !term.IsTerminal(int(tty.Fd())) {
		return rgbColor{}, false, nil
	}

	state, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return rgbColor{}, false, err
	}
	defer term.Restore(int(tty.Fd()), state)

	if _, err := tty.WriteString(queryBackgroundColor); err != nil {
		return rgbColor{}, false, err
	}

	deadline := time.Now().Add(2 * time.Second)
	var buf bytes.Buffer
	tmp := make([]byte, 256)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		ready, err := waitForInput(tty, remaining)
		if err != nil {
			return rgbColor{}, false, err
		}
		if !ready {
			break
		}
		n, err := tty.Read(tmp)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return rgbColor{}, false, err
		}
		buf.Write(tmp[:n])
		if color, ok := extractOSC11Color(buf.Bytes()); ok {
			return color, true, nil
		}
	}
	return rgbColor{}, false, nil
}

func extractOSC11Color(data []byte) (rgbColor, bool) {
	for i := 0; i < len(data)-4; i++ {
		if data[i] != 0x1b || data[i+1] != ']' {
			continue
		}
		payloadStart := i + 2
		for j := payloadStart; j < len(data); j++ {
			switch data[j] {
			case 0x07:
				return parseOSC11Payload(data[payloadStart:j])
			case 0x1b:
				if j+1 < len(data) && data[j+1] == '\\' {
					return parseOSC11Payload(data[payloadStart:j])
				}
			}
		}
	}
	return rgbColor{}, false
}

func parseOSC11Payload(payload []byte) (rgbColor, bool) {
	text := string(payload)
	if !strings.HasPrefix(text, "11;") {
		return rgbColor{}, false
	}
	return parseOSCColor(strings.TrimPrefix(text, "11;"))
}
