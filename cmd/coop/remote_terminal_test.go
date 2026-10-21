package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/spf13/cobra"
)

type terminalInputChunks struct{ chunks [][]byte }

func (r *terminalInputChunks) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestForwardRemoteTerminalInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
		err    error
	}{
		{"normal bytes", []string{"abc", "\x03\x00\x1b[A", "\r\n"}, "abc\x03\x00\x1b[A\r\n", io.EOF},
		{"Ctrl+D is remote input", []string{"\x04", "still attached", "\x1dnot remote"}, "\x04still attached", errRemoteTerminalDetach},
		{"Ctrl+D before detach in chunk", []string{"abc\x04\x1dnot remote"}, "abc\x04", errRemoteTerminalDetach},
		{"detach in chunk", []string{"abc\x1dnot remote"}, "abc", errRemoteTerminalDetach},
		{"detach separate chunk", []string{"ab", "c", "\x1d", "not remote"}, "abc", errRemoteTerminalDetach},
		{"detach at chunk end", []string{"abc\x1d", "not remote"}, "abc", errRemoteTerminalDetach},
		{"detach first", []string{"\x1dnot remote"}, "", errRemoteTerminalDetach},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &terminalInputChunks{}
			for _, chunk := range tc.chunks {
				input.chunks = append(input.chunks, []byte(chunk))
			}
			var output bytes.Buffer
			if err := forwardRemoteTerminalInput(&output, input); !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if output.String() != tc.want {
				t.Fatalf("forwarded %q, want %q", output.String(), tc.want)
			}
		})
	}
}

type terminalAttachmentFixture struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	input  bytes.Buffer
	wrote  chan []byte
	exited chan struct{}
	once   sync.Once
	err    error
}

func newTerminalAttachmentFixture(t *testing.T) *terminalAttachmentFixture {
	t.Helper()
	r, w := io.Pipe()
	a := &terminalAttachmentFixture{reader: r, writer: w, exited: make(chan struct{})}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func (a *terminalAttachmentFixture) Read(p []byte) (int, error) { return a.reader.Read(p) }
func (a *terminalAttachmentFixture) Write(p []byte) (int, error) {
	n, err := a.input.Write(p)
	if a.wrote != nil {
		a.wrote <- append([]byte(nil), p[:n]...)
	}
	return n, err
}
func (a *terminalAttachmentFixture) Resize(hostrouter.TerminalSize) error { return nil }
func (a *terminalAttachmentFixture) Wait() error                          { <-a.exited; return a.err }
func (a *terminalAttachmentFixture) Close() error {
	a.once.Do(func() { close(a.exited) })
	_ = a.reader.Close()
	return a.writer.Close()
}

func terminalCancelablePipe(t *testing.T) (cancelreader.CancelReader, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	input, err := cancelreader.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	return input, w
}

func awaitTerminalBridge(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("terminal bridge did not return and join its workers")
		return nil
	}
}

func TestRemoteTerminalBridgeCancellation(t *testing.T) {
	a := newTerminalAttachmentFixture(t)
	input, _ := terminalCancelablePipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridgeRemoteTerminal(ctx, a, input, io.Discard) }()
	cancel()
	if err := awaitTerminalBridge(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestRemoteTerminalBridgeExitDrainsOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"success", nil}, {"failure", errors.New("remote exit failure")}} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTerminalAttachmentFixture(t)
			a.err = tc.err
			input, _ := terminalCancelablePipe(t)
			var output bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- bridgeRemoteTerminal(context.Background(), a, input, &output) }()
			// Wait completes before the final output becomes readable.
			a.once.Do(func() { close(a.exited) })
			writeDone := make(chan error, 1)
			want := strings.Repeat("trailing output\r\n", 8192)
			go func() {
				_, err := io.WriteString(a.writer, want)
				_ = a.writer.Close()
				writeDone <- err
			}()
			if err := awaitTerminalBridge(t, done); !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if err := <-writeDone; err != nil {
				t.Fatal(err)
			}
			if output.String() != want {
				t.Fatalf("output has %d bytes, want %d", output.Len(), len(want))
			}
		})
	}
}

func TestRemoteTerminalBridgeDetach(t *testing.T) {
	a := newTerminalAttachmentFixture(t)
	a.wrote = make(chan []byte, 1)
	input, w := terminalCancelablePipe(t)
	done := make(chan error, 1)
	go func() { done <- bridgeRemoteTerminal(context.Background(), a, input, io.Discard) }()
	if _, err := w.Write([]byte("\x04")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-a.wrote:
		if string(got) != "\x04" {
			t.Fatalf("Ctrl+D forwarded as %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+D was not forwarded")
	}
	select {
	case err := <-done:
		t.Fatalf("Ctrl+D detached: %v", err)
	default:
	}
	if _, err := w.Write([]byte("hello\x03\x1dignored")); err != nil {
		t.Fatal(err)
	}
	if err := awaitTerminalBridge(t, done); err != nil {
		t.Fatal(err)
	}
	if got := a.input.String(); got != "\x04hello\x03" {
		t.Fatalf("forwarded %q", got)
	}
}

func TestRunRemoteTerminalRequiresTerminalInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	for _, input := range []io.Reader{strings.NewReader("input"), r} {
		cmd := &cobra.Command{}
		cmd.SetIn(input)
		if err := runRemoteTerminal(context.Background(), hostrouter.ActionRequest{}, cmd); err == nil || !strings.Contains(err.Error(), "terminal stdin") {
			t.Fatalf("error = %v, want terminal stdin rejection", err)
		}
	}
}

func TestRunRemoteTerminalRestoresStateOnAttachFailure(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetIn(slave)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := runRemoteTerminal(context.Background(), hostrouter.ActionRequest{}, cmd); err == nil {
		t.Fatal("invalid attachment unexpectedly succeeded")
	}
	after, err := term.GetState(slave.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal state was not restored after attach failure")
	}
}
