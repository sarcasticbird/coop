package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configFixture(t *testing.T) Config {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Config{Version: 1, Name: "builder", Role: "implementer", Model: "example/model",
		StateDir: filepath.Join(root, "state"), WorkRoot: filepath.Join(root, "work"),
		Owner:       Actor{ID: 1, Login: "owner", Kind: "User"},
		Self:        Actor{ID: 2, Login: "builder[bot]", Kind: "Bot"},
		Implementer: Actor{ID: 2, Login: "builder[bot]", Kind: "Bot"},
		Reviewer:    Actor{ID: 3, Login: "reviewer[bot]", Kind: "Bot"},
		CodeOwners:  []string{"code"}, DiscussionRepositories: []string{"planning/board"},
		Projects: []ProjectRef{{Owner: "planning", Number: 2}},
		OpenCode: OpenCodeConfig{BaseURL: "http://127.0.0.1:4096", PasswordFile: filepath.Join(root, "password")}}
}

func TestConfigPrivateAndStrict(t *testing.T) {
	cfg := configFixture(t)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(cfg.StateDir), "config.json")
	write := func(data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(raw, 0600)
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.PollInterval != time.Minute || got.Limits.TurnLimit != 30*time.Minute {
		t.Fatal("missing bounded defaults")
	}
	for _, bad := range [][]byte{
		append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"token":"sentinel-secret"}`)...),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(strings.Repeat(" ", (1<<20)+1)),
	} {
		write(bad, 0600)
		if _, err := LoadConfig(path); err == nil || strings.Contains(err.Error(), "sentinel-secret") {
			t.Fatal("unsafe config was accepted or echoed")
		}
	}
	write(raw, 0644)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("public config accepted")
	}
	write(raw, 0600)
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestConfigScopeAndLimits(t *testing.T) {
	tests := map[string]func(*Config){
		"wrong version":      func(c *Config) { c.Version = 2 },
		"relative state":     func(c *Config) { c.StateDir = "state" },
		"same directories":   func(c *Config) { c.WorkRoot = c.StateDir },
		"bad role":           func(c *Config) { c.Role = "admin" },
		"bot owner":          func(c *Config) { c.Owner.Kind = "Bot" },
		"same reviewer":      func(c *Config) { c.Reviewer = c.Implementer },
		"different self":     func(c *Config) { c.Self = c.Reviewer },
		"missing model":      func(c *Config) { c.Model = "model" },
		"duplicate owner":    func(c *Config) { c.CodeOwners = []string{"code", "CODE"} },
		"invalid repository": func(c *Config) { c.DiscussionRepositories = []string{"planning/../board"} },
		"mixed selectors":    func(c *Config) { c.ProjectOwners = []string{"planning"} },
		"too many attempts":  func(c *Config) { c.Limits.MaxAttempts = 4 },
		"too many cycles":    func(c *Config) { c.Limits.MaxReviewCycles = 6 },
		"long turn":          func(c *Config) { c.Limits.TurnLimit = time.Hour + time.Second },
		"negative poll":      func(c *Config) { c.Limits.PollInterval = -time.Second },
		"public server":      func(c *Config) { c.OpenCode.BaseURL = "http://0.0.0.0:4096" },
		"url credentials":    func(c *Config) { c.OpenCode.BaseURL = "http://user:pass@127.0.0.1:4096" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := configFixture(t)
			mutate(&cfg)
			if err := ValidateConfig(cfg); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func policyFixture(t *testing.T) (Config, Snapshot, time.Time) {
	t.Helper()
	cfg := configFixture(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	goal := Goal{URL: "https://github.com/orgs/planning/discussions/1", Body: "Approved work", Repositories: []string{"code/repo"}, CreatedAt: now.Add(-2 * time.Hour), LastEditedAt: now.Add(-time.Hour), Readable: true,
		Decisions: []Decision{{ID: "approval", Command: "/coop approve", Author: cfg.Owner, CreatedAt: now.Add(-time.Minute)}}}
	goal.BodySHA = BodyHash(goal.Body)
	task := Task{URL: "https://github.com/code/repo/issues/4", Repo: "code/repo", GoalURL: goal.URL, Title: "Feature", Body: "Scope", Revision: "task-1", Ready: true, MetadataAuthor: cfg.Reviewer}
	task.BodySHA = TaskScopeHash(task.Title, task.Body)
	task.MetadataScopeSHA = task.BodySHA
	return cfg, Snapshot{Goals: []Goal{goal}, Tasks: []Task{task}, Complete: true}, now
}

func TestGoalApprovalRequiresCurrentHumanDecision(t *testing.T) {
	tests := map[string]func(*Goal, Config, time.Time){
		"bot impersonation": func(g *Goal, c Config, n time.Time) {
			g.Decisions[0].Author = Actor{ID: 2, Login: c.Owner.Login, Kind: "Bot"}
		},
		"missing":    func(g *Goal, c Config, n time.Time) { g.Decisions = nil },
		"unreadable": func(g *Goal, c Config, n time.Time) { g.Readable = false },
		"changed body": func(g *Goal, c Config, n time.Time) {
			g.LastEditedAt = n
			g.Body = "New scope"
			g.BodySHA = BodyHash(g.Body)
		},
		"pause": func(g *Goal, c Config, n time.Time) {
			g.Decisions = append(g.Decisions, Decision{Command: "/coop pause", Author: c.Owner, CreatedAt: n})
		},
		"reject": func(g *Goal, c Config, n time.Time) {
			g.Decisions = append(g.Decisions, Decision{Command: "/coop reject", Author: c.Owner, CreatedAt: n})
		},
		"complete": func(g *Goal, c Config, n time.Time) {
			g.Decisions = append(g.Decisions, Decision{Command: "/coop complete", Author: c.Owner, CreatedAt: n})
		},
		"resume without approval": func(g *Goal, c Config, n time.Time) { g.Decisions[0].Command = "/coop resume" },
		"quoted command":          func(g *Goal, c Config, n time.Time) { g.Decisions[0].Command = "> /coop approve" },
		"embedded command":        func(g *Goal, c Config, n time.Time) { g.Decisions[0].Command = "Please /coop approve" },
		"future approval":         func(g *Goal, c Config, n time.Time) { g.Decisions[0].CreatedAt = n.Add(time.Hour) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, s, n := policyFixture(t)
			g := s.Goals[0]
			mutate(&g, cfg, n)
			if got := EvaluateGoal(cfg, g, n); got.State == "approved" {
				t.Fatalf("unauthorized goal admitted: %s", got.Reason)
			}
		})
	}
	cfg, s, n := policyFixture(t)
	if got := EvaluateGoal(cfg, s.Goals[0], n); got.State != "approved" {
		t.Fatalf("human approval denied: %+v", got)
	}
	g := s.Goals[0]
	g.Decisions = append(g.Decisions, Decision{Command: "/coop pause", Author: cfg.Owner, CreatedAt: n.Add(-30 * time.Second)}, Decision{Command: "/coop resume", Author: cfg.Owner, CreatedAt: n})
	if EvaluateGoal(cfg, g, n).State != "approved" {
		t.Fatal("valid resume denied")
	}
	g.Decisions = nil
	cfg.BootstrapGoals = []BootstrapGoal{{URL: g.URL, BodySHA: g.BodySHA, OwnerID: cfg.Owner.ID, Repositories: g.Repositories, Source: "owner-confirmed"}}
	if EvaluateGoal(cfg, g, n).State != "approved" {
		t.Fatal("trusted bootstrap denied")
	}
	g.Body = "Changed bootstrap scope"
	g.BodySHA = BodyHash(g.Body)
	if EvaluateGoal(cfg, g, n).State == "approved" {
		t.Fatal("changed bootstrap accepted")
	}
}

func TestAdmissionAndIdleDedup(t *testing.T) {
	cfg, s, n := policyFixture(t)
	jobs := Select(cfg, s, State{}, n)
	if len(jobs) != 1 || jobs[0].Kind != "implement" {
		t.Fatalf("Ready work did not start: %+v", jobs)
	}
	state := State{Jobs: map[string]Job{jobs[0].ID: jobs[0]}}
	state.Jobs[jobs[0].ID] = Job{ID: jobs[0].ID, Phase: "completed"}
	for range 20 {
		if got := Select(cfg, s, state, n); len(got) != 0 {
			t.Fatal("unchanged completed work replayed")
		}
	}
	tests := map[string]func(*Config, *Snapshot){
		"partial":          func(c *Config, s *Snapshot) { s.Complete = false },
		"unapproved":       func(c *Config, s *Snapshot) { s.Goals[0].Decisions = nil },
		"missing metadata": func(c *Config, s *Snapshot) { s.Tasks[0].MetadataAuthor = Actor{} },
		"changed task":     func(c *Config, s *Snapshot) { s.Tasks[0].BodySHA = BodyHash("new scope") },
		"foreign repo":     func(c *Config, s *Snapshot) { s.Tasks[0].Repo = "foreign/repo" },
		"manual claim":     func(c *Config, s *Snapshot) { s.Tasks[0].Claim = &Claim{ID: "manual", Author: c.Owner} },
		"dependency":       func(c *Config, s *Snapshot) { s.Tasks[0].DependsOn = []string{"https://github.com/code/repo/issues/3"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c, s, n := policyFixture(t)
			mutate(&c, &s)
			if got := Select(c, s, State{}, n); len(got) != 0 {
				t.Fatalf("unsafe dispatch: %+v", got)
			}
		})
	}
}

func TestReviewFixAndDeliveryUseCurrentHead(t *testing.T) {
	cfg, s, n := policyFixture(t)
	s.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Branch: "agent/task", Revision: "pr-1", Author: cfg.Implementer, ChecksState: "success", MergeState: "CLEAN"}}
	pr := &s.PRs[0]
	cfg.Role = "planner"
	cfg.Self = cfg.Reviewer
	jobs := Select(cfg, s, State{}, n)
	if len(jobs) != 1 || jobs[0].Kind != "review" {
		t.Fatalf("Hawk did not review PR: %+v", jobs)
	}
	pr.Reviews = []Review{{ID: "review-1", HeadSHA: pr.HeadSHA, State: "CHANGES_REQUESTED", Author: cfg.Reviewer, SubmittedAt: n}}
	cfg.Role = "implementer"
	cfg.Self = cfg.Implementer
	jobs = Select(cfg, s, State{}, n)
	if len(jobs) != 1 || jobs[0].Kind != "fix" {
		t.Fatalf("Robin did not pick up review: %+v", jobs)
	}
	pr.Reviews[0].State = "APPROVED"
	jobs = Select(cfg, s, State{}, n)
	if len(jobs) != 1 || jobs[0].Kind != "deliver" {
		t.Fatalf("current-head delivery missing: %+v", jobs)
	}
	pr.HeadSHA = strings.Repeat("b", 40)
	if got := Select(cfg, s, State{}, n); len(got) != 0 {
		t.Fatal("old-head approval cleared delivery")
	}
	pr.Reviews[0].HeadSHA = pr.HeadSHA
	pr.ChecksState = "unknown"
	if got := Select(cfg, s, State{}, n); len(got) != 0 {
		t.Fatal("unknown checks cleared delivery")
	}
	pr.ChecksState = "success"
	pr.MergeState = "BLOCKED"
	if got := Select(cfg, s, State{}, n); len(got) != 0 {
		t.Fatal("repository rules bypassed")
	}
}

func TestChangedTaskMetadataRequiresFreshReviewAtSameHead(t *testing.T) {
	cfg, s, now := policyFixture(t)
	p := PR{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: cfg.Implementer, ChecksState: "success", MergeState: "CLEAN", ScopeUpdatedAt: now}
	p.Reviews = []Review{{ID: "old", State: "APPROVED", Author: cfg.Reviewer, HeadSHA: p.HeadSHA, SubmittedAt: now.Add(-time.Minute)}}
	s.PRs = []PR{p}
	if jobs := Select(cfg, s, State{}, now); len(jobs) != 0 {
		t.Fatal("review before changed task metadata allowed delivery")
	}
	cfg.Role, cfg.Self = "planner", cfg.Reviewer
	jobs := Select(cfg, s, State{}, now)
	if len(jobs) != 1 || jobs[0].Kind != "review" {
		t.Fatal("changed metadata did not request fresh independent review")
	}
}

func TestAdmissionRejectsUnboundURLsAndBotManualClaims(t *testing.T) {
	tests := map[string]func(*Config, *Snapshot){
		"issue URL differs from scope": func(c *Config, s *Snapshot) { s.Tasks[0].URL = "https://github.com/other/repo/issues/4" },
		"bot manual claim":             func(c *Config, s *Snapshot) { s.Tasks[0].Claim = &Claim{Author: c.Implementer, ID: "manual"} },
		"unhashed changed issue body":  func(c *Config, s *Snapshot) { s.Tasks[0].Body = "new scope" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c, s, n := policyFixture(t)
			mutate(&c, &s)
			if len(Select(c, s, State{}, n)) != 0 {
				t.Fatal("unbound task admitted")
			}
		})
	}
	c, s, n := policyFixture(t)
	c.Role = "planner"
	c.Self = c.Reviewer
	s.Events = []Event{{ID: "comment-1", Revision: "v1", URL: "https://github.com/orgs/foreign/discussions/1", Kind: "planning", Author: c.Owner}}
	if len(Select(c, s, State{}, n)) != 0 {
		t.Fatal("planning outside configured scope admitted")
	}
}

func TestConfigurationDurationsUseStrings(t *testing.T) {
	for _, raw := range []string{`{"poll_interval":60000000000}`, `{"poll_interval":"bad"}`, `{"unknown":"sentinel-secret"}`} {
		var limits Limits
		if err := json.Unmarshal([]byte(raw), &limits); err == nil || strings.Contains(err.Error(), "sentinel-secret") {
			t.Fatal("invalid duration config accepted or echoed")
		}
	}
}

func TestReviewCommentDoesNotEraseVerdictAndDismissalRequeues(t *testing.T) {
	c, s, n := policyFixture(t)
	p := PR{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, ChecksState: "success", MergeState: "CLEAN", Reviews: []Review{{ID: "r1", HeadSHA: strings.Repeat("a", 40), State: "APPROVED", Author: c.Reviewer, SubmittedAt: n.Add(-time.Minute)}, {ID: "r2", HeadSHA: strings.Repeat("a", 40), State: "COMMENTED", Author: c.Reviewer, SubmittedAt: n}}}
	s.PRs = []PR{p}
	if jobs := Select(c, s, State{}, n); len(jobs) != 1 || jobs[0].Kind != "deliver" {
		t.Fatal("ordinary comment erased approval")
	}
	s.PRs[0].Reviews[0].State = "DISMISSED"
	c.Role = "planner"
	c.Self = c.Reviewer
	if jobs := Select(c, s, State{}, n); len(jobs) != 1 || jobs[0].Kind != "review" {
		t.Fatal("dismissed review did not requeue")
	}
	first := Select(c, s, State{}, n)[0]
	s.PRs[0].Revision = "unrelated-comment-change"
	second := Select(c, s, State{}, n)[0]
	if first.ID != second.ID {
		t.Fatal("unchanged PR head produced a new review job")
	}
}
