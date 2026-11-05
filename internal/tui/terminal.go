package tui

import (
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// Bubble Tea releases its terminal for the existing CLI and redraws afterward.
// Restore both termios and descriptor flags if a child leaves them changed.
type foregroundCommand struct {
	command *exec.Cmd
	before  func() error
}

func (c *foregroundCommand) SetStdin(r io.Reader)  { c.command.Stdin = r }
func (c *foregroundCommand) SetStdout(w io.Writer) { c.command.Stdout = w }
func (c *foregroundCommand) SetStderr(w io.Writer) { c.command.Stderr = w }
func (c *foregroundCommand) Run() (err error) {
	if c.before != nil {
		if err := c.before(); err != nil {
			return err
		}
	}
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
		if err = conn.Control(func(fd uintptr) {
			if term.IsTerminal(fd) {
				var flags int
				flags, readErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
				if readErr == nil {
					saved = append(saved, descriptor{fd, flags})
				}
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
