// Package jobcontrol configures child commands for Unix terminal job control.
package jobcontrol

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Terminal records the foreground process group that must be restored after
// a configured child exits.
type Terminal struct {
	fd              int
	foregroundGroup int
	childGroup      int
	restoreNeeded   bool
	childChanges    chan os.Signal
	stopChanges     sync.Once
	mu              sync.Mutex

	getForeground func() (int, error)
	setForeground func(int) error
	suspendSelf   func() error
	signalGroup   func(int, syscall.Signal) error
	stopped       func(int) (bool, error)
}

// Configure places cmd in a dedicated process group. When input is a terminal,
// child setup atomically gives that group foreground ownership before exec.
func Configure(cmd *exec.Cmd, input *os.File) (*Terminal, error) {
	terminal := &Terminal{fd: int(input.Fd())}
	terminal.getForeground = func() (int, error) {
		return unix.IoctlGetInt(terminal.fd, unix.TIOCGPGRP)
	}
	terminal.setForeground = func(group int) error {
		return unix.IoctlSetPointerInt(terminal.fd, unix.TIOCSPGRP, group)
	}
	terminal.suspendSelf = func() error { return syscall.Kill(-syscall.Getpgrp(), syscall.SIGTSTP) }
	terminal.signalGroup = syscall.Kill
	terminal.stopped = processStopped
	foreground, err := terminal.getForeground()
	if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.ENODEV) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return terminal, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect terminal foreground group: %w", err)
	}
	current := syscall.Getpgrp()
	if foreground != current {
		return nil, fmt.Errorf("terminal belongs to foreground process group %d, not Coop group %d", foreground, current)
	}

	configureForegroundChild(cmd, terminal, foreground)
	return terminal, nil
}

func configureForegroundChild(cmd *exec.Cmd, terminal *Terminal, foreground int) {
	// Go's Foreground fork path blocks signals around the child-side
	// TIOCSPGRP, so the parent must not ignore SIGTTOU before Start: ignored
	// dispositions would survive exec in the target process.
	terminal.foregroundGroup = foreground
	terminal.restoreNeeded = true
	terminal.childChanges = make(chan os.Signal, 1)
	signal.Notify(terminal.childChanges, syscall.SIGCHLD)
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: terminal.fd}
}

// Monitor coordinates terminal ownership when the foreground child stops and
// later continues through the parent shell's fg/bg job-control commands.
func (t *Terminal) Monitor(processGroup int, done <-chan struct{}) <-chan error {
	result := make(chan error, 1)
	if t.childChanges == nil {
		result <- nil
		return result
	}
	t.mu.Lock()
	t.childGroup = processGroup
	t.mu.Unlock()
	go func() {
		defer t.stopChildNotifications()
		for {
			select {
			case <-done:
				result <- nil
				return
			case <-t.childChanges:
				stopped, err := t.stopped(processGroup)
				if err != nil {
					result <- t.abortChild(processGroup, fmt.Errorf("inspect child job-control state: %w", err))
					return
				}
				if stopped {
					if err := t.suspendForChild(processGroup); err != nil {
						result <- t.abortChild(processGroup, err)
						return
					}
				}
			}
		}
	}()
	return result
}

func (t *Terminal) abortChild(processGroup int, cause error) error {
	err := t.signalGroup(-processGroup, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("terminate child after job-control failure: %w", err)
	}
	return errors.Join(cause, err)
}

func (t *Terminal) suspendForChild(processGroup int) error {
	foreground, err := t.getForeground()
	if err != nil {
		return fmt.Errorf("inspect terminal before suspending Coop: %w", err)
	}
	if foreground == processGroup {
		if err := t.restoreForeground(); err != nil {
			return err
		}
	} else {
		// A child resumed with bg can stop again while the parent shell owns
		// the terminal. Preserve that ownership and restore normal SIGTTOU
		// handling before Coop suspends itself.
		signal.Reset(syscall.SIGTTOU)
	}
	if err := t.suspendSelf(); err != nil {
		return fmt.Errorf("suspend Coop with interactive child: %w", err)
	}

	foreground, err = t.getForeground()
	if err != nil {
		return fmt.Errorf("inspect terminal after Coop resumed: %w", err)
	}
	var foregroundErr error
	if foreground == t.foregroundGroup {
		foregroundErr = t.giveForeground(processGroup)
	}
	continueErr := t.signalGroup(-processGroup, syscall.SIGCONT)
	if continueErr != nil && !errors.Is(continueErr, syscall.ESRCH) {
		continueErr = fmt.Errorf("continue interactive child group: %w", continueErr)
	} else {
		continueErr = nil
	}
	return errors.Join(foregroundErr, continueErr)
}

func (t *Terminal) giveForeground(processGroup int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	signal.Ignore(syscall.SIGTTOU)
	if err := t.setForeground(processGroup); err != nil {
		signal.Reset(syscall.SIGTTOU)
		return fmt.Errorf("give terminal to continued child group: %w", err)
	}
	return nil
}

func (t *Terminal) restoreForeground() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	signal.Ignore(syscall.SIGTTOU)
	err := t.setForeground(t.foregroundGroup)
	signal.Reset(syscall.SIGTTOU)
	if err != nil {
		return fmt.Errorf("restore terminal foreground group: %w", err)
	}
	return nil
}

func (t *Terminal) stopChildNotifications() {
	t.stopChanges.Do(func() {
		if t.childChanges != nil {
			signal.Stop(t.childChanges)
		}
	})
}

// Restore returns terminal foreground ownership to Coop after the child exits.
func (t *Terminal) Restore() error {
	t.stopChildNotifications()
	if !t.restoreNeeded {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	defer signal.Reset(syscall.SIGTTOU)
	if t.childGroup != 0 {
		foreground, err := t.getForeground()
		if err != nil {
			return fmt.Errorf("inspect terminal before restoring foreground group: %w", err)
		}
		if foreground != t.childGroup {
			return nil
		}
	}
	signal.Ignore(syscall.SIGTTOU)
	if err := t.setForeground(t.foregroundGroup); err != nil {
		return fmt.Errorf("restore terminal foreground group: %w", err)
	}
	return nil
}
