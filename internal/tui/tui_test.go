package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type testRegistry struct {
	hosts []hostrouter.Host
	err   error
}

func (r *testRegistry) List() ([]hostrouter.Host, error) { return r.hosts, r.err }
func (r *testRegistry) ResolveTarget(t hostrouter.Target) (hostrouter.Host, error) {
	if t.Host == hostrouter.Local {
		return t.Host, nil
	}
	if r.err == nil && slices.Contains(r.hosts, t.Host) {
		return t.Host, nil
	}
	return hostrouter.Host{}, c.ErrConflict
}

type testInventory struct{}

func (testInventory) Watch(_ context.Context, hosts []hostrouter.Host) <-chan hostrouter.HostInventory {
	ch := make(chan hostrouter.HostInventory, len(hosts))
	for _, h := range hosts {
		ch <- hostrouter.HostInventory{Host: h}
	}
	close(ch)
	return ch
}

type testCoord struct {
	master  string
	self    c.SelfIdentity
	team    []c.Presence
	reports map[string]c.AgentDiagnostics
	caps    c.TaskCapabilities
	thread  c.Thread
	sent    []c.SendMessage
	send    func(c.SendMessage) (c.MessageReceipt, error)
	err     error
}

func (s *testCoord) MasterID() string                             { return s.master }
func (s *testCoord) Self(context.Context) (c.SelfIdentity, error) { return s.self, s.err }
func (s *testCoord) Team(context.Context) ([]c.Presence, error)   { return s.team, s.err }
func (s *testCoord) AgentDiagnostics(_ context.Context, id string) (c.AgentDiagnostics, error) {
	return s.reports[id], s.err
}
func (s *testCoord) TaskCapabilities(context.Context) (c.TaskCapabilities, error) {
	return s.caps, s.err
}
func (s *testCoord) Threads(context.Context, c.ThreadQuery) (c.ThreadPage, error) {
	return c.ThreadPage{Threads: []c.Thread{s.thread}}, s.err
}
func (s *testCoord) Thread(context.Context, string) (c.Thread, error) { return s.thread, s.err }
func (s *testCoord) Send(_ context.Context, in c.SendMessage) (c.MessageReceipt, error) {
	s.sent = append(s.sent, in)
	if s.send != nil {
		return s.send(in)
	}
	return c.MessageReceipt{MasterID: s.master, RestoreEpoch: s.self.RestoreEpoch, Backend: "kata", MessageID: "msg_sent", Deliveries: []c.Delivery{{AgentID: in.Recipients[0], MessageID: "msg_sent", State: "queued"}}}, nil
}

func tableFixture() (fleetModel, *testCoord) {
	remote := hostrouter.Host{Name: "rack", Address: "rack-alias"}
	source := &testCoord{master: "test-master", reports: map[string]c.AgentDiagnostics{}, self: c.SelfIdentity{RestoreEpoch: 1, Principal: c.Principal{ID: "owner", Kind: "owner", Capabilities: []string{"read", "send"}}}}
	project, issue := "01M69M973PWDTSMG3GYRQM6R74", "01M69NJWQXXBT2PQWX307ZFPFH"
	conversation := &c.ConversationSource{Provider: "kata", InstanceUID: "01M69M921GHQFESAHTGT7MB4CT", ProjectUID: project, IssueUID: issue}
	source.caps = c.TaskCapabilities{Provider: "kata", ProjectUID: project, Conversation: conversation}
	source.thread = c.Thread{ID: "th_team", Engagement: "work", Members: []string{"owner", "rook", "hawk"}, Source: conversation}
	row := func(host hostrouter.Host, id string) hostrouter.InventoryEntry {
		binding := &c.DiagnosticBinding{MasterID: source.master, Placement: c.Placement{AgentID: id, HostID: host.Name, InstanceID: "old-instance", Generation: 1}}
		current := binding.Placement
		current.InstanceID, current.Generation = "current-instance", 2
		source.team = append(source.team, c.Presence{Placement: current, State: "idle", LastSeen: time.Now()})
		ref, _ := c.KataTaskReference(project, issue, "owner/repo")
		source.reports[id] = c.AgentDiagnostics{ReceivedAt: time.Now(), Report: c.WorkerDiagnostics{Placement: current, ObservedAt: time.Now(), Work: c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: ref, WorkTitle: "Ship the useful change"}}}
		return hostrouter.InventoryEntry{Target: hostrouter.Target{Host: host, Kind: hostrouter.Agent, ID: id}, Name: "same-name", State: "running", Diagnostics: binding}
	}
	local, distant := row(hostrouter.Local, "rook"), row(remote, "hawk")
	m := fleetModel{ctx: context.Background(), width: 100, height: 24, services: Services{Registry: &testRegistry{hosts: []hostrouter.Host{remote}}, Inventory: testInventory{}, Coordination: source}, hosts: []hostState{
		{host: hostrouter.Local, entries: []hostrouter.InventoryEntry{local}, updated: time.Now()},
		{host: remote, entries: []hostrouter.InventoryEntry{distant}, updated: time.Now()},
	}, selected: distant.Target}
	return m, source
}

func tableKey(m fleetModel, key string) (fleetModel, tea.Cmd) {
	keys := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "ctrl+s": tea.KeyCtrlS, "ctrl+c": tea.KeyCtrlC, "left": tea.KeyLeft, "right": tea.KeyRight, "backspace": tea.KeyBackspace}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if kind, ok := keys[key]; ok {
		msg = tea.KeyMsg{Type: kind}
	}
	out, cmd := m.Update(msg)
	return out.(fleetModel), cmd
}
func tableResult(m fleetModel, cmd tea.Cmd) fleetModel {
	out, _ := m.Update(cmd())
	return out.(fleetModel)
}

func TestTableRefreshStateAndSelection(t *testing.T) {
	m, _ := tableFixture()
	selected := m.selected
	out, cmd := m.Update(fleetRefresh{})
	m = out.(fleetModel)
	if cmd == nil || !m.refreshing || m.epoch != 1 {
		t.Fatal("refresh lost its in-flight state or generation")
	}
	if m.refresh() != nil {
		t.Fatal("refreshes stacked")
	}
	m.hosts[0], m.hosts[1] = m.hosts[1], m.hosts[0]
	m.selectVisible()
	if m.selected != selected {
		t.Fatal("row reordering retargeted selection")
	}
	out, _ = m.Update(hostLoaded{epoch: 0, inventory: hostrouter.HostInventory{Host: selected.Host}})
	if out.(fleetModel).selected != selected || len(out.(fleetModel).rows()) != 2 {
		t.Fatal("old inventory replaced current rows")
	}
}

func TestTableKeepsStoppedRowsAndUnavailableHosts(t *testing.T) {
	m, _ := tableFixture()
	m.hosts[0].entries[0].State = "stopped"
	if rows := m.rows(); rows[0].Target != m.selected || rows[1].State != "stopped" {
		t.Fatal("running Coops were not first, or stopped Coop disappeared", rows)
	}
	ch := make(chan hostrouter.HostInventory)
	close(ch)
	m.stream = ch
	out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: m.selected.Host, Error: "unreachable"}})
	m = out.(fleetModel)
	if len(m.rows()) != 2 || !strings.Contains(m.View(), "unavailable") || !strings.Contains(m.View(), "stopped") {
		t.Fatal("host failure erased rows or falsely reported a stopped VM", m.View())
	}
	if _, ok := m.entry(m.selected); ok {
		t.Fatal("unreachable target was actionable")
	}
	out, _ = m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: m.selected.Host, Entries: m.hosts[1].entries}})
	m = out.(fleetModel)
	if _, ok := m.entry(m.selected); !ok {
		t.Fatal("reconnected Coop stayed unavailable")
	}
}

func TestTableRejectsWrongHostAndChangedDestination(t *testing.T) {
	for _, fault := range []string{"wrong-host", "invalid", "registry", "stale"} {
		t.Run(fault, func(t *testing.T) {
			m, _ := tableFixture()
			calls := 0
			m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) {
				calls++
				return exec.Command("true"), nil
			}
			switch fault {
			case "registry":
				m.services.Registry.(*testRegistry).hosts[0].Address = "other-host"
			case "stale":
				m.hosts[1].updated = time.Now().Add(-2 * c.PresenceStale)
			default:
				e := m.hosts[1].entries[0]
				if fault == "wrong-host" {
					e.Target.Host = hostrouter.Local
				} else {
					e.State = "invented"
				}
				ch := make(chan hostrouter.HostInventory)
				close(ch)
				m.stream = ch
				out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: m.hosts[1].host, Entries: []hostrouter.InventoryEntry{e}}})
				m = out.(fleetModel)
			}
			m, cmd := tableKey(m, "enter")
			if cmd != nil || calls != 0 || m.busy {
				t.Fatal("stale or malformed destination attached")
			}
		})
	}
}

func TestTableHasOnlyDirectSessionKataAndMessageActions(t *testing.T) {
	m, _ := tableFixture()
	var requests []hostrouter.ActionRequest
	m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
		requests = append(requests, req)
		return exec.Command("true"), nil
	}
	for _, key := range []string{"u", "s", "d", "x", "v", "g", "1", "2", "3", "tab", "y"} {
		_, cmd := tableKey(m, key)
		if cmd != nil || len(requests) != 0 {
			t.Fatal("removed control still launches an operation", key)
		}
	}
	selected := m.selected
	m, cmd := tableKey(m, "enter")
	if cmd == nil || !m.busy || len(requests) != 1 || requests[0].Target != selected || !reflect.DeepEqual(requests[0].Args, []string{"--agent", "hawk", "attach"}) {
		t.Fatal("Enter did not directly attach the captured bird", requests)
	}
	out, cmd := m.Update(fleetActionDone{})
	m = out.(fleetModel)
	if m.busy || !m.refreshing || cmd == nil || m.selected != selected {
		t.Fatal("attachment did not return to the same table selection")
	}
	m.refreshing = false
	m, cmd = tableKey(m, "k")
	if cmd == nil || requests[1].Operation != "work-ui" || !reflect.DeepEqual(requests[1].Args, []string{"task"}) {
		t.Fatal("Kata did not open the project without a current task")
	}
}

func TestTableKataUsesCurrentPlacementNotOldDisplayInstance(t *testing.T) {
	m, source := tableFixture()
	cmd := m.refreshWork()
	if cmd == nil || !m.workBusy {
		t.Fatal("work refresh was not scheduled")
	}
	m = tableResult(m, cmd)
	if _, ok := m.task(m.selected); !ok || !strings.Contains(m.View(), "Ship the useful change") {
		t.Fatal("container replacement hid its unchanged coworker's task", m.View())
	}
	var request hostrouter.ActionRequest
	m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
		request = req
		return exec.Command("true"), nil
	}
	_, cmd = tableKey(m, "k")
	if cmd == nil || !reflect.DeepEqual(request.Args, []string{"task", source.reports["hawk"].Report.Work.TaskURL}) {
		t.Fatal("Kata did not open the selected bird's actual current task", request)
	}
	for _, fault := range []string{"stale", "wrong-instance", "moved-host"} {
		t.Run(fault, func(t *testing.T) {
			m, source := tableFixture()
			r := source.reports["hawk"]
			switch fault {
			case "stale":
				r.ReceivedAt = time.Now().Add(-2 * c.PresenceStale)
			case "wrong-instance":
				r.Report.InstanceID = "old-instance"
			case "moved-host":
				source.team[1].HostID = "other-host"
			}
			source.reports["hawk"] = r
			cmd := m.refreshWork()
			m = tableResult(m, cmd)
			if _, ok := m.task(m.selected); ok {
				t.Fatal("unrelated or stale work was shown as the selected Coop's task")
			}
		})
	}
}

func readyMessage(t *testing.T, m fleetModel) fleetModel {
	t.Helper()
	m, cmd := tableKey(m, "m")
	if cmd == nil || m.draft == nil || !m.draft.loading {
		t.Fatal("message did not open")
	}
	m = tableResult(m, cmd)
	if m.draft == nil || m.draft.loading {
		t.Fatal("message destination did not resolve", m.status)
	}
	return m
}

func TestTableMessageAddressesSelectedBirdWithoutPreviewGate(t *testing.T) {
	m, source := tableFixture()
	m.hosts[1].entries[0].State = "stopped" // Offline coworkers can still receive queued messages.
	m = readyMessage(t, m)
	m, _ = tableKey(m, "Hello 界")
	m, _ = tableKey(m, "enter")
	m, _ = tableKey(m, "next line")
	if !strings.Contains(m.View(), "Hello 界") || !strings.Contains(m.View(), "Message to hawk") {
		t.Fatal(m.View())
	}
	m.selected = m.hosts[0].entries[0].Target // Refresh/navigation cannot retarget the captured composer.
	m, cmd := tableKey(m, "ctrl+s")
	if cmd == nil || !m.draft.sending || len(source.sent) != 0 {
		t.Fatal("message required another preview/confirmation or sent synchronously")
	}
	m = tableResult(m, cmd)
	if m.draft != nil || len(source.sent) != 1 {
		t.Fatal("message did not finish", m.status)
	}
	got := source.sent[0]
	if got.ThreadID != source.thread.ID || got.Body != "Hello 界\nnext line" || !reflect.DeepEqual(got.Recipients, []string{"hawk"}) || !reflect.DeepEqual(got.ExpectedMembers, source.thread.Members) || got.RequestID == "" {
		t.Fatal("message was changed or redirected", got)
	}
	if !strings.Contains(m.status, "queued for hawk") {
		t.Fatal("queued message incorrectly reported", m.status)
	}
}

func TestTableMessageUnknownOutcomeRetriesIdenticalInput(t *testing.T) {
	m, source := tableFixture()
	m = readyMessage(t, m)
	m, _ = tableKey(m, "one message")
	effects := map[string]string{}
	source.send = func(in c.SendMessage) (c.MessageReceipt, error) {
		if body, exists := effects[in.RequestID]; exists {
			if body != in.Body {
				t.Fatal("retry changed the payload")
			}
			return c.MessageReceipt{MasterID: source.master, RestoreEpoch: 1, MessageID: "msg_sent", Deliveries: []c.Delivery{{AgentID: "hawk", MessageID: "msg_sent"}}}, nil
		}
		effects[in.RequestID] = in.Body
		return c.MessageReceipt{}, errors.New("response lost after commit")
	}
	m, cmd := tableKey(m, "ctrl+s")
	m = tableResult(m, cmd)
	if m.draft == nil || !m.draft.frozen || len(effects) != 1 {
		t.Fatal("uncertain send was discarded")
	}
	// A later safe-read failure cannot resolve the first write's unknown outcome.
	source.err = c.ErrUnavailable
	m, cmd = tableKey(m, "ctrl+s")
	m = tableResult(m, cmd)
	if m.draft == nil || !m.draft.frozen || len(source.sent) != 1 {
		t.Fatal("failed retry preflight cleared an unresolved send")
	}
	source.err = nil
	m, _ = tableKey(m, "changed text")
	if string(m.draft.body) != "one message" {
		t.Fatal("uncertain payload remained editable")
	}
	out, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = out.(fleetModel)
	if len(source.sent) != 1 {
		t.Fatal("resize automatically retried a message")
	}
	m, cmd = tableKey(m, "ctrl+s")
	m = tableResult(m, cmd)
	if m.draft != nil || len(source.sent) != 2 || len(effects) != 1 || !reflect.DeepEqual(source.sent[0], source.sent[1]) {
		t.Fatal("retry duplicated the effect or changed its identity")
	}
}

func TestTableErrorsNeverHideAnActionableSelectedRow(t *testing.T) {
	m, _ := tableFixture()
	m.height = 8
	m.selected = m.hosts[0].entries[0].Target
	for i := 0; i < 8; i++ {
		m.hosts = append(m.hosts, hostState{host: hostrouter.Host{Name: fmt.Sprintf("offline-%d", i), Address: "offline"}, errorCode: "unreachable"})
	}
	m.status = "Some hosts could not be refreshed"
	view := m.View()
	if !strings.Contains(view, "> rook") {
		t.Fatal("error feedback hid the still-actionable selection", view)
	}
	if len(strings.Split(view, "\n")) > m.height {
		t.Fatal("error feedback exceeded the viewport")
	}
}

func TestTableMessageRechecksIdentityAndMembership(t *testing.T) {
	for _, fault := range []string{"principal", "epoch", "membership", "host", "registry"} {
		t.Run(fault, func(t *testing.T) {
			m, source := tableFixture()
			m = readyMessage(t, m)
			m, _ = tableKey(m, "private message")
			m, cmd := tableKey(m, "ctrl+s")
			switch fault {
			case "principal":
				source.self.Principal.ID = "other"
			case "epoch":
				source.self.RestoreEpoch++
			case "membership":
				source.thread.Members = []string{"owner", "rook"}
			case "host":
				source.team[1].HostID = "other-host"
			case "registry":
				m.services.Registry.(*testRegistry).hosts[0].Address = "other-alias"
			}
			m = tableResult(m, cmd)
			if len(source.sent) != 0 || m.draft == nil || m.draft.frozen {
				t.Fatal("changed authority sent a message or created a false uncertain write", m.status)
			}
		})
	}
}

func TestTableComposerCancelIgnoresLateReadAndPreservesUnicode(t *testing.T) {
	m, _ := tableFixture()
	m, read := tableKey(m, "m")
	m, _ = tableKey(m, "esc")
	m = tableResult(m, read)
	if m.draft != nil {
		t.Fatal("late read reopened a cancelled composer")
	}
	m = readyMessage(t, m)
	m, _ = tableKey(m, "a界z")
	m, _ = tableKey(m, "left")
	m, _ = tableKey(m, "backspace")
	if string(m.draft.body) != "az" || !utf8.ValidString(m.View()) {
		t.Fatal("rune editing corrupted text")
	}
	m.draft.body = []rune(strings.Repeat("界", 20))
	m.draft.cursor = len(m.draft.body)
	if !strings.Contains(strings.Join(m.messageView(40, 3), "\n"), "▏") {
		t.Fatal("cursor vanished when it wrapped past a full-width line")
	}
}

func TestTableViewFitsNarrowAndShortTerminals(t *testing.T) {
	for _, width := range []int{20, 40, 60, 80, 120} {
		for _, height := range []int{2, 8, 24} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				m, _ := tableFixture()
				m.width, m.height = width, height
				for i := 0; i < 30; i++ {
					e := m.hosts[0].entries[0]
					e.Target.ID = fmt.Sprintf("bird-%02d", i)
					e.Name = "界\x1b[2J\x00" + strings.Repeat("long", 20)
					e.Diagnostics = nil
					m.hosts[0].entries = append(m.hosts[0].entries, e)
				}
				m, _ = tableKey(m, "end")
				check := func() {
					view := m.View()
					if !utf8.ValidString(view) || len(strings.Split(view, "\n")) > height || strings.ContainsAny(view, "\x1b\x00") {
						t.Fatal("invalid table geometry or controls", view)
					}
					for _, line := range strings.Split(view, "\n") {
						if textansi.StringWidth(line) > width {
							t.Fatal("line exceeded terminal", line)
						}
					}
				}
				check()
				m = readyMessage(t, m)
				m, _ = tableKey(m, strings.Repeat("long 界\n", 20))
				check()
			})
		}
	}
}
