package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTeamRuntimeDispatchesThroughNormalTick(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review", "fix", "deliver"} {
			e, g, code, _ := teamEngineFixture(t, reverse, kind)
			c, s, state := activationFixture(t)
			e.Config.StateDir = c.StateDir
			e.Store = s
			g.snapshot.Cursor = map[string]string{}
			state.SnapshotCache = g.snapshot
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for code.calls == 0 && time.Now().Before(deadline) {
				if err := e.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Millisecond)
			}
			if code.calls != 1 || code.last.Kind != kind {
				t.Fatalf("reverse=%v kind=%s calls=%d submitted=%s status=%+v", reverse, kind, code.calls, code.last.Kind, e.status)
			}
		}
	}
}

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

type failingHistoricalReports struct {
	*teamEngineGitHub
	reports int
}

func (g *failingHistoricalReports) ReportBlock(context.Context, Job, string, string) error {
	g.reports++
	return errors.New("old target rejects comments")
}

func TestTeamRuntimeDoesNotReportMigratedHistoricalBlockers(t *testing.T) {
	e, g, code, _ := teamEngineFixture(t, false, "implement")
	old := Job{Kind: "deliver", GoalURL: "https://github.com/orgs/planning/discussions/1", TaskURL: "https://github.com/code/repo/issues/99", Phase: "blocked", WaitReason: "execution"}
	old.ID = JobKey(old)
	c, s, state := activationFixture(t, old)
	e.Config.StateDir = c.StateDir
	e.Store = s
	blocked := &failingHistoricalReports{teamEngineGitHub: g}
	e.GitHub = blocked
	g.snapshot.Cursor = map[string]string{}
	state.SnapshotCache = g.snapshot
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for code.calls == 0 && time.Now().Before(deadline) {
		if err := e.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if blocked.reports != 0 || code.calls != 1 {
		t.Fatalf("historical reports=%d eligible submissions=%d", blocked.reports, code.calls)
	}
	if !reflect.DeepEqual(e.state.Jobs[old.ID], old) {
		t.Fatal("historical evidence was changed")
	}
}

func TestTeamRuntimeStartsMigratedState(t *testing.T) {
	c, s, before := activationFixture(t)
	for range 2 {
		e := &Engine{Config: c, Store: s}
		if err := e.initialize(); err != nil {
			t.Fatal(err)
		}
		if e.state.InstanceID != before.InstanceID || !reflect.DeepEqual(e.state.Jobs, before.Jobs) {
			t.Fatal("startup changed saved identity/history")
		}
		if _, err := e.Wakes.Store.Routing(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFirstTeamWorkerRequiresExplicitInitialization(t *testing.T) {
	c, _, _, _, _ := teamScopeFixture(t)
	c.Name, c.Self = c.Team.Members[2].Name, c.Team.Members[2].Actor
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	s := NewVersionedFileStore(c.StateDir, 2)
	if err := (&Engine{Config: c, Store: s}).initialize(); err == nil {
		t.Fatal("run silently initialized a first worker")
	}
	id, err := InitializeTeamState(c)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		e := &Engine{Config: c, Store: s}
		if err = e.initialize(); err != nil {
			t.Fatal(err)
		}
		if e.state.InstanceID != id {
			t.Fatal("first worker changed identity on restart")
		}
	}
	if _, err = InitializeTeamState(c); err == nil {
		t.Fatal("initialization overwrote an existing worker")
	}
	if _, err = NewFileStore(c.StateDir).Load(); err == nil {
		t.Fatal("old reader accepted initialized team state")
	}
	w := NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
	if _, err = w.Requests(); err == nil {
		t.Fatal("old wake producer/reader accepted initialized storage")
	}
	if _, err = NewVersionedWakeStore(w.Dir, 2).Requests(); err != nil {
		t.Fatal(err)
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

func TestTeamRuntimeRejectsPartialWakeRestore(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c, s, _ := activationFixture(t)
		c.Wake.Enabled = enabled
		path := filepath.Join(c.StateDir, "wake", "v1", "inbox", wakeFenceName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		e := &Engine{Config: c, Store: s}
		if err := e.initialize(); err == nil {
			t.Fatal("partial restore started")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("startup silently repaired a missing fence")
		}
	}
}

func TestTeamRuntimeRejectsMissingMigrationEvidence(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, name := range []string{"migration.json", "migration-backup.json"} {
			c, s, _ := activationFixture(t)
			c.Wake.Enabled = enabled
			path := filepath.Join(c.StateDir, name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := (&Engine{Config: c, Store: s}).initialize(); err == nil {
				t.Fatalf("started without %s, wakes=%v", name, enabled)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("reconstructed missing migration evidence")
			}
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

func TestTeamRecoveryReadsVersionedTaskAndWake(t *testing.T) {
	for _, wake := range []bool{false, true} {
		c, s, state := activationFixture(t)
		j := Job{ID: BodyHash("wake recovery"), Kind: "wake", Phase: "recovery", SessionID: "ses_saved", MessageID: "msg_saved", SubmissionState: "unknown", WaitReason: "reconciliation_required", ReconcileFailures: 3}
		w := NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), 2)
		if wake {
			if _, err := QueuePing(c, w, PingInput{RequestID: j.ID, Prompt: "original prompt"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := w.SaveReceipt(WakeReceipt{Version: 1, RequestID: j.ID, Phase: "recovery", Job: j}); err != nil {
				t.Fatal(err)
			}
		} else {
			e, _, _, job := teamEngineFixture(t, false, "review")
			j = job
			j.Phase = "recovery"
			j.SessionID = "ses_saved"
			j.MessageID = "msg_saved"
			j.SubmissionState = "unknown"
			j.WaitReason = "reconciliation_required"
			j.ReconcileFailures = 3
			c.Self, c.Name, c.Team = e.Config.Self, e.Config.Name, e.Config.Team
			j.Checkout = c.WorkRoot
			state.Jobs[j.ID] = j
			state.Sessions[BindingKey(c, j)] = SessionBinding{SessionID: j.SessionID, Checkout: c.WorkRoot, TaskURL: j.TaskURL, GoalURL: j.GoalURL, Kind: j.Kind, Role: j.Binding.Role, Binding: j.Binding}
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
		}
		if err := RecoveryIntent(c, j.ID, j.SessionID, j.MessageID); err != nil {
			t.Fatal(err)
		}
		var got Job
		if wake {
			r, err := w.Receipt(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			got = r.Job
		} else {
			v, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			got = v.Jobs[j.ID]
		}
		if got.Phase != "active" || got.SessionID != j.SessionID || got.MessageID != j.MessageID || got.SubmissionState != j.SubmissionState || got.Attempts != j.Attempts {
			t.Fatal("recovery replaced original intent")
		}
	}
}
