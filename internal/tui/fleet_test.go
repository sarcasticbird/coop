package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type fixtureRegistry struct {
	hosts   []hostrouter.Host
	err     error
	changes []string
}

func (r *fixtureRegistry) List() ([]hostrouter.Host, error) { return r.hosts, r.err }
func (r *fixtureRegistry) Change(op, name, address string) error {
	r.changes = append(r.changes, op+":"+name+":"+address)
	if r.err != nil {
		return r.err
	}
	if op == "add" {
		r.hosts = append(r.hosts, hostrouter.Host{Name: name, Address: address})
		return nil
	}
	for i, h := range r.hosts {
		if h.Name == name {
			if op == "remove" {
				r.hosts = append(r.hosts[:i], r.hosts[i+1:]...)
			} else {
				r.hosts[i].Address = address
			}
			return nil
		}
	}
	return errors.New("unknown server")
}
func (r *fixtureRegistry) ResolveTarget(t hostrouter.Target) (hostrouter.Host, error) {
	if t.Host == hostrouter.Local {
		return t.Host, nil
	}
	for _, h := range r.hosts {
		if h == t.Host {
			return h, nil
		}
	}
	return hostrouter.Host{}, errors.New("stale target")
}

type fixtureActions struct{ requests []hostrouter.ActionRequest }

func (a *fixtureActions) Execute(_ context.Context, r hostrouter.ActionRequest, _ io.Reader, _ io.Writer, _ io.Writer) (hostrouter.ActionResult, error) {
	a.requests = append(a.requests, r)
	return hostrouter.ActionResult{Target: r.Target}, nil
}

type inventoryFunc func(context.Context, hostrouter.Host) ([]hostrouter.InventoryEntry, error)

func (f inventoryFunc) Inventory(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
	return f(ctx, h)
}

func fixtureRow(h hostrouter.Host, kind hostrouter.TargetKind, id string) hostrouter.InventoryEntry {
	return hostrouter.InventoryEntry{Target: hostrouter.Target{Host: h, Kind: kind, ID: id}, Name: "same-name", State: "running", Project: "/project"}
}
func fixtureFleet() fleetModel {
	remote := hostrouter.Host{Name: "remote", Address: "test-alias"}
	return fleetModel{ctx: context.Background(), width: 100, height: 30, services: Services{Registry: &fixtureRegistry{hosts: []hostrouter.Host{remote}}, Actions: &fixtureActions{}}, hosts: []hostState{
		{host: hostrouter.Local, entries: []hostrouter.InventoryEntry{fixtureRow(hostrouter.Local, hostrouter.Interactive, "same")}, updated: time.Now()},
		{host: remote, entries: []hostrouter.InventoryEntry{fixtureRow(remote, hostrouter.Agent, "same")}, updated: time.Now()},
	}, selected: fixtureRow(hostrouter.Local, hostrouter.Interactive, "same").Target, server: hostrouter.Local}
}
func fleetKey(m fleetModel, k string) (fleetModel, tea.Cmd) {
	msg := keyMsg(k)
	if k == "enter" {
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	}
	if k == "esc" {
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}
	if k == "tab" {
		msg = tea.KeyMsg{Type: tea.KeyTab}
	}
	out, cmd := m.Update(msg)
	return out.(fleetModel), cmd
}

func TestFleetConfirmationCapturesHostAndTargetAcrossReorder(t *testing.T) {
	m := fixtureFleet()
	m.selected = m.hosts[1].entries[0].Target
	captured := m.selected
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "d")
	if m.pending == nil || m.pending.entry.Target != captured {
		t.Fatal("confirmation did not capture the selected target")
	}
	m.selected = m.hosts[0].entries[0].Target
	m.hosts[0], m.hosts[1] = m.hosts[1], m.hosts[0]
	m, cmd := fleetKey(m, "y")
	if cmd == nil {
		t.Fatalf("no action: %s", m.status)
	}
	done := cmd().(fleetActionDone)
	if done.err != nil {
		t.Fatal(done.err)
	}
	requests := m.services.Actions.(*fixtureActions).requests
	if len(requests) != 1 || requests[0].Target != captured || requests[0].Operation != "command" || strings.Join(requests[0].Args, " ") != "--agent same destroy" || requests[0].Directory != "" {
		t.Fatalf("redirected request: %+v", requests)
	}
}

func TestFleetConfirmationRejectsChangedRegistryAndStaleInventory(t *testing.T) {
	for _, change := range []string{"address", "disconnect", "removed", "project"} {
		t.Run(change, func(t *testing.T) {
			m := fixtureFleet()
			m.selected = m.hosts[1].entries[0].Target
			m, _ = fleetKey(m, "enter")
			m, _ = fleetKey(m, "d")
			if m.pending == nil || m.pending.entry.Target != m.selected {
				t.Fatal("confirmation did not capture the selected target")
			}
			switch change {
			case "address":
				m.services.Registry.(*fixtureRegistry).hosts[0].Address = "replacement"
			case "disconnect":
				m.hosts[1].errorCode = "unreachable"
			case "removed":
				m.hosts = m.hosts[:1]
			case "project":
				m.hosts[1].entries[0].Project = "/other"
			}
			m, cmd := fleetKey(m, "y")
			if cmd != nil {
				t.Fatal("stale confirmation launched action")
			}
			if len(m.services.Actions.(*fixtureActions).requests) > 0 {
				t.Fatal("action dispatched")
			}
		})
	}
}

func TestFleetDropsRefreshFromBeforeAction(t *testing.T) {
	m := fixtureFleet()
	old := m.epoch
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "s")
	m, cmd := fleetKey(m, "y")
	if cmd == nil {
		t.Fatal("action not launched")
	}
	out, _ := m.Update(hostLoaded{epoch: old, inventory: hostrouter.HostInventory{Host: hostrouter.Local}})
	m = out.(fleetModel)
	if !m.busy || len(m.hosts[0].entries) != 1 {
		t.Fatal("stale refresh changed busy state or snapshot")
	}
	if _, ok := m.entry(m.selected); ok {
		t.Fatal("pending action inventory is authoritative")
	}
}

func TestFleetRetainsSnapshotOnFailureAndRecovers(t *testing.T) {
	for _, code := range []string{"unreachable", "timeout", "invalid_inventory", "unauthorized"} {
		t.Run(code, func(t *testing.T) {
			m := fixtureFleet()
			remote := m.hosts[1].host
			ch := make(chan hostrouter.HostInventory)
			close(ch)
			m.stream = ch
			out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: remote, Error: code}})
			m = out.(fleetModel)
			if len(m.hosts[1].entries) != 1 || !strings.Contains(m.View(), "stale snapshot") || !strings.Contains(m.View(), code) {
				t.Fatal("failure erased or hid snapshot")
			}
			if _, ok := m.entry(m.hosts[1].entries[0].Target); ok {
				t.Fatal("failed host allowed action")
			}
			out, _ = m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: remote, Entries: []hostrouter.InventoryEntry{fixtureRow(remote, hostrouter.Agent, "same")}}})
			m = out.(fleetModel)
			if m.hosts[1].errorCode != "" {
				t.Fatal("reconnect did not clear failure")
			}
		})
	}
}

func TestFleetRejectsMalformedAndWrongHostInventory(t *testing.T) {
	for _, wrongHost := range []bool{false, true} {
		m := fixtureFleet()
		row := fixtureRow(m.hosts[1].host, hostrouter.Agent, "same")
		if wrongHost {
			row.Target.Host = hostrouter.Local
		} else {
			row.State = "mystery"
		}
		out, _ := m.Update(hostLoaded{inventory: hostrouter.HostInventory{Host: m.hosts[1].host, Entries: []hostrouter.InventoryEntry{row}}})
		got := out.(fleetModel)
		if got.hosts[1].errorCode != "invalid_inventory" || len(got.hosts[1].entries) != 1 {
			t.Fatal("malformed response accepted or snapshot erased")
		}
	}
}

func TestFleetLocalArrivesWithoutWaitingForSlowRemote(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m := fixtureFleet()
	client := inventoryFunc(func(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		if h != hostrouter.Local {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []hostrouter.InventoryEntry{fixtureRow(h, hostrouter.Interactive, "same")}, nil
	})
	m.services.Inventory = hostrouter.Router{Local: client, Remote: client, Timeout: time.Second}
	cmd := m.refresh()
	out, cmd := m.Update(cmd())
	m = out.(fleetModel)
	defer m.cancel()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		h := msg.(hostLoaded)
		if h.inventory.Host != hostrouter.Local {
			t.Fatal("local queued behind remote")
		}
		out, _ = m.Update(msg)
		m = out.(fleetModel)
		m, _ = fleetKey(m, "3")
		if m.workspace != 2 {
			t.Fatal("navigation blocked during refresh")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("local inventory blocked by remote")
	}
}

func TestFleetServerFormsCRUDAndStaleCapture(t *testing.T) {
	m := fixtureFleet()
	m.workspace = 2
	m, _ = fleetKey(m, "a")
	m, _ = fleetKey(m, "new")
	m, _ = fleetKey(m, "tab")
	m, _ = fleetKey(m, "new-alias")
	m, cmd := fleetKey(m, "enter")
	if cmd == nil {
		t.Fatal("add not submitted")
	}
	if err := cmd().(registryDone).err; err != nil {
		t.Fatal(err)
	}
	m.busy = false
	m.form = nil
	m.server = m.hosts[1].host
	m, _ = fleetKey(m, "e")
	m.form.address = "updated"
	m, cmd = fleetKey(m, "enter")
	if err := cmd().(registryDone).err; err != nil {
		t.Fatal(err)
	}
	m.busy = false
	m.form = nil
	// Old displayed address cannot authorize removing an updated registration.
	m, _ = fleetKey(m, "d")
	m, cmd = fleetKey(m, "y")
	if err := cmd().(registryDone).err; err == nil {
		t.Fatal("stale server removal accepted")
	}
	m.busy = false
	m.form = nil
	m.server = hostrouter.Host{Name: "remote", Address: "updated"}
	m, _ = fleetKey(m, "d")
	m, cmd = fleetKey(m, "y")
	if err := cmd().(registryDone).err; err != nil {
		t.Fatal(err)
	}
	m.hosts = m.hosts[:1]
	m.reconcile()
	if m.server != hostrouter.Local {
		t.Fatal("removed host remains selected")
	}
	if len(m.services.Actions.(*fixtureActions).requests) != 0 {
		t.Fatal("server flow dispatched target action")
	}
}

func TestFleetRegistryFailureKeepsLocalAndServerErrorRecovers(t *testing.T) {
	m := fixtureFleet()
	m.busy = true
	m.form = &serverForm{operation: "add", name: "invalid"}
	out, _ := m.Update(registryDone{err: errors.New("validation rejected")})
	m = out.(fleetModel)
	if m.busy || m.form == nil || !strings.Contains(m.status, "validation rejected") {
		t.Fatal("form error not recoverable")
	}
	m, _ = fleetKey(m, "esc")
	if m.form != nil {
		t.Fatal("cannot leave failed form")
	}
	client := inventoryFunc(func(_ context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		return []hostrouter.InventoryEntry{fixtureRow(h, hostrouter.Interactive, "local")}, nil
	})
	m.services.Inventory = hostrouter.Router{Local: client}
	m.refreshing = true
	out, cmd := m.Update(registryLoaded{err: errors.New("private registry unavailable")})
	m = out.(fleetModel)
	defer m.cancel()
	if len(m.hosts) != 2 || m.hosts[0].host != hostrouter.Local || m.registryError == "" {
		t.Fatal("registry failure lost local or cached remote hosts")
	}
	out, _ = m.Update(cmd())
	if len(out.(fleetModel).hosts[0].entries) != 1 {
		t.Fatal("local inventory unavailable after registry failure")
	}
}

func TestFleetResizeHelpAndFocus(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		m := fixtureFleet()
		out, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = out.(fleetModel)
		if got := maxLineWidth(m.View()); got > width {
			t.Fatalf("width %d rendered %d", width, got)
		}
		m, _ = fleetKey(m, "tab")
		if !m.detail {
			t.Fatal("tab did not focus detail")
		}
		m, _ = fleetKey(m, "?")
		if !strings.Contains(m.View(), "Details Help") {
			t.Fatal("help missing")
		}
		m, _ = fleetKey(m, "esc")
		if m.help {
			t.Fatal("help did not close")
		}
		m, _ = fleetKey(m, "esc")
		if m.detail {
			t.Fatal("back did not restore browser focus")
		}
	}
}

func TestFleetRefreshReconcilesIdentityNotIndex(t *testing.T) {
	m := fixtureFleet()
	a := fixtureRow(hostrouter.Local, hostrouter.Interactive, "one")
	b := fixtureRow(hostrouter.Local, hostrouter.Interactive, "two")
	m.hosts[0].entries = []hostrouter.InventoryEntry{a, b}
	m.selected = b.Target
	out, _ := m.Update(hostLoaded{inventory: hostrouter.HostInventory{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{b, a}}})
	m = out.(fleetModel)
	if m.selected != b.Target {
		t.Fatal("refresh redirected selection by row index")
	}
	out, _ = m.Update(hostLoaded{inventory: hostrouter.HostInventory{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{a}}})
	m = out.(fleetModel)
	if m.selected != (hostrouter.Target{}) {
		t.Fatal("removed target was not cleared or another target was selected implicitly")
	}
}

func TestFleetLocalLifecycleAndEntryCompatibility(t *testing.T) {
	m := fixtureFleet()
	rt := runtime.NewMock()
	m.legacy = rt
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "s")
	m, cmd := fleetKey(m, "y")
	if cmd == nil {
		t.Fatal("local stop not launched")
	}
	if done := cmd().(fleetActionDone); done.err != nil {
		t.Fatal(done.err)
	}
	if len(rt.Stopped) != 1 || rt.Stopped[0] != "same-name" {
		t.Fatalf("wrong local target: %v", rt.Stopped)
	}
	if len(m.services.Actions.(*fixtureActions).requests) != 0 {
		t.Fatal("local compatibility routed remotely")
	}
	m = fixtureFleet()
	m.legacy = rt
	m, _ = fleetKey(m, "enter")
	m, cmd = fleetKey(m, "c")
	if cmd != nil || m.pending == nil || m.pending.operation != "enter" {
		t.Fatal("local connect did not require confirmation")
	}
	m, cmd = fleetKey(m, "y")
	if cmd == nil || m.result.EnterWorkdir != "/project" || m.result.EnterName != "same-name" {
		t.Fatal("local CLI entry result changed")
	}
}

type actionFunc func(context.Context, hostrouter.ActionRequest, io.Reader, io.Writer, io.Writer) (hostrouter.ActionResult, error)

func (f actionFunc) Execute(ctx context.Context, r hostrouter.ActionRequest, in io.Reader, out, errout io.Writer) (hostrouter.ActionResult, error) {
	return f(ctx, r, in, out, errout)
}

func TestFleetActionCancellationAndRemoteProjectContract(t *testing.T) {
	m := fixtureFleet()
	h := m.hosts[1].host
	e := fixtureRow(h, hostrouter.Interactive, "remote-id")
	m.hosts[1].entries = []hostrouter.InventoryEntry{e}
	m.selected = e.Target
	started := make(chan hostrouter.ActionRequest, 1)
	m.services.Actions = actionFunc(func(ctx context.Context, r hostrouter.ActionRequest, _ io.Reader, _ io.Writer, _ io.Writer) (hostrouter.ActionResult, error) {
		started <- r
		<-ctx.Done()
		return hostrouter.ActionResult{Target: r.Target}, ctx.Err()
	})
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "s")
	m, cmd := fleetKey(m, "y")
	if cmd == nil {
		t.Fatal("stop not launched")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	r := <-started
	if r.Target != e.Target || r.Operation != "command" || strings.Join(r.Args, " ") != "down" || r.Directory != e.Project {
		t.Fatalf("wrong remote request: %+v", r)
	}
	m, _ = fleetKey(m, "esc")
	select {
	case msg := <-done:
		if !errors.Is(msg.(fleetActionDone).err, context.Canceled) {
			t.Fatal("cancel lost")
		}
	case <-time.After(time.Second):
		t.Fatal("action did not honor cancellation")
	}
}

func TestFleetNarrowOverlayKeepsConfirmationAndFooter(t *testing.T) {
	m := fixtureFleet()
	m.width = 30
	m.height = 10
	m.selected = m.hosts[1].entries[0].Target
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "d")
	view := m.View()
	if !strings.Contains(view, "CONFIRM DESTROY") || !strings.Contains(view, "PgDn review") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("overlay is not usable: %s", view)
	}
	m.pending = nil
	m.workspace = 2
	m, _ = fleetKey(m, "a")
	m, _ = fleetKey(m, "bad\x1b[31m")
	if m.form.name != "" {
		t.Fatal("control characters entered form rendering")
	}
}
