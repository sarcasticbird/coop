package worker

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func ValidatePingInput(in PingInput) error {
	if in.RequestID != "" && !hexSHA.MatchString(in.RequestID) || !validWakeText(in.Prompt, MaxWakePrompt) {
		return errors.New("invalid ping input")
	}
	return nil
}
func DecodePingInput(in io.Reader) (PingInput, error) {
	var value PingInput
	raw, err := io.ReadAll(io.LimitReader(in, MaxWakeRecord+1))
	if err != nil || len(raw) > MaxWakeRecord {
		return value, errors.New("invalid ping input")
	}
	if strictJSON(raw, &value) != nil {
		return PingInput{}, errors.New("invalid ping input")
	}
	return value, ValidatePingInput(value)
}
func WorkerAcceptingWake(dir string, now time.Time) error {
	if err := ensurePrivateDir(dir, false); err != nil {
		return errors.New("worker_not_running")
	}
	status, err := ReadStatus(dir, now)
	if err != nil {
		return err
	}
	if !status.Enabled || !status.WakeEnabled || status.State == "not_running" || status.State == "stale" || status.Heartbeat.IsZero() || status.Heartbeat.After(now.Add(time.Minute)) {
		return errors.New("worker_not_running_or_wake_disabled")
	}
	path := filepath.Join(dir, "worker.lock")
	if err := safePath(path); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("worker_not_running")
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("unsafe worker lock")
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
			return errors.New("worker lock probe failed")
		}
		return errors.New("worker_not_running")
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil
	}
	return errors.New("worker lock probe failed")
}
