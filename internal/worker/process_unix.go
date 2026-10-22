//go:build darwin || linux

package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/sarcasticbird/coop/internal/githubapp"
)

type boundedOutput struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := b.limit - len(b.data)
	if n > left {
		b.data = append(b.data, p[:left]...)
		b.exceeded = true
		return n, nil
	}
	b.data = append(b.data, p...)
	return n, nil
}

// ExecRunner keeps command diagnostics private; callers expose categorized errors.
type ExecRunner struct{ Dir string }

func (r ExecRunner) RunIn(ctx context.Context, dir string, argv, env []string, input io.Reader) (CommandResult, error) {
	r.Dir = dir
	return r.Run(ctx, argv, env, input)
}

func (r ExecRunner) Run(ctx context.Context, argv, env []string, input io.Reader) (CommandResult, error) {
	if len(argv) == 0 {
		return CommandResult{}, errors.New("empty worker command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.Dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.Env = githubapp.CleanEnvironment(os.Environ())
	var clean []string
	for _, entry := range cmd.Env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GH_REPO" {
			clean = append(clean, entry)
		}
	}
	cmd.Env = clean
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "GH_TOKEN" || key == "GITHUB_TOKEN" || key == "GH_REPO" {
			return CommandResult{}, errors.New("invalid worker command environment")
		}
		var next []string
		for _, old := range cmd.Env {
			if !strings.HasPrefix(old, key+"=") {
				next = append(next, old)
			}
		}
		cmd.Env = append(next, entry)
	}
	cmd.Stdin = input
	out := &boundedOutput{limit: githubCommandResponseLimit(argv) + (64 << 10)}
	stderr := &boundedOutput{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err != nil {
		if cleanupErr := cmd.Cancel(); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
			return CommandResult{}, errors.New("worker command cleanup failed")
		}
	}
	result := CommandResult{Stdout: out.data, Stderr: stderr.data}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	if out.exceeded || stderr.exceeded {
		return result, errors.New("worker command output limit")
	}
	if err != nil {
		return result, errors.New("worker command failed")
	}
	return result, nil
}
