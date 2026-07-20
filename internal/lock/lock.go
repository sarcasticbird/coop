// Package lock provides per-project host locks so concurrent coop
// invocations (CLI + TUI + scripts) can't interleave lifecycle
// operations on the same container.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Acquire takes an exclusive flock named after the coop. It retries
// for up to wait, then fails loudly (a silent queue would hide a hung
// operation elsewhere). The returned release function is idempotent.
func Acquire(name string, wait time.Duration) (func(), error) {
	dir := filepath.Join(stateHome(), "coop", "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock file: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another coop operation on %s is in progress (lock timeout after %s)", name, wait)
		}
		time.Sleep(150 * time.Millisecond)
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

func stateHome() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}
