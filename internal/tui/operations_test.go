package tui

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func specIndex(t *testing.T, name string) int {
	t.Helper()
	for i, s := range operationSpecs {
		if s.name == name {
			return i
		}
	}
	t.Fatal("missing operation", name)
	return 0
}

func startOperation(t *testing.T, m fleetModel, cmd tea.Cmd) (fleetModel, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("operation not queued")
	}
	out, run := m.Update(cmd())
	m = out.(fleetModel)
	if run == nil || !strings.Contains(m.status, "running") {
		t.Fatal("operation not running", m.status)
	}
	return m, run
}

func TestOperationCommandFamilies(t *testing.T) {
	for _, tc := range []struct {
		name         string
		values, want []string
		directory    string
	}{
		{"enter", nil, []string{"--agent", "same", "--"}, ""},
		{"status", nil, []string{"--agent", "same", "status"}, ""},
		{"up", nil, []string{"--agent", "same", "up"}, ""},
		{"down", nil, []string{"--agent", "same", "down"}, ""},
		{"destroy", nil, []string{"--agent", "same", "destroy"}, ""},
		{"rebuild", nil, []string{"--agent", "same", "rebuild"}, ""},
		{"apply", nil, []string{"--agent", "same", "apply"}, ""},
		{"sync", nil, []string{"--agent", "same", "sync"}, ""},
		{"backup", []string{"/remote/archive with spaces"}, []string{"--agent", "same", "backup", "--", "/remote/archive with spaces"}, ""},
		{"credentials refresh", []string{"github,registry"}, []string{"--agent", "same", "--credentials=github,registry", "credentials", "refresh"}, ""},
		{"credentials watch", nil, []string{"credentials", "watch", "--profile=same"}, ""},
		{"worker status", nil, []string{"--agent", "same", "worker", "status"}, ""},
		{"worker pause", nil, []string{"--agent", "same", "worker", "pause"}, ""},
		{"worker resume", nil, []string{"--agent", "same", "worker", "resume"}, ""},
		{"worker requests", nil, []string{"--agent", "same", "worker", "requests"}, ""},
		{"ping", []string{"literal $(not-a-shell); --host=other"}, []string{"--agent", "same", "ping", "--prompt=literal $(not-a-shell); --host=other", "--request-id=" + strings.Repeat("a", 64)}, ""},
		{"doctor", nil, []string{"doctor"}, ""},
		{"upgrade", nil, []string{"upgrade"}, ""},
		{"init", []string{"/remote/new project"}, []string{"init"}, "/remote/new project"},
		{"restore", []string{"robin", "", "/remote/archive"}, []string{"--agent", "robin", "restore", "--", "/remote/archive"}, ""},
		{"restore", []string{"", "/remote/absent", "/remote/archive"}, []string{"restore", "--", "/remote/archive"}, "/remote/absent"},
		{"mcp register", []string{"/remote/private.toml", "/remote/project"}, []string{"mcp", "register", "--config=/remote/private.toml", "--project=/remote/project"}, ""},
		{"mcp inspect", []string{"/remote/private.toml", "op123"}, []string{"mcp", "inspect", "--config=/remote/private.toml", "--operation=op123"}, ""},
	} {
		t.Run(tc.name+strings.Join(tc.values, ""), func(t *testing.T) {
			m := fixtureFleet()
			e := m.hosts[1].entries[0]
			index := specIndex(t, tc.name)
			if operationSpecs[index].scope == "host" {
				e = hostrouter.InventoryEntry{Target: hostrouter.Target{Host: e.Target.Host}}
			}
			f := operationForm{entry: e, index: index, values: tc.values, requestID: strings.Repeat("a", 64)}
			p, err := f.action()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.args, tc.want) || p.directory != tc.directory || p.entry.Target != e.Target {
				t.Fatalf("wrong request: %+v", p)
			}
		})
	}
}

func TestOperationGatingAndCapture(t *testing.T) {
	m := fixtureFleet()
	m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) { return exec.Command("unused"), nil }
	e := m.hosts[0].entries[0]
	for _, name := range []string{"sync", "credentials refresh", "worker status", "upgrade", "apply", "backup"} {
		if m.operationReason(operationSpecs[specIndex(t, name)], e) == "" {
			t.Fatal("not gated", name)
		}
	}
	m, _ = fleetKey(m, "tab")
	m, _ = fleetKey(m, "x")
	m.operations.index = specIndex(t, "rebuild")
	m, _ = fleetKey(m, "enter")
	m.selected = m.hosts[1].entries[0].Target
	m, _ = fleetKey(m, "enter")
	if m.pending == nil || m.pending.entry.Target != e.Target {
		t.Fatal("form redirected by selection")
	}
	m.hosts[0].entries[0].Project = "/replacement"
	m, cmd := fleetKey(m, "y")
	if cmd != nil || m.busy {
		t.Fatal("stale form dispatched")
	}
}

func TestOperationDispatchIdentityOutputAndErrors(t *testing.T) {
	for _, mode := range []string{"success", "mismatch", "permission", "disconnect", "exit"} {
		t.Run(mode, func(t *testing.T) {
			m := fixtureFleet()
			e := m.hosts[1].entries[0]
			m.services.Actions = actionFunc(func(_ context.Context, req hostrouter.ActionRequest, _ io.Reader, out, _ io.Writer) (hostrouter.ActionResult, error) {
				if req.Target != e.Target || req.Directory != "" {
					t.Fatal("wrong host or path", req)
				}
				_, _ = io.WriteString(out, "warning: fixture\n\x1b[31m"+strings.Repeat("x", 64<<10))
				r := hostrouter.ActionResult{Target: req.Target}
				switch mode {
				case "mismatch":
					r.Target.Host = hostrouter.Local
				case "permission":
					return r, errors.New("permission denied")
				case "disconnect":
					return r, errors.New("SSH disconnected; outcome unknown")
				case "exit":
					r.ExitCode = 9
				}
				return r, nil
			})
			p, _ := (operationForm{entry: e, index: specIndex(t, "worker status")}).action()
			out, cmd := m.dispatchOperation(p)
			m = out.(fleetModel)
			if !m.busy || cmd == nil {
				t.Fatal("action not running")
			}
			m, cmd = startOperation(t, m, cmd)
			done := cmd().(fleetActionDone)
			if (done.err == nil) != (mode == "success") {
				t.Fatal("wrong result", done.err)
			}
			if len(done.output) > 32<<10 || strings.ContainsRune(done.output, '\x1b') {
				t.Fatal("unbounded or unsafe output")
			}
			// A stale completion cannot clear a newer operation's busy gate.
			wrong := done
			wrong.request++
			out, _ = m.Update(wrong)
			if !out.(fleetModel).busy {
				t.Fatal("stale completion accepted")
			}
			wrong = done
			wrong.target = m.hosts[0].entries[0].Target
			out, _ = m.Update(wrong)
			if !out.(fleetModel).busy {
				t.Fatal("wrong target accepted")
			}
		})
	}
}

func TestHostOperationsAndRegistrationChanges(t *testing.T) {
	for _, hostIndex := range []int{0, 1} {
		m := fixtureFleet()
		m.workspace = 2
		m.server = m.hosts[hostIndex].host
		m, _ = fleetKey(m, "x")
		m.operations.index = specIndex(t, "doctor")
		m, _ = fleetKey(m, "enter")
		m, cmd := fleetKey(m, "enter")
		if cmd == nil {
			t.Fatal(m.status)
		}
		m, cmd = startOperation(t, m, cmd)
		if done := cmd().(fleetActionDone); done.err != nil {
			t.Fatal(done.err)
		}
		reqs := m.services.Actions.(*fixtureActions).requests
		if len(reqs) != 1 || reqs[0].Target.Host != m.server || reqs[0].Target.Kind != "" {
			t.Fatal("wrong host request", reqs)
		}
	}
	m := fixtureFleet()
	m.workspace = 2
	m.server = m.hosts[1].host
	m, _ = fleetKey(m, "x")
	m.operations.index = specIndex(t, "upgrade")
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "enter")
	if m.pending == nil || m.pending.entry.Target.Host != m.server {
		t.Fatal("host confirmation did not capture the selected server")
	}
	m.services.Registry.(*fixtureRegistry).hosts[0].Address = "replacement"
	m, cmd := fleetKey(m, "y")
	if cmd != nil || m.busy {
		t.Fatal("host confirmation redirected")
	}
}

func TestOperationFormEditingCancellationAndValidation(t *testing.T) {
	m := fixtureFleet()
	m.selected = m.hosts[1].entries[0].Target
	m, _ = fleetKey(m, "tab")
	m, _ = fleetKey(m, "x")
	m.operations.index = specIndex(t, "ping")
	m, _ = fleetKey(m, "enter")
	requestID := m.operations.requestID
	if len(requestID) != 64 {
		t.Fatal("missing idempotency key")
	}
	m, _ = fleetKey(m, "literal ; $(input)")
	m, _ = fleetKey(m, "enter")
	if m.pending == nil || !strings.Contains(strings.Join(m.pending.args, " "), requestID) {
		t.Fatal("request ID changed")
	}
	m, _ = fleetKey(m, "esc")
	if m.pending != nil || m.busy {
		t.Fatal("cancel dispatched")
	}
	m, _ = fleetKey(m, "v")
	if !strings.Contains(m.View(), "operator-managed") {
		t.Fatal("missing service runbook")
	}
	m, _ = fleetKey(m, "esc")
	if m.runbook {
		t.Fatal("cannot return")
	}
	for _, values := range [][]string{{"", "", "archive"}, {"agent", "/project", "archive"}, {"", "relative", "archive"}} {
		_, err := (operationForm{index: specIndex(t, "restore"), values: values}).action()
		if err == nil {
			t.Fatal("invalid restore accepted", values)
		}
	}
}

func TestOperationCancellation(t *testing.T) {
	m := fixtureFleet()
	e := m.hosts[1].entries[0]
	m.services.Actions = actionFunc(func(ctx context.Context, r hostrouter.ActionRequest, _ io.Reader, _, _ io.Writer) (hostrouter.ActionResult, error) {
		<-ctx.Done()
		return hostrouter.ActionResult{Target: r.Target}, ctx.Err()
	})
	p, _ := (operationForm{entry: e, index: specIndex(t, "worker status")}).action()
	out, cmd := m.dispatchOperation(p)
	m = out.(fleetModel)
	m, _ = fleetKey(m, "esc")
	m, cmd = startOperation(t, m, cmd)
	if done := cmd().(fleetActionDone); !errors.Is(done.err, context.Canceled) {
		t.Fatal(done.err)
	}
}

func TestForegroundUsesTeaExecAndReturnsToRefresh(t *testing.T) {
	m := fixtureFleet()
	e := m.hosts[0].entries[0]
	m.services.Foreground = func(_ context.Context, r hostrouter.ActionRequest) (*exec.Cmd, error) {
		if r.Target != e.Target || r.Directory != e.Project {
			t.Fatal("local terminal lost identity")
		}
		return exec.Command("unused"), nil
	}
	p, _ := (operationForm{entry: e, index: specIndex(t, "enter")}).action()
	out, cmd := m.dispatchOperation(p)
	m = out.(fleetModel)
	m, cmd = startOperation(t, m, cmd)
	if cmd == nil || cmd() == nil || !m.busy {
		t.Fatal("not a foreground command")
	}
	m.services.Inventory = hostrouter.Router{Local: inventoryFunc(func(context.Context, hostrouter.Host) ([]hostrouter.InventoryEntry, error) { return nil, nil })}
	out, cmd = m.Update(fleetActionDone{target: e.Target, operation: "enter", request: m.actionSequence})
	m = out.(fleetModel)
	if m.busy || m.selected != e.Target || cmd == nil || !m.refreshing {
		t.Fatal("terminal did not restore selection/refresh")
	}
}
