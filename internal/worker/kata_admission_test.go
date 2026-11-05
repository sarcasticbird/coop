package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type kataAdmissionMaster struct {
	*dispatchMaster
	t *testing.T
}

func (m *kataAdmissionMaster) ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error) {
	m.t.Error("Kata execution resolved a private goal")
	return c.GoalAuthority{}, c.ErrUnsupported
}
func (m *kataAdmissionMaster) ApprovedGoals(context.Context, string, string) (c.GoalPage, error) {
	m.t.Error("Kata discovery queried private goals")
	return c.GoalPage{}, c.ErrUnsupported
}
func (m *kataAdmissionMaster) Artifacts(context.Context, c.ArtifactQuery) (c.ArtifactPage, error) {
	m.t.Error("Kata discovery queried private artifacts")
	return c.ArtifactPage{}, c.ErrUnsupported
}
func (m *kataAdmissionMaster) RegisterArtifact(context.Context, c.ArtifactLink) (c.ArtifactLink, error) {
	m.t.Error("Kata execution registered private artifacts")
	return c.ArtifactLink{}, c.ErrUnsupported
}

type kataAdmissionFixture struct {
	t                   *testing.T
	cfg                 Config
	m                   *kataAdmissionMaster
	g                   *GitHubClient
	transport           *nativeDeliveryTransport
	task                c.ProviderTask
	extraTasks          []c.ProviderTask
	stateRoot, workRoot string
	updates             []c.TaskUpdateInput
	fault               string
	runs                map[string]*NativeDispatcher
	models              map[string]*fakeOpenCode
}

func kataAdmissionSetup(t *testing.T, human bool) *kataAdmissionFixture {
	t.Helper()
	cfg, master, _, _ := dispatchFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	f := &kataAdmissionFixture{t: t, cfg: cfg, m: &kataAdmissionMaster{master, t}, runs: map[string]*NativeDispatcher{}, models: map[string]*fakeOpenCode{}, stateRoot: cfg.StateDir, workRoot: cfg.WorkRoot}
	f.task = c.ProviderTask{Reference: providerIssue, ProjectUID: providerProject, IssueUID: providerIssue, Title: "Bounded change", Body: "Repository: https://github.com/code/repo\nAcceptance: tests pass and preserve work.", Owner: cfg.Team.Members[1].Name, State: "open", Revision: 1}
	f.transport = &nativeDeliveryTransport{t: t, head: strings.Repeat("a", 40), branch: "existing-human-branch"}
	if human {
		f.task.Body += "\nPR: https://github.com/code/repo/pull/1"
		f.transport.prAuthor = Actor{ID: 99, Login: "actual-human", Kind: "User"}
		f.transport.prBody = "Human-authored source. No Coop metadata."
	}
	f.transport.commands = &fakeCommands{respond: f.transport.response}
	f.g = NewGitHubClient(f.transport)
	installTaskProviderRequestHTTP(t, &f.cfg, func(r *http.Request) any {
		sha, _ := TeamPolicySHA(*f.cfg.Team)
		switch r.URL.Path {
		case "/api/v1/tasks/capabilities":
			return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
		case "/api/v1/tasks/list":
			return c.ProviderTaskPage{Tasks: append([]c.ProviderTask{f.task}, f.extraTasks...)}
		case "/api/v1/tasks/read":
			var in c.TaskReadInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Reference != providerIssue {
				t.Error("read must use the native issue UID", in, err)
			}
			return f.task
		case "/api/v1/tasks/update":
			var in c.TaskUpdateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.ExpectedRevision != f.task.Revision || in.RequestID == "" {
				t.Error("task mutation lost revision or request identity", in)
			}
			if err := in.Validate(); err != nil {
				t.Error(err)
			}
			f.updates = append(f.updates, in)
			if in.Operation == "close" {
				if !f.transport.merged {
					t.Error("unmerged task closure")
				}
				if f.fault == "close-before-write" {
					return taskProviderFailure{503, map[string]string{"error": "unavailable"}}
				}
				f.task.State, f.task.ClosedReason = "closed", in.Reason
			} else if in.Operation == "edit" && in.Body != nil {
				f.task.Body = *in.Body
			} else {
				t.Error("unexpected task operation", in.Operation)
			}
			f.task.Revision++
			if f.fault == "close-after-write" && in.Operation == "close" {
				return taskProviderFailure{503, map[string]string{"error": "unavailable"}}
			}
			return f.task
		default:
			t.Error("unexpected provider route", r.URL.Path)
			return nil
		}
	})
	f.actor(1)
	return f
}

func (f *kataAdmissionFixture) actor(index int) {
	m := f.cfg.Team.Members[index]
	f.cfg.Name, f.cfg.Self, f.cfg.Master.AgentID = m.Name, m.Actor, m.Name
	f.cfg.StateDir, f.cfg.WorkRoot = filepath.Join(f.stateRoot, m.Name), filepath.Join(f.workRoot, m.Name)
	f.m.self.Principal.ID, f.m.self.Principal.GitHubBotID = m.Name, m.Actor.ID
	f.m.self.Principal.Capabilities = append([]string{"read", "send"}, m.Capabilities...)
	f.m.self.Placement.AgentID = m.Name
	saveNativeFixtureConnection(f.t, f.cfg)
	f.transport.cfg = f.cfg
}

func (f *kataAdmissionFixture) catalog() NativeCatalog {
	f.t.Helper()
	catalog, err := f.g.NativeSnapshot(f.t.Context(), f.cfg, f.m)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(catalog.Goals) != 0 {
		f.t.Fatal("Kata catalog manufactured a goal")
	}
	return catalog
}

func (f *kataAdmissionFixture) stage(index int, kind string) NativeExecution {
	f.t.Helper()
	f.actor(index)
	catalog := f.catalog()
	candidates := NativeCandidates(f.cfg, catalog, nil, time.Now())
	if len(candidates) != 1 || candidates[0].Job.Kind != kind {
		f.t.Fatalf("expected %s, got %+v", kind, candidates)
	}
	v := candidates[0]
	x := NativeExecution{Version: 3, MasterID: f.cfg.Master.MasterID, InstanceID: f.cfg.Master.InstanceID, Generation: f.cfg.Master.Generation, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	code := &fakeOpenCode{running: true}
	d, err := NewNativeDispatcher(f.cfg, f.m, f.g, code, NewCheckouts(f.cfg, f.transport), RealClock{})
	if err != nil {
		f.t.Fatal(err)
	}
	if kind == "implement" {
		f.transport.branch = "agent/" + f.cfg.Name + "/" + BindingKey(f.cfg, x.Job)[:16]
	}
	f.transport.checkoutBranch = "agent/" + f.cfg.Name + "/" + BindingKey(f.cfg, x.Job)[:16]
	if x.Job.Branch != "" {
		f.transport.checkoutBranch = x.Job.Branch
	}
	if _, err = d.advance(f.t.Context(), &x); err != nil {
		f.t.Fatal("dispatch", kind, err)
	}
	if code.calls != 1 || x.Phase != "active" {
		f.t.Fatal("no actual dispatch", x.Phase)
	}
	prompt, err := nativePrompt(f.cfg, x, v)
	if err != nil || strings.Contains(prompt, "approved native goal") {
		f.t.Fatal("stale goal prompt", err)
	}
	f.runs[x.Job.ID], f.models[x.Job.ID] = d, code
	return x
}

func (f *kataAdmissionFixture) action(x NativeExecution, in NativeAction) error {
	_, err := RunNativeAction(f.t.Context(), f.cfg, f.m, f.g, x.Job.ID, in)
	return err
}

func (f *kataAdmissionFixture) finish(x NativeExecution) {
	f.t.Helper()
	f.models[x.Job.ID].running = false
	done, err := f.runs[x.Job.ID].advance(f.t.Context(), &x)
	if err != nil || !done || x.Phase != "completed" {
		f.t.Fatal("stage did not finish", err, x.Phase)
	}
}

func (f *kataAdmissionFixture) clearPR() {
	f.t.Helper()
	peer := 0
	for i, m := range f.cfg.Team.Members {
		if m.Actor.ID == f.catalog().Tasks[0].Binding.PeerID {
			peer = i
		}
	}
	for _, stage := range []struct {
		index int
		kind  string
	}{{peer, "review"}, {2, "qa"}} {
		x := f.stage(stage.index, stage.kind)
		in := NativeAction{Operation: stage.kind, RequestID: stage.kind, Verdict: "approved", Body: "Independent current-head review", ReportBody: "Ran the task acceptance tests on the exact source artifact"}
		if err := f.action(x, in); err != nil {
			f.t.Fatal(stage.kind, err)
		}
		f.finish(x)
	}
}

func TestKataOrdinaryDeliveryUsesNoPrivateGoalOrBindingWrites(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-implementation", true: "existing-human-PR"}[human], func(t *testing.T) {
			f := kataAdmissionSetup(t, human)
			if human {
				f.task.Owner = "rook"
			} else {
				x := f.stage(1, "implement")
				if err := f.action(x, NativeAction{Operation: "publish-pr", RequestID: "publish", Title: "Change", Body: "Implemented task"}); err != nil {
					t.Fatal(err)
				}
				f.finish(x)
			}
			f.clearPR()
			x := f.stage(3, "deliver")
			in := NativeAction{Operation: "deliver", RequestID: "deliver"}
			if err := f.action(x, in); err != nil {
				t.Fatal(err)
			}
			if err := f.action(x, in); err != nil {
				t.Fatal("completed action retry", err)
			}
			f.finish(x)
			if f.task.ClosedReason != "done" || !f.transport.merged {
				t.Fatal("task not delivered")
			}
			merges := 0
			for _, write := range f.transport.writes {
				if strings.Contains(write, "/merge") {
					merges++
				}
				if strings.Contains(write, "/issues") {
					t.Fatal("GitHub task mutation", write)
				}
			}
			if merges != 1 {
				t.Fatal("merge replayed", merges)
			}
			if human && (f.transport.prAuthor.Login != "actual-human" || strings.Contains(f.transport.prBody, "coop-pr:")) {
				t.Fatal("human PR authorship/metadata changed")
			}
		})
	}
}

func TestKataCurrentTaskAndPRDenyUnsafeDelivery(t *testing.T) {
	for _, change := range []string{"head", "owner", "body", "restored-task-scope", "hold", "checks", "base", "peer-dismissed", "qa-dismissed", "rook-contributor", "revoked", "github-identity", "epoch"} {
		t.Run(change, func(t *testing.T) {
			f := kataAdmissionSetup(t, true)
			f.clearPR()
			x := f.stage(3, "deliver")
			before := len(f.transport.writes)
			switch change {
			case "head":
				f.transport.head = strings.Repeat("b", 40)
			case "owner":
				f.task.Owner = "rook"
			case "body":
				f.task.Body += "\nChanged acceptance"
			case "restored-task-scope":
				f.task.Revision += 2 // An edit followed by restoration is still newer.
			case "hold":
				f.task.Body += "\nStop before merge"
			case "checks":
				f.transport.checks = "FAILURE"
			case "base":
				f.transport.mergeState = "DIRTY"
			case "peer-dismissed":
				f.transport.reviews[0]["state"] = "DISMISSED"
			case "qa-dismissed":
				f.transport.reviews[1]["state"] = "DISMISSED"
			case "rook-contributor":
				f.transport.prAuthor = f.cfg.Self
			case "revoked":
				f.m.self.Principal.Capabilities = []string{"read", "send"}
			case "github-identity":
				f.transport.cfg.Self = f.cfg.Team.Members[0].Actor
			case "epoch":
				f.m.self.RestoreEpoch++
			}
			if err := f.action(x, NativeAction{Operation: "deliver", RequestID: "denied"}); err == nil {
				t.Fatal("unsafe merge admitted")
			}
			if len(f.transport.writes) != before || len(f.updates) != 0 {
				t.Fatal("denial mutated external state", f.transport.writes, f.updates)
			}
		})
	}
}

func TestKataReviewCannotApproveItsActualContribution(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.actor(0)
	f.transport.prAuthor = f.cfg.Self
	if jobs := NativeCandidates(f.cfg, f.catalog(), nil, time.Now()); len(jobs) != 0 {
		t.Fatal("PR author admitted as reviewer", jobs)
	}
}

func TestKataPreparedReviewRevalidatesTaskAndLiveRole(t *testing.T) {
	for _, change := range []string{"owner", "scope", "role", "head"} {
		t.Run(change, func(t *testing.T) {
			f := kataAdmissionSetup(t, true)
			x := f.stage(0, "review")
			switch change {
			case "owner":
				f.task.Owner = "rook"
			case "scope":
				f.task.Body += "\nNew scope"
			case "role":
				f.m.self.Principal.Capabilities = []string{"read", "send"}
			case "head":
				f.transport.head = strings.Repeat("c", 40)
			}
			if err := f.action(x, NativeAction{Operation: "review", RequestID: "denied", Verdict: "approved", Body: "review"}); err == nil {
				t.Fatal("stale review admitted")
			}
			if len(f.transport.writes) != 0 {
				t.Fatal("review published after denial")
			}
		})
	}
}

func TestKataRestartObservesExistingTurnWithoutResubmission(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	x := f.stage(0, "review")
	code := f.models[x.Job.ID]
	restarted, err := NewNativeDispatcher(f.cfg, f.m, f.g, code, NewCheckouts(f.cfg, f.transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.active == nil || restarted.active.Job.SessionID != x.Job.SessionID || restarted.active.Job.MessageID != x.Job.MessageID {
		t.Fatal("restart lost native ownership")
	}
	if _, err = restarted.advance(t.Context(), restarted.active); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("restart resubmitted the model")
	}
}

func TestKataUnknownWriteRemainsUncertainAfterRestart(t *testing.T) {
	for _, fault := range []string{"merge-after-write", "close-before-write", "close-after-write"} {
		t.Run(fault, func(t *testing.T) {
			f := kataAdmissionSetup(t, true)
			f.clearPR()
			x := f.stage(3, "deliver")
			f.fault = fault
			if fault == "merge-after-write" {
				f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
					result, err := f.transport.response(owner, endpoint, method, body)
					if strings.HasSuffix(endpoint, "/merge") && method == "PUT" {
						return response("503 Service Unavailable", `{}`), nil
					}
					return result, err
				}
			}
			in := NativeAction{Operation: "deliver", RequestID: "original-write"}
			if err := f.action(x, in); err == nil {
				t.Fatal("fault not exercised")
			}
			writes, updates := len(f.transport.writes), len(f.updates)
			f.models[x.Job.ID].running = false
			if _, err := f.runs[x.Job.ID].advance(t.Context(), &x); err != nil || x.Phase != "blocked" {
				t.Fatal("terminal uncertainty not retained", err, x.Phase)
			}
			restarted, err := NewNativeDispatcher(f.cfg, f.m, f.g, f.models[x.Job.ID], NewCheckouts(f.cfg, f.transport), RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			if restarted.held {
				t.Fatal("finished task froze the coworker")
			}
			if _, err = RecoverNativeDelivery(t.Context(), f.cfg, f.m, f.g, f.models[x.Job.ID], x.Job.ID, x.Job.SessionID, x.Job.MessageID, in); err == nil {
				t.Fatal("uncertain write replayed")
			}
			if len(f.transport.writes) != writes || len(f.updates) != updates || f.models[x.Job.ID].calls != 1 {
				t.Fatal("restart duplicated an effect or model turn")
			}
			records, err := loadNativeJobActions(f.cfg, x)
			if err != nil || len(records) != 1 || records[0].State != "uncertain" {
				t.Fatal("uncertainty cleared", err, records)
			}
		})
	}
}

func TestKataAcknowledgedMergeRecoveryFinishesSameTaskWithoutReplay(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.clearPR()
	x := f.stage(3, "deliver")
	f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		result, err := f.transport.response(owner, endpoint, method, body)
		if strings.HasSuffix(endpoint, "/merge") && method == "PUT" {
			f.m.self.Principal.Capabilities = []string{"read", "send"}
		}
		return result, err
	}
	in := NativeAction{Operation: "deliver", RequestID: "acknowledged-merge"}
	if err := f.action(x, in); err == nil || !f.transport.merged || len(f.updates) != 0 {
		t.Fatal("grant revocation was not honored", err)
	}
	f.models[x.Job.ID].running = false
	if _, err := f.runs[x.Job.ID].advance(t.Context(), &x); err != nil || x.Phase != "blocked" {
		t.Fatal(err, x.Phase)
	}
	f.m.self.Principal.Capabilities = []string{"read", "send", "deliver"}
	if _, err := RecoverNativeDelivery(t.Context(), f.cfg, f.m, f.g, f.models[x.Job.ID], x.Job.ID, x.Job.SessionID, x.Job.MessageID, in); err != nil {
		t.Fatal(err)
	}
	merges := 0
	for _, write := range f.transport.writes {
		if strings.Contains(write, "/merge") {
			merges++
		}
	}
	if merges != 1 || len(f.updates) != 1 || f.task.ClosedReason != "done" || f.models[x.Job.ID].calls != 1 {
		t.Fatal("recovery replayed or failed to finish")
	}
}

func TestKataAdmissionNeedsAssignmentCodeScopeAndPinnedProject(t *testing.T) {
	for _, change := range []string{"unassigned", "nonengineering", "no-repository", "foreign-repository", "foreign-project", "closed", "unknown-state"} {
		t.Run(change, func(t *testing.T) {
			f := kataAdmissionSetup(t, false)
			f.actor(1)
			switch change {
			case "unassigned":
				f.task.Owner = ""
			case "nonengineering":
				f.task.Owner = "qa"
			case "no-repository":
				f.task.Body = "No code scope"
			case "foreign-repository":
				f.task.Body = "https://github.com/foreign/repo"
			case "foreign-project":
				f.task.ProjectUID = providerIssue
			case "closed":
				f.task.State = "closed"
			case "unknown-state":
				f.task.State = "unknown"
			}
			catalog, err := f.g.NativeSnapshot(t.Context(), f.cfg, f.m)
			if err == nil && len(NativeCandidates(f.cfg, catalog, nil, time.Now())) != 0 {
				t.Fatal("unscoped task admitted")
			}
		})
	}
}

func (f *kataAdmissionFixture) stackTransport(merged map[int]bool, authors map[int]Actor) {
	f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		number := 1
		if strings.Contains(endpoint, "/pulls/2") {
			number = 2
		}
		if endpoint == "graphql" {
			var request struct {
				Variables struct {
					Number int
					ID     string
				}
			}
			if err := json.Unmarshal(body, &request); err != nil {
				f.t.Fatal(err)
			}
			if request.Variables.Number == 2 || request.Variables.ID == "commit2" {
				number = 2
			}
		}
		if authors != nil {
			f.transport.prAuthor = authors[number]
			for _, actor := range authors {
				if endpoint == "user/"+stringID(int(actor.ID)) {
					return jsonResponse(map[string]any{"id": actor.ID, "login": actor.Login, "type": actor.Kind}), nil
				}
			}
		}
		f.transport.merged = merged[number]
		result, err := f.transport.response(owner, strings.ReplaceAll(endpoint, "/pulls/2", "/pulls/1"), method, body)
		if method == "PUT" && strings.HasSuffix(endpoint, "/merge") {
			merged[number] = f.transport.merged
		}
		if number == 2 {
			result.Stdout = []byte(strings.ReplaceAll(string(result.Stdout), "/pull/1", "/pull/2"))
			result.Stdout = []byte(strings.ReplaceAll(string(result.Stdout), `"id":"commit"`, `"id":"commit2"`))
		}
		return result, err
	}
}

func TestKataLinkedPRStackKeepsTaskOpenUntilLastMerge(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-human", true: "different-bot-contributors"}[distinct], func(t *testing.T) {
			f := kataAdmissionSetup(t, true)
			f.task.Body += "\nNext PR: https://github.com/code/repo/pull/2"
			merged := map[int]bool{}
			var authors map[int]Actor
			if distinct {
				f.task.Owner = "rook"
				authors = map[int]Actor{1: f.cfg.Team.Members[1].Actor, 2: f.cfg.Team.Members[0].Actor}
			}
			f.stackTransport(merged, authors)
			for _, number := range []int{1, 2} {
				f.clearPR()
				x := f.stage(3, "deliver")
				if !strings.HasSuffix(x.Job.PRURL, stringID(number)) {
					t.Fatal("stack reordered", x.Job.PRURL)
				}
				if err := f.action(x, NativeAction{Operation: "deliver", RequestID: "deliver-stack"}); err != nil {
					t.Fatal(err)
				}
				f.finish(x)
				if (f.task.State == "closed") != (number == 2) {
					t.Fatal("task closed before all PRs merged", number, f.task.State)
				}
			}
			if len(f.updates) != 1 || !merged[1] || !merged[2] {
				t.Fatal("stack did not finish once", f.updates, merged)
			}
		})
	}
}

func TestKataEarlierMergedStackMemberNeedsActualDeliveryReceipt(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.task.Body += "\nNext PR: https://github.com/code/repo/pull/2"
	merged := map[int]bool{1: true}
	f.stackTransport(merged, nil)
	f.clearPR()
	x := f.stage(3, "deliver")
	if !strings.HasSuffix(x.Job.PRURL, "/2") {
		t.Fatal("wrong current PR")
	}
	if err := f.action(x, NativeAction{Operation: "deliver", RequestID: "missing-earlier-clearance"}); err == nil {
		t.Fatal("historical merge was treated as earlier review/QA")
	}
	if f.task.State != "open" || len(f.updates) != 0 {
		t.Fatal("task falsely completed")
	}
	if !merged[2] {
		t.Fatal("test did not exercise the acknowledged current merge")
	}
}

func TestKataTimedOutTurnContinuesSavedSessionWithoutGoalApproval(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	x := f.stage(0, "review")
	original := x.Job
	x.Phase, x.Reason = "blocked", "native_turn_limit"
	x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now().UTC()
	if err := saveNativeExecution(f.cfg, x); err != nil {
		t.Fatal(err)
	}
	code := f.models[x.Job.ID]
	code.running = false
	d, err := NewNativeDispatcher(f.cfg, f.m, f.g, code, NewCheckouts(f.cfg, f.transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog = f.catalog()
	if d.resumableBlocked() == nil {
		t.Fatal("task-native timeout was stranded without a goal")
	}
	if _, err = d.resumeBlocked(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	if code.calls != 2 || x.Job.SessionID != original.SessionID || x.Job.MessageID == original.MessageID || len(x.Turns) != 1 {
		t.Fatal("continuation lost the saved session or old turn")
	}
}

func TestKataRookOwnedBotPRSelectsActualIndependentPeer(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.task.Owner = "rook"
	f.transport.prAuthor = f.cfg.Team.Members[1].Actor
	f.actor(0)
	catalog := f.catalog()
	if catalog.Tasks[0].Binding.PeerID != f.cfg.Self.ID {
		t.Fatal("bot PR was stranded on its contributor reviewer")
	}
	x := f.stage(0, "review")
	if err := f.action(x, NativeAction{Operation: "review", RequestID: "actual-peer", Verdict: "approved", Body: "Independent review"}); err != nil {
		t.Fatal(err)
	}
}

func TestKataBadPRLinkDoesNotBlockUnrelatedTask(t *testing.T) {
	f := kataAdmissionSetup(t, false)
	bad := f.task
	bad.Reference, bad.IssueUID = "01K00000000000000000000002", "01K00000000000000000000002"
	bad.Body += "\nhttps://github.com/code/repo/pull/9"
	f.extraTasks = []c.ProviderTask{bad}
	f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo/pulls/9" {
			return response("404 Not Found", `{}`), nil
		}
		return f.transport.response(owner, endpoint, method, body)
	}
	f.actor(1)
	catalog := f.catalog()
	jobs := NativeCandidates(f.cfg, catalog, nil, time.Now())
	if len(catalog.Tasks) != 1 || len(jobs) != 1 || jobs[0].Job.Kind != "implement" {
		t.Fatal("bad task froze unrelated work", catalog, jobs)
	}
	ref, _ := c.KataTaskReference(providerProject, bad.IssueUID, "code/repo")
	if _, err := f.g.kataTask(t.Context(), f.cfg, bad); err == nil {
		t.Fatal("targeted bad PR was accepted", ref)
	}
}

func TestKataMergedLinkCannotInventReviewQAOrCompletion(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.actor(3)
	f.transport.merged = true
	if jobs := NativeCandidates(f.cfg, f.catalog(), nil, time.Now()); len(jobs) != 0 {
		t.Fatal("already-merged link manufactured delivery", jobs)
	}
	f.transport.merged = false
	f.clearPR()
	x := f.stage(3, "deliver")
	before := len(f.transport.writes)
	f.transport.merged = true // Somebody else merged after this run was selected.
	f.transport.reviews = nil
	if err := f.action(x, NativeAction{Operation: "deliver", RequestID: "external-merge"}); err == nil {
		t.Fatal("external merge bypassed current clearance")
	}
	if len(f.transport.writes) != before || len(f.updates) != 0 {
		t.Fatal("merged link silently completed Kata")
	}
}

func TestKataFixesForDifferentPRsHaveSeparateWorkspaces(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	f.actor(1)
	f.transport.checks = "FAILURE"
	jobs := NativeCandidates(f.cfg, f.catalog(), nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "fix" {
		t.Fatal("missing fix")
	}
	first, second := jobs[0].Job, jobs[0].Job
	second.PRURL, second.Branch = "https://github.com/code/repo/pull/2", "other-existing-branch"
	if BindingKey(f.cfg, first) == BindingKey(f.cfg, second) {
		t.Fatal("two PR fixes share a checkout binding")
	}
	checkouts := NewCheckouts(f.cfg, f.transport)
	f.transport.checkoutBranch = first.Branch
	a, err := checkouts.Prepare(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	f.transport.checkoutBranch = second.Branch
	b, err := checkouts.Prepare(t.Context(), second)
	if err != nil {
		t.Fatal("second PR collided with first checkout", err)
	}
	if a.Checkout == b.Checkout {
		t.Fatal("two PR fixes share a workspace")
	}
}

func TestKataAcknowledgedPRLinkFinalizesReadOnlyAfterRestart(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "later-scope-change"}[changed], func(t *testing.T) {
			f := kataAdmissionSetup(t, false)
			x := f.stage(1, "implement")
			in := NativeAction{Operation: "publish-pr", RequestID: "publish-link", Title: "Change", Body: "Implemented task"}
			if err := f.action(x, in); err != nil {
				t.Fatal(err)
			}
			records, err := loadNativeJobActions(f.cfg, x)
			if err != nil || len(records) != 1 {
				t.Fatal(err)
			}
			r := records[0]
			r.State, r.Result.Phase = "prepared", ""
			if r.Step != "task-pr-linked" {
				t.Fatal("missing link acknowledgement", r.Step)
			}
			if err := saveNativeAction(f.cfg, r); err != nil {
				t.Fatal(err)
			}
			code := f.models[x.Job.ID]
			code.running = false
			if _, err := f.runs[x.Job.ID].advance(t.Context(), &x); err != nil || x.Phase != "blocked" {
				t.Fatal(err, x.Phase)
			}
			writes, updates := len(f.transport.writes), len(f.updates)
			if changed {
				f.task.Title += " new scope"
			}
			d, err := NewNativeDispatcher(f.cfg, f.m, f.g, code, NewCheckouts(f.cfg, f.transport), RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			done, err := d.reconcileNativeAction(t.Context(), &x)
			if changed {
				if err == nil || done {
					t.Fatal("later scope accepted as the old acknowledgement")
				}
			} else if err != nil || !done {
				t.Fatal("acknowledged link stranded", err)
			}
			if len(f.transport.writes) != writes || len(f.updates) != updates || code.calls != 1 {
				t.Fatal("link recovery repeated an external effect or model")
			}
		})
	}
}

func TestKataFinalPublicationRetainsOtherRefusalsAndUncertainty(t *testing.T) {
	for _, mode := range []string{"acknowledged-link", "completed-receipt", "uncertain-sibling"} {
		t.Run(mode, func(t *testing.T) {
			f := kataAdmissionSetup(t, false)
			x := f.stage(1, "implement")
			bad := NativeAction{Operation: "publish-pr", RequestID: "00-local-refusal", Body: "No title supplied"}
			if err := f.action(x, bad); err == nil {
				t.Fatal("invalid publication accepted")
			}
			good := NativeAction{Operation: "publish-pr", RequestID: "corrected-publication", Title: "Change", Body: "Implemented task"}
			if err := f.action(x, good); err != nil {
				t.Fatal(err)
			}
			records, err := loadNativeJobActions(f.cfg, x)
			if err != nil || len(records) != 2 {
				t.Fatal(err, records)
			}
			for i := range records {
				r := &records[i]
				if r.RequestID == good.RequestID && mode != "completed-receipt" {
					r.State, r.Result.Phase = "prepared", ""
				}
				if r.RequestID == bad.RequestID && mode == "uncertain-sibling" {
					r.State, r.Step = "uncertain", "push"
				}
				if err := saveNativeAction(f.cfg, *r); err != nil {
					t.Fatal(err)
				}
			}
			code := f.models[x.Job.ID]
			code.running = false
			x.Phase, x.Reason = "blocked", "native_completed_without_result"
			x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now().UTC()
			if err := saveNativeExecution(f.cfg, x); err != nil {
				t.Fatal(err)
			}
			if mode == "completed-receipt" {
				f.task.Title += " later edit"
			}
			writes, updates := len(f.transport.writes), len(f.updates)
			d, err := NewNativeDispatcher(f.cfg, f.m, f.g, code, NewCheckouts(f.cfg, f.transport), RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			done, err := d.reconcileNativeAction(t.Context(), &x)
			if mode == "uncertain-sibling" {
				if err == nil || done {
					t.Fatal("uncertain sibling was bypassed")
				}
			} else if err != nil || !done {
				t.Fatal("final publication stranded behind a local refusal", err)
			}
			after, err := loadNativeJobActions(f.cfg, x)
			if err != nil || len(after) != len(records) {
				t.Fatal(err)
			}
			for _, r := range after {
				if r.RequestID == bad.RequestID && (r.State != records[0].State || r.Step != records[0].Step) {
					t.Fatal("earlier refusal/uncertainty was rewritten", r)
				}
			}
			if writes != len(f.transport.writes) || updates != len(f.updates) || code.calls != 1 {
				t.Fatal("finalization replayed an effect or model")
			}
		})
	}
}

func TestKataCompletedReviewDoesNotHideLaterUnknownReviewWrite(t *testing.T) {
	f := kataAdmissionSetup(t, true)
	x := f.stage(0, "review")
	if err := f.action(x, NativeAction{Operation: "review", RequestID: "first-verdict", Verdict: "approved", Body: "Independent review"}); err != nil {
		t.Fatal(err)
	}
	f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		result, err := f.transport.response(owner, endpoint, method, body)
		if strings.HasSuffix(endpoint, "/reviews") && method == "POST" {
			return response("503 Service Unavailable", `{}`), nil
		}
		return result, err
	}
	if err := f.action(x, NativeAction{Operation: "review", RequestID: "later-verdict", Verdict: "changes-needed", Body: "Found a concrete issue"}); err == nil {
		t.Fatal("lost review response was not exercised")
	}
	f.models[x.Job.ID].running = false
	if done, err := f.runs[x.Job.ID].advance(t.Context(), &x); err != nil || done || x.Phase != "blocked" {
		t.Fatal("earlier success released an unknown later effect", done, err, x.Phase)
	}
	if complete, err := nativeJobHasResult(f.cfg, x); err != nil || complete {
		t.Fatal("unknown sibling hidden by completed result", complete, err)
	}
}
