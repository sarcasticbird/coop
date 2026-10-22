package tui

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestForegroundTTYFlagChild(t *testing.T) {
	value := os.Getenv("COOP_TEST_TTY_FD")
	if value == "" {
		return
	}
	fd, err := strconv.Atoi(value)
	if err != nil {
		os.Exit(98)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		os.Exit(98)
	}
	if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags^unix.O_NONBLOCK); err != nil {
		os.Exit(98)
	}
	code, _ := strconv.Atoi(os.Getenv("COOP_TEST_TTY_EXIT"))
	os.Exit(code)
}

func TestForegroundRestoresTTYDescriptorFlags(t *testing.T) {
	for _, target := range []int{0, 1, 2} {
		for _, code := range []int{0, 7} {
			t.Run("fd"+strconv.Itoa(target)+"/exit"+strconv.Itoa(code), func(t *testing.T) {
				var files [3]*os.File
				var fds [3]uintptr
				var flags [3]int
				for i := range files {
					master, slave, err := pty.Open()
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
					files[i] = slave
					fds[i] = slave.Fd()
					flags[i], err = unix.FcntlInt(fds[i], unix.F_GETFL, 0)
					if err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestForegroundTTYFlagChild$")
				cmd.Env = append(os.Environ(), "COOP_TEST_TTY_FD="+strconv.Itoa(target), "COOP_TEST_TTY_EXIT="+strconv.Itoa(code))
				cmd.Stdin, cmd.Stdout, cmd.Stderr = files[0], files[1], files[2]
				err := (&foregroundCommand{command: cmd}).Run()
				var exit *exec.ExitError
				if code == 0 && err != nil || code != 0 && (!errors.As(err, &exit) || exit.ExitCode() != code) {
					t.Fatalf("child exit %d: %v", code, err)
				}
				for i := range files {
					// File.Fd can itself restore blocking mode on a pollable file.
					// Inspect the saved raw descriptor without masking the leak.
					after, err := unix.FcntlInt(fds[i], unix.F_GETFL, 0)
					if err != nil {
						t.Fatal(err)
					}
					if after != flags[i] {
						t.Errorf("fd%d flags changed from %#x to %#x", i, flags[i], after)
					}
				}
			})
		}
	}
}
