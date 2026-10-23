package master

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testTasks struct {
	task                   c.ProviderTask
	creates, binds, closes int
}

func (b *testTasks) Capabilities() c.TaskCapabilities {
	return c.TaskCapabilities{Provider: "kata", ProjectUID: "01M5MCPEHHP4C98MZSHJEE3M8D", PolicySHA: strings.Repeat("a", 64)}
}
func (b *testTasks) Read(_ context.Context, ref string) (c.ProviderTask, error) {
	if b.task.Reference != ref {
		return c.ProviderTask{}, c.ErrNotFound
	}
	return b.task, nil
}
func (b *testTasks) Create(_ context.Context, in c.TaskCreateInput, _ c.Principal) (c.ProviderTask, error) {
	b.creates++
	if b.task.Reference == "" {
		ref, _ := c.KataTaskReference(b.Capabilities().ProjectUID, "01M5MSA8FZCCD7DWMTP6RWM3MD", in.Repository)
		b.task = c.ProviderTask{Reference: ref, ProjectUID: b.Capabilities().ProjectUID, IssueUID: "01M5MSA8FZCCD7DWMTP6RWM3MD", Repository: in.Repository, Title: in.Title, Body: in.Body, Goal: in.Goal, State: "open", Revision: 1}
	}
	return b.task, nil
}
func (b *testTasks) Bind(_ context.Context, in c.TaskBindInput, p c.Principal) (c.ProviderTask, error) {
	b.binds++
	if b.task.Binding != nil && digest(*b.task.Binding) == digest(in.Binding) {
		return b.task, nil
	}
	if in.ExpectedRevision != b.task.Revision {
		return b.task, c.ErrConflict
	}
	b.task.Binding = &in.Binding
	b.task.BindingActor = p
	b.task.Revision++
	return b.task, nil
}
func (b *testTasks) Complete(_ context.Context, _ c.TaskCompleteInput, _ c.Principal) (c.ProviderTask, error) {
	b.closes++
	b.task.State = "closed"
	b.task.ClosedReason = "done"
	return b.task, nil
}
func taskFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.TaskCreateInput, *testTasks) {
	t.Helper()
	s, o, b, proposal := goalFixture(t)
	b.Capabilities = append(b.Capabilities, "plan")
	if err := s.Store.Configure(context.Background(), []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.ProposeGoal(context.Background(), b, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideGoal(context.Background(), o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, Action: "approve", RequestID: "approve"}); err != nil {
		t.Fatal(err)
	}
	tasks := &testTasks{}
	s.Tasks = tasks
	return s, o, b, c.TaskCreateInput{Goal: v.Ref, Repository: "example/coop", BaseBranch: "main", ExpectedApprovalSequence: 1, Title: "Fix task", Body: "Bounded change", RequestID: "create-task"}, tasks
}
func TestTaskCreateBindsCurrentApprovalAndRegisters(t *testing.T) {
	s, o, _, in, b := taskFixture(t)
	ctx := context.Background()
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	if task.Binding == nil || task.Binding.AuthorID == 0 || task.Binding.PeerID == 0 || task.Binding.PolicySHA != b.Capabilities().PolicySHA || task.Binding.TaskScopeSHA != digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{in.Title, in.Body}) {
		t.Fatalf("binding %+v", task.Binding)
	}
	page, err := s.Artifacts(ctx, o, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(page.Links) != 1 || page.Links[0].AssignmentSHA != digest(task.Binding) {
		t.Fatalf("registry %+v %v", page, err)
	}
	if _, err = s.TaskCreate(ctx, o, in); err != nil {
		t.Fatal("safe retry", err)
	}
	in.Body = "different"
	if _, err = s.TaskCreate(ctx, o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed request accepted", err)
	}
}
func TestTaskMutationGuardsAndCompletionRequiresMatchingPR(t *testing.T) {
	s, o, b, in, backend := taskFixture(t)
	ctx := context.Background()
	bad := in
	bad.BaseBranch = "other"
	if _, err := s.TaskCreate(ctx, o, bad); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	complete := c.TaskCompleteInput{Reference: task.Reference, Goal: in.Goal, AssignmentSHA: digest(task.Binding), PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "complete"}
	if _, err = s.TaskComplete(ctx, b, complete); !errors.Is(err, c.ErrConflict) || backend.closes != 0 {
		t.Fatal("unregistered PR closed task", err)
	}
	link := c.ArtifactLink{Goal: in.Goal, Kind: "pull_request", URL: complete.PullRequestURL, TaskURL: task.Reference, AssignmentSHA: complete.AssignmentSHA, RequestID: "pr"}
	if _, err = s.RegisterArtifact(ctx, b, link); err != nil {
		t.Fatal(err)
	}
	binding := *task.Binding
	binding.DependsOn = []string{task.Reference}
	if _, err = s.TaskBind(ctx, o, c.TaskBindInput{Binding: binding, ExpectedRevision: task.Revision, ExpectedAssignmentSHA: complete.AssignmentSHA, RequestID: "rebind"}); err == nil {
		t.Fatal("published work rebound")
	}
	if _, err = s.TaskComplete(ctx, b, complete); err != nil {
		t.Fatal(err)
	}
}
func TestTaskReadUsesCurrentMembershipAndDisabledIsExplicit(t *testing.T) {
	s, o, b, in, _ := taskFixture(t)
	ctx := context.Background()
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	b.Capabilities = nil
	if err = s.Store.Configure(ctx, []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TaskRead(ctx, b, c.TaskReadInput{Reference: task.Reference}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("revoked read accepted", err)
	}
	s.Tasks = nil
	if _, err = s.TaskCapabilities(ctx, o); !errors.Is(err, c.ErrUnsupported) {
		t.Fatal(err)
	}
}
func TestPlannerCapabilityDoesNotRequireSendForTaskCreation(t *testing.T) {
	s, _, b, in, _ := taskFixture(t)
	b.Capabilities = []string{"read", "plan"}
	if err := s.Store.Configure(context.Background(), []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskCreate(context.Background(), b, in); err != nil {
		t.Fatal("authorized planner cannot create", err)
	}
}
func TestPublishedTaskAssignmentCannotBeReplacedThroughArtifactAPI(t *testing.T) {
	s, o, b, in, _ := taskFixture(t)
	ctx := context.Background()
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	sha := digest(task.Binding)
	if _, err = s.RegisterArtifact(ctx, b, c.ArtifactLink{Goal: in.Goal, Kind: "pull_request", URL: "https://github.com/example/coop/pull/3", TaskURL: task.Reference, AssignmentSHA: sha, RequestID: "pr-published"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterArtifact(ctx, o, c.ArtifactLink{Goal: in.Goal, Kind: "task", URL: task.Reference, AssignmentSHA: strings.Repeat("f", 64), ExpectedAssignmentSHA: sha, RequestID: "unsafe-rebind"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("published assignment replaced", err)
	}
}
func TestTaskHTTPUsesMasterAuthenticationPinsAndStrictInputs(t *testing.T) {
	s, o, b, in, backend := taskFixture(t)
	h := NewHandler(s, policyFixture(o, b))
	raw, _ := json.Marshal(in)
	for _, tc := range []struct {
		token, pin, body string
		want             int
	}{{"wrong-token", "test-master", string(raw), 401}, {"owner-test-token", "other-master", string(raw), 409}, {"owner-test-token", "test-master", strings.TrimSuffix(string(raw), "}") + `,"actor":{"id":"owner"}}`, 400}, {"owner-test-token", "test-master", string(raw), 200}} {
		req := httptest.NewRequest("POST", "/api/v1/tasks/create", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Coop-Expected-Master-ID", tc.pin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), tc.want)
		}
	}
	if backend.creates != 1 {
		t.Fatal("unauthorized request reached backend", backend.creates)
	}
	list, err := s.TaskList(context.Background(), o, c.TaskListInput{Goal: in.Goal})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("task list %+v %v", list, err)
	}
	s.Tasks = nil
	req := httptest.NewRequest("GET", "/api/v1/tasks/capabilities", nil)
	req.Header.Set("Authorization", "Bearer owner-test-token")
	req.Header.Set("X-Coop-Expected-Master-ID", "test-master")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 501 || !strings.Contains(w.Body.String(), "unsupported_operation") {
		t.Fatalf("disabled provider %d %s", w.Code, w.Body.String())
	}
}
func TestTaskBindChecksScopeRolesPolicyAndApprovalBeforeBackend(t *testing.T) {
	s, o, _, in, backend := taskFixture(t)
	task, err := s.TaskCreate(context.Background(), o, in)
	if err != nil {
		t.Fatal(err)
	}
	before := backend.binds
	for _, change := range []func(*c.NativeTaskBinding){func(b *c.NativeTaskBinding) { b.AuthorID++ }, func(b *c.NativeTaskBinding) { b.TaskScopeSHA = strings.Repeat("f", 64) }, func(b *c.NativeTaskBinding) { b.PolicySHA = strings.Repeat("f", 64) }, func(b *c.NativeTaskBinding) { b.ApprovalSequence++ }, func(b *c.NativeTaskBinding) { b.BaseBranch = "outside" }} {
		binding := *task.Binding
		change(&binding)
		_, err = s.TaskBind(context.Background(), o, c.TaskBindInput{Binding: binding, ExpectedRevision: task.Revision, ExpectedAssignmentSHA: digest(task.Binding), RequestID: "invalid-binding"})
		if err == nil {
			t.Fatalf("invalid binding accepted %+v", binding)
		}
	}
	if backend.binds != before {
		t.Fatal("invalid binding reached backend")
	}
	stale := in
	stale.RequestID = "stale-approval"
	stale.ExpectedApprovalSequence++
	if _, err = s.TaskCreate(context.Background(), o, stale); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale approval accepted", err)
	}
}

func TestTaskCreateReplaysPersistedImmutableUID(t *testing.T) {
	s, o, _, in, backend := taskFixture(t)
	ctx := context.Background()
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.TaskCreate(ctx, o, in)
	if err != nil || again.Reference != task.Reference {
		t.Fatalf("retry %+v %v", again, err)
	}
	if backend.creates != 1 {
		t.Fatal("successful replay issued another provider create", backend.creates)
	}
}

func TestTaskBindRequestIdentityRejectsChangedPayloadBeforeProvider(t *testing.T) {
	s, o, _, in, backend := taskFixture(t)
	ctx := context.Background()
	task, err := s.TaskCreate(ctx, o, in)
	if err != nil {
		t.Fatal(err)
	}
	input := c.TaskBindInput{Binding: *task.Binding, ExpectedRevision: task.Revision, ExpectedAssignmentSHA: digest(task.Binding), RequestID: "bind-request"}
	if _, err = s.TaskBind(ctx, o, input); err != nil {
		t.Fatal(err)
	}
	before := backend.binds
	backend.task.Body = "Updated bounded work"
	input.Binding.TaskScopeSHA = digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{backend.task.Title, backend.task.Body})
	if _, err = s.TaskBind(ctx, o, input); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed bind request accepted", err)
	}
	if backend.binds != before {
		t.Fatal("changed request reached provider", backend.binds, before)
	}
}

func TestUnresolvedCreateCannotOutliveNativeIdempotencyWindow(t *testing.T) {
	s, o, _, in, _ := taskFixture(t)
	ctx := context.Background()
	_, a, err := s.taskAuthority(ctx, o, in.Goal, true)
	if err != nil {
		t.Fatal(err)
	}
	placeholder, _ := c.KataTaskReference(s.Tasks.Capabilities().ProjectUID, s.Tasks.Capabilities().ProjectUID, in.Repository)
	binding, err := s.taskBinding(ctx, o, a, c.ProviderTask{Reference: placeholder, Repository: in.Repository, Goal: in.Goal, Title: in.Title, Body: in.Body}, in.BaseBranch, in.DependsOn)
	if err != nil {
		t.Fatal(err)
	}
	binding.TaskURL = ""
	state := taskCreateIntent{Binding: binding, CreatedAt: s.Store.now().UTC().Add(-8 * 24 * time.Hour)}
	if err = saveRequest(ctx, s.Store.db, o.ID, in.RequestID, "task-create", in, state); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TaskCreate(ctx, o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("expired ambiguous create retried", err)
	}
	if s.Tasks.(*testTasks).creates != 0 {
		t.Fatal("expired request reached provider")
	}
}

type gatedTaskBind struct {
	*testTasks
	entered   chan struct{}
	resume    chan struct{}
	failAfter bool
}

func (b *gatedTaskBind) Bind(ctx context.Context, in c.TaskBindInput, p c.Principal) (c.ProviderTask, error) {
	if b.entered != nil {
		close(b.entered)
		<-b.resume
		b.entered = nil
	}
	task, err := b.testTasks.Bind(ctx, in, p)
	if err != nil {
		return task, err
	}
	if b.failAfter {
		b.failAfter = false
		return task, c.ErrUnavailable
	}
	return task, nil
}
func prepareTaskRebind(t *testing.T) (*Service, c.Principal, c.Principal, c.TaskBindInput, *testTasks) {
	t.Helper()
	s, o, b, in, backend := taskFixture(t)
	task, err := s.TaskCreate(context.Background(), o, in)
	if err != nil {
		t.Fatal(err)
	}
	input := c.TaskBindInput{Binding: *task.Binding, ExpectedRevision: task.Revision, ExpectedAssignmentSHA: digest(task.Binding), RequestID: "rebind-concurrency"}
	backend.task.Body = "Revised bounded work"
	input.Binding.TaskScopeSHA = digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{backend.task.Title, backend.task.Body})
	return s, o, b, input, backend
}
func TestTaskRebindReservesAssignmentBeforeConcurrentPRRegistration(t *testing.T) {
	s, o, author, in, backend := prepareTaskRebind(t)
	gate := &gatedTaskBind{testTasks: backend, entered: make(chan struct{}), resume: make(chan struct{})}
	s.Tasks = gate
	result := make(chan error, 1)
	go func() { _, err := s.TaskBind(context.Background(), o, in); result <- err }()
	<-gate.entered
	_, prErr := s.RegisterArtifact(context.Background(), author, c.ArtifactLink{Goal: in.Binding.Goal, Kind: "pull_request", URL: "https://github.com/example/coop/pull/8", TaskURL: in.Binding.TaskURL, AssignmentSHA: in.ExpectedAssignmentSHA, RequestID: "concurrent-old-pr"})
	close(gate.resume)
	bindErr := <-result
	if !errors.Is(prErr, c.ErrConflict) {
		t.Fatalf("old-assignment PR published during provider CAS: %v; bind returned %v", prErr, bindErr)
	}
	if bindErr != nil {
		t.Fatal("reserved bind could not publish", bindErr)
	}
	var sha string
	if err := s.Store.db.QueryRow("SELECT assignment_sha FROM artifacts WHERE url=?", in.Binding.TaskURL).Scan(&sha); err != nil || sha != digest(in.Binding) {
		t.Fatal("provider and registry diverged", sha, err)
	}
}
func TestFailedTaskRebindKeepsReservationForExactRetry(t *testing.T) {
	s, o, author, in, backend := prepareTaskRebind(t)
	s.Tasks = &gatedTaskBind{testTasks: backend, failAfter: true}
	if _, err := s.TaskBind(context.Background(), o, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("expected uncertain provider response", err)
	}
	_, err := s.RegisterArtifact(context.Background(), author, c.ArtifactLink{Goal: in.Binding.Goal, Kind: "pull_request", URL: "https://github.com/example/coop/pull/9", TaskURL: in.Binding.TaskURL, AssignmentSHA: in.ExpectedAssignmentSHA, RequestID: "uncertain-old-pr"})
	if !errors.Is(err, c.ErrConflict) {
		t.Fatal("failed mutation allowed old-assignment PR", err)
	}
	other := in
	other.RequestID = "different-rebind-request"
	if _, err = s.TaskBind(context.Background(), o, other); !errors.Is(err, c.ErrConflict) {
		t.Fatal("different request stole pending mutation", err)
	}
	if _, err = s.TaskBind(context.Background(), o, in); err != nil {
		t.Fatal("exact retry failed to reconcile", err)
	}
	if _, err = s.RegisterArtifact(context.Background(), author, c.ArtifactLink{Goal: in.Binding.Goal, Kind: "pull_request", URL: "https://github.com/example/coop/pull/10", TaskURL: in.Binding.TaskURL, AssignmentSHA: digest(in.Binding), RequestID: "reconciled-pr"}); err != nil {
		t.Fatal("reconciled assignment remained blocked", err)
	}
}
func TestTaskCreateNormalizesExplicitEmptyDependenciesForReplay(t *testing.T) {
	s, o, _, in, backend := taskFixture(t)
	in.DependsOn = []string{}
	first, err := s.TaskCreate(context.Background(), o, in)
	if err != nil {
		t.Fatal("explicit empty dependencies failed creation", err)
	}
	again, err := s.TaskCreate(context.Background(), o, in)
	if err != nil || again.Reference != first.Reference || backend.creates != 1 {
		t.Fatalf("explicit empty dependencies failed replay %+v %v", again, err)
	}
}

type rejectedTaskBind struct {
	*testTasks
	advanceRevision, unknown bool
}

func (b *rejectedTaskBind) Bind(ctx context.Context, in c.TaskBindInput, p c.Principal) (c.ProviderTask, error) {
	if b.advanceRevision {
		b.advanceRevision = false
		b.task.Revision++
		return b.task, c.ErrConflict
	}
	if b.unknown {
		b.unknown = false
		return c.ProviderTask{}, c.ErrUnavailable
	}
	return b.testTasks.Bind(ctx, in, p)
}
func TestRejectedTaskCASAllowsFreshRequestWithoutChangingOriginal(t *testing.T) {
	s, o, _, in, backend := prepareTaskRebind(t)
	s.Tasks = &rejectedTaskBind{testTasks: backend, advanceRevision: true}
	ctx := context.Background()
	if _, err := s.TaskBind(ctx, o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("expected definitive stale CAS", err)
	}
	corrected := in
	corrected.ExpectedRevision = backend.task.Revision
	if _, err := s.TaskBind(ctx, o, corrected); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed original request accepted", err)
	}
	corrected.RequestID = "corrected-provider-cas"
	if _, err := s.TaskBind(ctx, o, corrected); err != nil {
		t.Fatal("fresh valid CAS cannot recover failed reservation", err)
	}
}
func TestTaskBindEntryReconcilesRejectedReservationAfterRestart(t *testing.T) {
	s, o, _, in, backend := prepareTaskRebind(t)
	ctx := context.Background()
	if _, err := s.reserveTaskBinding(ctx, o.ID, in, false); err != nil {
		t.Fatal(err)
	}
	// The prior process exited after another metadata write invalidated its CAS,
	// before it could clean up the reservation. A new service sees durable state.
	backend.task.Revision++
	s = &Service{Store: s.Store, Tasks: backend}
	in.ExpectedRevision = backend.task.Revision
	in.RequestID = "after-restart-cas"
	if _, err := s.TaskBind(ctx, o, in); err != nil {
		t.Fatal("restart could not reconcile definitively rejected CAS", err)
	}
}
func TestUnknownSameRevisionTaskCASRetainsReservation(t *testing.T) {
	s, o, _, in, backend := prepareTaskRebind(t)
	s.Tasks = &rejectedTaskBind{testTasks: backend, unknown: true}
	ctx := context.Background()
	if _, err := s.TaskBind(ctx, o, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("expected unknown outcome", err)
	}
	other := in
	other.RequestID = "cannot-steal-unknown-cas"
	if _, err := s.TaskBind(ctx, o, other); !errors.Is(err, c.ErrConflict) {
		t.Fatal("unknown same-revision reservation was released", err)
	}
	if _, err := s.TaskBind(ctx, o, in); err != nil {
		t.Fatal("exact unknown-outcome retry cannot reconcile", err)
	}
}
