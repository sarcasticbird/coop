package worker

import (
	"context"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type acceptanceSource struct {
	tasks map[string]NativeTask
	reads int
}

func (s *acceptanceSource) Observe(_ context.Context, _ Config, l c.GitHubLease, _ []c.GitHubDatasetChunk) (GitHubObservation, error) {
	s.reads++
	task := s.tasks[l.Object.URL]
	raw, e := json.Marshal(task)
	state := "open"
	if task.Task.Closed {
		state = "closed"
	}
	sum := c.GitHubDigest(raw)
	return GitHubObservation{Commit: c.GitHubCommit{LeaseID: l.ID, Header: c.GitHubHeader{RepositoryID: 1, ObjectID: l.Object.URL, Revision: sum, ScopeRevision: task.Task.BodySHA, State: state, Completed: task.Task.Completed}, Datasets: []c.GitHubDatasetRef{{Kind: "task", Version: 1, SHA256: sum, Total: len(raw)}}}, Payloads: map[string][]byte{"task": raw}, ProviderRequests: 1}, e
}
func acceptanceMaster(t *testing.T, n int) (Config, *master.Service, *masterclient.Client, *masterclient.Client, *acceptanceSource, *fakeClock) {
	t.Helper()
	ctx := t.Context()
	cfg, _, base, _ := dispatchFixture(t)
	clock := &fakeClock{now: time.Now().UTC()}
	store, e := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.sqlite"), EpochPath: filepath.Join(cfg.StateDir, "cache-master.epoch"), MasterID: cfg.Master.MasterID, Now: clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = store.Close() })
	ps := []c.Principal{{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}}, {ID: "author", Alias: "author", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 12}, {ID: "peer", Alias: "peer", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 11}, {ID: "qa", Alias: "qa", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 13}, {ID: "rook", Alias: "rook", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 14}}
	placements := []c.Placement{{AgentID: "author", HostID: "rack-mini", InstanceID: cfg.Master.InstanceID, Generation: 1}, {AgentID: "peer", HostID: "rack-mini", InstanceID: BodyHash("peer"), Generation: 1}, {AgentID: "qa", HostID: "rack-mini", InstanceID: BodyHash("qa"), Generation: 1}, {AgentID: "rook", HostID: "blackbook", InstanceID: BodyHash("rook"), Generation: 1}}
	if e = store.Configure(ctx, ps, placements); e != nil {
		t.Fatal(e)
	}
	cfg.Master.HostID = "rack-mini"
	th, e := store.CreateThread(ctx, ps[0], c.CreateThread{Title: "Cache acceptance", Engagement: "coop", Members: []string{"owner", "author", "peer", "qa", "rook"}, RequestID: "thread"})
	if e != nil {
		t.Fatal(e)
	}
	svc := &master.Service{Store: store}
	goals := []c.GoalRef{}
	for i := range 5 {
		v, e := svc.ProposeGoal(ctx, ps[0], c.GoalProposal{ThreadID: th.ID, Purpose: fmt.Sprint("Goal ", i), Scope: "synthetic cache acceptance", Repositories: []c.RepositoryScope{{Name: "code/repo", BaseBranches: []string{"main"}}}, Acceptance: []string{"tests pass"}, AuthorID: "author", PeerID: "peer", RequestID: fmt.Sprint("goal-", i)})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.DecideGoal(ctx, ps[0], c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, Action: "approve", RequestID: fmt.Sprint("approve-", i)}); e != nil {
			t.Fatal(e)
		}
		goals = append(goals, v.Ref)
	}
	source := &acceptanceSource{tasks: map[string]NativeTask{}}
	for i := range n {
		task := base.catalog.Tasks[0]
		task.Binding.Goal = goals[i%5]
		task.Task.URL = fmt.Sprintf("https://github.com/code/repo/issues/%d", i+1)
		task.Binding.TaskURL = task.Task.URL
		task.Task.Title = fmt.Sprintf("Task %d", i)
		task.Task.BodySHA = TaskScopeHash(task.Task.Title, task.Task.Body)
		task.Binding.TaskScopeSHA = task.Task.BodySHA
		task.Task.Closed, task.Task.Completed = i < 4 || i >= 14, i < 4 || i >= 14
		source.tasks[task.Task.URL] = task
		if _, e = svc.RegisterArtifact(ctx, ps[0], c.ArtifactLink{Goal: task.Binding.Goal, Kind: "task", URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), RequestID: fmt.Sprintf("register-%d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	if e = store.ConfigureGitHubCache(ctx, master.GitHubCacheConfig{Modes: map[string]string{"coop": "on"}, Routes: []master.GitHubCacheRoute{{ID: "route", Engagement: "coop", Collector: "author", Host: "github.com", AppID: 1, InstallationID: 2, Repositories: []string{"code/repo"}, Threads: []string{th.ID}, Readers: []string{"owner", "author", "peer", "qa", "rook"}}}}); e != nil {
		t.Fatal(e)
	}
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: "author", InstanceID: cfg.Master.InstanceID, Generation: 1, SHA256: BodyHash("collector-test-token")}, {PrincipalID: "rook", InstanceID: BodyHash("rook"), Generation: 1, SHA256: BodyHash("rook-test-token")}}}
	srv := httptest.NewServer(master.NewHandler(svc, policy))
	t.Cleanup(srv.Close)
	client, e := masterclient.New(masterclient.ClientConfig{URL: srv.URL, MasterID: cfg.Master.MasterID, Token: "collector-test-token", AllowHTTP: true})
	if e != nil {
		t.Fatal(e)
	}
	rook, e := masterclient.New(masterclient.ClientConfig{URL: srv.URL, MasterID: cfg.Master.MasterID, Token: "rook-test-token", AllowHTTP: true})
	if e != nil {
		t.Fatal(e)
	}
	return cfg, svc, client, rook, source, clock
}
func TestGitHubCacheAcceptanceRequestCounts(t *testing.T) {
	for _, n := range []int{14, 140} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cfg, _, client, rook, source, clock := acceptanceMaster(t, n)
			cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true, CollectRoutes: []string{"route"}}
			collector := NewGitHubCollector(cfg, client, source, clock)
			for i := 0; i < n*3; i++ {
				clock.now = clock.now.Add(time.Second)
				if e := collector.round(t.Context(), "route"); e != nil {
					t.Fatal(e)
				}
			}
			before := source.reads
			live := &countedCatalog{dispatchCatalog: &dispatchCatalog{err: fmt.Errorf("provider must not run")}}
			reader := NewCachedNativeReader(cfg, live, client, clock)
			for range 10 {
				for range 3 {
					catalog, e := reader.NativeSnapshot(t.Context(), cfg, client)
					if e != nil {
						t.Fatal(e)
					}
					if len(catalog.Tasks) != n {
						t.Fatal("lost task facts", len(catalog.Tasks), n)
					}
					if jobs := NativeCandidates(cfg, catalog, nil, clock.Now()); len(jobs) != 10 {
						t.Fatal("lost active work", len(jobs))
					}
				}
			}
			status, e := rook.GitHubCacheStatus(t.Context(), "coop")
			if e != nil || len(status.Objects) != n {
				t.Fatal("cross-host status", len(status.Objects), e)
			}
			if source.reads != before || live.discovery != 0 || live.actions != 0 {
				t.Fatal("warm scheduler reached provider", source.reads, before)
			}
			t.Logf("objects=%d cold/maintenance observations=%d warm discovery provider calls=0; three consumers, ten polls", n, before)
		})
	}
}
func TestGitHubCacheAcceptanceCrossHostAndRestore(t *testing.T) {
	cfg, svc, client, rook, source, clock := acceptanceMaster(t, 14)
	cfg.GitHubCache = &GitHubCacheWorkerConfig{Enabled: true, CollectRoutes: []string{"route"}}
	collector := NewGitHubCollector(cfg, client, source, clock)
	if e := collector.round(t.Context(), "route"); e != nil {
		t.Fatal(e)
	}
	before, e := rook.GitHubCacheStatus(t.Context(), "coop")
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	if _, e = svc.Store.Backup(t.Context(), dir, nil); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restored")
	lineage := filepath.Join(cfg.StateDir, "cache-master.epoch")
	if e = client.DirtyGitHubObject(t.Context(), c.GitHubDirty{Object: before.Objects[0].Object, RequestID: "wake-cache"}); e != nil {
		t.Fatal(e)
	}
	after, e := rook.GitHubCacheStatus(t.Context(), "coop")
	if e != nil || !after.Objects[0].Dirty {
		t.Fatal("remote hint not visible", e)
	}
	statusJSON, e := json.Marshal(after)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(statusJSON), `"body"`) {
		t.Fatal("status exposed provider text")
	}
	if e = svc.Store.Close(); e != nil {
		t.Fatal(e)
	}
	restored, e := master.Restore(t.Context(), dir, target, lineage, false)
	if e != nil {
		t.Fatal(e)
	}
	if restored.Epoch != 2 {
		t.Fatal(restored)
	}
	store, e := master.OpenStore(t.Context(), master.StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: cfg.Master.MasterID, EpochPath: lineage, Now: clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = store.Close() }()
	next := &master.Service{Store: store}
	owner, e := store.Principal(t.Context(), "owner")
	if e != nil {
		t.Fatal(e)
	}
	page, e := next.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil || page.RestoreEpoch != 2 || len(page.Objects) != 0 {
		t.Fatal("restored readiness admitted before reconciliation", page, e)
	}
	bird, e := store.Principal(t.Context(), "author")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.ClaimGitHubRefresh(t.Context(), bird, c.GitHubClaim{RouteID: "route", RequestID: "after-restore"}); e == nil {
		t.Fatal("restored master admitted collection while quiescent")
	}
}
