package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func TestFleetDirectEnterReturnsSameSelection(t *testing.T) {
	m := fixtureFleet()
	m.selected = m.hosts[1].entries[0].Target
	m.filter = m.selected.Host
	m.scroll = 1
	target, filter := m.selected, m.filter
	calls := 0
	m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
		calls++
		if req.Target != target {
			t.Fatal("attach retargeted")
		}
		return exec.Command("true"), nil
	}
	m, cmd := fleetKey(m, "enter")
	if cmd == nil || !m.busy || m.detail || m.pending != nil || calls != 1 {
		t.Fatalf("Enter did not attach directly: %s", m.status)
	}
	out, _ := m.Update(fleetActionDone{target: target, request: m.actionSequence, operation: "enter"})
	m = out.(fleetModel)
	if m.selected != target || m.filter != filter || m.scroll != 1 || m.detail || m.busy {
		t.Fatal("detach lost Fleet context")
	}
}

func TestFleetStatusKeepsVMLivenessAndProgressSeparate(t *testing.T) {
	m, s := boundFleetFixture()
	now := time.Now()
	progress := now.Add(-20 * time.Minute)
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: now, ProgressAt: &progress, WorkTitle: "Repair dock sensor", TaskURL: "kata://01j00000000000000000000000/01J00000000000000000000001/example/coop"}
		s.reports[id] = r
	}
	cmd := m.refreshLedgerDiagnostics()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	p := m.previewDetails()
	body := strings.Join(p.fleetDetailsLines("running", "ok"), "\n")
	for _, want := range []string{"VM lifecycle: running", "Repair dock sensor", "Progress: 20m", "heartbeat:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Task: kata://") {
		t.Fatal("raw identity used as task name")
	}
	for id, r := range s.reports {
		r.Report.Work.ProgressAt = nil
		r.Report.Work.WorkTitle = ""
		s.reports[id] = r
	}
	m.ledgerDiagnostics.next = time.Time{}
	cmd = m.refreshLedgerDiagnostics()
	out, _ = m.Update(cmd())
	m = out.(fleetModel)
	p = m.previewDetails()
	body = strings.Join(p.fleetDetailsLines("running", "ok"), "\n")
	if !strings.Contains(body, "Progress: unknown") || !strings.Contains(body, "ticket title unknown") {
		t.Fatal(body)
	}
}

func TestFleetKataHandoffUsesBoundTicket(t *testing.T) {
	m, s := boundFleetFixture()
	ref, _ := c.KataTaskReference("01J00000000000000000000000", "01J00000000000000000000001", "example/coop")
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: ref, WorkTitle: "Repair dock sensor"}
		s.reports[id] = r
	}
	cmd := m.refreshLedgerDiagnostics()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	var requests []hostrouter.ActionRequest
	m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
		requests = append(requests, req)
		return exec.Command("true"), nil
	}
	m, cmd = fleetKey(m, "t")
	if cmd == nil || len(requests) != 1 || requests[0].Operation != "work-ui" || strings.Join(requests[0].Args, " ") != "task "+ref {
		t.Fatalf("native ticket handoff missing: %+v", requests)
	}
	if requests[0].Target.Host == m.selected.Host {
		t.Fatal("work master redirected to selected bird host")
	}
}

type nativeFleetSource struct {
	*teamViewSource
	capabilities    c.TaskCapabilities
	capabilitiesErr error
}

func (s *nativeFleetSource) TaskCapabilities(context.Context) (c.TaskCapabilities, error) {
	return s.capabilities, s.capabilitiesErr
}

func TestFleetPrimaryNavigationUsesCanonicalComms(t *testing.T) {
	m, base := boundFleetFixture()
	project := "01J00000000000000000000000"
	source := &nativeFleetSource{teamViewSource: base, capabilities: c.TaskCapabilities{Provider: "kata", ProjectUID: project, Conversation: &c.ConversationSource{Provider: "kata", InstanceUID: "native-instance", ProjectUID: project, IssueUID: "01J00000000000000000000001"}}}
	m.services.Coordination = source
	if m.canonicalComms() {
		t.Fatal("local source configuration hid legacy navigation")
	}
	if !strings.Contains(m.View(), "2 Coordination") {
		t.Fatal("unaccepted bridge hid primary legacy fallback", m.View())
	}
	cmd := m.refreshWorkCapabilities()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	if !m.canonicalComms() {
		t.Fatal("authenticated active bridge not recognized")
	}
	m.help = true
	if strings.Contains(m.View(), "2 Coordination") {
		t.Fatal("legacy screen remains primary", m.View())
	}
	m.help = false
	m, cmd = fleetKey(m, "g")
	if m.workspace != 1 || m.coordination.section != "goals" || cmd == nil {
		t.Fatal("goal decisions unavailable from secondary action")
	}
	m.coordination.busy = false
	m.workspace = 0
	m.work.capabilities.Conversation.ProjectUID = "01J00000000000000000000002"
	if m.canonicalComms() {
		t.Fatal("mismatched provider project promoted canonical navigation")
	}
	m.work.capabilities.Conversation.ProjectUID = project
	m.work.checked = time.Now().Add(-c.PresenceStale)
	if m.canonicalComms() {
		t.Fatal("stale bridge evidence hid legacy navigation")
	}
	m.work.next = time.Time{}
	source.capabilitiesErr = c.ErrForbidden
	cmd = m.refreshWorkCapabilities()
	out, _ = m.Update(cmd())
	m = out.(fleetModel)
	if m.canonicalComms() {
		t.Fatal("denied bridge evidence hid legacy navigation")
	}
}

func TestFleetKataHandoffDetailsAndStalePlacement(t *testing.T) {
	for _, change := range []string{"details", "denied", "stale", "placement", "missing", "wrong-master", "access-context"} {
		t.Run(change, func(t *testing.T) {
			m, s := boundFleetFixture()
			ref, _ := c.KataTaskReference("01J00000000000000000000000", "01J00000000000000000000001", "example/coop")
			for id, r := range s.reports {
				r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: ref}
				s.reports[id] = r
			}
			m, cmd := fleetKey(m, "tab")
			m = runFleetDiagnostic(t, m, cmd)
			switch change {
			case "denied":
				m.diagnostics.failure = "access denied"
			case "stale":
				m.diagnostics.report.Stale = true
			case "placement":
				b := *m.hosts[1].entries[0].Diagnostics
				b.Generation++
				m.hosts[1].entries[0].Diagnostics = &b
			case "missing":
				m.diagnostics.report = nil
			case "wrong-master":
				m.hosts[1].entries[0].Diagnostics.MasterID = "another-master"
			case "access-context":
				m.coordination.failed = true
				m.coordination.checked = time.Now()
			}
			var captured hostrouter.ActionRequest
			m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
				captured = req
				return exec.Command("true"), nil
			}
			m, cmd = fleetKey(m, "t")
			want := "task"
			if change == "details" {
				want += " " + ref
			}
			if cmd == nil || strings.Join(captured.Args, " ") != want {
				t.Fatalf("%s handoff: %+v", change, captured)
			}
		})
	}
}

func TestFleetDirectEnterRejectsStalePlacementAndInventory(t *testing.T) {
	for _, change := range []string{"placement", "host", "stale", "future", "stopped", "removed"} {
		t.Run(change, func(t *testing.T) {
			m, _ := boundFleetFixture()
			m.selected = hostrouter.Target{}
			m, _ = fleetKey(m, "down")
			m, _ = fleetKey(m, "down")
			calls := 0
			m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) {
				calls++
				return exec.Command("true"), nil
			}
			switch change {
			case "placement":
				b := *m.hosts[1].entries[0].Diagnostics
				b.Generation++
				m.hosts[1].entries[0].Diagnostics = &b
			case "host":
				m.services.Registry.(*fixtureRegistry).hosts[0].Address = "changed"
			case "stale":
				m.hosts[1].updated = time.Now().Add(-c.PresenceStale)
			case "future":
				m.hosts[1].updated = time.Now().Add(time.Minute)
			case "stopped":
				m.hosts[1].entries[0].State = "stopped"
			case "removed":
				m.hosts[1].entries = nil
			}
			m, cmd := fleetKey(m, "enter")
			if cmd != nil || calls != 0 || m.busy || m.status == "" {
				t.Fatalf("%s dispatched stale target", change)
			}
		})
	}
}

func TestFleetPreviewKeepsIdentifiersSecondary(t *testing.T) {
	m, s := boundFleetFixture()
	m.width, m.height = 120, 40
	m.ledgerDiagnostics.master = s.MasterID()
	m.ledgerDiagnostics.self = s.self
	cmd := m.refreshLedgerDiagnostics()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	right := strings.Join(actualSplitRightInterior(t, m), "\n")
	for _, raw := range []string{"Master:", "Principal:", "Target:", "test-alias"} {
		if strings.Contains(right, raw) {
			t.Fatalf("default preview exposes %q: %s", raw, right)
		}
	}
	if !strings.Contains(right, "Host: remote") || !strings.Contains(right, "same-name") {
		t.Fatal("named bird or host missing", right)
	}
}

func TestFleetHeartbeatDoesNotAdvanceProgress(t *testing.T) {
	m, s := boundFleetFixture()
	progress := time.Now().Add(-10 * time.Minute)
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), ProgressAt: &progress, WorkTitle: "Repair dock sensor"}
		s.reports[id] = r
	}
	cmd := m.refreshLedgerDiagnostics()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	before := m.previewDetails().diagnostics.report.Report.Work.ProgressAt
	for i := range s.rows {
		s.rows[i].LastSeen = time.Now()
	}
	for id, r := range s.reports {
		r.Report.ObservedAt = time.Now()
		r.ReceivedAt = time.Now()
		r.Report.Work.ObservedAt = time.Now()
		r.Report.Wait.ObservedAt = time.Now()
		s.reports[id] = r
	}
	m.ledgerDiagnostics.next = time.Time{}
	cmd = m.refreshLedgerDiagnostics()
	out, _ = m.Update(cmd())
	m = out.(fleetModel)
	after := m.previewDetails().diagnostics.report.Report.Work.ProgressAt
	if before == nil || after == nil || !after.Equal(*before) {
		t.Fatal("heartbeat-only read changed verified progress")
	}
	p := m.previewDetails()
	body := strings.Join(p.fleetDetailsLines("running", "ok"), "\n")
	if !strings.Contains(body, "Progress: 10m") || !strings.Contains(body, "heartbeat: 0s") {
		t.Fatal("liveness obscured progress age", body)
	}
}

func TestFleetNarrowTerminalNativeActionsAndReturn(t *testing.T) {
	for _, width := range []int{20, 40, 80, 99, 100, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height = width, 20
			m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) { return exec.Command("true"), nil }
			for _, key := range []string{"enter", "t", "C"} {
				target, filter, scroll := m.selected, m.filter, m.scroll
				next, cmd := fleetKey(m, key)
				if cmd == nil || next.pending != nil || next.detail {
					t.Fatalf("%s needs another navigation step", key)
				}
				out, _ := next.Update(fleetActionDone{target: next.activeTarget, request: next.actionSequence, operation: map[string]string{"enter": "enter", "t": "task", "C": "chat"}[key]})
				m = out.(fleetModel)
				if m.selected != target || m.filter != filter || m.scroll != scroll || maxLineWidth(m.View()) > width || len(strings.Split(m.View(), "\n")) > 20 {
					t.Fatal("narrow return changed selection or exceeded bounds")
				}
				for i := range m.hosts {
					m.hosts[i].errorCode = ""
				}
			}
		})
	}
}

func TestFleetNarrowPreviewShowsNamedWorkProgressAndBlocker(t *testing.T) {
	m, s := boundFleetFixture()
	m.width, m.height = 40, 20
	progress := time.Now().Add(-3 * time.Minute)
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), ProgressAt: &progress, WorkTitle: "Repair dock sensor"}
		s.reports[id] = r
	}
	cmd := m.refreshLedgerDiagnostics()
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	view := m.View()
	for _, want := range []string{"Task: Repair dock sensor", "Progress: 3m", "Blocker: local pause"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing narrow preview %q: %s", want, view)
		}
	}
	if strings.Contains(view, "Master:") || strings.Contains(view, "Principal:") {
		t.Fatal("raw identities occupy default narrow preview", view)
	}
}

func TestFleetCanonicalNavigationDoesNotInterceptPrivateForms(t *testing.T) {
	m, s := coordinationFixture()
	m, _ = coordinationKey(m, "n")
	if m.coordination.form == nil {
		t.Fatal("missing private form")
	}
	m.work = fleetWork{checked: time.Now(), master: s.MasterID(), self: m.coordination.self, capabilities: c.TaskCapabilities{Provider: "kata", ProjectUID: "project", Conversation: &c.ConversationSource{Provider: "kata", InstanceUID: "native", ProjectUID: "project", IssueUID: "team"}}}
	calls := 0
	m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) {
		calls++
		return exec.Command("true"), nil
	}
	for _, key := range []string{"2", "3"} {
		var cmd tea.Cmd
		m, cmd = coordinationKey(m, key)
		if cmd != nil || m.busy || calls != 0 {
			t.Fatal("form text launched native work tools")
		}
	}
	if m.coordination.form.values[0] != "23" {
		t.Fatal("form text lost")
	}
}

func TestFleetDetailsTasksInvalidatesChangedAuthenticatedContext(t *testing.T) {
	for _, change := range []string{"same", "principal", "epoch", "capabilities", "denied", "principal-returned", "epoch-returned", "denied-returned"} {
		t.Run(change, func(t *testing.T) {
			m, s := boundFleetFixture()
			ref, _ := c.KataTaskReference("01J00000000000000000000000", "01J00000000000000000000001", "example/coop")
			for id, r := range s.reports {
				r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: ref, WorkTitle: "Repair dock sensor"}
				s.reports[id] = r
			}
			m, cmd := fleetKey(m, "tab")
			m = runFleetDiagnostic(t, m, cmd)
			identity := s.self
			var err error
			switch strings.TrimSuffix(change, "-returned") {
			case "principal":
				identity.Principal.ID = "replacement-owner"
			case "epoch":
				identity.RestoreEpoch++
			case "capabilities":
				identity.Principal.Capabilities = []string{"read"}
			case "denied":
				err = c.ErrForbidden
			}
			out, _ := m.Update(coordResult{serial: m.coordination.serial, kind: "identity", self: identity, err: err})
			m = out.(fleetModel)
			if strings.HasSuffix(change, "-returned") {
				out, _ = m.Update(coordResult{serial: m.coordination.serial, kind: "identity", self: s.self})
				m = out.(fleetModel)
			}
			var captured hostrouter.ActionRequest
			m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
				captured = req
				return exec.Command("true"), nil
			}
			m, cmd = fleetKey(m, "t")
			want := "task"
			if change == "same" {
				want += " " + ref
			}
			if cmd == nil || strings.Join(captured.Args, " ") != want {
				t.Fatalf("%s context launched %q, want %q", change, captured.Args, want)
			}
		})
	}
}

func fourBirdFleetFixture() (fleetModel, *teamViewSource) {
	m, s := boundFleetFixture()
	m.width, m.height = 40, 20
	remote := m.hosts[1].host
	m.hosts[0].entries[0].Name = "Hark"
	m.hosts[0].entries[0].Target.Kind = hostrouter.Agent
	selected := m.hosts[1].entries[0]
	selected.Name = "Rook"
	first, second := fixtureRow(remote, hostrouter.Agent, "first"), fixtureRow(remote, hostrouter.Agent, "second")
	first.Name, second.Name = "Wren", "Finch"
	m.hosts[1].entries = []hostrouter.InventoryEntry{first, second, selected}
	progress := time.Now().Add(-3 * time.Minute)
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), ProgressAt: &progress, WorkTitle: "Repair dock sensor"}
		s.reports[id] = r
	}
	m.selected = hostrouter.Target{}
	return m, s
}

func TestFleetFourBirdNarrowSelectionRevealsWorkBlock(t *testing.T) {
	for _, transition := range []string{"select", "resize", "inventory-refresh", "diagnostic-refresh"} {
		t.Run(transition, func(t *testing.T) {
			m, _ := fourBirdFleetFixture()
			if transition == "resize" {
				m.width, m.height = 120, 40
			}
			if transition != "diagnostic-refresh" {
				m = runLedgerDiagnostic(t, m)
			}
			for range 4 {
				m, _ = fleetKey(m, "down")
			}
			switch transition {
			case "resize":
				out, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
				m = out.(fleetModel)
			case "inventory-refresh":
				m.scroll = 0
				out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: m.hosts[1].host, Entries: m.hosts[1].entries}})
				m = out.(fleetModel)
			case "diagnostic-refresh":
				m = runLedgerDiagnostic(t, m)
			}
			view := m.View()
			for _, want := range []string{"> Rook", "Task: Repair dock sensor", "Progress: 3m", "Blocker: local pause", "Report 0s ago / receipt 0s ago"} {
				if !strings.Contains(view, want) {
					t.Errorf("%s hides %q at scroll=%d:\n%s", transition, want, m.scroll, view)
				}
			}
			if m.selected != m.hosts[1].entries[2].Target || maxLineWidth(view) > 40 || len(strings.Split(view, "\n")) != 20 {
				t.Fatal("work block reveal changed target or exceeded terminal bounds")
			}
		})
	}
}

func TestFleetDetailsRejectsReadFromEarlierAuthenticatedContext(t *testing.T) {
	m, s := boundFleetFixture()
	for id, r := range s.reports {
		r.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), WorkTitle: "Repair dock sensor"}
		s.reports[id] = r
	}
	m, cmd := fleetKey(m, "tab")
	earlier := cmd()
	identity := s.self
	identity.Principal.ID = "replacement-owner"
	out, _ := m.Update(coordResult{serial: m.coordination.serial, kind: "identity", self: identity})
	m = out.(fleetModel)
	out, _ = m.Update(earlier)
	m = out.(fleetModel)
	if m.diagnostics.report != nil || !strings.Contains(m.diagnostics.failure, "context changed") {
		t.Fatal("earlier authenticated read revived Details evidence")
	}
	s.self = identity
	m = runFleetDiagnostic(t, m, m.refreshFleetDiagnostics())
	if m.diagnostics.report == nil || m.diagnostics.self.Principal.ID != identity.Principal.ID {
		t.Fatal("fresh read did not restore evidence in the new authenticated context")
	}
}
