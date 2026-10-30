package master

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

type cacheQueryCounter struct {
	queryer
	reads int
}

func (q *cacheQueryCounter) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	q.reads++
	return q.queryer.QueryContext(ctx, statement, args...)
}

func (q *cacheQueryCounter) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	q.reads++
	return q.queryer.QueryRowContext(ctx, statement, args...)
}

func TestGitHubCacheSlowRoundUsesValidationTime(t *testing.T) {
	s, o, b, _ := cacheFixture(t)
	now := time.Now().UTC()
	s.Store.now = func() time.Time { return now }
	l, err := s.ClaimGitHubRefresh(t.Context(), b, c.GitHubClaim{RouteID: "route", RequestID: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	commit := stageCache(t, s, b, l)
	now = now.Add(2 * time.Minute)
	commit.HeaderObservedAt = now
	commit.Datasets[0].ObservedAt = now
	if err = s.CommitGitHubRefresh(t.Context(), b, commit); err != nil {
		t.Fatal("fresh verification after a slow round was rejected", err)
	}
	page, err := s.GitHubCatalog(t.Context(), o, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil || page.Objects[0].Dirty || !page.Objects[0].DueAt.Equal(now.Add(time.Minute)) || !page.Objects[0].Datasets[0].ObservedAt.Equal(now) {
		t.Fatal("slow round published stale observations", page, err)
	}
	old := page.Objects[0].Datasets[0]
	now = now.Add(61 * time.Second)
	l, err = s.ClaimGitHubRefresh(t.Context(), b, c.GitHubClaim{RouteID: "route", RequestID: "retained"})
	if err != nil {
		t.Fatal(err)
	}
	commit = stageCache(t, s, b, l)
	commit.HeaderObservedAt = now.Add(time.Second)
	if err = s.CommitGitHubRefresh(t.Context(), b, commit); !errors.Is(err, c.ErrInvalid) {
		t.Fatal("future header verification accepted", err)
	}
	commit.HeaderObservedAt = now
	commit.Datasets[0].ObservedAt = now.Add(time.Second)
	if err = s.CommitGitHubRefresh(t.Context(), b, commit); !errors.Is(err, c.ErrInvalid) {
		t.Fatal("future dataset verification accepted", err)
	}
	commit.Datasets[0].ObservedAt = old.ObservedAt
	if err = s.CommitGitHubRefresh(t.Context(), b, commit); err != nil {
		t.Fatal(err)
	}
	page, err = s.GitHubCatalog(t.Context(), o, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil || !page.Objects[0].Datasets[0].DueAt.Equal(old.DueAt) {
		t.Fatal("retained dataset freshness advanced", page, err)
	}
}

func TestGitHubCacheCatalogLoadsOnlyRequestedCoverage(t *testing.T) {
	s, o, b, ref := cacheFixture(t)
	for i := 2; i <= 151; i++ {
		if _, err := s.RegisterArtifact(t.Context(), o, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: fmt.Sprintf("https://github.com/example/coop/issues/%d", i), AssignmentSHA: ref.AssignmentSHA, RequestID: fmt.Sprintf("paging-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimGitHubRefresh(t.Context(), b, c.GitHubClaim{RouteID: "route", RequestID: "seed-pages"}); err != nil {
		t.Fatal(err)
	}
	// This object sorts on page two. Its broken coverage must not be read by
	// page one, which only needs the complete registration manifest plus 100 facts.
	if _, err := s.Store.db.ExecContext(t.Context(), "UPDATE github_cache_objects SET header='invalid' WHERE json_extract(ref,'$.url')=?", "https://github.com/example/coop/issues/99"); err != nil {
		t.Fatal(err)
	}
	page, err := s.GitHubCatalog(t.Context(), o, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil || len(page.Objects) != 100 || page.Next == "" {
		t.Fatal("first page hydrated unrelated coverage", len(page.Objects), err)
	}
	counted := &cacheQueryCounter{queryer: s.Store.db}
	if err = s.cachePageCoverage(t.Context(), counted, page.Objects); err != nil || counted.reads != 2 {
		t.Fatal("coverage query count grew with page size", counted.reads, err)
	}
	if _, err = s.GitHubCatalog(t.Context(), o, c.GitHubCatalogQuery{Engagement: "coop", Manifest: page.Manifest, After: page.Next}); err == nil {
		t.Fatal("selected second page failed to read its own coverage")
	}
}

func TestGitHubCacheClaimWithKataTaskLink(t *testing.T) {
	s, o, b, in, _ := taskFixture(t)
	task, err := s.TaskCreate(t.Context(), o, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterArtifact(t.Context(), b, c.ArtifactLink{Goal: in.Goal, Kind: "pull_request", TaskURL: task.Reference, URL: "https://github.com/example/coop/pull/3", AssignmentSHA: digest(*task.Binding), RequestID: "kata-pr-cache"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.ResolveGoal(t.Context(), o, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.ConfigureGitHubCache(t.Context(), GitHubCacheConfig{
		Modes:  map[string]string{"coop": "on"},
		Routes: []GitHubCacheRoute{{ID: "route", Engagement: "coop", Collector: b.ID, Host: "github.com", AppID: 1, InstallationID: 2, Repositories: []string{"example/coop"}, Threads: []string{a.Revision.Proposal.ThreadID}, Readers: []string{o.ID, b.ID}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, page, e := s.cacheManifest(t.Context(), s.Store.db, b, "coop"); e != nil || len(page.Objects) != 0 {
		t.Fatalf("Kata objects entered GitHub cache manifest: task=%s err=%v objects=%d", task.Reference, e, len(page.Objects))
	}
	lease, err := s.ClaimGitHubRefresh(t.Context(), b, c.GitHubClaim{RouteID: "route", RequestID: "kata-task-claim"})
	if err != nil || lease.ID != "" {
		t.Fatalf("Kata task was offered as a GitHub cache lease: lease=%+v err=%v", lease, err)
	}
	if _, err = s.RegisterArtifact(t.Context(), o, c.ArtifactLink{Goal: in.Goal, Kind: "task", URL: "https://github.com/example/coop/issues/2", AssignmentSHA: strings.Repeat("a", 64), RequestID: "github-task-cache"}); err != nil {
		t.Fatal(err)
	}
	lease, err = s.ClaimGitHubRefresh(t.Context(), b, c.GitHubClaim{RouteID: "route", RequestID: "github-task-claim"})
	if err != nil || lease.Object.URL != "https://github.com/example/coop/issues/2" {
		t.Fatalf("Kata task prevented a separate GitHub task lease: lease=%+v err=%v", lease, err)
	}
}

func cacheFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.GitHubObjectRef) {
	t.Helper()
	svc, o, b, in := goalFixture(t)
	ctx := context.Background()
	v, e := svc.ProposeGoal(ctx, b, in)
	if e != nil {
		t.Fatal(e)
	}
	_, e = svc.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, Action: "approve", RequestID: "approve-cache"})
	if e != nil {
		t.Fatal(e)
	}
	link, e := svc.RegisterArtifact(ctx, o, c.ArtifactLink{Goal: v.Ref, Kind: "task", URL: "https://github.com/example/coop/issues/1", AssignmentSHA: strings.Repeat("a", 64), RequestID: "task-cache"})
	if e != nil {
		t.Fatal(e)
	}
	cfg := GitHubCacheConfig{Modes: map[string]string{"coop": "on"}, Routes: []GitHubCacheRoute{{ID: "route", Engagement: "coop", Collector: b.ID, Host: "github.com", AppID: 1, InstallationID: 2, Repositories: []string{"example/coop"}, Threads: []string{in.ThreadID}, Readers: []string{o.ID, b.ID, "agent-two"}}}}
	if e = svc.Store.ConfigureGitHubCache(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	return svc, o, b, c.GitHubObjectRef{RouteID: "route", Goal: v.Ref, TaskURL: link.URL, URL: link.URL, Kind: "task", AssignmentSHA: link.AssignmentSHA}
}
func stageCache(t *testing.T, s *Service, b c.Principal, l c.GitHubLease) c.GitHubCommit {
	t.Helper()
	raw := []byte(`{"ok":true}`)
	sum := c.GitHubDigest(raw)
	if e := s.UploadGitHubDataset(context.Background(), b, c.GitHubUpload{LeaseID: l.ID, Kind: "task", Data: raw, Total: len(raw), SHA256: sum}); e != nil {
		t.Fatal(e)
	}
	return c.GitHubCommit{LeaseID: l.ID, Header: c.GitHubHeader{Revision: strings.Repeat("b", 64), RepositoryID: 1, ObjectID: "I_1", State: "open"}, Datasets: []c.GitHubDatasetRef{{Kind: "task", SHA256: sum, Total: len(raw), Version: 1}}}
}
func TestGitHubCacheLeaseCAS(t *testing.T) {
	s, _, b, ref := cacheFixture(t)
	ctx := context.Background()
	l, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "claim"})
	if e != nil || l.ID == "" {
		t.Fatal(l, e)
	}
	duplicate, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "claim"})
	if e != nil || duplicate.ID != l.ID {
		t.Fatal(duplicate, e)
	}
	other, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "other"})
	if e != nil || other.ID != "" {
		t.Fatal(other, e)
	}
	commit := stageCache(t, s, b, l)
	if e = s.DirtyGitHubObject(ctx, b, c.GitHubDirty{Object: ref, RequestID: "dirty"}); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitGitHubRefresh(ctx, b, commit); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("late commit: %v", e)
	}
	var staged int
	if e = s.Store.db.QueryRowContext(ctx, "SELECT count(*) FROM github_cache_chunks WHERE lease=?", l.ID).Scan(&staged); e != nil || staged != 0 {
		t.Fatal("invalidated lease retained staging", staged, e)
	}
	s.Store.now = func() time.Time { return l.StartedAt.Add(2 * time.Second) }
	next, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "after-dirty"})
	if e != nil || next.ID == "" || next.ID == l.ID {
		t.Fatal("invalidated lease held route until expiry", next, e)
	}
}

func TestGitHubCachePresenceDoesNotInvalidateObservation(t *testing.T) {
	s, o, b, _ := cacheFixture(t)
	ctx := t.Context()
	l, err := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "presence"})
	if err != nil {
		t.Fatal(err)
	}
	commit := stageCache(t, s, b, l)
	hb := c.Heartbeat{Placement: l.Collector, State: "busy"}
	if err = s.Store.Heartbeat(ctx, b, hb); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitGitHubRefresh(ctx, b, commit); err != nil {
		t.Fatal("heartbeat invalidated in-flight collection", err)
	}
	before, err := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil {
		t.Fatal(err)
	}
	hb.State = "idle"
	if err = s.Store.Heartbeat(ctx, b, hb); err != nil {
		t.Fatal(err)
	}
	after, err := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop", Manifest: before.Manifest})
	if err != nil || after.Objects[0].Dirty {
		t.Fatal("heartbeat dirtied published observations", after, err)
	}
	newPlacement := l.Collector
	newPlacement.Generation++
	if _, err = s.Store.db.ExecContext(ctx, "UPDATE placements SET body=? WHERE agent_id=?", encode(newPlacement), b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop", Manifest: before.Manifest}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("placement identity change did not invalidate manifest", err)
	}
}
func TestGitHubCachePublicationBounds(t *testing.T) {
	s, o, b, _ := cacheFixture(t)
	ctx := context.Background()
	l, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "claim"})
	if e != nil {
		t.Fatal(e)
	}
	commit := stageCache(t, s, b, l)
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatalf("idempotent commit: %v", e)
	}
	page, e := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil || len(page.Objects) != 1 || page.Objects[0].Dirty {
		t.Fatal(page, e)
	}
	d, e := s.GitHubDataset(ctx, o, c.GitHubDatasetQuery{Ref: page.Objects[0].Datasets[0], Manifest: page.Manifest})
	if e != nil || string(d.Data) != `{"ok":true}` {
		t.Fatal(d, e)
	}
	commit.Header.RepositoryID = 2
	if e = s.CommitGitHubRefresh(ctx, b, commit); !errors.Is(e, c.ErrConflict) {
		t.Fatal("changed retry", e)
	}
}
func TestGitHubCacheRetentionAndRestore(t *testing.T) {
	s, o, b, _ := cacheFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Store.now = func() time.Time { return now }
	l, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "claim"})
	if e != nil {
		t.Fatal(e)
	}
	commit := stageCache(t, s, b, l)
	now = now.Add(6 * time.Minute)
	if e = s.CommitGitHubRefresh(ctx, b, commit); !errors.Is(e, c.ErrConflict) {
		t.Fatal("expired commit", e)
	}
	next, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "claim2"})
	if e != nil || next.ID == "" || next.ID == l.ID {
		t.Fatal(next, e)
	}
	commit = stageCache(t, s, b, next)
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatal(e)
	}
	s.Store.epoch++
	page, e := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil || !page.Objects[0].Dirty {
		t.Fatal("restore left fresh", page, e)
	}
}
func TestGitHubCacheManifestIsolation(t *testing.T) {
	s, o, b, _ := cacheFixture(t)
	ctx := context.Background()
	page, e := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.GitHubCatalog(ctx, b, c.GitHubCatalogQuery{Engagement: "else"}); !errors.Is(e, c.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = s.Store.db.ExecContext(ctx, "DELETE FROM members WHERE principal_id=?", b.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop", Manifest: page.Manifest}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("changed manifest", e)
	}
}

func TestGitHubCacheTerminalRefreshCadence(t *testing.T) {
	s, o, b, ref := cacheFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	s.Store.now = func() time.Time { return now }
	l, e := s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "close"})
	if e != nil {
		t.Fatal(e)
	}
	commit := stageCache(t, s, b, l)
	commit.Header.State = "closed"
	commit.Header.Completed = true
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatal(e)
	}
	page, e := s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil {
		t.Fatal(e)
	}
	if !page.Objects[0].DueAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatal("unreferenced terminal object not daily", page.Objects[0].DueAt)
	}
	_, e = s.RegisterArtifact(ctx, o, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: "https://github.com/example/coop/issues/2", AssignmentSHA: strings.Repeat("a", 64), RequestID: "dependent"})
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Second)
	l, e = s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "collect-dependent"})
	if e != nil {
		t.Fatal(e)
	}
	commit = stageCache(t, s, b, l)
	commit.Header.ObjectID = "I_2"
	commit.Header.DependsOn = []string{ref.URL}
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Second)
	l, e = s.ClaimGitHubRefresh(ctx, b, c.GitHubClaim{RouteID: "route", RequestID: "refresh-referenced"})
	if e != nil {
		t.Fatal(e)
	}
	if l.Object.URL != ref.URL {
		t.Fatal("new dependency did not dirty closed fact", l.Object.URL)
	}
	commit = stageCache(t, s, b, l)
	commit.Header.State = "closed"
	commit.Header.Completed = true
	if e = s.CommitGitHubRefresh(ctx, b, commit); e != nil {
		t.Fatal(e)
	}
	page, e = s.GitHubCatalog(ctx, o, c.GitHubCatalogQuery{Engagement: "coop"})
	if e != nil || !page.Objects[0].DueAt.Equal(now.Add(15*time.Minute)) {
		t.Fatal("referenced fact not on 15-minute cadence", page, e)
	}
}
