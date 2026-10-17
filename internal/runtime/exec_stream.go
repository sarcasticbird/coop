package runtime

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// ExecStreams is noninteractive and never starts a container or acquires credentials.
func (a *Apple) ExecStreams(parent context.Context, name, workdir string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, execTimeout)
	defer cancel()
	args := append([]string{"exec"}, ifStdin(stdin)...)
	args = append(args, "--workdir", workdir, name)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("guest transport: %w", e)
	}
	return nil
}
