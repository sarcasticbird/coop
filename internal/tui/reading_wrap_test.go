package tui

import (
	"strings"
	"testing"

	textansi "github.com/charmbracelet/x/ansi"
)

func TestReadingWrap(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		width             int
	}{
		{"words", "aaaaaa bbbbbb cccccc", "aaaaaa \nbbbbbb \ncccccc", 10},
		{"paragraphs", "alpha bravo\n\ncharlie delta", "alpha \nbravo\n\ncharlie\n delta", 7},
		{"wide-combining", "\u754ce\u0301 \u754ce\u0301 \u754ce\u0301", "\u754ce\u0301 \n\u754ce\u0301 \n\u754ce\u0301", 6},
		{"unbounded", "alpha bravo\ncharlie", "alpha bravo\ncharlie", 0},
		{"negative", "alpha bravo", "alpha bravo", -1},
		{"empty", "", "", 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := readingWrap(test.input, test.width)
			if got != test.want {
				t.Fatalf("readingWrap = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadingWrapExactIdentities(t *testing.T) {
	for _, id := range []string{
		"Exact CLI arguments: [\"--path\" \"/fixture/project with spaces\"]",
		"msg_" + strings.Repeat("Ab9", 40),
		"agent-" + strings.Repeat("exact-", 30) + "distinct-suffix",
		strings.Repeat("\u754ce\u0301", 35) + "-suffix",
		"\x1b[31m" + strings.Repeat("identity", 20) + "\x1b[0m",
	} {
		for _, width := range []int{3, 7, 40, 65, 80} {
			got := readingWrap(id, width)
			if strings.ReplaceAll(got, "\n", "") != id {
				t.Fatalf("identity changed at width %d: %q", width, got)
			}
			for _, line := range strings.Split(got, "\n") {
				if textansi.StringWidth(line) > width {
					t.Fatalf("width %d overflow: %q", width, line)
				}
			}
		}
	}
}
