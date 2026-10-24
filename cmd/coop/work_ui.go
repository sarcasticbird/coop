package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"github.com/spf13/cobra"
)

func taskUICommand() *cobra.Command {
	return &cobra.Command{Use: "tui [REFERENCE]", Short: "Open the configured native Kata project or issue", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return openWorkUI(cmd, "task", args) }}
}

func chatCommand() *cobra.Command {
	return &cobra.Command{Use: "chat", Short: "Open the standing team ticket in native Kata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return openWorkUI(cmd, "chat", args) }}
}

func openWorkUI(cmd *cobra.Command, kind string, args []string) error {
	if cmd.Flags().Changed("credentials") {
		return errors.New("native work entry does not accept guest credentials")
	}
	cfg, err := config.LoadWorkTools()
	if err != nil {
		return err
	}
	client, err := masterClient(cmd)
	if err != nil {
		return errors.New("native work entry requires the private master-client.toml profile")
	}
	if client.MasterID() != cfg.MasterID {
		return errors.New("work tools and master client identities differ")
	}
	caps, err := client.TaskCapabilities(cmd.Context())
	if err != nil {
		return fmt.Errorf("verify selected task provider: %w", err)
	}
	if caps.Provider != "kata" || caps.ProjectUID != cfg.ProjectUID {
		return errors.New("selected master does not serve the pinned Kata project")
	}
	req := hostrouter.WorkUIRequest{MasterID: cfg.MasterID, ProjectUID: cfg.ProjectUID}
	if kind == "chat" {
		if caps.Conversation == nil || caps.Conversation.Provider != "kata" || caps.Conversation.ProjectUID != cfg.ProjectUID || caps.Conversation.InstanceUID == "" {
			return errors.New("kata conversation bridge is not configured on the selected master")
		}
		req.IssueUID = caps.Conversation.IssueUID
		if cfg.TeamIssueUID != "" && req.IssueUID != cfg.TeamIssueUID {
			return errors.New("standing team ticket differs from the configured pin")
		}
	} else if len(args) > 0 {
		project, issue, _, err := c.ParseKataTaskReference(args[0])
		if err != nil || project != cfg.ProjectUID {
			return errors.New("use a canonical Kata reference in the pinned project")
		}
		req.IssueUID = issue
	}
	registry, err := hostrouter.DefaultRegistry()
	if err != nil {
		return err
	}
	action, err := req.Action(registry, cfg.HostName)
	if err != nil {
		return err
	}
	if action.Target.Host == hostrouter.Local {
		return receiveWorkUI(cmd, req)
	}
	return remoteExecute(cmd.Context(), action, cmd, true)
}

func workUIExecCommand() *cobra.Command {
	return &cobra.Command{Use: "work-ui-exec ENVELOPE", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req, err := hostrouter.DecodeWorkUIRequest(args[0])
		if err != nil {
			return err
		}
		return receiveWorkUI(cmd, req)
	}}
}

func receiveWorkUI(cmd *cobra.Command, req hostrouter.WorkUIRequest) error {
	// A caller may not select private paths through the request or CLI overrides.
	path := masterclient.DefaultConfigPath("master.toml")
	raw, err := masterclient.ReadPrivateConfig(path)
	if err != nil {
		return errors.New("native work receiver requires private master.toml on the selected host")
	}
	var identity struct {
		MasterID string `toml:"master_id"`
		Tasks    struct {
			ProjectUID string `toml:"project_uid"`
		} `toml:"tasks"`
	}
	if _, err := toml.Decode(string(raw), &identity); err != nil || req.Validate() != nil || req.MasterID != identity.MasterID || req.ProjectUID != identity.Tasks.ProjectUID {
		return errors.New("native work master or project identity mismatch")
	}
	// Resolve only the trusted installed profile before the existing strict loader.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	cfg, err := master.LoadConfig(resolved)
	if err != nil {
		return errors.New("native work receiver configuration is invalid")
	}
	if cfg.Tasks == nil {
		return errors.New("native Kata provider is not configured on the selected host")
	}
	ctx, stop := runtime.NotifyContext(cmd.Context())
	defer stop()
	child, err := nativeWorkUICommand(ctx, req, cfg.MasterID, *cfg.Tasks)
	if err != nil {
		return err
	}
	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := child.Run(); err != nil {
		return fmt.Errorf("native Kata TUI exited; verify Kata supports tui on the selected host: %w", err)
	}
	return nil
}

func nativeWorkUICommand(ctx context.Context, req hostrouter.WorkUIRequest, masterID string, cfg taskstore.Config) (*exec.Cmd, error) {
	if req.Validate() != nil || req.MasterID != masterID || req.ProjectUID != cfg.ProjectUID {
		return nil, errors.New("native work master or project identity mismatch")
	}
	provider, err := taskstore.New(cfg)
	if err != nil {
		return nil, errors.New("native Kata provider configuration is invalid")
	}
	if cfg.InstanceUID != "" {
		work, ok := provider.(taskstore.WorkBackend)
		if !ok {
			return nil, errors.New("native Kata identity verification unavailable")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = work.Events(probeCtx, taskstore.EventCursor{InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID}, 1)
		cancel()
		if err != nil {
			return nil, errors.New("native Kata instance or project identity could not be verified")
		}
	}
	info, err := os.Stat(cfg.Binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("native Kata tool is missing or not executable on the selected host")
	}
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "KATA_") {
			env = append(env, value)
		}
	}
	env = append(env, "KATA_HOME="+cfg.Home)
	base := []string{"--workspace", cfg.Home, "--project", cfg.ProjectName}
	probe := func(args ...string) ([]byte, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		argv := append(append([]string{}, base...), "--json")
		argv = append(argv, args...)
		child := exec.CommandContext(probeCtx, cfg.Binary, argv...)
		child.Dir, child.Env = cfg.Home, env
		out := runtime.NewBoundedCapture(c.MaxJSON)
		child.Stdout = out
		if err := child.Run(); err != nil || out.Dropped() > 0 {
			return nil, errors.New("native Kata tool is unavailable or incompatible; verify the selected host provider")
		}
		return out.Bytes(), nil
	}
	raw, err := probe("daemon", "locate")
	if err != nil {
		return nil, err
	}
	var location struct{ Kind, Source, Network, Address string }
	if json.Unmarshal(raw, &location) != nil || location.Kind != "local" || location.Source != "local_default" || location.Network != "unix" || !strings.HasPrefix(location.Address, "unix:///") {
		return nil, errors.New("native Kata profile must select its local owner daemon")
	}
	raw, err = probe("projects", "show", cfg.ProjectName)
	if err != nil {
		return nil, err
	}
	var project struct {
		Project struct {
			UID string `json:"uid"`
		} `json:"project"`
	}
	if json.Unmarshal(raw, &project) != nil || project.Project.UID != req.ProjectUID {
		return nil, errors.New("native Kata project identity changed; display names cannot select a project")
	}
	if req.IssueUID != "" {
		// A fresh unclaimed inbound actor selects no discussion, as in the
		// provider's bounded issue inspector. TUI argv still contains only the UID.
		raw, err = probe("show", req.IssueUID, "--inbound=coop-inspect-"+rand.Text())
		if err != nil {
			return nil, err
		}
		var issue struct {
			Issue struct {
				UID        string `json:"uid"`
				ProjectUID string `json:"project_uid"`
			} `json:"issue"`
		}
		if json.Unmarshal(raw, &issue) != nil || issue.Issue.UID != req.IssueUID || issue.Issue.ProjectUID != req.ProjectUID {
			return nil, errors.New("native Kata issue does not belong to the pinned project")
		}
	}
	argv := append(base, "tui")
	if req.IssueUID != "" {
		argv = append(argv, req.IssueUID)
	}
	child := exec.CommandContext(ctx, cfg.Binary, argv...)
	child.Dir, child.Env = cfg.Home, env
	child.Cancel = func() error { return child.Process.Signal(os.Interrupt) }
	child.WaitDelay = 2 * time.Second
	return child, nil
}

// workUIForegroundCommand is shared by fleet Tasks/Chat. It launches the normal
// installed CLI and leaves host/provider resolution to that same entry path.
func workUIForegroundCommand(ctx context.Context, args []string, masterConfig ...string) (*exec.Cmd, error) {
	if len(args) < 1 || len(args) > 2 || (args[0] != "task" && args[0] != "chat") || (args[0] == "chat" && len(args) != 1) {
		return nil, errors.New("invalid native work action")
	}
	argv := []string{"chat"}
	if args[0] == "task" {
		argv = []string{"task", "tui"}
	}
	if len(args) == 2 {
		if _, _, _, err := c.ParseKataTaskReference(args[1]); err != nil {
			return nil, errors.New("invalid native task reference")
		}
		argv = append(argv, args[1])
	}
	if len(masterConfig) > 0 && masterConfig[0] != "" {
		argv = append([]string{"--master-config=" + masterConfig[0]}, argv...)
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	child := exec.CommandContext(ctx, binary, argv...)
	child.Cancel = func() error { return child.Process.Signal(os.Interrupt) }
	child.WaitDelay = 2 * time.Second
	return child, nil
}
