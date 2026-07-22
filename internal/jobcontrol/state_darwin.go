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
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Proc.P_stat == darwinProcessStopped, nil
}
