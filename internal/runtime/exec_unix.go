//go:build unix

package runtime

import "golang.org/x/sys/unix"

// sysExec replaces the current process — required for clean tty
// passthrough into `container exec -it`.
func sysExec(bin string, argv []string, env []string) error {
	return unix.Exec(bin, argv, env)
}
