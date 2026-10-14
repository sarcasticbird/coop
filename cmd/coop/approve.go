package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/spf13/cobra"
)

func approveForAgent(cmd *cobra.Command, name, goal string) error {
	_, profile, err := config.LoadProfile(name)
	if err != nil {
		return err
	}
	var path string
	for _, seed := range profile.Seeds {
		if seed.Dest == "/var/lib/coop/worker/config.json" {
			path = seed.Src
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		return errors.New("agent requires a host worker config seed for approval")
	}
	c, err := loadHostApprovalConfig(path)
	if err != nil {
		return err
	}
	if c.Name != name {
		return errors.New("worker configuration does not match selected agent")
	}
	repos, err := cmd.Flags().GetStringSlice("repo")
	if err != nil {
		return err
	}
	comment, err := worker.ApproveGoal(cmd.Context(), c, goal, repos, worker.ExecRunner{})
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"approved": true, "goal": goal, "decision_url": comment.URL})
}

func loadHostApprovalConfig(path string) (worker.Config, error) {
	// Host seed sources intentionally support stow symlinks. Resolve only this
	// host-owned source; the guest's private state retains its no-symlink rules.
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return worker.Config{}, errors.New("cannot resolve host worker config source")
	}
	return worker.LoadConfig(canonical)
}
