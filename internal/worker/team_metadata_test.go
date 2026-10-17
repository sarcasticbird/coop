package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func teamMarker(kind string, value any) string {
	raw, _ := json.Marshal(value)
	return "<!-- coop-" + kind + ":v2 " + string(raw) + " -->"
}

func teamScopeFixture(t *testing.T) (Config, Task, Goal, Comment, time.Time) {
	t.Helper()
	c := configFixture(t)
	p := teamPolicyFixture()
	c.Version, c.Team, c.Role, c.Implementer, c.Reviewer = 2, &p, "", Actor{}, Actor{}
	c.Name, c.Self = p.Members[0].Name, p.Members[0].Actor
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	g := Goal{URL: "https://github.com/planning/board/discussions/1", Readable: true, CreatedAt: now.Add(-time.Hour), Repositories: []string{"code/repo"}}
	g.Body = teamMarker("goal", TeamGoalMetadata{Repositories: g.Repositories, BaseBranches: map[string]string{"code/repo": "feature/integration"}})
	g.BodySHA = BodyHash(g.Body)
	g.Decisions = []Decision{{ID: "1", Author: c.Owner, Command: "/coop approve", CreatedAt: now}}
	task := Task{URL: "https://github.com/code/repo/issues/1", Repo: "code/repo", Title: "Implement scope", Body: "Do the scoped work", Ready: true}
	task.BodySHA = TaskScopeHash(task.Title, task.Body)
	a := assignmentFixture(t, p)
	a.GoalBodySHA, a.TaskBodySHA = g.BodySHA, task.BodySHA
	var planner Actor
	for _, m := range p.Members {
		if m.Name == p.Planner {
			planner = m.Actor
		}
	}
	comment := Comment{ID: "2", Author: planner, CreatedAt: now, Body: teamMarker("task", TeamTaskMetadata{Goal: g.URL, ScopeSHA: task.BodySHA, Assignment: a})}
	return c, task, g, comment, now
}

func TestTeamMetadataFreshScope(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	digest, err := ValidateTeamScope(c, task, goal, now)
	if err != nil {
		t.Fatal(err)
	}
	body := teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: digest})
	if _, err := ParseTeamPRMetadata(body, task.Repo, task.URL, goal.URL, digest); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Task, *Goal){
		func(t *Task, g *Goal) { t.Body += " changed" },
		func(t *Task, g *Goal) { t.Assignment.Author = "outsider" },
		func(t *Task, g *Goal) { t.Assignment.GoalBodySHA = BodyHash("different approved scope") },
		func(t *Task, g *Goal) { t.MetadataAuthor = c.Self; t.MetadataAuthor.ID = 999 },
		func(t *Task, g *Goal) {
			g.Body += " changed acceptance"
			g.BodySHA = BodyHash(g.Body)
			g.LastEditedAt = now.Add(time.Minute)
		},
		func(t *Task, g *Goal) { t.Repo = "code/foreign" },
		func(t *Task, g *Goal) { t.Claim = &Claim{Author: c.Self} },
	} {
		changed, g := task, goal
		a := *task.Assignment
		changed.Assignment = &a
		mutate(&changed, &g)
		if _, err := ValidateTeamScope(c, changed, g, now); err == nil {
			t.Fatal("changed scope admitted")
		}
	}
	// Project ownership/status cannot change the authoritative assignment digest.
	task.CurrentStatusID, task.ItemID = "another-owner", "another-card"
	if got, err := ValidateTeamScope(c, task, goal, now); err != nil || got != digest {
		t.Fatal("board changed authority", err)
	}
	if _, err := ParseTeamPRMetadata(body, "code/foreign", task.URL, goal.URL, digest); err == nil {
		t.Fatal("foreign PR metadata accepted")
	}
	if _, err := ParseTeamPRMetadata(body, task.Repo, task.URL, goal.URL, BodyHash("stale")); err == nil {
		t.Fatal("stale PR assignment accepted")
	}
}

func TestTeamMetadataStrict(t *testing.T) {
	if validTeamGoalURL("https://github.com/issues/repo/pull/1") || validTeamGoalURL("https://github.com/code/discussions/pull/1") {
		t.Fatal("PR accepted as goal")
	}
	valid := `<!-- coop-goal:v2 {"repositories":["code/repo"],"base_branches":{"code/repo":"feature/integration"},"qa":{"required":false}} -->`
	for _, body := range []string{
		strings.Replace(valid, `"qa":`, `"extra":true,"qa":`, 1),
		strings.Replace(valid, `"required":false`, `"required":true,"required":false`, 1),
		strings.Replace(valid, `"required":false`, `"required":true,"Required":false`, 1),
		strings.Replace(valid, `["code/repo"]`, `["code/repo","CODE/REPO"]`, 1),
		strings.Replace(valid, ":v2 ", ":v1 ", 1),
		valid + valid,
		valid + `<!-- coop-goal:v1 {} -->`,
		strings.Replace(valid, `false`, `true`, 1),
	} {
		if _, err := ParseTeamGoalMetadata(body); err == nil {
			t.Fatalf("bad metadata accepted: %s", body)
		}
	}
	if _, err := ParseTeamGoalMetadata(valid); err != nil {
		t.Fatal(err)
	}
}

func TestTeamMetadataFreshAssignmentReads(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	original, err := ValidateTeamScope(c, task, goal, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TeamTaskMetadata){
		func(m *TeamTaskMetadata) { m.Assignment.Author = "outsider" },
		func(m *TeamTaskMetadata) { m.Assignment.PolicySHA = BodyHash("new host policy") },
		func(m *TeamTaskMetadata) { m.ScopeSHA = BodyHash("stale") },
		func(m *TeamTaskMetadata) {
			m.DependsOn = []string{"https://github.com/code/repo/issues/2", "https://github.com/code/repo/issues/2"}
		},
	} {
		var m TeamTaskMetadata
		if err := teamMetadata(comment.Body, "task", &m); err != nil {
			t.Fatal(err)
		}
		change(&m)
		newer := comment
		newer.ID, newer.CreatedAt, newer.Body = "3", now.Add(time.Second), teamMarker("task", m)
		fresh := task
		if err := ApplyTaskMetadata(c, &fresh, []Comment{comment, newer}); err == nil {
			t.Fatal("changed assignment accepted")
		}
		if fresh.Assignment != nil {
			t.Fatal("failed parse retained assignment")
		}
	}
	// An outsider cannot replace planner metadata, even with a later comment.
	outsider := comment
	outsider.Author.ID, outsider.CreatedAt = 999, now.Add(time.Second)
	outsider.Body = "<!-- coop-task:v2 invalid -->"
	if err := ApplyTaskMetadata(c, &task, []Comment{outsider, comment}); err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateTeamScope(c, task, goal, now); err != nil || got != original {
		t.Fatal("outsider changed assignment", err)
	}
}

func TestTeamMetadataBotCannotRelaxQA(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{"code/repo": "feature/integration"}, QA: QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: BodyHash("acceptance"), Environment: "fixture", EnvironmentRevision: "1"}})
	goal.BodySHA = BodyHash(goal.Body)
	if a := EvaluateGoal(c, goal, now); a.State != "approved" {
		t.Fatal(a)
	}
	// A bot edits the body to remove QA but cannot approve the new scope.
	goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{"code/repo": "feature/integration"}})
	goal.BodySHA, goal.LastEditedAt = BodyHash(goal.Body), now.Add(time.Second)
	goal.Decisions = append(goal.Decisions, Decision{ID: "2", Author: comment.Author, Command: "/coop approve", CreatedAt: now.Add(2 * time.Second)})
	if a := EvaluateGoal(c, goal, now.Add(3*time.Second)); a.State == "approved" {
		t.Fatal("bot relaxed policy")
	}
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTeamScope(c, task, goal, now.Add(3*time.Second)); err == nil {
		t.Fatal("unapproved relaxation admitted")
	}
}

func TestTeamConfigVersionDispatch(t *testing.T) {
	c, _, _, _, _ := teamScopeFixture(t)
	if err := ValidateConfig(c); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Team = nil },
		func(c *Config) { c.Role = "planner" },
		func(c *Config) { c.Reviewer = c.Self },
		func(c *Config) { c.Implementer = c.Self },
		func(c *Config) { c.Self.ID = 999 },
		func(c *Config) { c.Name = "outsider" },
		func(c *Config) { c.Version = 1 },
	} {
		bad := c
		mutate(&bad)
		if err := ValidateConfig(bad); err == nil {
			t.Fatal("invalid version authority accepted")
		}
	}
	if ok, _ := Admit(c, Job{}, Snapshot{Complete: true}, time.Now()); ok {
		t.Fatal("incomplete v2 runtime admitted a job")
	}
	e := Engine{Config: c, Store: &memoryStore{}}
	if err := e.initialize(); err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatal("v2 runtime did not stop before loading v1 state", err)
	}
}

func TestTeamMetadataReplacesHistoricalPolicy(t *testing.T) {
	c, task, goal, old, now := teamScopeFixture(t)
	c.Team.Members = append(c.Team.Members, TeamMember{Name: "fourth", Actor: Actor{ID: 14, Login: "fourth[bot]", Kind: "Bot"}, Capabilities: []string{"implement"}})
	c.Team.EngineeringPeers["fourth"] = "engineer"
	var m TeamTaskMetadata
	if err := teamMetadata(old.Body, "task", &m); err != nil {
		t.Fatal(err)
	}
	sha, err := TeamPolicySHA(*c.Team)
	if err != nil {
		t.Fatal(err)
	}
	m.Assignment.PolicySHA = sha
	m.Assignment.Author, m.Assignment.Peer = "fourth", "engineer"
	current := old
	current.ID, current.CreatedAt, current.Body = "3", now.Add(time.Second), teamMarker("task", m)
	if err := ApplyTaskMetadata(c, &task, []Comment{current, old}); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTeamScope(c, task, goal, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if task.Assignment.Author != "fourth" {
		t.Fatal("historical assignment retained")
	}
	current.Body = "<!-- coop-task:v2 invalid -->"
	if err := ApplyTaskMetadata(c, &task, []Comment{old, current}); err == nil || task.Assignment != nil {
		t.Fatal("invalid current metadata fell back")
	}
}

func TestTeamConfigStrictLoad(t *testing.T) {
	c, _, _, _, _ := teamScopeFixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(c.StateDir), "team.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"planner":"planner"`, `"planner":"outsider","Planner":"planner"`, 1),
		strings.Replace(string(raw), `"version":2`, `"version":2,"version":2`, 1),
		strings.Replace(string(raw), `"capabilities":[`, `"unknown":true,"capabilities":[`, 1),
	} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("ambiguous or unknown config field accepted")
		}
	}
}
