package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"golang.org/x/sys/unix"
)

type foregroundHarness struct {
	fleetModel
	initial      tea.Cmd
	returned     chan fleetModel
	windows      int
	finished     bool
	inputHandled bool
}

func (m foregroundHarness) Init() tea.Cmd { return nil }
func (m foregroundHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	out, cmd := m.fleetModel.Update(msg)
	m.fleetModel = out.(fleetModel)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windows++
		if m.windows == 1 {
			return m, m.initial
		}
	case fleetActionDone:
		m.returned <- m.fleetModel
		m.finished = true
		cmd = nil
	case tea.KeyMsg:
		if m.finished && msg.String() == "r" {
			m.inputHandled = true
			cmd = nil
		}
	}
	if m.finished && m.inputHandled && m.windows >= 2 {
		return m, tea.Quit
	}
	return m, cmd
}

func TestTableForegroundRestoresTTYAndReturnsToInteractiveTable(t *testing.T) {
	for _, action := range []string{"local", "remote", "Kata", "child-failure", "changed-registration"} {
		t.Run(action, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			fd, err := unix.Dup(int(master.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(fd, true); err != nil {
				_ = unix.Close(fd)
				t.Fatal(err)
			}
			reader := os.NewFile(uintptr(fd), "table-test-output")
			drained := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, reader); close(drained) }()
			defer func() { _ = reader.Close(); <-drained }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m, _ := tableFixture()
			m.ctx = ctx
			m.services.Foreground = func(ctx context.Context, _ hostrouter.ActionRequest) (*exec.Cmd, error) {
				command := "test -t 0 && test -t 1 || exit 99; stty -echo; exit 0"
				if action == "child-failure" {
					command = "stty -echo; exit 7"
				}
				return exec.CommandContext(ctx, "sh", "-c", command), nil
			}
			key := "enter"
			if action == "local" {
				m.selected = m.hosts[0].entries[0].Target
			}
			if action == "Kata" {
				key = "k"
			}
			selected := m.selected
			m, cmd := tableKey(m, key)
			if cmd == nil || !m.busy {
				t.Fatal("foreground action was not scheduled")
			}
			if action == "changed-registration" {
				m.services.Registry.(*testRegistry).hosts[0].Address = "replacement-host"
			}
			returned := make(chan fleetModel, 1)
			program := tea.NewProgram(foregroundHarness{fleetModel: m, initial: cmd, returned: returned}, tea.WithInput(slave), tea.WithOutput(slave), tea.WithAltScreen(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
			done := make(chan error, 1)
			go func() { _, err := program.Run(); done <- err }()
			select {
			case current := <-returned:
				if current.busy || current.selected != selected || !current.refreshing {
					t.Fatal("child did not restore the selected table")
				}
			case <-ctx.Done():
				t.Fatal("foreground action did not return")
			}
			if _, err := master.Write([]byte("r")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				program.Kill()
				t.Fatal("table stopped handling terminal input after return")
			}
			after, err := term.GetState(slave.Fd())
			if err != nil || *after != *before {
				t.Fatal("terminal settings were not restored", err)
			}
		})
	}
}
