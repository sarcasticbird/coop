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
	initial      tea.Cmd
	returned     chan fleetModel
	windowSizes  int
	finished     bool
	inputHandled bool
}

func (m foregroundHarness) Init() tea.Cmd { return nil }
func (m foregroundHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	out, cmd := m.fleetModel.Update(msg)
	m.fleetModel = out.(fleetModel)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowSizes++
		if m.windowSizes == 1 {
			return m, m.initial
		}
	case fleetActionDone:
		m.returned <- m.fleetModel
		m.finished = true
		cmd = nil
	case tea.KeyMsg:
		if m.finished && msg.String() == "?" {
			m.inputHandled = true
		}
	}
	// Consume both startup and post-exec resize checks before closing their TTY.
	if m.finished && m.windowSizes >= 2 && m.inputHandled {
		return m, tea.Quit
	}
	return m, cmd
}

func TestForegroundProgramRestoresRealTTYAfterExitAndFailure(t *testing.T) {
	for _, code := range []string{"0", "7", "signal", "remote detach"} {
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m.ctx = ctx
			m.services.Foreground = func(ctx context.Context, _ hostrouter.ActionRequest) (*exec.Cmd, error) {
				finish := "exit " + code
				if code == "signal" {
					finish = "kill -TERM $$"
				}
				return exec.CommandContext(ctx, "sh", "-c", "test -t 0 && test -t 1 || exit 99; stty -echo; "+finish), nil
			}
			p, _ := (operationForm{entry: m.hosts[0].entries[0], index: specIndex(t, "enter")}).action()
			var client *fixtureTerminal
			var attachment *fixtureAttachment
			if code == "remote detach" {
				r, w := io.Pipe()
				defer func() { _ = w.Close() }()
				attachment = &fixtureAttachment{reader: r, done: make(chan struct{})}
				defer func() { _ = attachment.Close() }()
				client = &fixtureTerminal{attachment: attachment, ready: make(chan struct{})}
				// Exercise the in-process attachment bridge rather than the CLI adapter.
				m.services.Foreground = nil
				m.services.Terminal = client
				m.selected = m.hosts[1].entries[0].Target
				p = pendingAction{entry: m.hosts[1].entries[0], operation: "enter"}
			}
			m.selected = p.entry.Target
			m.filter = p.entry.Target.Host
			captured := p.entry
			m.detail, m.detailEntry = true, &captured
			m.scroll, m.ledgerScroll, m.ledgerFocus = 2, 1, true
			var out tea.Model
			var cmd tea.Cmd
			if client != nil {
				out, cmd = m.dispatch(p)
			} else {
				out, cmd = m.dispatchOperation(p)
			}
			m = out.(fleetModel)
			returned := make(chan fleetModel, 1)
			program := tea.NewProgram(foregroundHarness{fleetModel: m, initial: cmd, returned: returned}, tea.WithInput(slave), tea.WithOutput(slave), tea.WithAltScreen(), tea.WithoutSignalHandler())
			done := make(chan error, 1)
			exited := make(chan struct{})
			var final tea.Model
			go func() {
				defer close(exited)
				var err error
				final, err = program.Run()
				done <- err
			}()
			defer func() { cancel(); program.Kill(); <-exited }()
			if client != nil {
				select {
				case <-client.ready:
				case <-time.After(5 * time.Second):
					t.Fatal("remote terminal did not attach")
				}
				if _, err := master.Write([]byte("\x04\x1dignored")); err != nil {
					t.Fatal(err)
				}
			}
			var result fleetModel
			select {
			case result = <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("foreground completion did not return to browser")
			}
			// Send real terminal input after exec; a model-only Update cannot prove
			// that Bubble Tea released and reacquired its cancelable input reader.
			if _, err := master.Write([]byte("?")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				cancel()
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
			if resumed := final.(foregroundHarness); !resumed.help || !resumed.inputHandled {
				t.Fatal("browser did not handle keyboard input after foreground return")
			}
			if attachment != nil && attachment.writes.String() != "\x04" {
				t.Fatalf("remote input = %q, want only Ctrl+D", attachment.writes.String())
			}
			want := "succeeded"
			if code == "7" || code == "signal" {
				want = "failed"
			}
			if result.busy || result.selected != m.selected || !result.refreshing || !strings.Contains(result.status, want) || !result.detail || result.detailEntry.Target != captured.Target || result.filter != m.filter || result.scroll != 2 || result.ledgerScroll != 1 || !result.ledgerFocus {
				t.Fatalf("unusable return: busy=%t selected=%t refreshing=%t status=%q detail=%t target=%t filter=%t scroll=%d ledgerScroll=%d ledgerFocus=%t", result.busy, result.selected == m.selected, result.refreshing, result.status, result.detail, result.detailEntry != nil && result.detailEntry.Target == captured.Target, result.filter == m.filter, result.scroll, result.ledgerScroll, result.ledgerFocus)
			}
		})
	}
}
