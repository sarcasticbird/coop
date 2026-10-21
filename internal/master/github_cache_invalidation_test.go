package master

import (
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestGitHubCacheUnchangedArtifactPreservesCoverage(t *testing.T) {
	s, owner, collector, ref := cacheFixture(t)
	lease, err := s.ClaimGitHubRefresh(t.Context(), collector, c.GitHubClaim{RouteID: "route", RequestID: "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	commit := stageCache(t, s, collector, lease)
	for i := range 3 {
		_, err = s.RegisterArtifact(t.Context(), owner, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: ref.URL, AssignmentSHA: ref.AssignmentSHA, RequestID: []string{"again-a", "again-b", "again-c"}[i]})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CommitGitHubRefresh(t.Context(), collector, commit); err != nil {
		t.Fatal("identical registration invalidated in-flight coverage", err)
	}
	before, err := s.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RegisterArtifact(t.Context(), owner, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: ref.URL, AssignmentSHA: ref.AssignmentSHA, RequestID: "after-publish"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop", Manifest: before.Manifest})
	if err != nil || after.Objects[0].Dirty || !after.Objects[0].DueAt.Equal(before.Objects[0].DueAt) {
		t.Fatal("identical registration dirtied published coverage", after, err)
	}
}

func TestGitHubCacheArtifactChangeKeepsUnrelatedCoverage(t *testing.T) {
	s, owner, collector, ref := cacheFixture(t)
	second := c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: "https://github.com/example/coop/issues/2", AssignmentSHA: ref.AssignmentSHA, RequestID: "second"}
	if _, err := s.RegisterArtifact(t.Context(), owner, second); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.Store.now = func() time.Time { return now }
	for i, request := range []string{"first-warm", "second-warm"} {
		now = now.Add(time.Second)
		lease, err := s.ClaimGitHubRefresh(t.Context(), collector, c.GitHubClaim{RouteID: "route", RequestID: request})
		if err != nil || lease.ID == "" {
			t.Fatal(i, lease, err)
		}
		if err = s.CommitGitHubRefresh(t.Context(), collector, stageCache(t, s, collector, lease)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil {
		t.Fatal(err)
	}
	second.ExpectedAssignmentSHA, second.AssignmentSHA, second.RequestID = second.AssignmentSHA, strings.Repeat("c", 64), "rebind-second"
	if _, err = s.RegisterArtifact(t.Context(), owner, second); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop", Manifest: before.Manifest}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed registration did not fence the old manifest", err)
	}
	after, err := s.GitHubCatalog(t.Context(), owner, c.GitHubCatalogQuery{Engagement: "coop"})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range after.Objects {
		if o.Object.URL == ref.URL && (o.Dirty || len(o.Datasets) == 0) {
			t.Fatal("unrelated task lost fresh coverage", o)
		}
		if o.Object.URL == second.URL && (!o.Dirty || len(o.Datasets) != 0) {
			t.Fatal("rebound task inherited old assignment coverage", o)
		}
	}
}

func TestGitHubCacheUpgradesInstalledInvalidationTrigger(t *testing.T) {
	s, owner, collector, ref := cacheFixture(t)
	// Reproduce the trigger already present on deployed #126 databases.
	_, err := s.Store.db.ExecContext(t.Context(), `DROP TRIGGER cache_revision_artifacts_UPDATE;
CREATE TRIGGER cache_revision_artifacts_UPDATE AFTER UPDATE ON artifacts BEGIN
UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='github_cache_revision';
UPDATE github_cache_objects SET generation=generation+1,due='';
UPDATE github_cache_leases SET result='invalidated' WHERE result=''; END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.initializeGitHubCache(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimGitHubRefresh(t.Context(), collector, c.GitHubClaim{RouteID: "route", RequestID: "upgraded-trigger"})
	if err != nil {
		t.Fatal(err)
	}
	commit := stageCache(t, s, collector, lease)
	_, err = s.RegisterArtifact(t.Context(), owner, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: ref.URL, AssignmentSHA: ref.AssignmentSHA, RequestID: "upgrade-no-op"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitGitHubRefresh(t.Context(), collector, commit); err != nil {
		t.Fatal("deployed old trigger survived initialization", err)
	}
}

func TestGitHubCacheDirtyFairnessRetainsObservationAge(t *testing.T) {
	s, owner, collector, ref := cacheFixture(t)
	other := "https://github.com/example/coop/issues/2"
	if _, err := s.RegisterArtifact(t.Context(), owner, c.ArtifactLink{Goal: ref.Goal, Kind: "task", URL: other, AssignmentSHA: ref.AssignmentSHA, RequestID: "fairness-second"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.Store.now = func() time.Time { return now }
	for i := range 9 {
		now = now.Add(61 * time.Second)
		lease, err := s.ClaimGitHubRefresh(t.Context(), collector, c.GitHubClaim{RouteID: "route", RequestID: "fairness-" + strings.Repeat("x", i+1)})
		if err != nil || lease.ID == "" {
			t.Fatal(i, lease, err)
		}
		if err = s.CommitGitHubRefresh(t.Context(), collector, stageCache(t, s, collector, lease)); err != nil {
			t.Fatal(err)
		}
	}
	// A real authority change dirties both objects; prior observation age must
	// still determine the tenth, age-priority selection rather than URL order.
	_, err := s.Store.db.ExecContext(t.Context(), "UPDATE github_cache_objects SET due='',updated=CASE WHEN json_extract(ref,'$.url')=? THEN ? ELSE ? END", other, cacheTime(now.Add(-10*time.Minute)), cacheTime(now))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	lease, err := s.ClaimGitHubRefresh(t.Context(), collector, c.GitHubClaim{RouteID: "route", RequestID: "tenth-oldest"})
	if err != nil || lease.Object.URL != other {
		t.Fatal("dirty oldest object lost fairness priority", lease.Object.URL, err)
	}
}
