package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTeamDiscussionExtractionRecognizesEngineers(t *testing.T) {
	c, _, goal, _, now := teamScopeFixture(t)
	engineer := c.Team.Members[1].Actor
	d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: now, Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}}
	for _, actor := range []Actor{engineer, c.Team.Members[2].Actor, {ID: 99, Login: "outsider", Kind: "Bot"}} {
		g := &GitHubClient{config: c, goalIDs: map[string]string{}}
		_, events := g.goalFromDiscussion(d, []Comment{{ID: "engineer-note", Body: "Please reconcile the completed work", Author: actor, CreatedAt: now}})
		found := false
		for _, e := range events {
			if e.ID == "engineer-note" {
				found = true
			}
		}
		if found != (actor.ID == engineer.ID) {
			t.Fatalf("actor %d planning=%v", actor.ID, found)
		}
	}
}

func TestTeamInitialTaskMetadataExplicitAssignment(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	var metadata TeamTaskMetadata
	if err := teamMetadata(comment.Body, "task", &metadata); err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Complete: true, Tasks: []Task{task}, Goals: []Goal{goal}}
	raw, _ := json.Marshal(metadata.Assignment)
	file := filepath.Join(t.TempDir(), "assignment.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	text, err := TaskMetadataWithAssignment(c, s, task.URL, goal.URL, file, now)
	if err != nil {
		t.Fatal(err)
	}
	trusted := Comment{ID: "initial", Author: c.Self, CreatedAt: now, Body: text}
	if err = ApplyTaskMetadata(c, &task, []Comment{trusted}); err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateTeamScope(c, task, goal, now); err != nil {
		t.Fatal(err)
	}
	s.Tasks[0] = task
	if _, err = TaskMetadataWithAssignment(c, s, task.URL, goal.URL, file, now); err == nil {
		t.Fatal("explicit input reassigned existing work")
	}
}

func TestTeamInitialAssignmentRejectsStaleOrUnauthorizedInput(t *testing.T) {
	for _, problem := range []string{"self-peer", "unknown-author", "stale-task", "stale-goal", "stale-policy", "nonplanner", "paused-goal", "incomplete", "unknown-field", "duplicate-field", "claimed"} {
		c, task, goal, comment, now := teamScopeFixture(t)
		var m TeamTaskMetadata
		if err := teamMetadata(comment.Body, "task", &m); err != nil {
			t.Fatal(err)
		}
		s := Snapshot{Complete: true, Tasks: []Task{task}, Goals: []Goal{goal}}
		switch problem {
		case "self-peer":
			m.Assignment.Peer = m.Assignment.Author
		case "unknown-author":
			m.Assignment.Author = "outsider"
		case "stale-task":
			m.Assignment.TaskBodySHA = BodyHash("stale")
		case "stale-goal":
			m.Assignment.GoalBodySHA = BodyHash("stale")
		case "stale-policy":
			m.Assignment.PolicySHA = BodyHash("stale")
		case "nonplanner":
			c.Name, c.Self = c.Team.Members[1].Name, c.Team.Members[1].Actor
		case "paused-goal":
			s.Goals[0].Decisions = append(s.Goals[0].Decisions, Decision{ID: "pause", Author: c.Owner, Command: "/coop pause", CreatedAt: now.Add(time.Second)})
			now = now.Add(2 * time.Second)
		case "incomplete":
			s.Complete = false
		case "claimed":
			s.Tasks[0].Claim = &Claim{ID: "existing"}
		}
		raw, _ := json.Marshal(m.Assignment)
		if problem == "unknown-field" {
			raw = []byte(strings.Replace(string(raw), "{", `{"extra":true,`, 1))
		}
		if problem == "duplicate-field" {
			raw = []byte(strings.Replace(string(raw), "{", `{"version":2,`, 1))
		}
		file := filepath.Join(t.TempDir(), "assignment.json")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := TaskMetadataWithAssignment(c, s, task.URL, goal.URL, file, now); err == nil {
			t.Fatalf("accepted %s", problem)
		}
	}
}

func TestFirstTeamWorkerRejectsExistingAndPartialStorage(t *testing.T) {
	for _, name := range []string{"state.json", "migration.json", "migration-backup.json", "wake", "notes"} {
		c, _, _, _, _ := teamScopeFixture(t)
		if err := os.MkdirAll(c.StateDir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(c.StateDir, name)
		if err := os.WriteFile(path, []byte("preserved evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := InitializeTeamState(c); err == nil {
			t.Fatalf("overwrote existing %s", name)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "preserved evidence" {
			t.Fatal("changed existing evidence")
		}
	}
}

func TestInitialAssignmentCannotReplaceRejectedTrustedMetadata(t *testing.T) {
	for _, problem := range []string{"edited-task", "malformed", "legacy"} {
		c, task, goal, comment, now := teamScopeFixture(t)
		var old TeamTaskMetadata
		if err := teamMetadata(comment.Body, "task", &old); err != nil {
			t.Fatal(err)
		}
		switch problem {
		case "edited-task":
			task.Body += " edited"
		case "malformed":
			comment.Body = "<!-- coop-task:v2 invalid -->"
		case "legacy":
			comment.Body = encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA})
		}
		f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
			if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
				return jsonResponse([]restComment{{ID: 2, Body: comment.Body, Author: restActor(comment.Author), CreatedAt: now, UpdatedAt: now}}), nil
			}
			if endpoint == "repos/code/repo/issues/1" {
				return jsonResponse(map[string]any{"html_url": task.URL, "number": 1, "title": task.Title, "body": task.Body, "state": "open", "updated_at": now}), nil
			}
			t.Fatalf("unexpected endpoint %s", endpoint)
			return CommandResult{}, nil
		}}
		g := NewGitHubClient(f)
		g.config = c
		fresh, err := g.taskByURL(context.Background(), task.URL)
		if err != nil {
			t.Fatal(err)
		}
		old.Assignment.TaskBodySHA = fresh.BodySHA
		raw, _ := json.Marshal(old.Assignment)
		file := filepath.Join(t.TempDir(), "assignment.json")
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = TaskMetadataWithAssignment(c, Snapshot{Complete: true, Tasks: []Task{fresh}, Goals: []Goal{goal}}, fresh.URL, goal.URL, file, now); err == nil {
			t.Fatalf("replaced %s trusted metadata", problem)
		}
	}
}

// Historical state fixtures still exercise codecs, identity fences and retained
// receipts. They do not start the retired runtime to manufacture old work.
func activationFixture(t *testing.T, history ...Job) (Config, *FileStore, State) {
	t.Helper()
	c, _, _, _, _ := teamScopeFixture(t)
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	s := NewFileStore(c.StateDir)
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range history {
		state.Jobs[j.ID] = j
	}
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	if err = MigrateState(c, 1, 2, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	state, err = NewVersionedFileStore(c.StateDir, 2).Load()
	if err != nil {
		t.Fatal(err)
	}
	return c, NewVersionedFileStore(c.StateDir, 2), state
}
