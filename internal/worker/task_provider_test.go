package worker

import (
	"context"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const providerProject = "01K00000000000000000000000"
const providerIssue = "01K00000000000000000000001"

func TestNativeKataReferenceAndPRMetadata(t *testing.T) {
	ref, err := c.KataTaskReference(providerProject, providerIssue, "code/repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo, err := nativeTaskRepo(ref); err != nil || repo != "code/repo" {
		t.Fatalf("repo %q: %v", repo, err)
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
	reader := NewCachedNativeReader(cfg, live, f, RealClock{})
	if reader != live {
		t.Fatal("Kata discovery still uses GitHub task cache")
	}

}

func TestKataSourceDiscoveryDoesNotLimitSavedGitHubSnapshot(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	if nativeDiscoveryTask(cfg, job.TaskURL) {
		t.Fatal("unretained legacy task discovered")
	}
	// The capability transport is used even when validating retained GitHub work.
	installTaskProviderHTTP(t, &cfg, func(path string) any {
		if path != "/api/v1/tasks/capabilities" {
			t.Fatalf("unexpected provider request %s", path)
		}
		sha, _ := TeamPolicySHA(*cfg.Team)
		return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	})
	got, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job)
	if err != nil || len(got.Tasks) != 1 || got.TaskScope != job.TaskURL {
		t.Fatalf("saved GitHub validation failed: %#v %v", got, err)
	}
	cfg.TaskSource.RetainGitHubTasks = []string{job.TaskURL}
	if !nativeDiscoveryTask(cfg, job.TaskURL) {
		t.Fatal("retained legacy task omitted")
	}
}

func TestKataReadPinsScopePolicyAndDoneReason(t *testing.T) {
	for _, mutation := range []string{"open", "done", "canceled", "superseded", "policy", "project", "reference", "scope", "role", "state"} {
		t.Run(mutation, func(t *testing.T) {
			cfg, _, catalog, _ := dispatchFixture(t)
			cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
			task := catalog.catalog.Tasks[0]
			ref, _ := c.KataTaskReference(providerProject, providerIssue, task.Task.Repo)
			task.Binding.TaskURL = ref
			provider := c.ProviderTask{Reference: ref, ProjectUID: providerProject, IssueUID: providerIssue, Repository: task.Task.Repo, Title: task.Task.Title, Body: task.Task.Body, Revision: 7, Goal: task.Binding.Goal, Binding: &task.Binding, State: "open"}
			caps := c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: task.Binding.PolicySHA}
			switch mutation {
			case "done", "canceled", "superseded":
				provider.State = "closed"
				provider.ClosedReason = mutation
			case "policy":
				caps.PolicySHA = strings.Repeat("0", 64)
			case "project":
				caps.ProjectUID = providerIssue
			case "reference":
				provider.Reference += "/other"
			case "scope":
				provider.Body += "changed"
			case "role":
				provider.Binding.PeerID = provider.Binding.AuthorID
			case "state":
				provider.State = "unknown"
			}
			installTaskProviderHTTP(t, &cfg, func(path string) any {
				switch path {
				case "/api/v1/tasks/capabilities":
					return caps
				case "/api/v1/tasks/read":
					return provider
				default:
					t.Fatalf("unexpected request %s", path)
					return nil
				}
			})
			g := NewGitHubClient(&fakeCommands{respond: func(string, string, string, []byte) (CommandResult, error) {
				t.Fatal("Kata read invoked GitHub")
				return CommandResult{}, nil
			}})
			got, err := g.ReadNativeTask(t.Context(), cfg, ref)
			switch mutation {
			case "open", "done", "canceled", "superseded":
				if err != nil {
					t.Fatal(err)
				}
				if got.ProviderRevision != 7 || got.Task.Completed != (mutation == "done") || got.Task.Closed != (mutation != "open") {
					t.Fatalf("wrong provider mapping: %#v", got)
				}
			default:
				if err == nil {
					t.Fatal("invalid provider response accepted")
				}
			}
		})
	}
}

func TestKataPreparedCreationRecoveryKeepsConcreteActionClient(t *testing.T) {
	ref, _ := c.KataTaskReference(providerProject, providerIssue, "code/repo")
	if !nativePreparedAction(nativeActionRecord{Operation: "create-task", State: "prepared", Step: "create-task", Result: NativeActionResult{URL: ref}}) {
		t.Fatal("acknowledged Kata creation cannot recover")
	}
	for _, bad := range []string{"kata://bad", ref + "/extra"} {
		if nativePreparedAction(nativeActionRecord{Operation: "create-task", State: "prepared", Step: "create-task", Result: NativeActionResult{URL: bad}}) {
			t.Fatal("noncanonical creation recovered")
		}
	}
	cfg, _, _, _ := cachedFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	g := NewGitHubClient(&fakeCommands{})
	if nativeActionClient(NewCachedNativeReader(cfg, g, nil, nil)) != g {
		t.Fatal("concrete action client lost")
	}
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
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": cfg.Master.MasterID, "restore_epoch": 1, "data": handle(r)}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	path := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(token, []byte("provider-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", srv.URL, cfg.Master.MasterID, token)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Master.ClientConfig = path
}

func TestKataNativeEngineeringDeliveryAndRebind(t *testing.T) {
	testKataNativeEngineeringDeliveryAndRebind(t, false)
}
func TestKataNativeOfflineMergedDeliveryRecovery(t *testing.T) {
	testKataNativeEngineeringDeliveryAndRebind(t, true)
}
func testKataNativeEngineeringDeliveryAndRebind(t *testing.T, offline bool) {
	cfg, m, _, _ := dispatchFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	// Even with configured GitHub Projects, Kata creation needs no Project choice.
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}, {Owner: "code", Number: 2}}}).Projects
	cfg.ProjectOwners = nil
	transport := &nativeDeliveryTransport{t: t, head: strings.Repeat("a", 40)}
	transport.commands = &fakeCommands{respond: transport.response}
	var provider c.ProviderTask
	creates, binds, completes := 0, 0, 0
	installTaskProviderRequestHTTP(t, &cfg, func(r *http.Request) any {
		sha, _ := TeamPolicySHA(*cfg.Team)
		switch r.URL.Path {
		case "/api/v1/tasks/capabilities":
			return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
		case "/api/v1/tasks/read":
			var in c.TaskReadInput
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Reference != provider.Reference {
				t.Error("wrong read reference")
			}
			return provider
		case "/api/v1/tasks/create":
			var in c.TaskCreateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Goal != m.authority.Revision.Ref || in.ExpectedApprovalSequence != m.authority.ApprovalSequence || in.Repository != "code/repo" || in.RequestID == "" {
				t.Error("creation lost goal/repo/sequence/request")
			}
			creates++
			ref, _ := c.KataTaskReference(providerProject, providerIssue, in.Repository)
			b := NativeTaskBinding{Version: 3, Goal: in.Goal, TaskURL: ref, TaskScopeSHA: TaskScopeHash(in.Title, in.Body), PolicySHA: sha, ApprovalSequence: m.authority.ApprovalSequence, BaseBranch: in.BaseBranch, AuthorID: 12, PeerID: 11, QAID: 13, DependsOn: in.DependsOn}
			provider = c.ProviderTask{Reference: ref, ProjectUID: providerProject, IssueUID: providerIssue, Repository: in.Repository, Title: in.Title, Body: in.Body, State: "open", Revision: 1, Goal: in.Goal, Binding: &b}
			return provider
		case "/api/v1/tasks/bind":
			var in c.TaskBindInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.ExpectedRevision != provider.Revision || in.ExpectedAssignmentSHA != NativeAssignmentSHA(*provider.Binding) || in.RequestID == "" {
				t.Error("rebind lost exact CAS or request")
			}
			binds++
			provider.Binding = &in.Binding
			provider.Goal = in.Binding.Goal
			provider.Revision++
			return provider
		case "/api/v1/tasks/complete":
			var in c.TaskCompleteInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if !transport.merged || in.Reference != provider.Reference || in.AssignmentSHA != NativeAssignmentSHA(*provider.Binding) || in.PullRequestURL != "https://github.com/code/repo/pull/1" || in.RequestID == "" {
				t.Error("completion lost merged exact assignment or request")
			}
			completes++
			provider.State = "closed"
			provider.ClosedReason = "done"
			provider.Revision++
			return provider
		default:
			t.Errorf("unexpected provider route %s", r.URL.Path)
			return nil
		}
	})
	ctx := context.Background()
	g := NewGitHubClient(transport)
	stateRoot, workRoot := cfg.StateDir, cfg.WorkRoot
	runs := map[string]*NativeDispatcher{}
	models := map[string]*fakeOpenCode{}
	stage := func(index int, agent, kind string) NativeExecution {
		t.Helper()
		cfg.StateDir = filepath.Join(stateRoot, agent)
		cfg.WorkRoot = filepath.Join(workRoot, agent)
		cfg.Name = cfg.Team.Members[index].Name
		cfg.Self = cfg.Team.Members[index].Actor
		cfg.Master.AgentID = agent
		m.self.Principal.ID = agent
		m.self.Principal.GitHubBotID = cfg.Self.ID
		m.self.Placement.AgentID = agent
		saveNativeFixtureConnection(t, cfg)
		transport.cfg = cfg
		catalog, err := g.NativeSnapshot(ctx, cfg, m)
		if err != nil {
			t.Fatal(kind, err)
		}
		jobs := NativeCandidates(cfg, catalog, nil, time.Now())
		if len(jobs) != 1 || jobs[0].Job.Kind != kind {
			t.Fatalf("expected %s: %+v", kind, jobs)
		}
		v := jobs[0]
		x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Phase: "selected"}
		if v.Task != nil {
			x.Binding = &v.Task.Binding
			transport.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
		}
		code := &fakeOpenCode{running: true}
		d, err := NewNativeDispatcher(cfg, m, g, code, NewCheckouts(cfg, transport), RealClock{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.advance(ctx, &x); err != nil {
			t.Fatal(kind, err)
		}
		if code.calls != 1 {
			t.Fatal("native engineering dispatch missing")
		}
		runs[x.Job.ID] = d
		models[x.Job.ID] = code
		return x
	}
	finish := func(x NativeExecution) {
		t.Helper()
		models[x.Job.ID].running = false
		done, err := runs[x.Job.ID].advance(ctx, &x)
		if err != nil || !done || x.Phase != "completed" {
			t.Fatal("native terminal result missing", err, x.Phase)
		}
	}
	plan := stage(0, "peer", "plan")
	action := NativeAction{Operation: "create-task", RequestID: "kata-task", Repository: "code/repo", Title: "Task", Body: "scope", BaseBranch: "main"}
	result, err := RunNativeAction(ctx, cfg, m, g, plan.Job.ID, action)
	if err != nil {
		t.Fatal("create", err)
	}
	if _, err = RunNativeAction(ctx, cfg, m, g, plan.Job.ID, action); err != nil || creates != 1 {
		t.Fatal("creation replayed", err, creates)
	}
	if _, err = RunNativeAction(ctx, cfg, m, g, plan.Job.ID, NativeAction{Operation: "complete-plan", RequestID: "kata-planned"}); err != nil {
		t.Fatal(err)
	}
	finish(plan)
	m.authority.ApprovalSequence++
	renewed := stage(0, "peer", "plan")
	if _, err = RunNativeAction(ctx, cfg, m, g, renewed.Job.ID, NativeAction{Operation: "rebind-task", RequestID: "kata-rebind", TaskURL: result.URL, BaseBranch: "main"}); err != nil {
		t.Fatal("rebind", err)
	}
	if _, err = RunNativeAction(ctx, cfg, m, g, renewed.Job.ID, NativeAction{Operation: "complete-plan", RequestID: "kata-replanned"}); err != nil {
		t.Fatal(err)
	}
	finish(renewed)
	author := stage(1, "author", "implement")
	if _, err = RunNativeAction(ctx, cfg, m, g, author.Job.ID, NativeAction{Operation: "publish-pr", RequestID: "kata-pr", Title: "Implementation", Body: "Public change summary"}); err != nil {
		t.Fatal("publish", err)
	}
	finish(author)
	peer := stage(0, "peer", "review")
	if _, err = RunNativeAction(ctx, cfg, m, g, peer.Job.ID, NativeAction{Operation: "review", RequestID: "kata-review", Verdict: "approved", Body: "Exact head checked"}); err != nil {
		t.Fatal("review", err)
	}
	finish(peer)
	qa := stage(2, "qa", "qa")
	if _, err = RunNativeAction(ctx, cfg, m, g, qa.Job.ID, NativeAction{Operation: "qa", RequestID: "kata-qa", Verdict: "approved", ReportBody: "Private acceptance evidence"}); err != nil {
		t.Fatal("qa", err)
	}
	finish(qa)
	delivery := stage(1, "author", "deliver")

	action = NativeAction{Operation: "deliver", RequestID: "kata-deliver"}
	if offline {
		// The native model ended after the merge acknowledgement was journaled.
		// Recovery must finish Kata completion without submitting another merge.
		transport.merged = true
		delivery.Phase, delivery.Reason = "blocked", "native_completed_without_result"
		delivery.Job.Phase, delivery.Job.SubmissionState = "completed", "completed"
		delivery.Job.TerminalAt = time.Now().UTC()
		if err = saveNativeExecution(cfg, delivery); err != nil {
			t.Fatal(err)
		}
		if err = persistNativeActionIntent(cfg, delivery, action); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(action)
		if err = saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: delivery.Job.ID, RequestID: action.RequestID, InputSHA: BodyHash(string(raw)), Operation: "deliver", State: "prepared", Step: "merge"}); err != nil {
			t.Fatal(err)
		}
		models[delivery.Job.ID].running = false
		if _, err = RecoverNativeDelivery(ctx, cfg, m, g, models[delivery.Job.ID], delivery.Job.ID, delivery.Job.SessionID, delivery.Job.MessageID, action); err != nil {
			t.Fatal("offline Kata completion", err)
		}
		saved, e := loadNativeExecution(cfg, delivery.Job.ID)
		if e != nil || saved.Phase != "completed" {
			t.Fatal("offline recovery did not finish", e)
		}
		for _, write := range transport.writes {
			if strings.Contains(write, "/merge") {
				t.Fatal("offline recovery replayed acknowledged merge")
			}
		}
	} else {
		if _, err = RunNativeAction(ctx, cfg, m, g, delivery.Job.ID, action); err != nil {
			t.Fatal("delivery", err)
		}
		finish(delivery)
	}
	if creates != 1 || binds != 1 || completes != 1 || !transport.merged || provider.ClosedReason != "done" {
		t.Fatalf("incomplete provider delivery %d %d %d %+v", creates, binds, completes, provider)
	}
	for _, write := range transport.writes {
		if strings.Contains(write, "/issues") {
			t.Fatalf("Kata execution wrote GitHub task %s", write)
		}
	}
}

func TestKataSourceConfigValidation(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject, RetainGitHubTasks: []string{"https://github.com/code/repo/issues/1"}}
	if err := validateTaskSource(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Version = 2 },
		func(c *Config) { c.Master = nil },
		func(c *Config) { c.TaskSource.Provider = "github" },
		func(c *Config) { c.TaskSource.ProjectUID = strings.ToLower(providerProject) },
		func(c *Config) { c.TaskSource.RetainGitHubTasks = []string{"https://github.com/foreign/repo/issues/1"} },
		func(c *Config) { c.TaskSource.RetainGitHubTasks = []string{"https://github.com/code/repo/pull/1"} },
		func(c *Config) {
			c.TaskSource.RetainGitHubTasks = append(c.TaskSource.RetainGitHubTasks, c.TaskSource.RetainGitHubTasks[0])
		},
	} {
		invalid := Defaults(cfg)
		change(&invalid)
		if err := validateTaskSource(invalid); err == nil {
			t.Fatal("invalid task source accepted")
		}
	}
	if len(cfg.TaskSource.RetainGitHubTasks) != 1 {
		t.Fatal("defaults mutated original source configuration")
	}
}

func TestKataPlanningClientAllowsCoordinatorRosterWithoutWeakeningEngineering(t *testing.T) {
	cfg, _, _, _ := dispatchFixture(t)
	sha, _ := TeamPolicySHA(*cfg.Team)
	cfg.Team.Members = append(cfg.Team.Members, TeamMember{Name: "coordinator", Actor: Actor{ID: 99, Login: "coordinator[bot]", Kind: "Bot"}})
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	caps := c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	installTaskProviderHTTP(t, &cfg, func(path string) any {
		if path != "/api/v1/tasks/capabilities" {
			t.Errorf("unexpected planning request %s", path)
		}
		return caps
	})
	if _, err := TaskPlanningClient(t.Context(), cfg); err != nil {
		t.Fatal("coordinator planning required engineering roster hash", err)
	}
	if _, err := TaskProviderClient(t.Context(), cfg); err == nil {
		t.Fatal("engineering accepted coordinator roster mismatch")
	}
	for _, change := range []func(){
		func() { caps.PolicySHA = "" },
		func() { caps.PolicySHA = strings.Repeat("g", 64) },
		func() { caps.PolicySHA = sha; caps.ProjectUID = providerIssue },
		func() { caps.ProjectUID = providerProject; caps.Provider = "github" },
	} {
		change()
		if _, err := TaskPlanningClient(t.Context(), cfg); err == nil {
			t.Fatal("planning accepted invalid capability identity or policy hash")
		}
	}
}

func TestKataCoordinatorSkipsDiscoveryButRetainedHistoryKeepsStrictPolicy(t *testing.T) {
	d, _, _ := coordinatorDispatcher(t)
	d.config.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	canonical := *d.config.Team
	canonical.Members = canonical.Members[:len(canonical.Members)-1]
	sha, _ := TeamPolicySHA(canonical)
	calls := 0
	installTaskProviderHTTP(t, &d.config, func(path string) any {
		calls++
		return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	})
	d.github = NewGitHubClient(&fakeCommands{respond: func(string, string, string, []byte) (CommandResult, error) {
		t.Error("coordinator queried GitHub engineering catalog")
		return CommandResult{}, nil
	}})
	if d.Ready() {
		t.Fatal("coordinator became engineering-ready")
	}
	if preview, err := d.Preview(t.Context()); err != nil || len(preview.Jobs) != 0 {
		t.Fatal("coordinator preflight scanned engineering catalog", err)
	}
	if calls != 0 {
		t.Fatal("coordinator checked engineering provider without history")
	}
	d.history["retained"] = NativeExecution{Phase: "completed"}
	if _, err := d.Preview(t.Context()); err == nil {
		t.Fatal("retained engineering history bypassed strict roster policy")
	}
	if calls == 0 {
		t.Fatal("retained engineering history skipped provider pin")
	}
}

// A native issue, even with plausible assignment metadata, cannot enter the
// engineering catalog without a current registered task artifact.
func TestNativeCreatedTicketCannotExecuteWithoutAdmission(t *testing.T) {
	cfg, authority, runner, _, _ := taskSnapshotFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	authority.links = nil
	reads := 0
	sha, _ := TeamPolicySHA(*cfg.Team)
	installTaskProviderHTTP(t, &cfg, func(path string) any {
		if path != "/api/v1/tasks/capabilities" {
			reads++
			t.Errorf("unregistered issue read %s", path)
		}
		return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	})
	catalog, err := NewGitHubClient(runner).NativeSnapshot(t.Context(), cfg, authority)
	if err != nil || !catalog.Complete || len(catalog.Tasks) != 0 || reads != 0 {
		t.Fatalf("unregistered native intake in engineering catalog %+v reads=%d err=%v", catalog, reads, err)
	}
	for _, candidate := range NativeCandidates(cfg, catalog, nil, time.Now()) {
		if candidate.Job.Kind != "plan" {
			t.Fatal("native intake executed", candidate.Job.Kind)
		}
	}
}
