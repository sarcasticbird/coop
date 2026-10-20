package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
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
	initial  tea.Cmd
	returned chan fleetModel
}

func (m foregroundHarness) Init() tea.Cmd { return m.initial }
func (m foregroundHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	out, cmd := m.fleetModel.Update(msg)
	m.fleetModel = out.(fleetModel)
	if _, ok := msg.(fleetActionDone); ok {
		m.returned <- m.fleetModel
		return m, tea.Quit
	}
	return m, cmd
}

func TestForegroundProgramRestoresRealTTYAfterExitAndFailure(t *testing.T) {
	for _, code := range []string{"0", "7", "signal"} {
		t.Run(code, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			if err := pty.Setsize(slave, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			// Drain the real terminal so rendering cannot block on its PTY buffer.
			fd, err := unix.Dup(int(master.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.SetNonblock(fd, true); err != nil {
				_ = unix.Close(fd)
				t.Fatal(err)
			}
			reader := os.NewFile(uintptr(fd), "test-tty-output")
			drained := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, reader); close(drained) }()
			defer func() { _ = reader.Close(); <-drained }()
			m := fixtureFleet()
			m.services.Foreground = func(ctx context.Context, _ hostrouter.ActionRequest) (*exec.Cmd, error) {
				finish := "exit " + code
				if code == "signal" {
					finish = "kill -TERM $$"
				}
				return exec.CommandContext(ctx, "sh", "-c", "test -t 0 && test -t 1 || exit 99; stty -echo; "+finish), nil
			}
			p, _ := (operationForm{entry: m.hosts[0].entries[0], index: specIndex(t, "enter")}).action()
			out, cmd := m.dispatchOperation(p)
			m = out.(fleetModel)
			returned := make(chan fleetModel, 1)
			program := tea.NewProgram(foregroundHarness{fleetModel: m, initial: cmd, returned: returned}, tea.WithInput(slave), tea.WithOutput(slave), tea.WithAltScreen(), tea.WithoutSignalHandler())
			done := make(chan error, 1)
			go func() { _, err := program.Run(); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				program.Kill()
				<-done
				t.Fatal("foreground did not return")
			}
			after, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			if *before != *after {
				t.Fatal("Bubble Tea did not restore controlling terminal state")
			}
			result := <-returned
			want := "succeeded"
			if code != "0" {
				want = "failed"
			}
			if result.busy || result.selected != m.selected || !result.refreshing || !strings.Contains(result.status, want) {
				t.Fatalf("unusable return: %+v", result)
			}
		})
	}
}
