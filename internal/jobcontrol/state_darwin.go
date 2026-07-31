//go:build darwin

package jobcontrol

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinProcessStopped = 4

func processStopped(pid int) (bool, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// SysctlKinfoProc reports EIO when kern.proc.pid returns no KinfoProc bytes,
	// which is the normal result after the observed child has been reaped.
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EIO) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Proc.P_stat == darwinProcessStopped, nil
}
