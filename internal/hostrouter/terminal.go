package hostrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

var _ TerminalClient = SSH{}
var _ Attachment = (*sshTerminal)(nil)

// Attach uses normal Coop entry (including a persistent agent's application
// attachment) without resolving remote project paths on the client.
func (s SSH) Attach(ctx context.Context, target Target, size TerminalSize) (Attachment, error) {
	args := []string{"--"}
	if target.Kind == Agent {
		args = []string{"--agent", target.ID, "--"}
	}
	return s.AttachCommand(ctx, ActionRequest{Target: target, Operation: "command", Args: args}, size)
}

// AttachCommand uses a local PTY and forces SSH's remote PTY allocation. Closing
// an attachment detaches the transport; it never runs a remote stop/down action.
func (s SSH) AttachCommand(ctx context.Context, req ActionRequest, size TerminalSize) (Attachment, error) {
	command, err := actionCommand(req)
	if err != nil {
		return nil, err
	}
	if size.Rows == 0 || size.Columns == 0 {
		return nil, errors.New("invalid terminal size")
	}
	registry, err := DefaultRegistry()
	if err != nil {
		return nil, errors.New("terminal registry unavailable")
	}
	h, err := registry.Resolve(req.Target.Host.Name)
	if err != nil || h != req.Target.Host {
		return nil, errors.New("stale host selection; refresh inventory")
	}
	if req.Target.Kind != "" || req.Target.ID != "" {
		if _, err := registry.ResolveTarget(req.Target); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", sshArgs(req.Target.Host, true, command)...)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: size.Rows, Cols: size.Columns})
	if err != nil {
		return nil, errors.New("SSH terminal unavailable")
	}
	// PTY setup uses File.Fd, which switches the master to blocking mode.
	// Restore polling so closing it interrupts blocked readers and writers.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		_ = master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("SSH terminal unavailable")
	}
	a := &sshTerminal{master: master, cmd: cmd, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, func() { _ = a.Close() })
	go func() {
		err := cmd.Wait()
		stop()
		a.mu.Lock()
		switch {
		case ctx.Err() != nil:
			a.err = ctx.Err()
			_ = master.Close()
		case a.detached:
			// Intentional detach is successful even though SSH was killed.
		case err != nil:
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				a.err = terminalExitError{code: exit.ExitCode()}
			} else {
				a.err = errors.New("SSH terminal disconnected")
			}
		}
		a.mu.Unlock()
		close(a.done)
	}()
	return a, nil
}

type sshTerminal struct {
	master   *os.File
	cmd      *exec.Cmd
	done     chan struct{}
	mu       sync.Mutex
	detached bool
	err      error
}

func (a *sshTerminal) Read(p []byte) (int, error) {
	n, err := a.master.Read(p)
	// Linux PTY masters report EIO when the last slave closes.
	if errors.Is(err, syscall.EIO) {
		err = io.EOF
	} else if err != nil && err != io.EOF {
		err = errors.New("SSH terminal read unavailable")
	}
	return n, err
}

func (a *sshTerminal) Write(p []byte) (int, error) {
	n, err := a.master.Write(p)
	if err != nil {
		err = errors.New("SSH terminal write unavailable")
	}
	return n, err
}

func (a *sshTerminal) Resize(size TerminalSize) error {
	if size.Rows == 0 || size.Columns == 0 {
		return errors.New("invalid terminal size")
	}
	// Avoid File.Fd (including pty.Setsize), which disables nonblocking IO.
	conn, err := a.master.SyscallConn()
	if err != nil {
		return errors.New("SSH terminal resize unavailable")
	}
	var resizeErr error
	err = conn.Control(func(fd uintptr) {
		resizeErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: size.Rows, Col: size.Columns})
	})
	if err != nil || resizeErr != nil {
		return errors.New("SSH terminal resize unavailable")
	}
	return nil
}

// Close detaches and reaps SSH. An intentional detach makes Wait return nil;
// context cancellation instead preserves the context error.
func (a *sshTerminal) Close() error {
	a.mu.Lock()
	if !a.detached {
		a.detached = true
		_ = a.master.Close()
		_ = a.cmd.Process.Kill()
	}
	a.mu.Unlock()
	<-a.done
	return nil
}

// Wait is idempotent and leaves output readable. Callers must still Close the
// attachment to release the PTY after draining it.
func (a *sshTerminal) Wait() error {
	<-a.done
	return a.err
}

// ExitCode allows callers to preserve the remote status without exposing an
// exec error's process arguments or SSH diagnostics.
type terminalExitError struct{ code int }

func (e terminalExitError) Error() string {
	if e.code == 255 {
		return "SSH terminal connection failed or disconnected; remote outcome may be unknown; no retry"
	}
	return fmt.Sprintf("SSH terminal exited (status %d)", e.code)
}

func (e terminalExitError) ExitCode() int       { return e.code }
func (e terminalExitError) RemoteExitCode() int { return e.code }
