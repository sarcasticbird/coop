package tui

import (
	"strings"
	"testing"

	textansi "github.com/charmbracelet/x/ansi"
)

func actualSplitRightInterior(t *testing.T, m fleetModel) []string {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if !m.splitFleet() || len(lines) != m.height {
		t.Fatal("expected a terminal-height split view")
	}
	left, right := m.splitWidths()
	body := make([]string, 0, m.height-4)
	for _, line := range lines[1 : m.height-3] {
		body = append(body, strings.TrimRight(textansi.Cut(line, left+4, left+4+right), " "))
	}
	return body
}
