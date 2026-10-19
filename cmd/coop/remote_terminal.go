package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/spf13/cobra"
)

func runRemoteTerminal(ctx context.Context, req hostrouter.ActionRequest, cmd *cobra.Command) (err error) {
	stdin, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(stdin.Fd()) {
		return errors.New("remote terminal requires terminal stdin")
	}
	fd := stdin.Fd()
	size := func() (hostrouter.TerminalSize, error) {
		columns, rows, err := term.GetSize(fd)
		if err != nil {
			return hostrouter.TerminalSize{}, fmt.Errorf("read terminal size: %w", err)
		}
		if rows <= 0 || columns <= 0 || rows > 65535 || columns > 65535 {
			return hostrouter.TerminalSize{}, errors.New("invalid terminal size")
		}
		return hostrouter.TerminalSize{Rows: uint16(rows), Columns: uint16(columns)}, nil
	}
	initial, err := size()
	if err != nil {
		return err
	}
	input, err := cancelreader.NewReader(stdin)
	if err != nil {
		return fmt.Errorf("open cancelable terminal input: %w", err)
	}
	defer input.Close()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set terminal raw mode: %w", err)
	}
	defer func() { err = errors.Join(err, term.Restore(fd, state)) }()
	attachment, err := (hostrouter.SSH{}).AttachCommand(ctx, req, initial)
	if err != nil {
		return err
	}
	resizeCtx, cancelResize := context.WithCancel(ctx)
	resizeDone := make(chan struct{})
	go func() {
		defer close(resizeDone)
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
	defer func() {
		cancelResize()
		<-resizeDone
	}()
	// SSH's PTY merges stdout and stderr; diagnostics from Cobra retain its
	// configured stderr, while all terminal output goes to its configured stdout.
	return bridgeRemoteTerminal(ctx, attachment, input, cmd.OutOrStdout())
}

var errRemoteTerminalDetach = errors.New("local terminal detach")

// forwardRemoteTerminalInput reserves only Ctrl-]. In particular Ctrl-C stays
// a byte for the remote terminal rather than becoming a local process signal.
func forwardRemoteTerminalInput(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 4096)
	for {
		n, readErr := src.Read(buf)
		data := buf[:n]
		detach := bytes.IndexByte(data, 0x1d)
		if detach >= 0 {
			data = data[:detach]
		}
		if len(data) > 0 {
			written, err := dst.Write(data)
			if err != nil {
				return err
			}
			if written != len(data) {
				return io.ErrShortWrite
			}
		}
		if detach >= 0 {
			return errRemoteTerminalDetach
		}
		if readErr != nil {
			return readErr
		}
	}
}

// The attachment must unblock Read, Write and Wait when closed. The input must
// support cancellation of an in-flight read (the CLI supplies a terminal file).
func bridgeRemoteTerminal(ctx context.Context, attachment hostrouter.Attachment, input cancelreader.CancelReader, output io.Writer) error {
	inputDone, outputDone, waitDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(3)
	go func(done chan<- error) {
		defer workers.Done()
		done <- forwardRemoteTerminalInput(attachment, input)
	}(inputDone)
	go func(done chan<- error) {
		defer workers.Done()
		_, err := io.Copy(output, attachment)
		done <- err
	}(outputDone)
	go func(done chan<- error) {
		defer workers.Done()
		done <- attachment.Wait()
	}(waitDone)
	defer func() {
		input.Cancel()
		_ = attachment.Close()
		workers.Wait()
	}()
	var waitErr error
	for waitDone != nil || outputDone != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputDone:
			if errors.Is(err, errRemoteTerminalDetach) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-outputDone:
			outputDone = nil
			if err != nil {
				return err
			}
		case waitErr = <-waitDone:
			waitDone = nil
			// Do not close the attachment yet: buffered PTY output is still
			// readable after the process exits and must reach the caller.
			inputDone = nil
			input.Cancel()
		}
	}
	return waitErr
}
