package tui

import (
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestFleetRefreshKeepsCachedRowsStationary(t *testing.T) {
	for _, width := range []int{40, 100} {
		m := fixtureFleet()
		m.width = width
		m.hosts[0].entries = nil
		before := m.View()
		for i := range m.hosts {
			m.hosts[i].loading = true
			m.hosts[i].updated = time.Now().Add(-2 * time.Second)
		}
		if got := m.View(); got != before {
			t.Fatalf("width %d: starting a background read changed cached content\nbefore:\n%s\nafter:\n%s", width, before, got)
		}
	}
}

func TestCoordinationNavigationShowsInitialReadProgress(t *testing.T) {
	for _, view := range []string{"section", "engagement", "thread"} {
		t.Run(view, func(t *testing.T) {
			m, _ := coordinationFixture()
			m.width, m.height = 120, 40
			m.coordination.threads = []c.Thread{{ID: "thread", Title: "Thread"}}
			var cmd = m.coordCall("read", nil)
			out, _ := m.coordUpdate(coordResult{serial: m.coordination.serial, kind: "read", self: m.coordination.self, data: c.ThreadPage{Threads: m.coordination.threads}})
			m = out.(fleetModel)
			switch view {
			case "section":
				m, cmd = coordinationKey(m, "t")
			case "engagement":
				m.coordination.section, m.coordination.engagement = "goals", "one"
				m.coordination.self.Principal.Engagements = []string{"one", "two"}
				m, cmd = coordinationKey(m, "e")
			case "thread":
				out, next := m.coordUpdate(coordResult{serial: m.coordination.serial, kind: "thread", self: m.coordination.self, data: c.Thread{ID: "thread", Title: "Thread"}})
				m, cmd = out.(fleetModel), next
			}
			if cmd == nil || !strings.Contains(m.View(), "Loading read") || !strings.Contains(m.View(), "Coordination request in progress") {
				t.Fatalf("uncached %s hid initial loading: %s", view, m.View())
			}
		})
	}
}

func TestCoordinationRefreshKeepsSnapshotAndControlsVisible(t *testing.T) {
	m, source := coordinationFixture()
	m.width, m.height = 120, 40
	m.coordination.status = "Snapshot refreshed"
	m.coordination.seen = time.Now()
	m.coordination.threads = []c.Thread{{ID: "existing", Title: "Existing discussion"}}
	out, _ := m.coordUpdate(coordResult{serial: m.coordination.serial, kind: "read", self: m.coordination.self, data: c.ThreadPage{Threads: m.coordination.threads}})
	m = out.(fleetModel)
	source.threads = func(c.ThreadQuery) (c.ThreadPage, error) {
		return c.ThreadPage{Threads: []c.Thread{{ID: "existing", Title: "Updated discussion"}}}, nil
	}
	before := m.View()
	cmd := m.coordLoad(false)
	if cmd == nil || !m.coordination.busy {
		t.Fatal("refresh was not scheduled")
	}
	if got := m.View(); got != before {
		t.Fatalf("background read replaced the cached view or controls\nbefore:\n%s\nafter:\n%s", before, got)
	}
	m = coordinationRun(t, m, cmd)
	if !strings.Contains(m.View(), "Updated discussion") {
		t.Fatal("completed refresh did not update the snapshot")
	}
}

func TestCoordinationStillShowsInitialLoadingAndWriteProgress(t *testing.T) {
	for _, kind := range []string{"read", "write"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := coordinationFixture()
			if kind == "read" {
				m.coordination.seen = time.Time{}
			}
			m.coordCall(kind, nil)
			view := m.View()
			if !strings.Contains(view, "Loading "+kind) || !strings.Contains(view, "Coordination request in progress") {
				t.Fatalf("initial loading or write progress was hidden: %s", view)
			}
		})
	}
}

func TestCoordinationBackgroundReadExplainsBlockedControls(t *testing.T) {
	m, _ := coordinationFixture()
	out, _ := m.coordUpdate(coordResult{serial: m.coordination.serial, kind: "read", self: m.coordination.self, data: c.ThreadPage{}})
	m = out.(fleetModel)
	m.coordLoad(false)
	section := m.coordination.section
	m, cmd := coordinationKey(m, "t")
	if cmd != nil || m.coordination.section != section {
		t.Fatal("pending read should preserve serialized navigation")
	}
	if !strings.Contains(m.View(), "Refreshing snapshot; controls resume when the read completes") {
		t.Fatal("blocked key did not explain the pending refresh")
	}
	out, _ = m.coordUpdate(coordResult{serial: m.coordination.serial, kind: "read", self: m.coordination.self, data: c.ThreadPage{}})
	m = out.(fleetModel)
	m, cmd = coordinationKey(m, "t")
	if cmd == nil || m.coordination.section != "team" {
		t.Fatal("navigation did not resume after the refresh")
	}
}
