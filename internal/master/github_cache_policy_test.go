package master

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

func TestGitHubCachePolicyDefaultsOff(t *testing.T) {
	if err := validateGitHubCacheConfig(GitHubCacheConfig{}, nil); err != nil {
		t.Fatal(err)
	}
	if mode := (GitHubCacheConfig{}).mode("team"); mode != "off" {
		t.Fatal(mode)
	}
}
func TestGitHubCachePolicyRejectsAmbiguousRoute(t *testing.T) {
	p := []c.Principal{{ID: "bird", Kind: "agent", Engagements: []string{"team"}}}
	r := GitHubCacheRoute{ID: "route", Engagement: "team", Collector: "bird", Host: "github.com", AppID: 1, InstallationID: 2, Repositories: []string{"org/repo"}, Threads: []string{"thread"}, Readers: []string{"bird"}}
	cfg := GitHubCacheConfig{Modes: map[string]string{"team": "shadow"}, Routes: []GitHubCacheRoute{r}}
	if err := validateGitHubCacheConfig(cfg, p); err != nil {
		t.Fatal(err)
	}
	cfg.Routes = append(cfg.Routes, r)
	if err := validateGitHubCacheConfig(cfg, p); err == nil {
		t.Fatal("duplicate route accepted")
	}
	cfg.Routes = []GitHubCacheRoute{r}
	cfg.Routes[0].Repositories = []string{"org/*"}
	if err := validateGitHubCacheConfig(cfg, p); err == nil {
		t.Fatal("wildcard accepted")
	}
}
