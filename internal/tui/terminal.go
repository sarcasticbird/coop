package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"golang.org/x/sys/unix"
)

// Bubble Tea reinitializes raw mode after exec and captures a new baseline.
// Restore the original cooked state first, even if a child exits in raw mode.
type foregroundCommand struct{ command *exec.Cmd }

func (c *foregroundCommand) SetStdin(r io.Reader)  { c.command.Stdin = r }
func (c *foregroundCommand) SetStdout(w io.Writer) { c.command.Stdout = w }
func (c *foregroundCommand) SetStderr(w io.Writer) { c.command.Stderr = w }
func (c *foregroundCommand) Run() (err error) {
	restoreFlags, err := saveTerminalDescriptorFlags(c.command.Stdin, c.command.Stdout, c.command.Stderr)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restoreFlags()) }()
	file, ok := c.command.Stdin.(*os.File)
	if !ok || !term.IsTerminal(file.Fd()) {
		return errors.New("foreground action requires terminal stdin")
	}
	state, err := term.GetState(file.Fd())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(file.Fd(), state)) }()
	return c.command.Run()
}

// A child shares inherited open-file descriptions with its caller. Restoring
// termios does not undo O_NONBLOCK left by a terminal client on stdout/stderr.
func saveTerminalDescriptorFlags(streams ...any) (func() error, error) {
	type descriptor struct {
		fd    uintptr
		flags int
	}
	var saved []descriptor
	for _, stream := range streams {
		file, ok := stream.(*os.File)
		if !ok {
			continue
		}
		conn, err := file.SyscallConn()
		if err != nil {
			return nil, err
		}
		var readErr error
		// File.Fd can itself change blocking mode; observe the original flags.
		if err = conn.Control(func(fd uintptr) {
			if !term.IsTerminal(fd) {
				return
			}
			var flags int
			flags, readErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
			if readErr == nil {
				saved = append(saved, descriptor{fd, flags})
			}
		}); err != nil || readErr != nil {
			return nil, errors.Join(err, readErr)
		}
	}
	return func() (err error) {
		for _, item := range saved {
			_, restoreErr := unix.FcntlInt(item.fd, unix.F_SETFL, item.flags)
			err = errors.Join(err, restoreErr)
		}
		return err
	}, nil
}

// Bubble Tea releases its input reader and alternate screen before Run and
// redraws the model afterward. Only this presentation bridge owns raw mode.
type terminalCommand struct {
	ctx      context.Context
	client   hostrouter.TerminalClient
	registry Registry
	target   hostrouter.Target
	input    io.Reader
	output   io.Writer
}

func (c *terminalCommand) SetStdin(r io.Reader)  { c.input = r }
func (c *terminalCommand) SetStdout(w io.Writer) { c.output = w }
func (c *terminalCommand) SetStderr(io.Writer)   {}

func (c *terminalCommand) Run() (err error) {
	file, ok := c.input.(*os.File)
	if !ok || !term.IsTerminal(file.Fd()) {
		return errors.New("attachment requires terminal stdin")
	}
	if _, err := c.registry.ResolveTarget(c.target); err != nil {
		return err
	}
	size := func() (hostrouter.TerminalSize, error) {
		w, h, err := term.GetSize(file.Fd())
		if err != nil || w <= 0 || h <= 0 || w > 65535 || h > 65535 {
			return hostrouter.TerminalSize{}, errors.New("invalid terminal dimensions")
		}
		return hostrouter.TerminalSize{Rows: uint16(h), Columns: uint16(w)}, nil
	}
	initial, err := size()
	if err != nil {
		return err
	}
	input, err := cancelreader.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	state, err := term.MakeRaw(file.Fd())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(file.Fd(), state)) }()
	ctx, cancel := signal.NotifyContext(c.ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	attachment, err := c.client.Attach(ctx, c.target, initial)
	if err != nil {
		return err
	}
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	resizeCtx, cancelResize := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-resizeCtx.Done():
				return
			case <-resizes:
				if next, err := size(); err == nil {
					_ = attachment.Resize(next)
				}
			}
		}
	}()
	defer func() { cancelResize(); <-done }()
	return bridgeTerminal(ctx, attachment, input, c.output)
}

var errDetach = errors.New("terminal detached")

func forwardInput(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		data := buf[:n]
		detach := bytes.IndexByte(data, 0x1d)
		if detach >= 0 {
			data = data[:detach]
		}
		if len(data) > 0 {
			written, writeErr := dst.Write(data)
			if writeErr != nil {
				return writeErr
			}
			if written != len(data) {
				return io.ErrShortWrite
			}
		}
		if detach >= 0 {
			return errDetach
		}
		if err != nil {
			return err
		}
	}
}

func bridgeTerminal(ctx context.Context, a hostrouter.Attachment, input cancelreader.CancelReader, output io.Writer) error {
	inputDone, outputDone, waitDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(3)
	go func(done chan<- error) { defer workers.Done(); done <- forwardInput(a, input) }(inputDone)
	go func(done chan<- error) { defer workers.Done(); _, err := io.Copy(output, a); done <- err }(outputDone)
	go func(done chan<- error) { defer workers.Done(); done <- a.Wait() }(waitDone)
	defer func() { input.Cancel(); _ = a.Close(); workers.Wait() }()
	var waitErr error
	for outputDone != nil || waitDone != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputDone:
			if errors.Is(err, errDetach) || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("terminal input: %w", err)
		case err := <-outputDone:
			outputDone = nil
			if err != nil {
				return err
			}
		case waitErr = <-waitDone:
			waitDone = nil
			inputDone = nil
			input.Cancel()
		}
	}
	return waitErr
}
