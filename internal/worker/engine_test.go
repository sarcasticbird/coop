package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

type memoryStore struct {
	state  State
	paused bool
}

func (s *memoryStore) Load() (State, error) {
	if s.state.Version == 0 {
		s.state = newState()
	}
	return copyState(s.state), nil
}
func (s *memoryStore) Save(state State) error { s.state = copyState(state); return nil }
func (s *memoryStore) Lock() (func(), error)  { return func() {}, nil }
func (s *memoryStore) Paused() (bool, error)  { return s.paused, nil }
func copyState(s State) State {
	raw, _ := json.Marshal(s)
	var copy State
	_ = json.Unmarshal(raw, &copy)
	return copy
}

type workflowGitHub struct {
	snapshot      Snapshot
	claims, syncs int
}

func (g *workflowGitHub) Snapshot(context.Context, Config, Snapshot) (Snapshot, error) {
	return g.snapshot, nil
}
func (g *workflowGitHub) Claim(ctx context.Context, j Job, instance string) (Claim, error) {
	g.claims++
	c := Claim{ID: "claim", Marker: marker(j, "claim"), InstanceID: instance, Author: g.snapshot.PRsAuthor()}
	g.snapshot.Tasks[0].Claim = &c
	return c, nil
}
func (s Snapshot) PRsAuthor() Actor { return Actor{ID: 2, Login: "builder[bot]", Kind: "Bot"} }
func (g *workflowGitHub) Reconcile(ctx context.Context, j Job) (Artifacts, error) {
	a := Artifacts{PlanResponded: true}
	if t, ok := taskFor(g.snapshot, j.TaskURL); ok {
		a.Claim = t.Claim
		a.IssueCompleted = t.Completed
	}
	for _, p := range g.snapshot.PRs {
		if p.TaskURL == j.TaskURL {
			copy := p
			a.PR = &copy
		}
	}
	return a, nil
}
func (g *workflowGitHub) SyncTask(context.Context, Job, string, string) error    { g.syncs++; return nil }
func (g *workflowGitHub) ReportBlock(context.Context, Job, string, string) error { return nil }

type fakeCheckouts struct{ progress string }

type pausingCheckouts struct {
	fakeCheckouts
	store *memoryStore
}

type changingGoalGitHub struct{ *workflowGitHub }

func (g changingGoalGitHub) Revalidate(_ context.Context, _ Job, s Snapshot) (Snapshot, error) {
	s.Goals = append([]Goal(nil), s.Goals...)
	s.Goals[0].Body = "changed after checkout preparation"
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	s.Goals[0].LastEditedAt = s.Goals[0].Decisions[0].CreatedAt.Add(time.Second)
	return s, nil
}
func TestEngineRevalidatesGoalBeforeSubmission(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	e.GitHub = changingGoalGitHub{g}
	tick(t, e, clock)
	if code.calls != 0 {
		t.Fatal("goal changed during preparation allowed a model turn")
	}
}

func (c *pausingCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	c.store.paused = true
	return c.fakeCheckouts.Prepare(ctx, j)
}
func TestEnginePauseDuringPreparationPreventsSubmission(t *testing.T) {
	e, _, code, clock, store := engineFixture(t)
	e.Checkouts = &pausingCheckouts{store: store}
	tick(t, e, clock)
	if code.calls != 0 {
		t.Fatal("pause during checkout preparation allowed a model turn")
	}
}

func (c *fakeCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	if j.Checkout == "" {
		j.Checkout = "/workspace/worker/task"
		j.Branch = "agent/task"
	}
	return j, nil
}
func (c *fakeCheckouts) Progress(context.Context, Job) (string, error) { return c.progress, nil }

type fakeOpenCode struct {
	HealthError, AbortError error
	healthCalls             int
	calls, aborts           int
	running, lost           bool
	last                    Job
	effect                  func(Config, Job)
}

func (c *fakeOpenCode) Health(context.Context) (ServerHealth, error) {
	c.healthCalls++
	if c.HealthError != nil {
		return ServerHealth{}, c.HealthError
	}
	return ServerHealth{Healthy: true, Version: "1.18.18"}, nil
}
func (c *fakeOpenCode) EnsureSession(ctx context.Context, cfg Config, j Job) (Job, error) {
	if j.SessionID == "" {
		j.SessionID = "ses_" + BindingKey(cfg, j)[:16]
	}
	return j, nil
}
func (c *fakeOpenCode) Submit(ctx context.Context, cfg Config, j Job, prompt string) (Turn, error) {
	c.calls++
	c.last = j
	if c.effect != nil {
		c.effect(cfg, j)
	}
	if c.lost {
		return Turn{State: "unknown"}, errors.New("lost response")
	}
	return Turn{Accepted: true, State: "accepted", SessionID: j.SessionID, MessageID: j.MessageID}, nil
}
func (c *fakeOpenCode) Observe(ctx context.Context, j Job) (Turn, error) {
	state := "completed"
	if c.running {
		state = "running"
	}
	return Turn{Accepted: true, State: state, Terminal: !c.running, SessionID: j.SessionID, MessageID: j.MessageID}, nil
}
func (c *fakeOpenCode) Abort(context.Context, Job) error {
	c.aborts++
	if c.AbortError != nil {
		return c.AbortError
	}
	c.running = false
	return nil
}
func engineFixture(t *testing.T) (*Engine, *workflowGitHub, *fakeOpenCode, *fakeClock, *memoryStore) {
	t.Helper()
	cfg, s, n := policyFixture(t)
	cfg = Defaults(cfg)
	cfg.Limits.PollInterval = time.Second
	g := &workflowGitHub{snapshot: s}
	code := &fakeOpenCode{}
	clock := &fakeClock{now: n}
	store := &memoryStore{}
	e := &Engine{Config: cfg, GitHub: g, Store: store, Checkouts: &fakeCheckouts{progress: "initial"}, OpenCode: code, Clock: clock}
	return e, g, code, clock, store
}
func tick(t *testing.T, e *Engine, c *fakeClock) {
	t.Helper()
	c.now = c.now.Add(2 * time.Second)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestEngineUnchangedIdlePollsDoNotInvokeModel(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	g.snapshot.Tasks = nil
	for range 20 {
		tick(t, e, clock)
	}
	if code.calls != 0 {
		t.Fatal("idle polls invoked a model")
	}
}
func TestEngineLostAcceptedResponseSurvivesWorkerRestart(t *testing.T) {
	e, g, code, clock, store := engineFixture(t)
	code.running = true
	code.lost = true
	tick(t, e, clock)
	if code.calls != 1 || store.state.Jobs[code.last.ID].MessageID == "" {
		t.Fatal("submission intent was not persisted")
	}
	restarted := &Engine{Config: e.Config, GitHub: g, Store: store, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
	tick(t, restarted, clock)
	if code.calls != 1 {
		t.Fatal("restart replayed accepted prompt")
	}
}
func TestEnginePauseStopsOnlyOwnedTurnAndGoalPausePersists(t *testing.T) {
	e, g, code, clock, store := engineFixture(t)
	code.running = true
	tick(t, e, clock)
	store.paused = true
	tick(t, e, clock)
	if code.aborts != 1 || code.calls != 1 {
		t.Fatal("pause failed to stop owned turn")
	}
	g.snapshot.Goals[0].Decisions = append(g.snapshot.Goals[0].Decisions, Decision{ID: "pause", Command: "/coop pause", Author: e.Config.Owner, CreatedAt: clock.Now()})
	store.paused = false
	tick(t, e, clock)
	if code.calls != 1 {
		t.Fatal("local resume bypassed owner goal pause")
	}
}
func TestEngineWholeReviewFixDeliveryWorkflow(t *testing.T) {
	robin, g, robinCode, clock, _ := engineFixture(t)
	hawkCfg := robin.Config
	hawkCfg.Role = "planner"
	hawkCfg.Self = hawkCfg.Reviewer
	hawkCode := &fakeOpenCode{}
	hawk := &Engine{Config: hawkCfg, GitHub: g, Store: &memoryStore{}, Checkouts: &fakeCheckouts{}, OpenCode: hawkCode, Clock: clock}
	robinCode.effect = func(c Config, j Job) {
		switch j.Kind {
		case "implement":
			g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: j.TaskURL, GoalURL: j.GoalURL, Author: c.Implementer, HeadSHA: strings.Repeat("a", 40), Branch: j.Branch, ChecksState: "success", MergeState: "CLEAN"}}
		case "fix":
			g.snapshot.PRs[0].HeadSHA = strings.Repeat("b", 40)
		case "deliver":
			g.snapshot.PRs[0].Merged = true
			g.snapshot.PRs[0].Closed = true
			g.snapshot.Tasks[0].Completed = true
		default:
			t.Fatalf("unexpected Robin job %s", j.Kind)
		}
	}
	hawkCode.effect = func(c Config, j Job) {
		if j.Kind != "review" {
			t.Fatalf("unexpected Hawk job %s", j.Kind)
		}
		p := &g.snapshot.PRs[0]
		verdict := "CHANGES_REQUESTED"
		id := "r1"
		if p.HeadSHA == strings.Repeat("b", 40) {
			verdict = "APPROVED"
			id = "r2"
		}
		p.Reviews = append(p.Reviews, Review{ID: id, HeadSHA: p.HeadSHA, Author: c.Reviewer, State: verdict, SubmittedAt: clock.Now()})
	}
	for _, engine := range []*Engine{robin, robin, hawk, hawk, robin, robin, hawk, hawk, robin, robin} {
		tick(t, engine, clock)
	}
	if !g.snapshot.PRs[0].Merged || !g.snapshot.Tasks[0].Completed || robinCode.calls != 3 || hawkCode.calls != 2 || g.claims != 1 {
		t.Fatalf("handoff failed: merged=%v Robin=%d Hawk=%d claims=%d", g.snapshot.PRs[0].Merged, robinCode.calls, hawkCode.calls, g.claims)
	}
}

func TestEngineNoProgressRetriesAreBounded(t *testing.T) {
	e, _, code, clock, store := engineFixture(t)
	for range 12 {
		clock.now = clock.now.Add(16 * time.Minute)
		if err := e.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if code.calls > 3 {
		t.Fatal("unchanged no-progress task exceeded retry ceiling")
	}
	found := false
	for _, j := range store.state.Jobs {
		if j.Phase == "blocked" {
			found = true
		}
	}
	if !found {
		t.Fatal("no-progress task never became visibly blocked")
	}
}

func TestAdmissionPinsSavedTaskScopeDuringResume(t *testing.T) {
	c, s, n := policyFixture(t)
	j := Select(c, s, State{}, n)[0]
	j.ScopeSHA = s.Tasks[0].BodySHA
	s.Tasks[0].Body = "new scope"
	s.Tasks[0].BodySHA = TaskScopeHash(s.Tasks[0].Title, s.Tasks[0].Body)
	s.Tasks[0].MetadataScopeSHA = s.Tasks[0].BodySHA
	if allowed, _ := Admit(c, j, s, n); allowed {
		t.Fatal("old job resumed with new task scope")
	}
}

func TestEngineServerOutageBacksOffAndAbortFailureReservesSlot(t *testing.T) {
	e, _, code, clock, _ := engineFixture(t)
	e.Config.Limits.PollInterval = time.Minute
	code.HealthError = &OpenCodeError{Category: "transport_failed"}
	for range 20 {
		clock.now = clock.now.Add(2 * time.Second)
		_ = e.Tick(context.Background())
	}
	if code.calls != 0 || code.healthCalls > 3 {
		t.Fatal("server outage caused a dispatch or busy retry loop")
	}
	e, _, code, clock, _ = engineFixture(t)
	code.running = true
	tick(t, e, clock)
	clock.now = clock.now.Add(31 * time.Minute)
	code.AbortError = errors.New("abort unavailable")
	_ = e.Tick(context.Background())
	first := code.aborts
	for range 10 {
		clock.now = clock.now.Add(time.Second)
		_ = e.Tick(context.Background())
	}
	if code.calls != 1 || code.aborts != first {
		t.Fatal("uncertain abort released the slot or retried without backoff")
	}
}

type slowGitHub struct {
	workflowGitHub
	started chan struct{}
	once    sync.Once
}

func (g *slowGitHub) Snapshot(ctx context.Context, c Config, s Snapshot) (Snapshot, error) {
	g.once.Do(func() { close(g.started) })
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

type signalAbort struct {
	fakeOpenCode
	aborted chan struct{}
	once    sync.Once
}

func (c *signalAbort) Abort(context.Context, Job) error {
	c.once.Do(func() { close(c.aborted) })
	return nil
}
func TestRunCanPauseActiveTurnDuringSlowGitHubScan(t *testing.T) {
	c, s, _ := policyFixture(t)
	c = Defaults(c)
	store := NewFileStore(c.StateDir)
	state := newState()
	j := Select(c, s, state, time.Now())[0]
	j.Phase = "active"
	j.SessionID = "ses_owned"
	j.MessageID = "msg_owned"
	j.Deadline = time.Now().Add(time.Minute)
	j.ScopeSHA = s.Tasks[0].BodySHA
	s.Tasks[0].Claim = &Claim{Marker: marker(j, "claim"), InstanceID: state.InstanceID, Author: c.Implementer}
	state.SnapshotCache = s
	state.Jobs[j.ID] = j
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	g := &slowGitHub{workflowGitHub: workflowGitHub{snapshot: s}, started: make(chan struct{})}
	code := &signalAbort{fakeOpenCode: fakeOpenCode{running: true}, aborted: make(chan struct{})}
	e := &Engine{Config: c, Store: store, GitHub: g, OpenCode: code, Checkouts: &fakeCheckouts{}, Clock: RealClock{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-g.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not start")
	}
	if err := store.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-code.aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow GitHub scan prevented local pause")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
