package worker

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func scheduleEngineFixture(t *testing.T) (*Engine, *teamEngineGitHub, *fakeOpenCode) {
	t.Helper()
	e, g, code, _ := teamEngineFixture(t, false, "review")
	s := &g.snapshot
	s.Events = []Event{{ID: "owner-plan", URL: s.Goals[0].URL, Kind: "planning", Revision: "owner-revision", Author: e.Config.Owner}}
	task := s.Tasks[0]
	task.URL = "https://github.com/code/repo/issues/3"
	a := *task.Assignment
	a.Author, a.Peer = "planner", "engineer"
	task.Assignment, task.Claim, task.Ready = &a, nil, true
	s.Tasks = append(s.Tasks, task)
	e.state.SnapshotCache = *s
	if len(Select(e.Config, *s, e.state, e.Clock.Now())) != 3 {
		t.Fatal("fixture must admit review, plan and implementation")
	}
	return e, g, code
}

func TestTeamScheduleEngineSharesNewAndWaitingCandidates(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		for _, tc := range []struct {
			counters ScheduleCounters
			kind     string
		}{
			{ScheduleCounters{}, "review"},
			{ScheduleCounters{2, 2}, "plan"},
			{ScheduleCounters{2, 3}, "implement"},
		} {
			e, g, code := scheduleEngineFixture(t)
			e.teamScheduleState().Counters = tc.counters
			if waiting {
				for _, j := range Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now()) {
					if j.Kind == "implement" {
						j.Phase = "waiting"
						e.state.Jobs[j.ID] = j
						g.snapshot.Tasks[1].Claim = &Claim{ID: "owned", Marker: marker(j, "claim"), InstanceID: e.state.InstanceID, Author: e.Config.Self, Binding: j.Binding}
						g.snapshot.Tasks[1].Ready = false // Claimed work has moved to In Progress.
					}
				}
			}
			if err := e.scheduleTeam(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if code.calls != 1 || code.last.Kind != tc.kind {
				t.Fatalf("waiting=%t counters=%+v dispatched %+v", waiting, tc.counters, code.last)
			}
			if e.state.Schedule.Counters != tc.counters {
				t.Fatal("dispatch consumed completion counters")
			}
			if len(e.state.Schedule.Eligible) != 3 {
				t.Fatal("losing candidates did not retain age")
			}
		}
	}
}

func TestTeamScheduleEngineAgeSurvivesRestart(t *testing.T) {
	e, g, code := scheduleEngineFixture(t)
	e.Store = NewVersionedFileStore(filepath.Join(t.TempDir(), "ledger"), 2)
	// Reject the selected review after the durable age checkpoint, without a turn.
	g.change = func(_ int, s *Snapshot) { s.PRs[0].ContributorsComplete = false }
	if err := e.scheduleTeam(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 {
		t.Fatal("stale selection dispatched")
	}
	before := copyState(e.state).Schedule.Eligible
	durable, err := e.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	e.state = durable
	e.Clock.(*fakeClock).now = e.Clock.Now().Add(time.Hour)
	g.change = nil
	g.snapshot.PRs[0].ContributorsComplete = true
	e.state.SnapshotCache = g.snapshot
	if err := e.scheduleTeam(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.state.Schedule.Eligible) {
		t.Fatal("restart reset admitted age")
	}
	if code.calls != 1 || code.last.Kind != "review" {
		t.Fatal("fresh positive control did not dispatch")
	}
}

func TestTeamScheduleEngineUnclaimedPreparationRecovery(t *testing.T) {
	for _, phase := range []string{"preparing", "waiting"} {
		for _, uncertain := range []bool{false, true} {
			e, g, code, j := teamEngineFixture(t, false, "implement")
			j.Phase = phase
			if uncertain {
				j.MessageID, j.SubmissionState, j.SubmittedAt = "msg_unknown", "unknown", e.Clock.Now()
			}
			e.state.Jobs[j.ID] = j
			e.teamScheduleState().Counters = ScheduleCounters{2, 3}
			e.Store = NewVersionedFileStore(filepath.Join(t.TempDir(), "ledger"), 2)
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			// Reload the actual pre-claim checkpoint instead of constructing a new job.
			s, err := e.Store.Load()
			if err != nil {
				t.Fatal(err)
			}
			e.state = s
			if err := e.scheduleTeam(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			want := 1
			if uncertain {
				want = 0
			}
			if code.calls != want || g.claims != want {
				t.Fatalf("%s uncertain=%t: submits=%d claims=%d", phase, uncertain, code.calls, g.claims)
			}
			if e.state.Schedule.Counters != (ScheduleCounters{2, 3}) {
				t.Fatal("pre-claim recovery consumed service")
			}
		}
	}
}

func TestTeamScheduleEngineRevalidatesSelectedJob(t *testing.T) {
	for _, kind := range []string{"review", "plan", "implement"} {
		for _, at := range []int{1, 2, 3} {
			if kind == "plan" && at == 3 {
				continue
			}
			e, g, code := scheduleEngineFixture(t)
			switch kind {
			case "plan":
				e.teamScheduleState().Counters = ScheduleCounters{2, 2}
			case "implement":
				e.teamScheduleState().Counters = ScheduleCounters{2, 3}
			}
			g.change = func(read int, s *Snapshot) {
				if read == at {
					if kind == "plan" {
						s.Events[0].Revision = "changed"
					} else {
						index := 0
						if kind == "implement" {
							index = 1
						}
						a := *s.Tasks[index].Assignment
						a.PolicySHA = BodyHash("changed policy")
						s.Tasks[index].Assignment = &a
					}
				}
			}
			before := e.teamScheduleState().Counters
			if err := e.scheduleTeam(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if code.calls != 0 || e.state.Schedule.Counters != before {
				t.Fatalf("%s read %d executed or consumed stale assignment", kind, at)
			}
		}
	}
}

func TestTeamScheduleEngineIgnoresInadmissibleAndDelayedJobs(t *testing.T) {
	for _, reason := range []string{"blocked", "backoff", "assignment", "not-ready"} {
		e, g, code := scheduleEngineFixture(t)
		e.teamScheduleState().Counters = ScheduleCounters{2, 3}
		for _, j := range Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now()) {
			if j.Kind != "implement" {
				continue
			}
			switch reason {
			case "blocked":
				j.Phase = "blocked"
				e.state.Jobs[j.ID] = j
			case "backoff":
				j.Phase = "waiting"
				j.NextAttemptAt = e.Clock.Now().Add(time.Hour)
				e.state.Jobs[j.ID] = j
			case "assignment":
				a := *g.snapshot.Tasks[1].Assignment
				a.PolicySHA = BodyHash("changed policy")
				g.snapshot.Tasks[1].Assignment = &a
			case "not-ready":
				g.snapshot.Tasks[1].Ready = false
			}
		}
		if err := e.scheduleTeam(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if code.calls != 1 || code.last.Kind != "plan" {
			t.Fatalf("%s prevented actionable planning", reason)
		}
		if e.state.Schedule.Counters != (ScheduleCounters{2, 3}) {
			t.Fatal("inadmissible candidate consumed service")
		}
	}
}

type teamScheduleWakeGitHub struct {
	*teamEngineGitHub
	conversation *wakeGitHub
}

func (g *teamScheduleWakeGitHub) RevalidateWake(ctx context.Context, r WakeRequest) (ConversationSource, Goal, error) {
	return g.conversation.RevalidateWake(ctx, r)
}
func (g *teamScheduleWakeGitHub) WakeReply(ctx context.Context, r WakeRequest) (Comment, bool, error) {
	return g.conversation.WakeReply(ctx, r)
}

func TestTeamScheduleEngineWakeSharesOwnedTurn(t *testing.T) {
	e, g, code := scheduleEngineFixture(t)
	e.Config.Wake = &WakeConfig{Enabled: true, CoordinationURL: g.snapshot.Goals[0].URL, PeerHopLimit: 2}
	ws := NewWakeStore(filepath.Join(t.TempDir(), "wakes"))
	backend := &teamScheduleWakeGitHub{g, &wakeGitHub{workflowGitHub: g.workflowGitHub, replies: map[string]Comment{}}}
	e.GitHub = backend
	e.Wakes = NewWakeCoordinator(e.Config, ws, backend, code, e.Clock)
	e.Wakes.Paused = e.paused
	q, err := QueuePing(e.Config, ws, PingInput{Prompt: "owner request"}, e.Clock.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	e.lastSnapshotStarted = e.Clock.Now()
	e.teamScheduleState().Counters = ScheduleCounters{2, 2}
	code.running = true
	if err := e.scheduleTeam(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 || code.last.Kind != "wake" {
		t.Fatal("oldest owner wake was not served")
	}
	e.initialized = true // Exercise the turn invariant without enabling v2 initialization.
	e.nextPoll = e.Clock.Now().Add(time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 || code.aborts != 0 {
		t.Fatal("fairness interrupted an active wake")
	}
	backend.conversation.replies[q.ID] = Comment{NodeID: "reply", URL: q.ID}
	code.running = false
	e.Clock.(*fakeClock).now = e.Clock.Now().Add(e.Config.OpenCode.ReconcileInterval)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Accounting precedes selection, so the third foreground completion yields coding.
	if err := e.scheduleTeam(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if code.calls != 2 || code.last.Kind != "implement" {
		t.Fatal("wake completion did not yield to coding")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code.calls != 2 || code.aborts != 0 {
		t.Fatal("queued wake interrupted an active task")
	}
}

func TestTeamScheduleWakeCannotSubstituteUnselectedIntake(t *testing.T) {
	e, _, clock, store := wakeEngineFixture(t)
	first, err := QueuePing(e.Config, store, PingInput{RequestID: BodyHash("first")}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := QueuePing(e.Config, store, PingInput{RequestID: BodyHash("second")}, clock.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute)
	// A stale selection must not let Tick silently run the current queue winner.
	if err := e.Wakes.tick(context.Background(), Snapshot{Complete: true}, second.ID); err != nil {
		t.Fatal(err)
	}
	if e.OpenCode.(*fakeOpenCode).calls != 0 {
		t.Fatal("wake selection changed during dispatch")
	}
	if err := e.Wakes.tick(context.Background(), Snapshot{Complete: true}, first.ID); err != nil {
		t.Fatal(err)
	}
	if e.OpenCode.(*fakeOpenCode).calls != 1 {
		t.Fatal("selected wake did not dispatch")
	}
}

func TestTeamScheduleRejectsInvalidDurableMetadata(t *testing.T) {
	for _, corrupt := range []func(*State){
		func(s *State) { s.Version = 1 },
		func(s *State) { s.Schedule.Counters.Handoffs = -1 },
		func(s *State) { s.Schedule.Counters.Handoffs = 3 },
		func(s *State) { s.Schedule.Counters.Foreground = -1 },
		func(s *State) { s.Schedule.Counters.Foreground = 4 },
		func(s *State) { s.Schedule.Eligible["invalid"] = time.Now() },
		func(s *State) { s.Schedule.Eligible[BodyHash("job")] = time.Time{} },
		func(s *State) { s.Schedule.CompletedWakes["invalid"] = true },
		func(s *State) { s.Schedule.CompletedWakes[BodyHash("wake")] = false },
	} {
		s := newState()
		s.Version = 2
		s.Schedule = &TeamScheduleState{Eligible: map[string]time.Time{}, CompletedWakes: map[string]bool{}}
		if err := validateState(s); err != nil {
			t.Fatal(err)
		}
		corrupt(&s)
		if validateState(s) == nil {
			t.Fatal("corrupt schedule admitted")
		}
	}
}
