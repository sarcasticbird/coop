package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/tui"
	"github.com/spf13/cobra"
)

// Re-exec keeps Cobra globals, cwd, signals and interactive prompts out of the
// Bubble Tea event loop. The receiver and SSH backend retain all CLI safeguards.
func tuiCommand(ctx context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
	if req.Operation != "command" {
		return nil, errors.New("invalid TUI operation")
	}
	v := hostrouter.Invocation{Version: 1, Target: req.Target, Directory: req.Directory, Args: req.Args}
	checked := v
	checked.Target.Host = hostrouter.Local
	if err := checked.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > 95<<10 {
		return nil, errors.New("TUI invocation too large")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, "tui-exec", base64.RawURLEncoding.EncodeToString(b))
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

type tuiActions struct{}

func (tuiActions) Execute(ctx context.Context, req hostrouter.ActionRequest, input io.Reader, output, stderr io.Writer) (hostrouter.ActionResult, error) {
	if req.Target.Host != hostrouter.Local {
		return (hostrouter.SSH{}).Execute(ctx, req, input, output, stderr)
	}
	result := hostrouter.ActionResult{Target: req.Target}
	cmd, err := tuiCommand(ctx, req)
	if err != nil {
		return result, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		}
		return result, errors.New("local Coop command failed; inspect host state before retrying")
	}
	return result, nil
}

func tuiForegroundCommand(ctx context.Context, req hostrouter.ActionRequest, credentials []string) (*exec.Cmd, error) {
	if len(credentials) > 0 && req.Target.Kind != "" {
		args := req.Args
		if len(args) >= 2 && args[0] == "--agent" {
			args = args[2:]
		}
		entry := len(args) > 0 && (args[0] == "--" || args[0] == "shell")
		agentAction := req.Target.Kind == hostrouter.Agent && len(args) > 0 &&
			(args[0] == "up" || args[0] == "sync" || (len(args) > 1 && args[0] == "credentials" && args[1] == "refresh"))
		if entry || agentAction {
			var b bytes.Buffer
			w := csv.NewWriter(&b)
			if err := w.Write(credentials); err != nil {
				return nil, err
			}
			w.Flush()
			if err := w.Error(); err != nil {
				return nil, err
			}
			req.Args = append([]string{"--credentials=" + strings.TrimSuffix(b.String(), "\n")}, req.Args...)
		}
	}
	return tuiCommand(ctx, req)
}

func runOperationalTUI(_ runtime.Runtime, credentials []string, masterConfig string) (tui.Result, error) {
	r, err := hostrouter.DefaultRegistry()
	if err != nil {
		return tui.Result{}, err
	}
	foreground := func(ctx context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
		return tuiForegroundCommand(ctx, req, credentials)
	}
	if masterConfig == "" {
		masterConfig = defaultMasterClientPath()
	}
	// A missing/invalid private config must not disable the fleet browser or
	// expose credential/path details. Coordination renders its unavailable state.
	var coordination tui.Coordination
	if client, err := masterclient.LoadConfig(masterConfig); err == nil {
		coordination = client
	}
	return tui.RunWithServices(tui.Services{Registry: r, Version: resolvedVersion(),
		Coordination: coordination,
		Inventory:    hostrouter.Router{Local: hostrouter.LocalClient{}, Remote: hostrouter.SSH{}},
		Actions:      tuiActions{}, Terminal: hostrouter.SSH{}, Foreground: foreground})
}

func tuiExecCommand() *cobra.Command {
	return &cobra.Command{Use: "tui-exec ENVELOPE", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := runtime.NotifyContext(cmd.Context())
		defer stop()
		cmd.SetContext(ctx)
		if len(args[0]) > 128<<10 {
			return errors.New("TUI invocation too large")
		}
		b, err := base64.RawURLEncoding.DecodeString(args[0])
		if err != nil {
			return errors.New("invalid TUI invocation")
		}
		var v hostrouter.Invocation
		if err := json.Unmarshal(b, &v); err != nil {
			return errors.New("invalid TUI invocation")
		}
		host := v.Target.Host
		if err := host.Validate(); err != nil {
			return err
		}
		v.Target.Host = hostrouter.Local
		local, err := json.Marshal(v)
		if err != nil {
			return err
		}
		encoded := base64.RawURLEncoding.EncodeToString(local)
		if _, err := hostrouter.DecodeInvocation(encoded); err != nil {
			return err
		}
		r, err := hostrouter.DefaultRegistry()
		if err != nil {
			return err
		}
		current, err := r.Resolve(host.Name)
		if err != nil || current != host {
			return errors.New("captured host changed; refresh before retry")
		}
		if host == hostrouter.Local {
			return receiveHostInvocation(cmd, []string{encoded})
		}
		target := v.Target
		target.Host = host
		return runRemoteTerminal(cmd.Context(), hostrouter.ActionRequest{Target: target, Operation: "command", Directory: v.Directory, Args: v.Args}, cmd)
	}}
}
