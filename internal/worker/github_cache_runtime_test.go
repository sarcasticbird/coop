package worker

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubCacheKeepsAutomaticRecoveryLive(t *testing.T) {
	cfg, _, api, code, d, transport, x, _, _ := automaticRecoveryFixture(t)
	d.github = NewCachedNativeReader(cfg, api, &fakeCacheClient{err: errors.New("cache must not authorize recovery")}, RealClock{})
	ready, err := d.automaticRecoveries()
	if err != nil || len(ready) != 1 || ready[0].Job.ID != x.Job.ID {
		t.Fatal("cache wrapper disabled live recovery", ready, err)
	}
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	if len(transport.writes) == 0 || code.calls != 0 {
		t.Fatal("recovery failed to use the guarded action client", transport.writes, code.calls)
	}
}

type blockedCache struct {
	*fakeCacheClient
	started chan struct{}
}

type healthyRouteCache struct {
	*fakeCacheClient
	good chan struct{}
}

type objectFailureCache struct {
	*fakeCacheClient
	claimed chan struct{}
}

type claimFailureCache struct{ *fakeCacheClient }

func (claimFailureCache) ClaimGitHubRefresh(context.Context, c.GitHubClaim) (c.GitHubLease, error) {
	return c.GitHubLease{}, c.ErrForbidden
}

func TestGitHubCollectorFailureStagePreservesRetryClassification(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	for _, tc := range []struct {
		name, stage string
		client      GitHubCacheClient
		want        error
	}{
		{"capabilities", "capabilities", &fakeCacheClient{err: c.ErrUnavailable}, c.ErrUnavailable},
		{"claim", "claim", claimFailureCache{&fakeCacheClient{}}, c.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := NewGitHubCollector(cfg, tc.client, nil, RealClock{}).round(t.Context(), "route")
			var staged *cacheRefreshError
			if !errors.As(err, &staged) || staged.stage != tc.stage || !errors.Is(err, tc.want) || !cacheRouteFailure(err) {
				t.Fatalf("stage or retry classification lost: stage=%+v err=%v", staged, err)
			}
		})
	}
}

func (f objectFailureCache) ClaimGitHubRefresh(context.Context, c.GitHubClaim) (c.GitHubLease, error) {
	f.claimed <- struct{}{}
	return c.GitHubLease{}, nil
}

func TestGitHubCollectorObjectFailureDoesNotPauseRoute(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true, CollectRoutes: []string{"route"}}
	clock := &fakeClock{now: time.Now()}
	client := objectFailureCache{fakeCacheClient: &fakeCacheClient{}, claimed: make(chan struct{}, 1)}
	collector := NewGitHubCollector(cfg, client, nil, clock)
	defer collector.Close()
	collector.step, collector.cancel, collector.currentRoute = make(chan error, 1), func() {}, "route"
	collector.step <- ghError("cache_invalid_header")
	collector.Tick(t.Context())
	clock.now = clock.now.Add(time.Second)
	collector.Tick(t.Context())
	select {
	case <-client.claimed:
	case <-time.After(time.Second):
		t.Fatal("one bad object paused its healthy route")
	}
}

func (f healthyRouteCache) ClaimGitHubRefresh(_ context.Context, in c.GitHubClaim) (c.GitHubLease, error) {
	if in.RouteID == "healthy" {
		f.good <- struct{}{}
		return c.GitHubLease{}, nil
	}
	return c.GitHubLease{}, c.ErrUnavailable
}
func TestGitHubCollectorFailureDoesNotHoldHealthyRoute(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true, CollectRoutes: []string{"failed", "healthy"}}
	clock := &fakeClock{now: time.Now().UTC()}
	client := healthyRouteCache{fakeCacheClient: &fakeCacheClient{}, good: make(chan struct{}, 1)}
	collector := NewGitHubCollector(cfg, client, nil, clock)
	defer collector.Close()
	collector.step = make(chan error, 1)
	collector.cancel = func() {}
	collector.step <- c.ErrUnavailable
	collector.currentRoute = "failed"
	collector.route = 1
	collector.Tick(t.Context())
	clock.now = clock.now.Add(time.Second)
	collector.Tick(t.Context())
	select {
	case <-client.good:
	case <-time.After(time.Second):
		t.Fatal("one route outage held healthy collection")
	}
}

func (b blockedCache) GitHubCacheCapabilities(ctx context.Context, _ string) (c.GitHubCacheCapabilities, error) {
	close(b.started)
	<-ctx.Done()
	return c.GitHubCacheCapabilities{}, ctx.Err()
}
func TestGitHubCacheRuntimeDoesNotOwnNativeSlot(t *testing.T) {
	cfg, comms, s, o, b, th, code := commsFixture(t)
	cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true, CollectRoutes: []string{"route"}}
	receipt, e := s.Send(t.Context(), o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "status", RequestID: "cache-busy"})
	if e != nil {
		t.Fatal(e)
	}
	fake := blockedCache{fakeCacheClient: &fakeCacheClient{}, started: make(chan struct{})}
	collector := NewGitHubCollector(cfg, fake, nil, RealClock{})
	defer collector.Close()
	engineering := &busyProducerEngineering{}
	engine := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: comms, Engineering: engineering, Collector: collector}}
	if e = engine.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("background collector not started")
	}
	status, e := s.MessageStatus(t.Context(), o, receipt.MessageID)
	if e != nil || status.Deliveries[0].State != "received" || engineering.ticks != 1 || code.calls != 0 {
		t.Fatal(status, e)
	}
}

type hintFailureMaster struct {
	*dispatchMaster
	*fakeCacheClient
}

func (m *hintFailureMaster) DirtyGitHubObject(context.Context, c.GitHubDirty) error {
	m.dirty++
	return errors.New("master hint unavailable")
}
func TestGitHubCacheDirtyHintAfterSuccess(t *testing.T) {
	cfg, f, base, cat := cachedFixture(t)
	cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true}
	candidate := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Binding: &candidate.Task.Binding, Phase: "active"}
	if e := saveNativeExecution(cfg, x); e != nil {
		t.Fatal(e)
	}
	in := NativeAction{Operation: "publish-pr", RequestID: "stable-hint"}
	raw, _ := json.Marshal(in)
	record := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "completed", Result: NativeActionResult{Phase: "completed", URL: "https://github.com/code/repo/pull/1"}}
	if e := ensurePrivateDir(filepath.Dir(nativeActionPath(cfg, x.Job.ID, in.RequestID)), true); e != nil {
		t.Fatal(e)
	}
	if e := saveNativeAction(cfg, record); e != nil {
		t.Fatal(e)
	}
	m := &hintFailureMaster{base, f}
	g := NewGitHubClient(&fakeCommands{respond: func(string, string, string, []byte) (CommandResult, error) {
		t.Fatal("successful effect replayed")
		return CommandResult{}, nil
	}})
	for range 2 {
		result, e := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
		if e != nil || result.Phase != "completed" {
			t.Fatal(result, e)
		}
	}
	if m.dirty != 2 {
		t.Fatal("hint not attempted", m.dirty)
	}
	saved, e := os.ReadFile(nativeActionPath(cfg, x.Job.ID, in.RequestID))
	if e != nil || json.Unmarshal(saved, &record) != nil || record.State != "completed" {
		t.Fatal("hint changed journal", e)
	}
}
