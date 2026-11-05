package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

const providerProject = "01K00000000000000000000000"
const providerIssue = "01K00000000000000000000001"

type taskProviderFailure struct {
	status int
	body   any
}

func installTaskProviderHTTP(t *testing.T, cfg *Config, handle func(string) any) {
	t.Helper()
	installTaskProviderRequestHTTP(t, cfg, func(r *http.Request) any { return handle(r.URL.Path) })
}

func installTaskProviderRequestHTTP(t *testing.T, cfg *Config, handle func(*http.Request) any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-test-token" || r.Header.Get("X-Coop-Expected-Master-ID") != cfg.Master.MasterID {
			t.Error("provider call lost authenticated master pin")
		}
		w.Header().Set("X-Coop-Master-ID", cfg.Master.MasterID)
		w.Header().Set("X-Coop-Restore-Epoch", "1")
		result := handle(r)
		if failure, ok := result.(taskProviderFailure); ok {
			w.WriteHeader(failure.status)
			_ = json.NewEncoder(w).Encode(failure.body)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": cfg.Master.MasterID, "restore_epoch": 1, "data": result}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	token, path := filepath.Join(dir, "token"), filepath.Join(dir, "client.toml")
	if err := os.WriteFile(token, []byte("provider-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", srv.URL, cfg.Master.MasterID, token)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Master.ClientConfig = path
}

func TestNativeKataReferenceAndPRMetadata(t *testing.T) {
	ref, err := c.KataTaskReference(providerProject, providerIssue, "code/repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo, err := nativeTaskRepo(ref); err != nil || repo != "code/repo" {
		t.Fatal(repo, err)
	}
	goal := c.GoalRef{Source: "master", MasterID: "master", GoalID: "goal", RevisionSHA256: strings.Repeat("a", 64)}
	meta := NativePRMetadata{Version: 3, TaskURL: ref, Goal: goal, AssignmentSHA: strings.Repeat("b", 64)}
	if _, err = ParseNativePRMetadata(NativePRBody(meta), "code/repo", ref, goal, meta.AssignmentSHA); err != nil {
		t.Fatal(err)
	}
	if _, err = ParseNativePRMetadata(NativePRBody(meta), "code/other", ref, goal, meta.AssignmentSHA); err == nil {
		t.Fatal("foreign repo accepted")
	}
}

func TestKataBypassesGitHubCatalogCache(t *testing.T) {
	cfg, f, _, live := cachedFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	if NewCachedNativeReader(cfg, live, f, RealClock{}) != live {
		t.Fatal("Kata discovery uses GitHub task cache")
	}
}

func TestKataSourceDiscoveryDoesNotLimitSavedGitHubSnapshot(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	if nativeDiscoveryTask(cfg, job.TaskURL) {
		t.Fatal("unretained legacy task discovered")
	}
	installTaskProviderHTTP(t, &cfg, func(path string) any {
		if path != "/api/v1/tasks/capabilities" {
			t.Fatalf("unexpected provider request %s", path)
		}
		sha, _ := TeamPolicySHA(*cfg.Team)
		return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	})
	got, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job)
	if err != nil || len(got.Tasks) != 1 || got.TaskScope != job.TaskURL {
		t.Fatal("saved GitHub validation failed", err)
	}
}

func TestKataSourceConfigValidation(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject, RetainGitHubTasks: []string{"https://github.com/code/repo/issues/1"}}
	if err := validateTaskSource(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Version = 2 }, func(c *Config) { c.Master = nil },
		func(c *Config) { c.TaskSource.Provider = "github" },
		func(c *Config) { c.TaskSource.ProjectUID = strings.ToLower(providerProject) },
		func(c *Config) { c.TaskSource.RetainGitHubTasks = []string{"https://github.com/foreign/repo/issues/1"} },
		func(c *Config) { c.TaskSource.RetainGitHubTasks = []string{"https://github.com/code/repo/pull/1"} },
	} {
		invalid := Defaults(cfg)
		change(&invalid)
		if err := validateTaskSource(invalid); err == nil {
			t.Fatal("invalid task source accepted")
		}
	}
}

func TestKataPlanningClientAllowsCoordinatorRosterWithoutWeakeningEngineering(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	sha, _ := TeamPolicySHA(*cfg.Team)
	cfg.Team.Members = append(cfg.Team.Members, TeamMember{Name: "coordinator", Actor: Actor{ID: 99, Login: "coordinator[bot]", Kind: "Bot"}})
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	caps := c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	installTaskProviderHTTP(t, &cfg, func(path string) any { return caps })
	if _, err := TaskPlanningClient(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := TaskProviderClient(t.Context(), cfg); err == nil {
		t.Fatal("engineering accepted roster mismatch")
	}
	for _, change := range []func(){
		func() { caps.PolicySHA = "" }, func() { caps.PolicySHA = strings.Repeat("g", 64) },
		func() { caps.PolicySHA = sha; caps.ProjectUID = providerIssue },
		func() { caps.ProjectUID = providerProject; caps.Provider = "github" },
	} {
		change()
		if _, err := TaskPlanningClient(t.Context(), cfg); err == nil {
			t.Fatal("invalid capability pin accepted")
		}
	}
}
