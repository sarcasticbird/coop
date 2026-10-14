package main

import (
	"context"
	"github.com/sarcasticbird/coop/internal/runtime"
	"slices"
	"testing"
)

func TestAgentSelectorDoesNotConsumeGuestArguments(t *testing.T) {
	var name string
	var argv []string
	old := runNamedAgent
	t.Cleanup(func() { runNamedAgent = old })
	runNamedAgent = func(_ context.Context, n string, args, grants []string) error {
		name = n
		argv = slices.Clone(args)
		return nil
	}
	cmd := root()
	cmd.SetArgs([]string{"--agent", "robin", "claude", "--agent", "guest-value"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if name != "robin" || !slices.Equal(argv, []string{"claude", "--agent", "guest-value"}) {
		t.Fatalf("name=%q argv=%q", name, argv)
	}
}
func TestExplicitEmptyAgentRejectedBeforeRuntime(t *testing.T) {
	old := newRuntime
	t.Cleanup(func() { newRuntime = old })
	newRuntime = func() runtime.Runtime { t.Fatal("constructed runtime for invalid selection"); return nil }
	cmd := root()
	cmd.SetArgs([]string{"--agent="})
	if err := cmd.Execute(); err == nil {
		t.Fatal("empty agent accepted")
	}
}

func TestWorkerControlsRequireAgentAndRejectCredentialGrants(t *testing.T) {
	for _, args := range [][]string{{"worker", "status"}, {"--agent", "robin", "--credentials", "sentinel", "worker", "pause"}} {
		cmd := root()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("worker control accepted missing agent or credential grant")
		}
	}
}

func TestPingRequiresAgentAndRejectsCredentialGrants(t *testing.T) {
	for _, args := range [][]string{{"ping", "--prompt", "question"}, {"--agent", "robin", "--credentials", "sentinel", "ping"}, {"--agent", "robin", "ping", "unexpected-positional"}, {"--agent", "robin", "ping", "--request-id", "invalid"}} {
		cmd := root()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("invalid ping accepted: %v", args)
		}
	}
}

func TestApproveRequiresAgentAndGoalAndRejectsCredentialGrants(t *testing.T) {
	for _, args := range [][]string{{"approve", "https://github.com/planning/coordination/discussions/1"}, {"--agent", "hawk", "approve"}, {"--agent", "hawk", "--credentials", "secret", "approve", "https://github.com/planning/coordination/discussions/1"}} {
		cmd := root()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("unsafe approval accepted %v", args)
		}
	}
}
