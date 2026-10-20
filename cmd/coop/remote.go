package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var remoteExecute = func(ctx context.Context, req hostrouter.ActionRequest, cmd *cobra.Command, tty bool) error {
	if tty {
		return runRemoteTerminal(ctx, req, cmd)
	}
	ctx, stop := runtime.NotifyContext(ctx)
	defer stop()
	_, err := (hostrouter.SSH{}).Execute(ctx, req, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	return err
}

func remoteSelected(cmd *cobra.Command) bool {
	name, _ := cmd.Root().PersistentFlags().GetString("host")
	return name != "local" && name != ""
}

func commandParts(cmd *cobra.Command) []string {
	var parts []string
	for c := cmd; c.Parent() != nil; c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return parts
}

// Every executable command must be deliberately classified. A new CLI action
// cannot accidentally acquire remote capability without updating this list.
func remoteCommandAllowed(parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	switch strings.Join(parts, " ") {
	case "init", "up", "down", "status", "shell", "sync", "apply", "rebuild", "destroy", "backup", "restore", "doctor", "upgrade", "ping", "approve",
		"credentials refresh", "credentials watch", "worker status", "worker pause", "worker resume", "worker requests",
		"master serve", "master backup", "master restore", "master reconcile status", "master reconcile pause", "master reconcile resume", "master reconcile evidence", "master reconcile resolve", "master placement set", "master history import", "master history list", "master cutover",
		"thread list", "thread create", "thread read", "thread members", "message send", "message continue", "message status", "team", "team inspect", "inbox", "goal propose", "goal list", "goal authority", "goal approve", "goal reject", "goal pause", "goal resume", "goal complete", "goal reopen",
		"mcp serve", "mcp stdio", "mcp inspect", "mcp register":
		return true
	}
	return false
}

func projectCommand(parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	switch parts[0] {
	case "init", "up", "down", "status", "shell", "apply", "rebuild", "destroy", "backup", "restore":
		return true
	}
	return false
}

func routeHostCommand(cmd *cobra.Command, args []string, h hostrouter.Host) error {
	parts := commandParts(cmd)
	if !remoteCommandAllowed(parts) {
		return errors.New("this command is controller-local and does not support remote --host; no local action was executed")
	}
	global := cmd.Root().PersistentFlags()
	directory, _ := global.GetString("remote-directory")
	kind, _ := global.GetString("target-kind")
	id, _ := global.GetString("target-id")
	agent, _ := global.GetString("agent")
	if global.Changed("agent") && agent == "" {
		return errors.New("--agent requires a configured profile name")
	}
	if kind == "" && id == "" && agent != "" {
		kind, id = string(hostrouter.Agent), agent
	}
	if kind == string(hostrouter.Agent) {
		if agent == "" {
			agent = id
		}
		if id != agent || directory != "" {
			return errors.New("agent target must match --agent and cannot select a project directory")
		}
	} else if agent != "" {
		return errors.New("--agent conflicts with captured target")
	}
	if projectCommand(parts) && agent == "" && directory == "" && kind != string(hostrouter.Interactive) {
		return errors.New("remote project commands require --remote-directory /server/project or a captured interactive target; controller cwd is never forwarded")
	}
	argv := []string{}
	var flagErr error
	appendFlag := func(f *pflag.Flag) {
		value := f.Value.String()
		if slice, ok := f.Value.(pflag.SliceValue); ok {
			var b bytes.Buffer
			w := csv.NewWriter(&b)
			if err := w.Write(slice.GetSlice()); err != nil {
				flagErr = err
				return
			}
			w.Flush()
			if w.Error() != nil {
				flagErr = w.Error()
				return
			}
			value = strings.TrimSuffix(b.String(), "\n")
		}
		argv = append(argv, "--"+f.Name+"="+value)
	}
	global.VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		switch f.Name {
		case "host", "remote-directory", "target-kind", "target-id", "tty", "agent":
			return
		}
		appendFlag(f)
	})
	if agent != "" {
		argv = append(argv, "--agent="+agent)
	}
	argv = append(argv, parts...)
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f == global.Lookup(f.Name) {
			return
		}
		appendFlag(f)
	})
	if flagErr != nil {
		return flagErr
	}
	// Stop parsing before guest argv, archive names or other positional input.
	argv = append(argv, "--")
	argv = append(argv, args...)
	req := hostrouter.ActionRequest{Target: hostrouter.Target{Host: h, Kind: hostrouter.TargetKind(kind), ID: id}, Operation: "command", Directory: directory, Args: argv}
	tty, _ := global.GetBool("tty")
	if len(parts) == 0 || (len(parts) == 1 && (parts[0] == "shell" || parts[0] == "init" || parts[0] == "destroy")) {
		tty = true
	}
	if err := remoteExecute(cmd.Context(), req, cmd, tty); err != nil {
		return fmt.Errorf("host %s: %w", h.Name, err)
	}
	return nil
}

func receiveHostInvocation(cmd *cobra.Command, args []string) error {
	v, err := hostrouter.DecodeInvocation(args[0])
	if err != nil {
		return err
	}
	if v.Target.Kind == hostrouter.Interactive {
		rows, err := newInventoryRouter().Local.Inventory(cmd.Context(), hostrouter.Local)
		if err != nil {
			return fmt.Errorf("revalidate remote target: %w", err)
		}
		found := false
		for _, row := range rows {
			if row.Target == v.Target {
				if row.Project == "" || (v.Directory != "" && filepath.Clean(v.Directory) != filepath.Clean(row.Project)) {
					return errors.New("captured project path changed; refresh inventory")
				}
				v.Directory, found = row.Project, true
				break
			}
		}
		if !found {
			return errors.New("captured target no longer exists; refresh inventory")
		}
	}
	if v.Directory != "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(v.Directory); err != nil {
			return fmt.Errorf("open selected server directory: %w", err)
		}
		defer func() { _ = os.Chdir(cwd) }()
		if v.Target.Kind == hostrouter.Interactive {
			s, err := session.New(newRuntime(), v.Directory)
			if err != nil {
				return err
			}
			if s.Name != v.Target.ID {
				return errors.New("project identity changed; refresh inventory")
			}
		}
	}
	local := root()
	// Enforce the wire boundary after Cobra parses flags but before any normal
	// command handler. The receiving process retains all existing local checks.
	var guard func(*cobra.Command)
	guard = func(c *cobra.Command) {
		if c.RunE != nil {
			original := c.RunE
			c.RunE = func(c *cobra.Command, a []string) error {
				parts := commandParts(c)
				if !remoteCommandAllowed(parts) {
					return errors.New("command is not remotely routable")
				}
				host, _ := local.PersistentFlags().GetString("host")
				if host != "local" {
					return errors.New("nested remote routing is forbidden")
				}
				for _, f := range []string{"remote-directory", "target-kind", "target-id", "tty"} {
					if local.PersistentFlags().Changed(f) {
						return errors.New("nested host selectors are forbidden")
					}
				}
				agent, _ := local.PersistentFlags().GetString("agent")
				if v.Target.Kind == hostrouter.Agent && agent != v.Target.ID {
					return errors.New("agent identity mismatch")
				}
				if v.Target.Kind == hostrouter.Interactive && agent != "" {
					return errors.New("target kind mismatch")
				}
				if projectCommand(parts) && agent == "" && v.Directory == "" {
					return errors.New("remote project directory is required")
				}
				return original(c, a)
			}
		}
		for _, child := range c.Commands() {
			guard(child)
		}
	}
	guard(local)
	local.SetContext(cmd.Context())
	local.SetIn(cmd.InOrStdin())
	local.SetOut(cmd.OutOrStdout())
	local.SetErr(cmd.ErrOrStderr())
	local.SetArgs(append([]string{"--host=local"}, v.Args...))
	return local.Execute()
}
