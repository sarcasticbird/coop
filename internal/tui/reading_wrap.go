package tui

import (
	"strings"

	textansi "github.com/charmbracelet/x/ansi"
)

// Preserve spaces in exact paths/argv while wrapping at word boundaries. Only
// overlong tokens are split; reader height accounting uses this same function.
func readingWrap(line string, width int) string {
	if width <= 0 {
		return line
	}
	var out strings.Builder
	for i, paragraph := range strings.Split(line, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		used := 0
		for _, token := range strings.SplitAfter(paragraph, " ") {
			if token == "" {
				continue
			}
			if used > 0 && used+textansi.StringWidth(token) > width {
				out.WriteByte('\n')
				used = 0
			}
			wrapped := textansi.Hardwrap(token, width, true)
			out.WriteString(wrapped)
			if last := strings.LastIndexByte(wrapped, '\n'); last >= 0 {
				used = textansi.StringWidth(wrapped[last+1:])
			} else {
				used += textansi.StringWidth(wrapped)
			}
		}
	}
	return out.String()
}
