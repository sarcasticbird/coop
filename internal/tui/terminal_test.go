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
	err    error
}

func (a *fixtureAttachment) Read(p []byte) (int, error)           { return a.reader.Read(p) }
func (a *fixtureAttachment) Write(p []byte) (int, error)          { return a.writes.Write(p) }
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
			a := &fixtureAttachment{reader: r, done: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- bridgeTerminal(ctx, a, input, io.Discard) }()
			if cancelled {
				cancel()
			} else {
				if _, err := writer.Write([]byte("hello\x03\x1dignored")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel returned %v", err)
				}
				if !cancelled && (err != nil || a.writes.String() != "hello\x03") {
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
}

func (f *fixtureTerminal) Attach(_ context.Context, target hostrouter.Target, size hostrouter.TerminalSize) (hostrouter.Attachment, error) {
	f.target, f.size = target, size
	if f.ready != nil {
		close(f.ready)
	}
	return f.attachment, nil
}

func TestTerminalCommandRestoresTTYAndCapturesIdentity(t *testing.T) {
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
	client := &fixtureTerminal{attachment: a, ready: make(chan struct{})}
	m := fixtureFleet()
	target := m.hosts[1].entries[0].Target
	c := &terminalCommand{ctx: context.Background(), client: client, registry: m.services.Registry, target: target, input: slave, output: io.Discard}
	done := make(chan error, 1)
	go func() { done <- c.Run() }()
	select {
	case <-client.ready:
	case <-time.After(time.Second):
		t.Fatal("terminal did not attach")
	}
	if _, err := master.Write([]byte("\x1d")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("detach did not return")
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
}

func TestTerminalReturnRefreshesWithoutQuittingShell(t *testing.T) {
	m := fixtureFleet()
	m.services.Terminal = &fixtureTerminal{}
	m.selected = m.hosts[1].entries[0].Target
	m, _ = fleetKey(m, "enter")
	m, cmd := fleetKey(m, "enter")
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
