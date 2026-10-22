package tui

import (
	"fmt"
	"strings"
	"testing"

	textansi "github.com/charmbracelet/x/ansi"
)

func TestConversationLineHeightWordBoundaries(t *testing.T) {
	m := fleetModel{width: 10}
	if got := m.conversationLineHeight("aaaaaa bbbbbb cccccc"); got != 3 {
		t.Fatalf("word-wrapped height = %d, want 3", got)
	}
}

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
			m := fleetModel{width: test.width}
			if m.conversationLineHeight(test.input) != strings.Count(got, "\n")+1 {
				t.Fatal("reader height differs from shared wrap")
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

func TestTeamMediumReadingLedger(t *testing.T) {
	for _, width := range []int{65, 80, 99} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := teamViewFixture()
			m.width, m.height = width, 24
			m.coordination.team = m.coordination.team[:1]
			m.coordination.team[0].Alias = "\u754ce\u0301-agent"
			m.coordination.team[0].HostID = "HOST-test"
			m.coordination.team[0].State = "online"
			m.coordination.cursor = 0
			selected := m.coordination.team[0].Placement
			lines := m.teamLines("test-master")
			header, row := lines[5], lines[6]
			if len(lines) != 7 {
				t.Fatalf("medium ledger has %d lines, want 7: %q", len(lines), lines)
			}
			for _, column := range []struct{ heading, value string }{{"AGENT", "\u754ce\u0301-agent"}, {"HOST", "HOST-test"}, {"PRESENCE", "online"}, {"WORK / BLOCKER", "unknown"}} {
				hi, ri := strings.Index(header, column.heading), strings.Index(row, column.value)
				if hi < 0 || ri < 0 || textansi.StringWidth(header[:hi]) != textansi.StringWidth(row[:ri]) {
					t.Fatalf("unaligned %s: header %q, row %q", column.heading, header, row)
				}
			}
			if !strings.HasPrefix(row, "> ") || textansi.StringWidth(header) != width || textansi.StringWidth(row) != width {
				t.Fatalf("selection or geometry mismatch: %q / %q", header, row)
			}
			m.revealTeamSelection()
			if m.coordination.team[m.coordination.cursor].Placement != selected || !strings.Contains(m.View(), "> \u754ce\u0301-agent") {
				t.Fatal("rendering changed placement or selection visibility")
			}
		})
	}
}
