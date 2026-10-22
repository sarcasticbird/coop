package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type fixtureAttachment struct {
	reader io.ReadCloser
	done   chan struct{}
	once   sync.Once
	writes bytes.Buffer
	wrote  chan []byte
	err    error
}

func (a *fixtureAttachment) Read(p []byte) (int, error) { return a.reader.Read(p) }
func (a *fixtureAttachment) Write(p []byte) (int, error) {
	n, err := a.writes.Write(p)
	if a.wrote != nil {
		a.wrote <- append([]byte(nil), p[:n]...)
	}
	return n, err
}
func (a *fixtureAttachment) Resize(hostrouter.TerminalSize) error { return nil }
func (a *fixtureAttachment) Wait() error                          { <-a.done; return a.err }
func (a *fixtureAttachment) Close() error {
	a.once.Do(func() { _ = a.reader.Close(); close(a.done) })
	return nil
}

func terminalInput(t *testing.T) (cancelreader.CancelReader, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := cancelreader.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = r.Close(); _ = w.Close() })
	return reader, w
}

func TestTerminalBridgeDetachAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "detach", true: "cancel"}[cancelled], func(t *testing.T) {
			input, writer := terminalInput(t)
			r, w := io.Pipe()
			defer func() { _ = w.Close() }()
			a := &fixtureAttachment{reader: r, done: make(chan struct{}), wrote: make(chan []byte, 1)}
			defer func() { _ = a.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- bridgeTerminal(ctx, a, input, io.Discard) }()
			if cancelled {
				cancel()
			} else {
				if _, err := writer.Write([]byte("\x04")); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-a.wrote:
					if string(got) != "\x04" {
						t.Fatalf("Ctrl+D forwarded as %q", got)
					}
				case <-time.After(time.Second):
					t.Fatal("Ctrl+D was not forwarded")
				}
				select {
				case err := <-done:
					t.Fatalf("Ctrl+D detached: %v", err)
				default:
				}
				if _, err := writer.Write([]byte("hello\x03\x1dignored")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel returned %v", err)
				}
				if !cancelled && (err != nil || a.writes.String() != "\x04hello\x03") {
					t.Fatalf("detach err=%v input=%q", err, a.writes.String())
				}
			case <-time.After(time.Second):
				t.Fatal("attachment failed to close and reap bridge workers")
			}
		})
	}
}

func TestTerminalBridgeDrainsOutputAndPreservesExit(t *testing.T) {
	input, _ := terminalInput(t)
	want := strings.Repeat("terminal output\n", 1000)
	a := &fixtureAttachment{reader: io.NopCloser(strings.NewReader(want)), done: make(chan struct{}), err: errors.New("remote exit 7")}
	// Wait may complete before all output is read. Close still owns the done signal.
	a.once.Do(func() { close(a.done) })
	var output bytes.Buffer
	err := bridgeTerminal(context.Background(), a, input, &output)
	if !errors.Is(err, a.err) || output.String() != want {
		t.Fatalf("output lost or exit swallowed: err=%v bytes=%d", err, output.Len())
	}
}

type fixtureTerminal struct {
	attachment hostrouter.Attachment
	target     hostrouter.Target
	size       hostrouter.TerminalSize
	ready      chan struct{}
	err        error
}

func (f *fixtureTerminal) Attach(_ context.Context, target hostrouter.Target, size hostrouter.TerminalSize) (hostrouter.Attachment, error) {
	f.target, f.size = target, size
	if f.ready != nil {
		close(f.ready)
	}
	return f.attachment, f.err
}

func TestTerminalCommandRestoresTTYAndCapturesIdentity(t *testing.T) {
	for _, outcome := range []string{"detach", "cancel", "exit", "failure", "attach failure"} {
		t.Run(outcome, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			r, w := io.Pipe()
			defer func() { _ = w.Close() }()
			a := &fixtureAttachment{reader: r, done: make(chan struct{})}
			defer func() { _ = a.Close() }()
			client := &fixtureTerminal{attachment: a, ready: make(chan struct{})}
			wantErr := errors.New("synthetic terminal failure")
			if outcome == "failure" {
				a.err = wantErr
			}
			if outcome == "attach failure" {
				client.err = wantErr
			}
			m := fixtureFleet()
			target := m.hosts[1].entries[0].Target
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &terminalCommand{ctx: ctx, client: client, registry: m.services.Registry, target: target, input: slave, output: io.Discard}
			done := make(chan error, 1)
			go func() { done <- c.Run() }()
			select {
			case <-client.ready:
			case <-time.After(time.Second):
				t.Fatal("terminal did not attach")
			}
			if outcome != "attach failure" {
				during, err := term.GetState(slave.Fd())
				if err != nil {
					t.Fatal(err)
				}
				if *during == *before {
					t.Fatal("attachment never entered raw mode")
				}
			}
			switch outcome {
			case "detach":
				if _, err := master.Write([]byte("\x04\x1dignored")); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			case "exit", "failure":
				a.once.Do(func() { close(a.done) })
				_ = w.Close()
			}
			select {
			case err := <-done:
				var want error
				switch outcome {
				case "cancel":
					want = context.Canceled
				case "failure", "attach failure":
					want = wantErr
				}
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal did not return")
			}
			after, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			if *before != *after {
				t.Fatal("terminal state was not restored")
			}
			if client.target != target || client.size != (hostrouter.TerminalSize{Rows: 24, Columns: 80}) {
				t.Fatalf("wrong attachment: %+v %+v", client.target, client.size)
			}
			if outcome == "detach" && a.writes.String() != "\x04" {
				t.Fatalf("forwarded %q, want only Ctrl+D", a.writes.String())
			}
		})
	}
}

func TestTerminalReturnRefreshesWithoutQuittingShell(t *testing.T) {
	m := fixtureFleet()
	m.services.Terminal = &fixtureTerminal{}
	m.selected = m.hosts[1].entries[0].Target
	m, _ = fleetKey(m, "enter")
	m, cmd := fleetKey(m, "c")
	if cmd != nil || m.pending == nil || m.pending.operation != "enter" {
		t.Fatal("remote connect did not require confirmation")
	}
	m, cmd = fleetKey(m, "y")
	if cmd == nil || !m.busy || m.result.EnterName != "" {
		t.Fatal("remote enter did not suspend into terminal")
	}
	out, cmd := m.Update(fleetActionDone{target: m.selected, operation: "terminal"})
	m = out.(fleetModel)
	if m.busy || !m.refreshing || cmd == nil || m.result.EnterName != "" {
		t.Fatal("terminal return did not restore and refresh browser")
	}
}
