package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type wakeGitHub struct {
	*workflowGitHub
	replies     map[string]Comment
	sourceError error
	polls       int
	pollError   error
}

func (g *wakeGitHub) Snapshot(ctx context.Context, c Config, s Snapshot) (Snapshot, error) {
	g.polls++
	if g.pollError != nil {
		return Snapshot{}, g.pollError
	}
	return g.workflowGitHub.Snapshot(ctx, c, s)
}
func (g *wakeGitHub) RevalidateWake(ctx context.Context, r WakeRequest) (ConversationSource, Goal, error) {
	if g.sourceError != nil {
		return ConversationSource{}, Goal{}, g.sourceError
	}
	s := r.Source
	if r.Kind == "ping" {
		s.NodeID = "discussion"
		s.DiscussionID = "discussion"
		s.RootID = "discussion"
		s.Body = "Planning"
		s.Revision = ConversationRevision(s)
	}
	goal, _ := goalFor(g.snapshot, r.Source.DiscussionURL)
	return s, goal, nil
}
func (g *wakeGitHub) WakeReply(ctx context.Context, r WakeRequest) (Comment, bool, error) {
	v, ok := g.replies[r.ID]
	return v, ok, nil
}
func wakeEngineFixture(t *testing.T) (*Engine, *fakeOpenCode, *fakeClock, *FileWakeStore) {
	t.Helper()
	e, g, code, clock, _ := engineFixture(t)
	c, s := wakeFixture(t)
	c.Limits = e.Config.Limits
	e.Config = c
	g.snapshot.Tasks = nil
	g.snapshot.PRs = nil
	g.snapshot.Events = nil
	github := &wakeGitHub{workflowGitHub: g, replies: map[string]Comment{}}
	e.GitHub = github
	e.Wakes = NewWakeCoordinator(c, s, github, code, clock)
	return e, code, clock, s
}
func TestWakeLostSubmitResponseIsNotReplayedAfterRestart(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "check queue"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	code.running, code.lost = true, true
	tick(t, e, clock)
	records, _ := store.Requests()
	before, _ := store.Receipt(records[0].ID)
	restarted := &Engine{Config: e.Config, GitHub: e.GitHub, Store: e.Store, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
	restarted.Wakes = NewWakeCoordinator(e.Config, NewWakeStore(store.Dir), e.GitHub, code, clock)
	tick(t, restarted, clock)
	after, _ := store.Receipt(records[0].ID)
	if code.calls != 1 || before.Job.MessageID == "" || before.Job.MessageID != after.Job.MessageID || after.Phase != "running" {
		t.Fatal("uncertain wake intent replayed or lost")
	}
}
func TestWakeWaitsForActiveTaskThenUsesOwnSession(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	_, snapshot, _ := policyFixture(t)
	g.snapshot = snapshot
	code.running = true
	tick(t, e, clock)
	task := code.last
	for _, text := range []string{"one", "two"} {
		if _, err := QueuePing(e.Config, store, PingInput{Prompt: text}, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	tick(t, e, clock)
	if code.calls != 1 || code.last.SessionID != task.SessionID {
		t.Fatal("wake interrupted active task")
	}
	code.running = false
	clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
	g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/1", TaskURL: task.TaskURL, GoalURL: task.GoalURL, Author: e.Config.Self, Branch: task.Branch}}
	tick(t, e, clock)
	tick(t, e, clock)
	if code.calls != 2 || code.last.Kind != "wake" || code.last.SessionID == task.SessionID || code.last.Checkout == task.Checkout {
		t.Fatalf("wake not isolated: %+v", code.last)
	}
}
func TestWakeIdleWithoutReplyDoesNotComplete(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	q, err := QueuePing(e.Config, store, PingInput{Prompt: "question"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	tick(t, e, clock)
	clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
	tick(t, e, clock)
	receipt, _ := store.Receipt(q.ID)
	if receipt.Phase == "completed" {
		t.Fatal("native idle fabricated reply")
	}
	if code.calls != 1 {
		t.Fatal("immediate duplicate prompt")
	}
}
func TestWakeReplyCompletionAndPause(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	q, _ := QueuePing(e.Config, store, PingInput{}, clock.Now())
	e.Store.(*memoryStore).paused = true
	tick(t, e, clock)
	if code.calls != 0 {
		t.Fatal("paused queued wake ran")
	}
	e.Store.(*memoryStore).paused = false
	code.running = true
	tick(t, e, clock)
	e.Store.(*memoryStore).paused = true
	tick(t, e, clock)
	if code.aborts != 1 {
		t.Fatal("pause did not abort owned turn")
	}
	receipt, _ := store.Receipt(q.ID)
	if receipt.Phase == "running" {
		t.Fatal("confirmed abort still owns slot")
	}
	e.Store.(*memoryStore).paused = false
	tick(t, e, clock)
	e.GitHub.(*wakeGitHub).replies[q.ID] = Comment{NodeID: "actual", URL: "https://github.com/orgs/planning/discussions/1#discussioncomment-5"}
	code.running = false
	clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
	tick(t, e, clock)
	receipt, _ = store.Receipt(q.ID)
	if receipt.Phase != "completed" || receipt.ReplyNodeID != "actual" {
		t.Fatal("actual reply not recorded")
	}
}
func TestWakePingCannotBypassGitHubBackoff(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	retry := clock.Now().Add(time.Hour)
	g.pollError = &GitHubError{Category: "rate_limited", RetryAt: retry}
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing API failure")
	}
	polls := g.polls
	if _, err := QueuePing(e.Config, store, PingInput{}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	g.pollError = nil
	clock.now = clock.now.Add(time.Second)
	if err := e.Tick(context.Background()); err != nil && !errors.Is(err, errPollPending) {
		t.Fatal(err)
	}
	if g.polls != polls || code.calls != 0 {
		t.Fatal("ping bypassed API backoff")
	}
}

func TestWakeFairnessAllowanceSurvivesRestart(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	q, _ := QueuePing(e.Config, store, PingInput{}, clock.Now())
	g := e.GitHub.(*wakeGitHub)
	c, s, n := policyFixture(t)
	e.Config.Role = "planner"
	e.Config.Self = c.Reviewer
	e.Wakes.Config = e.Config
	task := s.Tasks[0]
	pr := PR{URL: "https://github.com/code/repo/pull/5", TaskURL: task.URL, GoalURL: task.GoalURL, Author: c.Implementer, HeadSHA: strings.Repeat("a", 40), Branch: "agent/task", ChecksState: "success"}
	g.snapshot = s
	g.snapshot.PRs = []PR{pr}
	g.snapshot.Events = nil
	jobs := Select(e.Config, g.snapshot, State{}, n)
	if len(jobs) == 0 || jobs[0].Kind != "review" {
		t.Fatalf("invalid urgent fixture: %+v", jobs)
	}
	tick(t, e, clock)
	if code.last.Kind != "review" {
		t.Fatal("urgent review did not get allowance")
	}
	routing, _ := store.Routing()
	if routing.UrgentBeforeWake != q.ID {
		t.Fatal("urgent allowance not durable")
	}
	// Simulate a completed review before restart, then expose a different urgent head.
	mem := e.Store.(*memoryStore)
	for id, j := range mem.state.Jobs {
		j.Phase = "completed"
		mem.state.Jobs[id] = j
	}
	g.snapshot.PRs[0].HeadSHA = strings.Repeat("b", 40)
	restarted := &Engine{Config: e.Config, GitHub: g, Store: mem, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
	restarted.Wakes = NewWakeCoordinator(e.Config, NewWakeStore(store.Dir), g, code, clock)
	tick(t, restarted, clock)
	if code.last.Kind != "wake" {
		t.Fatal("second urgent job starved wake after restart")
	}
}
func TestWakeSourceSupersededAndMetadataBlockDoesNotBlockConversation(t *testing.T) {
	for _, reason := range []string{"source_deleted", "source_superseded", ""} {
		e, code, clock, store := wakeEngineFixture(t)
		g := e.GitHub.(*wakeGitHub)
		g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/1", Author: e.Config.Self}}
		q, _ := QueuePing(e.Config, store, PingInput{}, clock.Now())
		if reason != "" {
			g.sourceError = ghError(reason)
		}
		tick(t, e, clock)
		receipt, _ := store.Receipt(q.ID)
		if reason != "" {
			if receipt.Phase != "superseded" || code.calls != 0 {
				t.Fatal("stale input dispatched")
			}
		} else if code.calls != 1 {
			t.Fatal("metadata barrier blocked conversation")
		}
	}
}

func TestWakeRevalidationRateLimitAlsoConstrainsPingRefresh(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	g.sourceError = &GitHubError{Category: "rate_limited", RetryAt: clock.Now().Add(time.Hour)}
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "one"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(context.Background()); errorCategory(err) != "rate_limited" {
		t.Fatalf("rate deadline was swallowed: %v", err)
	}
	polls := g.polls
	g.sourceError = nil
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "two"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute)
	tick(t, e, clock)
	if code.calls != 0 || g.polls != polls {
		t.Fatal("source rate limit bypassed")
	}
}
func TestWakeAbortUnconfirmedRetainsSlot(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	q, _ := QueuePing(e.Config, store, PingInput{}, clock.Now())
	code.running = true
	tick(t, e, clock)
	code.AbortError = errors.New("lost abort")
	e.Store.(*memoryStore).paused = true
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing abort failure")
	}
	receipt, _ := store.Receipt(q.ID)
	if receipt.Phase != "running" || receipt.Reason != "abort_unconfirmed" {
		t.Fatal("uncertain abort released slot")
	}
	e.Store.(*memoryStore).paused = false
	tick(t, e, clock)
	if code.calls != 1 {
		t.Fatal("second turn ran before confirmed abort")
	}
}

type busyWakeCode struct{ *fakeOpenCode }

func (c busyWakeCode) CheckIdle(context.Context, Job) error {
	return &OpenCodeError{Category: "session_busy"}
}
func TestWakeManualBusySessionHoldsWithoutDirectoryMutation(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	q, _ := QueuePing(e.Config, store, PingInput{}, clock.Now())
	records, _ := store.Requests()
	r := records[0]
	r.Source, _, _ = e.GitHub.(*wakeGitHub).RevalidateWake(context.Background(), r)
	key := WakeBindingKey(e.Config, r)
	routing, _ := store.Routing()
	dir := filepath.Join(e.Config.WorkRoot, "must-not-exist", "conversation")
	routing.Bindings[key] = WakeBinding{r.Source.DiscussionID, r.Source.RootID, dir, "ses_manual"}
	if err := store.SaveRouting(routing); err != nil {
		t.Fatal(err)
	}
	busy := busyWakeCode{code}
	e.OpenCode = busy
	e.Wakes.OpenCode = busy
	tick(t, e, clock)
	receipt, _ := store.Receipt(q.ID)
	if code.calls != 0 || code.aborts != 0 || receipt.Reason != "session_busy" {
		t.Fatal("manual takeover not held")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("busy check happened after preparation")
	}
}
func TestWakePollStartedBeforePingNeedsAnotherPoll(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	started := clock.Now()
	clock.now = clock.now.Add(time.Second)
	if _, err := QueuePing(e.Config, store, PingInput{}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.asynchronous = true
	e.poll = make(chan snapshotPoll, 1)
	e.poll <- snapshotPoll{snapshot: e.GitHub.(*wakeGitHub).snapshot, health: ServerHealth{Healthy: true, Version: "1.18.18"}, started: started}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 || e.poll == nil {
		t.Fatal("old poll dispatched a newly arrived ping")
	}
	result := <-e.poll
	e.poll <- result
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("fresh eligible poll did not dispatch")
	}
}

func TestWakeSaturatedDiscussionIntakeStillDrainsQueue(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	if err := e.Wakes.Ingest(Snapshot{Complete: true}); err != nil {
		t.Fatal(err)
	}
	for i := range MaxWakeQueued {
		if _, err := QueuePing(e.Config, store, PingInput{Prompt: fmt.Sprint(i)}, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	g.snapshot.WakeSources = []ConversationSource{sourceFixture(e.Config, "deferred", "@builder question", e.Config.Owner, clock.Now())}
	tick(t, e, clock)
	if code.calls != 1 {
		t.Fatal("full intake prevented queue draining")
	}
	records, _ := store.Requests()
	g.replies[records[0].ID] = Comment{NodeID: "done", URL: records[0].Source.DiscussionURL}
	clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
	tick(t, e, clock)
	tick(t, e, clock)
	records, _ = store.Requests()
	found := false
	for _, r := range records {
		found = found || r.Source.NodeID == "deferred"
	}
	if !found || len(records) != MaxWakeQueued+1 {
		t.Fatal("deferred source was lost or replayed")
	}
}
func TestWakePingRespectsTransportFailureBackoff(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	g.pollError = ghError("transport_failed")
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing transport failure")
	}
	polls := g.polls
	g.pollError = nil
	if _, err := QueuePing(e.Config, store, PingInput{}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(100 * time.Millisecond)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.polls != polls || code.calls != 0 {
		t.Fatal("ping bypassed transport retry delay")
	}
}
func TestWakeGitHubRetryDeadlineSurvivesRestart(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	g.pollError = &GitHubError{Category: "rate_limited", RetryAt: clock.Now().Add(time.Hour)}
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing rate failure")
	}
	polls := g.polls
	g.pollError = nil
	if _, err := QueuePing(e.Config, store, PingInput{}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	restarted := &Engine{Config: e.Config, GitHub: g, Store: e.Store, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
	restarted.Wakes = NewWakeCoordinator(e.Config, NewWakeStore(store.Dir), g, code, clock)
	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.polls != polls || code.calls != 0 {
		t.Fatal("restart bypassed Retry-After")
	}
}
