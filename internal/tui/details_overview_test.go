package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestDetailsOverviewIsCompactAndWorkFirst(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := boundFleetFixture()
			m.width, m.height = size.Width, size.Height
			m, cmd := fleetKey(m, "tab")
			m = runFleetDiagnostic(t, m, cmd)
			now := time.Now()
			m.diagnostics.report.Report.Work = c.DiagnosticWork{ObservedAt: now, TaskURL: "https://github.com/example/fixture/issues/42", PRURL: "https://github.com/example/fixture/pull/43", Stage: "implement"}
			m.diagnostics.report.Report.Wait.Dependencies = []string{"https://github.com/example/fixture/issues/41"}
			lines, _, _ := m.content()
			all := strings.Join(lines, "")
			for _, unwanted := range []string{"Requests since start:", "Submitted model:", "Executed model:", "Recent transitions", "Worker:", "worker:", "Generation:", "generation:", "Job:", "Session:"} {
				if strings.Contains(all, unwanted) {
					t.Fatalf("Details still dumps %q", unwanted)
				}
			}
			if strings.Count(all, "Master:") != 1 || strings.Count(all, "Principal:") != 1 {
				t.Fatal("short context unnecessarily repeated", all)
			}
			view := strings.Join(strings.Split(m.View(), "\n")[:size.Height-2], "")
			for _, want := range []string{"VM lifecycle: running", "Activity: waiting", "Task: ticket title unknown (fixture #42)", "PR: fixture #43", "Blocker: dependency fixture #41", "Host:"} {
				if !strings.Contains(view, want) {
					t.Fatalf("overview buries %q: %s", want, view)
				}
			}
			if size.Width >= 65 && len(lines) > size.Height-2 {
				t.Fatalf("normal overview requires scrolling: %d lines", len(lines))
			}
			if size.Width >= 65 && !strings.Contains(all, " | ") {
				t.Fatal("wide overview lost grouped rows")
			}
			if maxLineWidth(m.View()) > size.Width || len(strings.Split(m.View(), "\n")) != size.Height {
				t.Fatal("overview exceeds viewport")
			}
			if !strings.Contains(all, identity(m.detailEntry.Target)) {
				t.Fatal("overview lost full exact target")
			}
		})
	}
}

func TestDetailsOverviewDoesNotPromoteRetainedWork(t *testing.T) {
	for _, failure := range []string{"denied", "stale-report", "future-report", "missing-wait", "stale-work", "future-work", "missing-work"} {
		t.Run(failure, func(t *testing.T) {
			m, _ := boundFleetFixture()
			m, cmd := fleetKey(m, "tab")
			m = runFleetDiagnostic(t, m, cmd)
			r := m.diagnostics.report
			r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: "https://github.com/example/fixture/issues/42", PRURL: "https://github.com/example/fixture/pull/43", Stage: "implement"}
			switch failure {
			case "denied":
				m.diagnostics.failure = "diagnostics access denied; work unknown"
			case "stale-report":
				r.Stale = true
			case "future-report":
				r.Report.ObservedAt = time.Now().Add(time.Hour)
			case "missing-wait":
				r.Report.Wait.ObservedAt = time.Time{}
			case "stale-work":
				r.Report.Work.ObservedAt = time.Now().Add(-time.Hour)
			case "future-work":
				r.Report.Work.ObservedAt = time.Now().Add(time.Hour)
			case "missing-work":
				r.Report.Work.ObservedAt = time.Time{}
			}
			lines, _, _ := m.content()
			all := strings.Join(lines, "")
			if strings.Contains(all, "fixture #42") || strings.Contains(all, "fixture #43") || !strings.Contains(all, "Task: unknown") || !strings.Contains(all, "PR: unknown") {
				t.Fatal("retained work promoted as current", all)
			}
		})
	}
}
