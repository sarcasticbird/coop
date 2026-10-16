package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const intakeAllowed = "https://github.com/planning/board/discussions/9"
const intakeExcluded = "https://github.com/planning/board/discussions/6"

// Decode through the operator-facing field so these tests also cover JSON wiring.
func withDiscussionIntake(t *testing.T, c Config, refs ...string) Config {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["discussion_urls"], err = json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDiscussionIntakeConfig(t *testing.T) {
	for name, refs := range map[string][]string{
		"default": nil, "selected": {intakeAllowed}, "case": {"https://github.com/PLANNING/BOARD/discussions/9"},
		"duplicate": {intakeAllowed, "https://github.com/PLANNING/BOARD/discussions/9"},
		"query":     {intakeAllowed + "?x=1"}, "empty query": {intakeAllowed + "?"}, "fragment": {intakeAllowed + "#comment"},
		"empty fragment": {intakeAllowed + "#"}, "wildcard": {"https://github.com/planning/board/discussions/*"},
		"alias": {"https://github.com/orgs/planning/discussions/9"}, "outside": {"https://github.com/other/board/discussions/9"},
		"issue": {"https://github.com/planning/board/issues/9"}, "zero": {"https://github.com/planning/board/discussions/0"},
		"leading zero": {"https://github.com/planning/board/discussions/09"}, "overflow": {"https://github.com/planning/board/discussions/999999999999999999999999"},
	} {
		t.Run(name, func(t *testing.T) {
			c := withDiscussionIntake(t, configFixture(t), refs...)
			valid := name == "default" || name == "selected" || name == "case"
			if err := ValidateConfig(c); (err == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
		})
	}
	c := configFixture(t)
	var refs []string
	for i := 1; i <= 65; i++ {
		refs = append(refs, fmt.Sprintf("https://github.com/planning/board/discussions/%d", i))
	}
	if ValidateConfig(withDiscussionIntake(t, c, refs...)) == nil {
		t.Fatal("accepted more than 64 URLs")
	}
	if err := ValidateConfig(withDiscussionIntake(t, c, refs[:64]...)); err != nil {
		t.Fatal(err)
	}
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: intakeExcluded, PeerHopLimit: 2}
	c = withDiscussionIntake(t, c, intakeAllowed)
	if ValidateConfig(c) == nil {
		t.Fatal("accepted excluded ping destination")
	}
	c.Wake.Enabled = false
	if ValidateConfig(c) == nil {
		t.Fatal("accepted configured excluded ping destination while disabled")
	}
	c.Wake.CoordinationURL = intakeAllowed
	if err := ValidateConfig(c); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionIntakeCachedAdmission(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var c Config
			var s Snapshot
			var now time.Time
			if version == 1 {
				c, s, now = policyFixture(t)
				s.Goals[0].URL = intakeExcluded
				s.Tasks[0].GoalURL = intakeExcluded
			} else {
				c, s, now = engineeringFixture(t, false)
			}
			jobs := Select(c, s, newState(), now)
			if len(jobs) != 1 {
				t.Fatalf("fixture jobs=%+v", jobs)
			}
			excluded := withDiscussionIntake(t, c, intakeAllowed)
			if ok, _ := Admit(excluded, jobs[0], s, now); ok {
				t.Fatal("cached task admitted outside Discussion intake")
			}
			if got := Select(excluded, s, newState(), now); len(got) != 0 {
				t.Fatalf("cached task selected: %+v", got)
			}
			allowed := withDiscussionIntake(t, c, s.Goals[0].URL)
			if ok, reason := Admit(allowed, jobs[0], s, now); !ok {
				t.Fatal(reason)
			}
			if EvaluateGoal(excluded, s.Goals[0], now).State != "approved" {
				t.Fatal("intake changed historical approval")
			}
			s.Goals[0].Decisions = nil
			if ok, _ := Admit(allowed, jobs[0], s, now); ok {
				t.Fatal("intake bypassed owner approval")
			}
		})
	}
	c, s, now := policyFixture(t)
	c.Role, c.Self = "planner", c.Reviewer
	s.Events = []Event{{ID: "old", Revision: BodyHash("old"), URL: intakeExcluded, Kind: "planning", Body: "plan this", Author: c.Owner, OccurredAt: now}}
	jobs := Select(c, s, newState(), now)
	if len(jobs) != 1 {
		t.Fatalf("planning fixture jobs=%+v", jobs)
	}
	c = withDiscussionIntake(t, c, intakeAllowed)
	if ok, _ := Admit(c, jobs[0], s, now); ok {
		t.Fatal("cached planning event admitted")
	}
	s.Events[0].URL = intakeAllowed
	if len(Select(c, s, newState(), now)) != 1 {
		t.Fatal("allowed planning event blocked")
	}
	c, s, now = policyFixture(t)
	s.Goals[0].URL = "https://github.com/code/repo/issues/9"
	s.Tasks[0].GoalURL = s.Goals[0].URL
	c = withDiscussionIntake(t, c, intakeAllowed)
	if len(Select(c, s, newState(), now)) != 1 {
		t.Fatal("Discussion intake changed issue-goal behavior")
	}
}

func TestDiscussionIntakeGoalReferences(t *testing.T) {
	c := withDiscussionIntake(t, configFixture(t), intakeAllowed)
	c.BootstrapGoals = []BootstrapGoal{{URL: intakeExcluded}}
	tasks := map[string]Task{"old": {GoalURL: intakeExcluded}, "selected": {GoalURL: intakeAllowed}, "issue": {GoalURL: "https://github.com/code/repo/issues/1"}}
	got := goalReferences(c, tasks, nil)
	want := []string{"https://github.com/code/repo/issues/1", intakeAllowed}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs=%v want=%v", got, want)
	}
}

func TestDiscussionIntakeCatalog(t *testing.T) {
	for _, mode := range []string{"filtered", "default", "missing", "unselected board"} {
		t.Run(mode, func(t *testing.T) {
			c := configFixture(t)
			c.Wake = &WakeConfig{Enabled: true, CoordinationURL: intakeAllowed, PeerHopLimit: 2}
			if mode != "default" {
				c = withDiscussionIntake(t, c, intakeAllowed)
			}
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			roots := []graphDiscussion{{ID: "six", URL: intakeExcluded, Body: "old planning", CreatedAt: now, Author: graphActor{ID: 1, Kind: "User", Login: "owner"}}, {ID: "nine", URL: intakeAllowed, Body: "current planning", CreatedAt: now, Author: graphActor{ID: 1, Kind: "User", Login: "owner"}}}
			if mode == "missing" {
				roots = roots[:1]
			}
			reads := map[string]int{}
			runner := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
				var req struct {
					Query     string
					Variables map[string]any
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
				page := func(nodes any) any {
					return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
				}
				var data any
				switch {
				case strings.Contains(req.Query, "WorkerDiscussions"):
					reads["listing"]++
					data = map[string]any{"repository": map[string]any{"discussions": page(roots)}}
				case strings.Contains(req.Query, "WorkerDiscussionComments"):
					id, _ := req.Variables["id"].(string)
					reads[id]++
					data = map[string]any{"node": map[string]any{"comments": page([]any{})}}
				case strings.Contains(req.Query, "WorkerGoalVersion"):
					id, _ := req.Variables["id"].(string)
					for _, root := range roots {
						if root.ID == id {
							data = map[string]any{"node": root}
						}
					}
				default:
					t.Fatalf("unexpected query %s", req.Query)
				}
				return jsonResponse(map[string]any{"data": data}), nil
			}}
			g := NewGitHubClient(runner)
			g.config = c
			repo := "planning/board"
			if mode == "unselected board" {
				repo = "planning/other"
			}
			goals, events, sources, err := g.discussions(context.Background(), repo)
			if mode == "missing" {
				if errorCategory(err) != "goal_discussion_not_discovered" {
					t.Fatalf("missing thread err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "default" {
				want = 2
			}
			if mode == "unselected board" {
				want = 0
			}
			if len(goals) != want || len(events) != want || len(sources) != want {
				t.Fatalf("goals=%d events=%d sources=%d want=%d", len(goals), len(events), len(sources), want)
			}
			if mode != "default" && reads["six"] != 0 {
				t.Fatal("read excluded comments")
			}
			if mode == "unselected board" && reads["listing"] != 0 {
				t.Fatal("read board without selected URLs")
			}
		})
	}
}

func TestDiscussionIntakeWakeBaselineAndWidening(t *testing.T) {
	c, store := wakeFixture(t)
	c.Wake.CoordinationURL = intakeAllowed
	now := time.Now().UTC()
	allowed := sourceFixture(c, "old-nine", "@builder existing", c.Owner, now)
	allowed.DiscussionURL = intakeAllowed
	allowed.DiscussionID = "nine"
	excluded := sourceFixture(c, "old-six", "@builder excluded", c.Owner, now)
	excluded.DiscussionURL = intakeExcluded
	excluded.DiscussionID = "six"
	seenExcluded := excluded
	seenExcluded.NodeID = "seen-six"
	if err := IngestDiscussionWakes(c, Snapshot{Complete: true, WakeSources: []ConversationSource{allowed, seenExcluded}}, store, now); err != nil {
		t.Fatal(err)
	}
	before, err := store.Routing()
	if err != nil {
		t.Fatal(err)
	}
	c = withDiscussionIntake(t, c, intakeAllowed)
	if wakeScope(c) != before.ScopeSHA {
		t.Fatal("intake changed baseline scope")
	}
	fresh := allowed
	fresh.NodeID = "new-nine"
	fresh.Body = "@builder new question"
	fresh.CreatedAt = now.Add(time.Second)
	reply := fresh
	reply.NodeID = "reply-nine"
	reply.Body = "Please explain this result"
	reply.ParentID = "builder-comment"
	reply.ParentAuthor = c.Self
	reply.CreatedAt = now.Add(2 * time.Second)
	snapshot := Snapshot{Complete: true, WakeSources: []ConversationSource{allowed, seenExcluded, excluded, fresh, reply}}
	if err := IngestDiscussionWakes(c, snapshot, store, now); err != nil {
		t.Fatal(err)
	}
	requests, err := store.Requests()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%+v want allowed mention and reply", requests)
	}
	routing, err := store.Routing()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routing.Seen["old-six"]; ok {
		t.Fatal("excluded source marked seen")
	}
	if routing.Seen["old-nine"] != before.Seen["old-nine"] || routing.Chains["nine:root"].ID == "" {
		t.Fatal("baseline lost")
	}
	if routing.Seen["seen-six"] != before.Seen["seen-six"] || routing.Chains["six:root"] != before.Chains["six:root"] {
		t.Fatal("narrowing changed excluded history baseline or chain")
	}
	c = withDiscussionIntake(t, c)
	if err := IngestDiscussionWakes(c, snapshot, NewWakeStore(store.Dir), now); err != nil {
		t.Fatal(err)
	}
	requests, err = store.Requests()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("widening replayed seen or suppressed unseen input: %+v", requests)
	}
}

func TestDiscussionIntakePendingWakeHistoryAndRecovery(t *testing.T) {
	for _, phase := range []string{"queued", "blocked", "completed", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			c, store := wakeFixture(t)
			now := time.Now().UTC()
			c.Wake.CoordinationURL = intakeExcluded
			old, err := QueuePing(c, store, PingInput{Prompt: "older excluded question"}, now)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Receipt(old.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt.Phase = phase
			if phase == "blocked" {
				receipt.Reason = "prompt_size"
				receipt.Job.ID = old.ID
				receipt.Job.PromptFailure = &PromptSizeError{ActualBytes: MaxPromptBytes + 1, LimitBytes: MaxPromptBytes, JobID: old.ID, SourceURL: intakeExcluded, Sections: []PromptSectionSize{{Name: "Source", Bytes: MaxPromptBytes + 1}}}
			}
			if err := store.SaveReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			files := []string{filepath.Join(store.Dir, "inbox", old.ID+".json"), filepath.Join(store.Dir, "receipts", old.ID+".json")}
			before := map[string][]byte{}
			for _, path := range files {
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			c.Wake.CoordinationURL = intakeAllowed
			c = withDiscussionIntake(t, c, intakeAllowed)
			// Startup/restart must still validate and retain excluded historical records.
			restarted := NewWakeStore(store.Dir)
			if _, err := restarted.Requests(); err != nil {
				t.Fatal(err)
			}
			w := NewWakeCoordinator(c, restarted, nil, nil, nil)
			got, ok, err := w.Next(now)
			if err != nil || ok {
				t.Fatalf("excluded pending report/request selected: %+v %v %v", got, ok, err)
			}
			allowed, err := QueuePing(c, restarted, PingInput{Prompt: "allowed question"}, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			got, ok, err = w.Next(now.Add(time.Second))
			if err != nil || (phase == "recovery" && ok) || (phase != "recovery" && (!ok || got.ID != allowed.ID)) {
				t.Fatalf("selection=%+v %v %v", got, ok, err)
			}
			c = withDiscussionIntake(t, c)
			w.Config = c
			got, ok, err = w.Next(now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if phase == "queued" && (!ok || got.ID != old.ID) {
				t.Fatal("widening did not release held request")
			}
			if phase == "recovery" && ok {
				t.Fatal("excluded recovery did not retain global stop")
			}
			if _, err := w.Summaries(); err != nil {
				t.Fatal(err)
			}
			for _, path := range files {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before[path], after) {
					t.Fatal("historical bytes changed:", path, err)
				}
			}
		})
	}
}

func TestDiscussionIntakeTeamPlanningEngineeringAndQA(t *testing.T) {
	for _, kind := range []string{"plan", "implement", "review", "fix", "qa", "deliver"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			switch kind {
			case "plan":
				c = qaGateActor(c, c.Team.Planner)
				s.Events = []Event{{ID: "owner", URL: s.Goals[0].URL, Kind: "planning", Revision: BodyHash("plan"), Author: c.Owner, OccurredAt: now}}
				s.PRs = nil
				s.Tasks = nil
			case "implement":
				s.PRs = nil
			case "review":
				c = qaGateActor(c, "planner")
				s.PRs[0].Reviews = nil
			case "fix":
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
				s.PRs[0].Reviews[0].State = "CHANGES_REQUESTED"
			case "qa":
				c = qaGateActor(c, "qa")
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			}
			jobs := Select(c, s, newState(), now)
			if len(jobs) != 1 || jobs[0].Kind != kind {
				t.Fatalf("fixture selected %+v want %s", jobs, kind)
			}
			selected := withDiscussionIntake(t, c, s.Goals[0].URL)
			if ok, reason := Admit(selected, jobs[0], s, now); !ok {
				t.Fatal(reason)
			}
			if got := Select(selected, s, newState(), now); !reflect.DeepEqual(got, jobs) {
				t.Fatal("intake changed job identity or policy binding")
			}
			excluded := withDiscussionIntake(t, c, intakeAllowed)
			if ok, _ := Admit(excluded, jobs[0], s, now); ok {
				t.Fatal("cached team job admitted outside intake")
			}
			if got := Select(excluded, s, newState(), now); len(got) != 0 {
				t.Fatal("excluded team job selected")
			}
			if kind == "qa" || kind == "deliver" {
				s.PRs[0].Reviews = nil
				if ok, _ := Admit(selected, jobs[0], s, now); ok {
					t.Fatal("intake bypassed independent engineering/QA gates")
				}
			}
		})
	}
}

func TestDiscussionIntakeManagedRestartPreservesMigrationHistory(t *testing.T) {
	for _, kind := range []string{"discussion", "ping"} {
		t.Run(kind, func(t *testing.T) {
			c, store, state, _, request, _ := legacyCompletedWakeFixture(t, kind)
			if err := MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			paths := []string{"state.json", "migration-backup.json", "migration.json", "wake/v1/routing.json", "wake/v1/inbox/" + request.ID + ".json", "wake/v1/receipts/" + request.ID + ".json", "wake/v1/inbox/version.json"}
			before := map[string][]byte{}
			for _, rel := range paths {
				raw, err := os.ReadFile(filepath.Join(store.Dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				before[rel] = raw
			}
			c.Wake.CoordinationURL = intakeAllowed
			c = withDiscussionIntake(t, c, intakeAllowed)
			for range 2 {
				e := &Engine{Config: c, Store: NewVersionedFileStore(store.Dir, 2)}
				if err := e.initialize(); err != nil {
					t.Fatal(err)
				}
				if e.state.InstanceID != state.InstanceID {
					t.Fatal("restart changed instance")
				}
				if _, active, err := e.Wakes.Active(); err != nil || active {
					t.Fatal("historical wake became active", err)
				}
				if _, pending, err := e.Wakes.Next(time.Now()); err != nil || pending {
					t.Fatal("historical wake became pending", err)
				}
				if _, err := e.Wakes.Summaries(); err != nil {
					t.Fatal(err)
				}
				for _, rel := range paths {
					raw, err := os.ReadFile(filepath.Join(store.Dir, rel))
					if err != nil || !bytes.Equal(before[rel], raw) {
						t.Fatalf("restart changed %s: %v", rel, err)
					}
				}
			}
		})
	}
}

func TestDiscussionIntakeSnapshotRetainsExcludedTaskWithoutGoalReadError(t *testing.T) {
	c := withDiscussionIntake(t, configFixture(t), intakeAllowed)
	c.Repositories = []string{"code/repo"}
	c.Projects = nil
	issue := restIssue{URL: "https://github.com/code/repo/issues/4", Number: 4, Title: "Saved task", Body: "Scoped work", State: "open"}
	d := graphDiscussion{ID: "nine", URL: intakeAllowed, Body: "Selected planning", CreatedAt: time.Now().UTC(), Author: graphActor{ID: 1, Kind: "User", Login: "owner"}}
	runner := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
		switch {
		case strings.Contains(endpoint, "/issues?"):
			return jsonResponse([]restIssue{issue}), nil
		case endpoint == "repos/code/repo/issues/4":
			return jsonResponse(issue), nil
		case strings.HasPrefix(endpoint, "repos/code/repo/issues/4/comments"):
			body := encodeMetadata("task", taskMetadata{Goal: intakeExcluded, ScopeSHA: TaskScopeHash(issue.Title, issue.Body)})
			return jsonResponse([]restComment{{ID: 1, Body: body, Author: restActor(c.Reviewer)}}), nil
		case strings.Contains(endpoint, "/pulls?"):
			return jsonResponse([]any{}), nil
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		page := func(nodes any) any {
			return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
		}
		var data any
		switch {
		case strings.Contains(req.Query, "WorkerDiscussions"):
			data = map[string]any{"repository": map[string]any{"discussions": page([]graphDiscussion{d})}}
		case strings.Contains(req.Query, "WorkerDiscussionComments"):
			data = map[string]any{"node": map[string]any{"comments": page([]any{})}}
		case strings.Contains(req.Query, "WorkerGoalVersion"):
			data = map[string]any{"node": d}
		default:
			t.Fatalf("unexpected read %s %s", endpoint, req.Query)
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	g := NewGitHubClient(runner)
	g.config = Defaults(c)
	previous := Snapshot{Complete: true, Tasks: []Task{{URL: issue.URL, Repo: "code/repo", GoalURL: intakeExcluded}}}
	snapshot, err := g.readSnapshot(context.Background(), previous)
	if err != nil {
		t.Fatal("excluded task invalidated selected snapshot:", err)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].GoalURL != intakeExcluded || len(snapshot.Goals) != 1 || snapshot.Goals[0].URL != intakeAllowed {
		t.Fatalf("unexpected filtered snapshot: %+v", snapshot)
	}
	snapshot.Complete = true
	if jobs := Select(c, snapshot, newState(), time.Now()); len(jobs) != 0 {
		t.Fatalf("excluded task selected: %+v", jobs)
	}
}

// Keep the runtime and its checkpoints real; replace only GitHub transport.
// A missing catalog goal models postDiscussion's unknown_goal_thread refusal.
type intakeBlockReportGitHub struct {
	*teamEngineGitHub
	reports []Job
}

func (g *intakeBlockReportGitHub) ReportBlock(_ context.Context, j Job, _, _ string) error {
	g.reports = append(g.reports, j)
	if _, ok := goalFor(g.snapshot, j.GoalURL); !ok {
		return ghError("unknown_goal_thread")
	}
	return nil
}

func TestDiscussionIntakeBlockedReportDoesNotStarveTick(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var e *Engine
			var remote *teamEngineGitHub
			var code *fakeOpenCode
			var blocked Job
			var selectedTask Task
			if version == 2 {
				e, remote, code = scheduleEngineFixture(t)
				for _, j := range Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now()) {
					if j.Kind == "plan" {
						blocked = j
					}
				}
				selectedTask = remote.snapshot.Tasks[1]
			} else {
				var base *workflowGitHub
				e, base, code, _, _ = engineFixture(t)
				e.state = newState()
				e.state.SnapshotCache = base.snapshot
				jobs := Select(e.Config, base.snapshot, e.state, e.Clock.Now())
				if len(jobs) != 1 {
					t.Fatalf("fixture jobs=%+v", jobs)
				}
				blocked = jobs[0]
				remote = &teamEngineGitHub{workflowGitHub: base, actor: e.Config.Self}
				selectedTask = base.snapshot.Tasks[0]
				selectedTask.URL = "https://github.com/code/repo/issues/5"
			}
			if blocked.ID == "" || version == 2 && blocked.Binding == nil {
				t.Fatal("missing bound blocked planning fixture")
			}
			blocked.Phase, blocked.WaitReason = "blocked", "execution"
			blocked.Attempts = 2
			e.state.Jobs[blocked.ID] = blocked
			// Persist the old cached catalog before installing the selected profile.
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			oldGoal := remote.snapshot.Goals[0]
			selectedGoal := oldGoal
			selectedGoal.URL = intakeAllowed
			selectedTask.GoalURL = intakeAllowed
			remote.snapshot = Snapshot{Complete: true, Goals: []Goal{selectedGoal}, Tasks: []Task{selectedTask}, Cursor: map[string]string{}}
			github := &intakeBlockReportGitHub{teamEngineGitHub: remote}
			e.GitHub = github
			e.Config = withDiscussionIntake(t, e.Config, intakeAllowed)
			// Give the allowed implementation a real, reconciliable result so it can
			// drain through Tick before the intake setting is widened.
			code.effect = func(c Config, j Job) {
				p := PR{URL: "https://github.com/code/repo/pull/8", TaskURL: j.TaskURL, GoalURL: j.GoalURL, Author: c.Self, Branch: j.Branch, HeadSHA: BodyHash("allowed head")}
				if j.Binding != nil {
					p.AssignmentSHA = j.Binding.AssignmentSHA
					p.BaseBranch = selectedTask.Assignment.BaseBranch
				}
				remote.snapshot.PRs = []PR{p}
			}
			// This is the supported startup path, including a synchronous fresh poll.
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal("excluded pending report starved allowed work:", err)
			}
			if code.calls != 1 || code.last.Kind != "implement" || code.last.GoalURL != intakeAllowed {
				t.Fatalf("allowed implementation did not progress: calls=%d job=%+v", code.calls, code.last)
			}
			if len(github.reports) != 0 {
				t.Fatal("excluded block report called GitHub")
			}
			if !reflect.DeepEqual(e.state.Jobs[blocked.ID], blocked) {
				t.Fatal("held block report changed saved job or counters")
			}
			e.Clock.(*fakeClock).now = e.Clock.Now().Add(e.Config.OpenCode.ReconcileInterval + e.Config.Limits.PollInterval)
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, active := e.active(); active || e.state.Jobs[code.last.ID].Phase != "completed" {
				t.Fatal("allowed implementation did not drain")
			}
			if !reflect.DeepEqual(e.state.Jobs[blocked.ID], blocked) || len(github.reports) != 0 {
				t.Fatal("draining allowed work changed excluded pending report")
			}
			// Preserve the held record and widen normally after the active turn drains.
			remote.snapshot.Goals = append(remote.snapshot.Goals, oldGoal)
			e.Config = withDiscussionIntake(t, e.Config, intakeAllowed, oldGoal.URL)
			e.nextPoll = time.Time{}
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(github.reports) != 1 || github.reports[0].ID != blocked.ID || !e.state.Jobs[blocked.ID].BlockReported {
				t.Fatal("widening did not release the preserved block report")
			}
			reported := e.state.Jobs[blocked.ID]
			reported.BlockReported = false
			if !reflect.DeepEqual(reported, blocked) {
				t.Fatal("reporting changed more than the existing BlockReported flag")
			}
		})
	}
}
