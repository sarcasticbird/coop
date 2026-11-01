package worker

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

func TestNativeCachedSupersededPRDoesNotHoldReboundTask(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	parent := f.page.Objects[0].Object
	parent.Kind, parent.URL, parent.AssignmentSHA = "pull_request", "https://github.com/code/repo/pull/1", strings.Repeat("f", 64)
	f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: parent, Dirty: true})
	cat, err := NewCachedNativeReader(cfg, live, f, RealClock{}).NativeSnapshot(t.Context(), cfg, m)
	if err != nil || len(NativeCandidates(cfg, cat, nil, time.Now())) != 1 {
		t.Fatal("superseded PR registration held current assignment", cat.Cache, err)
	}
	if f.dataReads != 1 {
		t.Fatal("superseded PR payload read", f.dataReads)
	}
}

type fakeCacheClient struct {
	dataReads int
	page      c.GitHubCatalogPage
	data      map[string][]byte
	dirty     int
	err       error
	claims    int
}

func (f *fakeCacheClient) GitHubCacheCapabilities(context.Context, string) (c.GitHubCacheCapabilities, error) {
	return c.GitHubCacheCapabilities{Version: 1, Mode: "on"}, f.err
}
func (f *fakeCacheClient) GitHubCatalog(context.Context, c.GitHubCatalogQuery) (c.GitHubCatalogPage, error) {
	return f.page, f.err
}
func (f *fakeCacheClient) GitHubDataset(_ context.Context, q c.GitHubDatasetQuery) (c.GitHubDatasetChunk, error) {
	f.dataReads++
	raw := f.data[q.Ref.SHA256]
	end := min(len(raw), q.Offset+c.GitHubChunkBytes)
	next := 0
	if end < len(raw) {
		next = end
	}
	return c.GitHubDatasetChunk{Ref: q.Ref, Data: raw[q.Offset:end], Offset: q.Offset, Next: next}, f.err
}
func (f *fakeCacheClient) ClaimGitHubRefresh(context.Context, c.GitHubClaim) (c.GitHubLease, error) {
	f.claims++
	return c.GitHubLease{}, f.err
}
func (f *fakeCacheClient) UploadGitHubDataset(context.Context, c.GitHubUpload) error { return f.err }
func (f *fakeCacheClient) CommitGitHubRefresh(context.Context, c.GitHubCommit) error { return f.err }
func (f *fakeCacheClient) FailGitHubRefresh(context.Context, c.GitHubFailure) error  { return f.err }
func (f *fakeCacheClient) DirtyGitHubObject(context.Context, c.GitHubDirty) error {
	f.dirty++
	return f.err
}
func cachedFixture(t *testing.T) (Config, *fakeCacheClient, *dispatchMaster, *dispatchCatalog) {
	t.Helper()
	cfg, m, live, _ := dispatchFixture(t)
	f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("manifest"), Goals: live.catalog.Goals}}
	for _, task := range live.catalog.Tasks {
		addCachedTask(t, f, task)
	}
	return cfg, f, m, live
}
func addCachedTask(t *testing.T, f *fakeCacheClient, task NativeTask) {
	t.Helper()
	raw, e := json.Marshal(task)
	if e != nil {
		t.Fatal(e)
	}
	ref := c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: task.Task.URL, Kind: "task", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
	hash := c.GitHubDigest(raw)
	f.data[hash] = raw
	f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Minute), Header: c.GitHubHeader{Revision: hash, ScopeRevision: task.Task.BodySHA, State: "open"}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "task", SHA256: hash, HeaderRevision: hash, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Hour)}}})
}

type countedCatalog struct {
	*dispatchCatalog
	discovery, actions int
}

func (g *countedCatalog) NativeSnapshot(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	g.discovery++
	return g.dispatchCatalog.NativeSnapshot(ctx, cfg, m)
}
func (g *countedCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, j Job, x ...string) (NativeCatalog, error) {
	g.actions++
	return g.dispatchCatalog.NativeTaskSnapshot(ctx, cfg, m, j, x...)
}
func TestNativeCachedDiscoveryZeroProviderReads(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	count := &countedCatalog{dispatchCatalog: live}
	reader := NewCachedNativeReader(cfg, count, f, RealClock{})
	expected := NativeCandidates(cfg, live.catalog, nil, time.Now())
	for range 10 {
		catalog, e := reader.NativeSnapshot(context.Background(), cfg, m)
		if e != nil {
			t.Fatal(e)
		}
		actual := NativeCandidates(cfg, catalog, nil, time.Now())
		if len(actual) != len(expected) || actual[0].Job.ID != expected[0].Job.ID {
			t.Fatal(actual, expected)
		}
	}
	if count.discovery != 0 || count.actions != 0 {
		t.Fatal("discovery reached GitHub", count)
	}
	if f.dataReads != 1 {
		t.Fatal("unchanged payload downloaded each poll", f.dataReads)
	}
}
func TestNativeCachedClosureCoverage(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	reader := NewCachedNativeReader(cfg, live, f, RealClock{})
	f.page.Objects[0].Dirty = true
	catalog, e := reader.NativeSnapshot(context.Background(), cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	if len(NativeCandidates(cfg, catalog, nil, time.Now())) != 0 {
		t.Fatal("unknown task became new work")
	}
	// Planner must not mistake a registered but missing task for an empty goal.
	cfg.Name = cfg.Team.Planner
	cfg.Self = teamPlanner(cfg)
	cfg.Master.AgentID = "peer"
	if len(NativeCandidates(cfg, catalog, nil, time.Now())) != 0 {
		t.Fatal("partial cache triggered duplicate plan")
	}
	cfg, f, m, live = cachedFixture(t)
	second := live.catalog.Tasks[0]
	second.Binding.TaskURL = "https://github.com/code/repo/issues/2"
	second.Task.URL = second.Binding.TaskURL
	second.Binding.DependsOn = []string{"https://github.com/code/repo/issues/99"}
	addCachedTask(t, f, second)
	catalog, e = NewCachedNativeReader(cfg, live, f, RealClock{}).NativeSnapshot(context.Background(), cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	jobs := NativeCandidates(cfg, catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.TaskURL != live.catalog.Tasks[0].Task.URL {
		t.Fatal("missing dependency blocked unrelated work", jobs)
	}
}
func TestNativeCachedActionsStayLive(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	count := &countedCatalog{dispatchCatalog: live}
	reader := NewCachedNativeReader(cfg, count, f, RealClock{})
	live.err = errors.New("provider unavailable")
	if _, e := reader.NativeTaskSnapshot(context.Background(), cfg, m, Job{}); e == nil {
		t.Fatal("action accepted cache instead of fresh read")
	}
	if count.actions != 1 {
		t.Fatal(count.actions)
	}
	if _, e := nativeValidationSnapshot(t.Context(), cfg, m, reader, Job{Kind: "plan"}); e == nil || count.discovery != 1 {
		t.Fatal("plan validation used cache", e, count.discovery)
	}
}

func TestNativeCachedFreshnessDuringPoll(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	clock := &fakeClock{now: time.Now().UTC()}
	reader := NewCachedNativeReader(cfg, live, f, clock)
	catalog, err := reader.NativeSnapshot(t.Context(), cfg, m)
	if err != nil || len(NativeCandidates(cfg, catalog, nil, clock.Now())) != 1 {
		t.Fatal(catalog, err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if len(NativeCandidates(cfg, catalog, nil, clock.Now())) != 0 {
		t.Fatal("expired header admitted work between polls")
	}
}

func TestNativeCachedRespectsWorkerRepositoryScope(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	cfg.Repositories = []string{"code/other"}
	catalog, e := NewCachedNativeReader(cfg, live, f, RealClock{}).NativeSnapshot(t.Context(), cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	if jobs := NativeCandidates(cfg, catalog, nil, time.Now()); len(jobs) != 0 {
		t.Fatal("cache widened worker repository scope", jobs)
	}
}

func TestNativeCachedBranchRefreshWaitDiagnostics(t *testing.T) {
	cfg, f, m, live := cachedFixture(t)
	clock := &fakeClock{now: time.Now().UTC()}
	catalog, e := NewCachedNativeReader(cfg, live, f, clock).NativeSnapshot(t.Context(), cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	head := BodyHash("head")[:40]
	catalog.Tasks[0].PRs = []PR{{URL: "https://github.com/code/repo/pull/1", HeadSHA: head, MergeState: "BEHIND"}}
	d := &NativeDispatcher{config: cfg, clock: clock, catalog: catalog, history: map[string]NativeExecution{}, active: &NativeExecution{SelectedAt: clock.Now().Add(-10 * time.Minute), Phase: "selected", Job: Job{ID: BodyHash("review"), Kind: "review", TaskURL: catalog.Tasks[0].Task.URL, PRURL: catalog.Tasks[0].PRs[0].URL, HeadSHA: head}}}
	if p := d.Progress(); p.Reason != "native_branch_refresh_wait" {
		t.Fatal(p.Reason)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if p := d.Progress(); p.Reason == "native_branch_refresh_wait" {
		t.Fatal("stale cache explained current wait")
	}
}

func TestNativeCachedRookHydratesLiveArtifact(t *testing.T) {
	for _, merged := range []bool{false, true} {
		for _, mode := range []string{"current", "unavailable", "head-drift"} {
			label := "open/" + mode
			if merged {
				label = "merged/" + mode
			}
			t.Run(label, func(t *testing.T) {
				cfg, m, g, _, s, x := nativeDeliveryActionFixture(t)
				m.authority.Revision.Proposal.QA.Required = false
				binding := *x.Binding
				binding.QAID = 0
				s.metadata = NativeTaskMetadata(binding)
				s.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: x.Goal, TaskURL: binding.TaskURL, AssignmentSHA: NativeAssignmentSHA(binding), Artifact: x.Job.QAArtifact})
				s.reviews, m.handoffs = nil, nil
				for i := range m.links {
					m.links[i].AssignmentSHA = NativeAssignmentSHA(binding)
				}
				cat, err := g.NativeSnapshot(t.Context(), cfg, m)
				if err != nil {
					t.Fatal(err)
				}
				task, p := cat.Tasks[0], cat.Tasks[0].PRs[0]
				p.Merged, s.merged = merged, merged
				// Cache a plausible but wrong digest. Rook must obtain the live artifact.
				artifact := *p.QAArtifact
				artifact.SHA256 = strings.Repeat("f", 64)
				p.QAArtifact = &artifact
				f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("manifest"), Goals: []c.GoalAuthority{m.authority}}}
				addCachedTask(t, f, task)
				ref := c.GitHubObjectRef{RouteID: "route", Goal: x.Goal, TaskURL: task.Task.URL, URL: p.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				hash := c.GitHubDigest(raw)
				f.data[hash] = raw
				f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Hour), Header: c.GitHubHeader{Revision: hash, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, ScopeRevision: p.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: hash, HeaderRevision: hash, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Hour)}}})
				artifactReads := 0
				s.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
					if endpoint == "repos/code/repo/actions/artifacts/1" {
						artifactReads++
						if mode == "unavailable" {
							return CommandResult{}, c.ErrUnavailable
						}
					}
					return s.response(owner, endpoint, method, body)
				}
				if mode == "head-drift" {
					s.head = strings.Repeat("b", 40)
				}
				out, err := NewCachedNativeReader(cfg, g, f, RealClock{}).NativeSnapshot(t.Context(), cfg, m)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "current" {
					if len(out.Tasks) != 0 || out.Cache.Ready[task.Task.URL] || len(NativeCandidates(cfg, out, nil, time.Now())) != 0 {
						t.Fatal("cache substituted for failed live artifact validation", out)
					}
					return
				}
				if len(out.Tasks) != 1 || len(out.Tasks[0].PRs) != 1 {
					t.Fatal("live hydration lost task/PR", out)
				}
				fresh := out.Tasks[0].PRs[0]
				if artifactReads == 0 || !qaArtifactEqual(fresh.QAArtifact, x.Job.QAArtifact) || fresh.Merged != merged || fresh.Author.ID != task.Binding.AuthorID {
					t.Fatal("Rook trusted cached artifact instead of live source", fresh, artifactReads)
				}
			})
		}
	}
}
