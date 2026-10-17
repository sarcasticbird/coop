package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/releasetool"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/spf13/cobra"
)

var runNamedAgent = func(ctx context.Context, name string, args, grants []string) error {
	s, err := agent.New(name)
	if err != nil {
		return err
	}
	ctx, stop := runtime.NotifyContext(ctx)
	defer stop()
	if len(args) > 0 {
		if err := s.PrepareCommand(ctx, grants); err != nil {
			return err
		}
		return s.Command(ctx, args)
	}
	if err := s.Up(ctx, grants); err != nil {
		return err
	}
	return s.Attach(ctx)
}

func installAgentCommands(root *cobra.Command, grants *[]string) {
	var selected string
	root.PersistentFlags().StringVar(&selected, "agent", "", "select a host-configured persistent agent machine")
	root.AddCommand(
		&cobra.Command{Use: "shell", Args: cobra.NoArgs, Short: "Open a shell in the selected Coop", RunE: func(cmd *cobra.Command, _ []string) error {
			s, cwd, err := current()
			if err != nil {
				return err
			}
			return s.Run(cwd, nil, *grants)
		}},
		&cobra.Command{Use: "sync", Args: cobra.NoArgs, Short: "Clone missing repositories for a named agent", RunE: agentOnly},
		&cobra.Command{Use: "apply", Args: cobra.NoArgs, Short: "Apply pending maintenance to a stopped Coop", RunE: func(cmd *cobra.Command, _ []string) error { return applyInteractive(cmd) }},
		&cobra.Command{Use: "backup destination", Args: cobra.ExactArgs(1), Short: "Back up a stopped Coop", RunE: func(cmd *cobra.Command, args []string) error { return backupInteractive(cmd, args[0]) }},
		&cobra.Command{Use: "restore archive", Args: cobra.ExactArgs(1), Short: "Restore a backup into an absent Coop", RunE: func(cmd *cobra.Command, args []string) error { return restoreInteractive(cmd, args[0]) }},
	)
	credentials := &cobra.Command{Use: "credentials", Short: "Refresh named-agent credentials"}
	credentials.AddCommand(&cobra.Command{Use: "refresh", Args: cobra.NoArgs, RunE: agentOnly})
	credentials.AddCommand(credentialWatchCommand())
	root.AddCommand(credentials)
	workerCommand := &cobra.Command{Use: "worker", Short: "Inspect or pause named-agent automation"}
	for _, operation := range []string{"status", "pause", "resume", "requests"} {
		workerCommand.AddCommand(&cobra.Command{Use: operation, Args: cobra.NoArgs, RunE: agentOnly})
	}
	root.AddCommand(workerCommand)
	ping := &cobra.Command{Use: "ping", Args: cobra.NoArgs, Short: "Queue an owner prompt for a running agent", RunE: agentOnly}
	ping.Flags().String("prompt", "", "private owner prompt")
	ping.Flags().String("request-id", "", "optional retry idempotency key")
	root.AddCommand(ping)
	approve := &cobra.Command{Use: "approve goal-url", Args: cobra.ExactArgs(1), Short: "Approve a goal using your host GitHub identity", RunE: agentOnly}
	approve.Flags().StringSlice("repo", nil, "goal repositories, required if the Discussion has no scope metadata")
	root.AddCommand(approve)
	var wrap func(*cobra.Command)
	wrap = func(cmd *cobra.Command) {
		if cmd.RunE != nil {
			original := cmd.RunE
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				if !root.PersistentFlags().Changed("agent") {
					return original(cmd, args)
				}
				if selected == "" {
					return fmt.Errorf("--agent requires a configured profile name")
				}
				requested, err := requestedCredentialNames(cmd, *grants)
				if err != nil {
					return err
				}
				if cmd == root {
					return runNamedAgent(cmd.Context(), selected, args, requested)
				}
				return namedOperation(cmd, selected, args, requested)
			}
		}
		for _, child := range cmd.Commands() {
			wrap(child)
		}
	}
	wrap(root)
}
func agentOnly(_ *cobra.Command, _ []string) error {
	return fmt.Errorf("this operation requires --agent <profile>")
}

func namedOperation(cmd *cobra.Command, name string, args, grants []string) error {
	op := cmd.Name()
	if cmd.Parent() != nil && cmd.Parent().Name() == "credentials" {
		op = "credentials refresh"
	}
	if cmd.Parent() != nil && cmd.Parent().Name() == "worker" {
		op = "worker " + cmd.Name()
	}
	switch op {
	case "up", "down", "status", "shell", "sync", "credentials refresh", "apply", "rebuild", "destroy", "backup", "restore", "worker status", "worker pause", "worker resume", "worker requests", "ping", "approve":
	default:
		return fmt.Errorf("--agent is not supported with %s", cmd.Name())
	}
	if len(grants) > 0 && op != "up" && op != "shell" && op != "sync" && op != "credentials refresh" {
		return fmt.Errorf("--credentials is not valid with %s", op)
	}
	if op == "approve" {
		return approveForAgent(cmd, name, args[0])
	}
	if op == "ping" {
		prompt, _ := cmd.Flags().GetString("prompt")
		id, _ := cmd.Flags().GetString("request-id")
		if err := worker.ValidatePingInput(worker.PingInput{Prompt: prompt, RequestID: id}); err != nil {
			return err
		}
	}
	s, err := agent.New(name)
	if err != nil {
		return err
	}
	s.Output = cmd.OutOrStdout()
	ctx, stop := runtime.NotifyContext(cmd.Context())
	defer stop()
	switch op {
	case "ping":
		prompt, _ := cmd.Flags().GetString("prompt")
		id, _ := cmd.Flags().GetString("request-id")
		return s.Ping(ctx, worker.PingInput{Prompt: prompt, RequestID: id})
	case "worker status", "worker pause", "worker resume", "worker requests":
		return s.Worker(ctx, strings.TrimPrefix(op, "worker "))
	case "up":
		return s.Up(ctx, grants)
	case "down":
		return s.Down(ctx)
	case "status":
		return s.Status(ctx)
	case "shell":
		return s.Shell(ctx, grants)
	case "sync":
		return s.Sync(ctx, grants)
	case "credentials refresh":
		return s.Refresh(ctx, grants)
	case "apply":
		return s.Apply(ctx)
	case "rebuild":
		return rebuildAgent(ctx, s, cmd.OutOrStdout(), cmd.ErrOrStderr())
	case "backup":
		return backupAgent(ctx, s, args[0])
	case "restore":
		return restoreAgent(ctx, s, args[0])
	case "destroy":
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "remove agent %s and its entire guest disk? [y/N] ", name); err != nil {
			return err
		}
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) != "y" {
			return fmt.Errorf("aborted")
		}
		return s.Destroy(ctx)
	}
	return fmt.Errorf("unknown agent operation")
}

func rebuildAgent(ctx context.Context, s *agent.Session, stdout, stderr io.Writer) error {
	resolved, err := resolveReleaseTools(ctx, s.Spec.Config.Tools.GitHubReleases)
	if err != nil {
		return err
	}
	cfg := s.Spec.Config
	cfg.Tools.ResolvedReleases = resolved
	dir, err := image.MaterializeMachineWithCoreLock(cfg.Tools.Packages, resolved, s.CoreLock)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	name := image.MachineImageName(cfg, s.CoreLock)
	if _, err := fmt.Fprintf(stdout, "agent image: %s\n", name); err != nil {
		return err
	}
	if err := buildImage([]string{"build", "-t", name, dir}, stdout, stderr); err != nil {
		return err
	}
	stateDir, err := releasetool.StateDir()
	if err != nil {
		return err
	}
	return saveReleaseToolLock(stateDir, "agent:"+s.Spec.Identity, cfg.Tools.GitHubReleases, resolved)
}
