package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
)

func teamCLIConfig(t *testing.T, name string) (worker.Config, string) {
	t.Helper()
	raw, err := os.ReadFile("../../examples/worker-team.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := worker.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c.StateDir = filepath.Join(t.TempDir(), "state")
	c.WorkRoot = filepath.Join(t.TempDir(), "work")
	for _, m := range c.Team.Members {
		if m.Name == name {
			c.Name, c.Self = m.Name, m.Actor
		}
	}
	return c, installConfigCopy(t, c)
}

func TestWorkerCLIInitializesFirstQAOffline(t *testing.T) {
	c, path := teamCLIConfig(t, "qa")
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) {
		t.Fatal("init-state contacted a service")
		return nil, nil, nil
	}
	var out bytes.Buffer
	if err := run([]string{"init-state", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	state, err := worker.NewVersionedFileStore(c.StateDir, 2).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(state.InstanceID)) {
		t.Fatal("CLI did not identify persisted instance")
	}
	before := installLedgerBytes(t, c.StateDir)
	if err = run([]string{"init-state", "--config", path}, &out); err == nil {
		t.Fatal("CLI reinitialized existing QA")
	}
	after := installLedgerBytes(t, c.StateDir)
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if !bytes.Equal(b1, b2) {
		t.Fatal("refusal changed saved evidence")
	}
}

type metadataGitHub struct {
	*readOnlyGitHub
	snapshot worker.Snapshot
}

func (g *metadataGitHub) Snapshot(context.Context, worker.Config, worker.Snapshot) (worker.Snapshot, error) {
	g.reads++
	return g.snapshot, nil
}

func (g *metadataGitHub) InitialAssignmentSnapshot(ctx context.Context, c worker.Config, _ string) (worker.Snapshot, error) {
	return g.Snapshot(ctx, c, worker.Snapshot{})
}

func TestWorkerCLITeamPlannerPublishesFirstAssignment(t *testing.T) {
	c, path := teamCLIConfig(t, "planner")
	now := time.Now().UTC()
	goal := worker.Goal{URL: "https://github.com/example/board/discussions/1", CreatedAt: now.Add(-time.Hour), Readable: true, Repositories: []string{"example/repo"}}
	body, _ := json.Marshal(worker.TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{"example/repo": "feature/integration"}})
	goal.Body = "<!-- coop-goal:v2 " + string(body) + " -->"
	goal.BodySHA = worker.BodyHash(goal.Body)
	goal.Decisions = []worker.Decision{{ID: "approved", Author: c.Owner, Command: "/coop approve", CreatedAt: now}}
	task := worker.Task{URL: "https://github.com/example/repo/issues/1", Repo: "example/repo", Title: "Bounded task", Body: "Approved work"}
	task.BodySHA = worker.TaskScopeHash(task.Title, task.Body)
	policy, err := worker.TeamPolicySHA(*c.Team)
	if err != nil {
		t.Fatal(err)
	}
	a := worker.TaskAssignment{Version: 2, Author: "builder", Peer: "planner", BaseBranch: "feature/integration", GoalBodySHA: goal.BodySHA, TaskBodySHA: task.BodySHA, PolicySHA: policy}
	raw, _ := json.Marshal(a)
	assignment := filepath.Join(t.TempDir(), "assignment.json")
	if err = os.WriteFile(assignment, raw, 0600); err != nil {
		t.Fatal(err)
	}
	g := &metadataGitHub{readOnlyGitHub: &readOnlyGitHub{t: t}, snapshot: worker.Snapshot{Complete: true, Tasks: []worker.Task{task}, Goals: []worker.Goal{goal}}}
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) { return g, nil, nil }
	var out bytes.Buffer
	if err = run([]string{"task-metadata", "--config", path, "--task", task.URL, "--goal", goal.URL, "--assignment", assignment}, &out); err != nil {
		t.Fatal(err)
	}
	if g.reads != 1 || !bytes.Contains(out.Bytes(), []byte("coop-task:v2")) {
		t.Fatal("CLI did not generate first v2 metadata")
	}
	if err = worker.ApplyTaskMetadata(c, &task, []worker.Comment{{ID: "first", Author: c.Self, CreatedAt: now, Body: out.String()}}); err != nil {
		t.Fatal(err)
	}
	if _, err = worker.ValidateTeamScope(c, task, goal, now); err != nil {
		t.Fatal(err)
	}
}
