package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
)

func authorCLIFixture(t *testing.T) (worker.Config, string, *metadataGitHub, time.Time) {
	t.Helper()
	c, path := teamCLIConfig(t, "planner")
	now := time.Now().UTC()
	goal := worker.Goal{URL: "https://github.com/example/board/discussions/1", CreatedAt: now.Add(-time.Hour), Readable: true, Repositories: []string{"example/repo"}}
	body, _ := json.Marshal(worker.TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{"example/repo": "feature/integration"}, QA: worker.QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: worker.BodyHash("acceptance"), Environment: "isolated source", EnvironmentRevision: "v1"}})
	goal.Body = "<!-- coop-goal:v2 " + string(body) + " -->"
	goal.BodySHA = worker.BodyHash(goal.Body)
	goal.Decisions = []worker.Decision{{ID: "approval", Author: c.Owner, Command: "/coop approve", CreatedAt: now}}
	task := worker.Task{URL: "https://github.com/example/repo/issues/1", Repo: "example/repo", Title: "A bounded task", Body: "Approved scope"}
	task.BodySHA = worker.TaskScopeHash(task.Title, task.Body)
	// JSON also exercises compatibility: omitted completeness must never mean empty.
	if err := json.Unmarshal([]byte(`{"AssigneesComplete":true,"Assignees":[],"PRHistoryComplete":true}`), &task); err != nil {
		t.Fatal(err)
	}
	dep := worker.Task{URL: "https://github.com/example/repo/issues/2", Repo: "example/repo", Title: "Prerequisite", Body: "Finish first"}
	dep.BodySHA = worker.TaskScopeHash(dep.Title, dep.Body)
	g := &metadataGitHub{readOnlyGitHub: &readOnlyGitHub{t: t}, snapshot: worker.Snapshot{Complete: true, Tasks: []worker.Task{task, dep}, Goals: []worker.Goal{goal}}}
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) { return g, nil, nil }
	return c, path, g, now
}

func TestTaskAuthorRefusesUnknownPRHistory(t *testing.T) {
	_, path, g, _ := authorCLIFixture(t)
	if err := json.Unmarshal([]byte(`{"PRHistoryComplete":false}`), &g.snapshot.Tasks[0]); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"task-metadata", "--config", path, "--task", g.snapshot.Tasks[0].URL, "--goal", g.snapshot.Goals[0].URL, "--author", "builder"}, &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("generated first assignment without native PR history completeness")
	}
}

func TestTaskAuthorDerivesTrustedAssignmentAndDependency(t *testing.T) {
	c, path, g, now := authorCLIFixture(t)
	before, _ := json.Marshal(g.snapshot)
	var out bytes.Buffer
	if err := run([]string{"task-metadata", "--config", path, "--task", g.snapshot.Tasks[0].URL, "--goal", g.snapshot.Goals[0].URL, "--author", "builder", "--depends-on", g.snapshot.Tasks[1].URL}, &out); err != nil {
		t.Fatal(err)
	}
	task := g.snapshot.Tasks[0]
	if err := worker.ApplyTaskMetadata(c, &task, []worker.Comment{{ID: "published", Author: c.Self, CreatedAt: now, Body: out.String()}}); err != nil {
		t.Fatal(err)
	}
	// The native snapshot attaches QA from the approved goal after reading comments.
	metadata, err := worker.ParseTeamGoalMetadata(g.snapshot.Goals[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	task.QA = metadata.QA
	if _, err := worker.ValidateTeamScope(c, task, g.snapshot.Goals[0], now); err != nil {
		t.Fatal(err)
	}
	a := task.Assignment
	if a.Author != "builder" || a.Peer != "planner" || a.QA != "qa" || a.BaseBranch != "feature/integration" || !reflect.DeepEqual(task.DependsOn, []string{g.snapshot.Tasks[1].URL}) {
		t.Fatalf("wrong assignment: %+v, dependencies=%v", a, task.DependsOn)
	}
	after, _ := json.Marshal(g.snapshot)
	if !bytes.Equal(before, after) || g.reads != 1 {
		t.Fatal("read-only generation changed snapshot or did not read fresh authority")
	}
	if !strings.HasPrefix(out.String(), "<!-- coop-task:v2 ") || strings.Count(out.String(), "coop-task:v2") != 1 {
		t.Fatal("helper did not emit one separate v2 comment")
	}
}

func TestTaskAuthorRefusesConflictsWithoutOutput(t *testing.T) {
	for _, name := range []string{"unknown", "unqualified", "unapproved", "partial", "assignee", "unknown_assignees", "closed", "claimed", "trusted", "assigned", "pr", "missing_dependency", "self_dependency", "duplicate_dependency", "foreign_dependency", "cycle", "existing_cycle"} {
		t.Run(name, func(t *testing.T) {
			c, path, g, _ := authorCLIFixture(t)
			author := "builder"
			deps := []string{g.snapshot.Tasks[1].URL}
			switch name {
			case "unknown":
				author = "outsider"
			case "unqualified":
				author = "qa"
			case "unapproved":
				g.snapshot.Goals[0].Decisions = nil
			case "partial":
				g.snapshot.Complete = false
			case "assignee":
				_ = json.Unmarshal([]byte(`{"AssigneesComplete":true,"Assignees":[{"id":99,"login":"human","kind":"User"}]}`), &g.snapshot.Tasks[0])
			case "unknown_assignees":
				_ = json.Unmarshal([]byte(`{"AssigneesComplete":false}`), &g.snapshot.Tasks[0])
			case "closed":
				g.snapshot.Tasks[0].Closed = true
			case "claimed":
				g.snapshot.Tasks[0].Claim = &worker.Claim{Author: c.Self}
			case "trusted":
				g.snapshot.Tasks[0].TrustedMetadataPresent = true
			case "assigned":
				g.snapshot.Tasks[0].Assignment = &worker.TaskAssignment{Author: "planner"}
			case "pr":
				g.snapshot.PRs = []worker.PR{{TaskURL: g.snapshot.Tasks[0].URL}}
			case "missing_dependency":
				deps = []string{"https://github.com/example/repo/issues/9"}
			case "self_dependency":
				deps = []string{g.snapshot.Tasks[0].URL}
			case "duplicate_dependency":
				deps = append(deps, deps[0])
			case "foreign_dependency":
				deps = []string{"https://github.com/example/foreign/issues/2"}
			case "cycle":
				g.snapshot.Tasks[1].DependsOn = []string{g.snapshot.Tasks[0].URL}
			case "existing_cycle":
				g.snapshot.Tasks[1].DependsOn = []string{g.snapshot.Tasks[1].URL}
			}
			args := []string{"task-metadata", "--config", path, "--task", g.snapshot.Tasks[0].URL, "--goal", g.snapshot.Goals[0].URL, "--author", author}
			for _, dep := range deps {
				args = append(args, "--depends-on", dep)
			}
			before, _ := json.Marshal(g.snapshot)
			var out bytes.Buffer
			if err := run(args, &out); err == nil || out.Len() != 0 {
				t.Fatalf("invalid request produced authority: %v %q", err, out.String())
			}
			after, _ := json.Marshal(g.snapshot)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal mutated source snapshot")
			}
		})
	}
}

func TestTaskAuthorFlagCombinationsRefuseBeforeAPI(t *testing.T) {
	for _, extra := range [][]string{{"--author", "builder", "--assignment", "/private/file"}, {"--depends-on", "https://github.com/example/repo/issues/2"}, {"--author", ""}} {
		_, path, g, _ := authorCLIFixture(t)
		args := append([]string{"task-metadata", "--config", path, "--task", g.snapshot.Tasks[0].URL, "--goal", g.snapshot.Goals[0].URL}, extra...)
		if err := run(args, &bytes.Buffer{}); err == nil || g.reads != 0 {
			t.Fatalf("invalid flags read API: %v reads=%d", err, g.reads)
		}
	}
}

func TestTaskAuthorReciprocalAssignmentAndPreservingRepair(t *testing.T) {
	c, path, g, now := authorCLIFixture(t)
	var out bytes.Buffer
	args := []string{"task-metadata", "--config", path, "--task", g.snapshot.Tasks[0].URL, "--goal", g.snapshot.Goals[0].URL, "--author", "planner"}
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	task := g.snapshot.Tasks[0]
	if err := worker.ApplyTaskMetadata(c, &task, []worker.Comment{{ID: "first", Author: c.Self, CreatedAt: now, Body: out.String()}}); err != nil {
		t.Fatal(err)
	}
	meta, err := worker.ParseTeamGoalMetadata(g.snapshot.Goals[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	task.QA = meta.QA
	if task.Assignment.Author != "planner" || task.Assignment.Peer != "builder" || task.Assignment.QA != "qa" {
		t.Fatalf("planner inherited a global reviewer role: %+v", task.Assignment)
	}
	g.snapshot.Tasks[0] = task
	first := out.String()
	out.Reset()
	if err = run(args, &out); err == nil || out.Len() != 0 {
		t.Fatal("replaced existing assignment")
	}
	if err = run(args[:len(args)-2], &out); err != nil || out.String() != first {
		t.Fatalf("repair changed assignment: %v", err)
	}
}
