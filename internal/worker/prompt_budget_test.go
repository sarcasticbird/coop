package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlanningPromptKeepsSelectedGoalOnceAndExcludesUnrelatedBodies(t *testing.T) {
	c, s, j := handoffFixture(t)
	s.Goals[0].Body = "SELECTED_GOAL_" + strings.Repeat("x", 11000)
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	s.Events[0].Body = s.Goals[0].Body
	other := s.Goals[0]
	other.URL = "https://github.com/orgs/planning/discussions/2"
	other.Body = "UNRELATED_PROPOSAL_" + strings.Repeat("y", 4759)
	other.BodySHA = BodyHash(other.Body)
	other.Decisions = nil
	s.Goals = append(s.Goals, other)
	before, _ := json.Marshal(s)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatalf("unrelated proposal blocked selected goal planning: %v", err)
	}
	if strings.Count(prompt, "SELECTED_GOAL_") != 1 || strings.Contains(prompt, "UNRELATED_PROPOSAL_") {
		t.Fatal("goal descriptions duplicated or unrelated body included")
	}
	for _, required := range []string{other.URL, other.BodySHA, s.Goals[0].BodySHA, s.Events[0].ID, j.ID} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing required identity %s", required)
		}
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("prompt construction mutated authority snapshot")
	}
}

func TestPromptOmitsRuntimeBookkeepingButKeepsCheckoutAndScope(t *testing.T) {
	c, s, j := handoffFixture(t)
	j.Checkout, j.Branch, j.ScopeSHA = "/workspace/worker/own", "agent/own", "scope-identity"
	j.SessionID = strings.Repeat("SESSION_BOOKKEEPING", 3000)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatalf("unused session bookkeeping prevented prompt: %v", err)
	}
	if strings.Contains(prompt, "SESSION_BOOKKEEPING") {
		t.Fatal("runtime session bookkeeping was sent to the model")
	}
	for _, required := range []string{j.Checkout, j.Branch, j.ScopeSHA} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing owned checkout/scope %s", required)
		}
	}
}

func TestWakePromptCanonicalizesFullGoalRootSource(t *testing.T) {
	c, s, _ := policyFixture(t)
	r := testWakeRequest(c)
	s.Goals[0].Body = "ROOT_SOURCE_" + strings.Repeat("z", 16000)
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	r.Source.DiscussionURL = s.Goals[0].URL
	r.Source.NodeID, r.Source.DiscussionID, r.Source.RootID = "goal", "goal", "goal"
	r.Source.Body = s.Goals[0].Body
	prompt, err := BuildWakePrompt(c, r, r.Source, s)
	if err != nil {
		t.Fatalf("duplicate root body prevented wake: %v", err)
	}
	if strings.Count(prompt, "ROOT_SOURCE_") != 1 || !strings.Contains(prompt, s.Goals[0].BodySHA) {
		t.Fatal("full root source not represented by a canonical goal")
	}
}

func TestEngineBlocksOversizeBeforeSubmissionWithoutRetry(t *testing.T) {
	e, g, code, clock, store := engineFixture(t)
	g.snapshot.Tasks[0].Body = strings.Repeat("oversize", 6000)
	g.snapshot.Tasks[0].BodySHA = TaskScopeHash(g.snapshot.Tasks[0].Title, g.snapshot.Tasks[0].Body)
	g.snapshot.Tasks[0].MetadataScopeSHA = g.snapshot.Tasks[0].BodySHA
	tick(t, e, clock)
	for _, job := range store.state.Jobs {
		if job.Phase != "blocked" || job.WaitReason != "prompt_size" || job.MessageID != "" || job.SubmissionState != "" || job.Attempts != 0 {
			t.Fatalf("size failure was treated as approval or execution retry: phase=%s reason=%s attempts=%d", job.Phase, job.WaitReason, job.Attempts)
		}
	}
	for range 5 {
		tick(t, e, clock)
	}
	if code.calls != 0 || g.claims != 1 {
		t.Fatal("oversized task submitted or re-claimed")
	}
}

func TestPromptBudgetCountsFinalBytesAndDoesNotTruncate(t *testing.T) {
	c, s, now := policyFixture(t)
	j := Select(c, s, State{}, now)[0]
	s.Tasks[0].Body = "private-sentinel 鸟 café <>&\"\\\n"
	base, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	s.Tasks[0].Body += strings.Repeat("x", 32768-len(base))
	prompt, err := BuildPrompt(c, j, s)
	if err != nil || len(prompt) != 32768 {
		t.Fatalf("exact byte boundary rejected: bytes=%d err=%v", len(prompt), err)
	}
	var input struct{ Task Task }
	parts := strings.SplitN(prompt, "Quoted GitHub input and verified job identity:\n", 2)
	if json.Unmarshal([]byte(parts[1]), &input) != nil || input.Task.Body != s.Tasks[0].Body {
		t.Fatal("escaping or UTF-8 content changed at boundary")
	}
	// A one-byte policy suffix must be measured along with the complete prompt.
	prompt, err = buildPrompt(c, j, s, "!", now)
	var size *PromptSizeError
	if prompt != "" || !errors.As(err, &size) || size.ActualBytes != 32769 || size.LimitBytes != 32768 {
		t.Fatalf("post-build policy escaped budget: prompt bytes=%d err=%v", len(prompt), err)
	}
	if validatePromptFailure(size) != nil || errorCategory(err) != "prompt_size" {
		t.Fatal("missing valid size diagnostics")
	}
	for _, forbidden := range []string{"private-sentinel", "café", s.Tasks[0].Body} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("size diagnostic disclosed quoted input")
		}
	}
}

func TestPromptRejectsOversizedRequiredSections(t *testing.T) {
	for _, section := range []string{"Goal", "Event", "PlanningGoals"} {
		t.Run(section, func(t *testing.T) {
			c, s, j := handoffFixture(t)
			switch section {
			case "Goal":
				s.Goals[0].Body = strings.Repeat("鸟", 14000)
			case "Event":
				s.Events[0].Body = strings.Repeat("<", 6000)
			case "PlanningGoals":
				for range 100 {
					s.Goals = append(s.Goals, s.Goals[0])
				}
			}
			prompt, err := BuildPrompt(c, j, s)
			var size *PromptSizeError
			if prompt != "" || !errors.As(err, &size) {
				t.Fatalf("required context was silently dropped: %v", err)
			}
			found := false
			for _, s := range size.Sections {
				if s.Name == section && s.Bytes > 20000 {
					found = true
				}
			}
			if !found || validatePromptFailure(size) != nil {
				t.Fatalf("missing responsible-section counts: %+v", size)
			}
		})
	}
}

func TestWakeSizeFailureIsDurableAndFreshRequestCanRun(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := e.GitHub.(*wakeGitHub)
	g.snapshot.Goals[0].Body = strings.Repeat("<", 6000)
	g.snapshot.Goals[0].BodySHA = BodyHash(g.snapshot.Goals[0].Body)
	q, err := QueuePing(e.Config, store, PingInput{Prompt: "check approved work"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	tick(t, e, clock)
	v, err := NewWakeStore(store.Dir).Receipt(q.ID)
	if err != nil || v.Phase != "blocked" || v.Reason != "prompt_size" || v.Job.Attempts != 0 || v.Job.MessageID != "" || v.Job.PromptFailure == nil || !v.Job.BlockReported {
		t.Fatalf("wake failure was retried or lost: phase=%s reason=%s err=%v", v.Phase, v.Reason, err)
	}
	before, _ := json.Marshal(v)
	for range 5 {
		tick(t, e, clock)
	}
	v, err = store.Receipt(q.ID)
	after, _ := json.Marshal(v)
	if err != nil || string(before) != string(after) || code.calls != 0 {
		t.Fatal("unchanged oversized request replayed or evidence rewritten")
	}
	summaries, err := WakeSummaries(store, 20, q.ID)
	if err != nil || len(summaries) != 1 || summaries[0].PromptFailure == nil {
		t.Fatal("operator request inspection lost size measurements")
	}
	// New supported source derives new work without resetting the failed receipt.
	g.snapshot.Goals[0].Body = "Compact current scope"
	g.snapshot.Goals[0].BodySHA = BodyHash(g.snapshot.Goals[0].Body)
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "fresh input"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	tick(t, e, clock)
	if code.calls != 1 || code.last.Kind != "wake" {
		t.Fatal("old blocked request prevented fresh eligible input")
	}
}

func TestSizeFailureStateCompatibilityAndStatus(t *testing.T) {
	e, g, _, clock, memory := engineFixture(t)
	g.snapshot.Cursor = map[string]string{}
	g.snapshot.Tasks[0].Body = strings.Repeat("<", 6000)
	g.snapshot.Tasks[0].BodySHA = TaskScopeHash(g.snapshot.Tasks[0].Title, g.snapshot.Tasks[0].Body)
	g.snapshot.Tasks[0].MetadataScopeSHA = g.snapshot.Tasks[0].BodySHA
	tick(t, e, clock)
	store := storeFixture(t)
	if err := store.Save(memory.state); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewFileStore(store.Dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	var blocked Job
	for _, j := range loaded.Jobs {
		blocked = j
	}
	if blocked.PromptFailure == nil || !blocked.BlockReported || loaded.InstanceID != memory.state.InstanceID {
		t.Fatal("durable diagnostics or owned state lost")
	}
	if e.status.PromptFailure == nil || e.status.WaitReason != "prompt_size" {
		t.Fatal("status hides actionable size failure")
	}
	// New readers preserve legacy jobs without rewriting their identity.
	raw, _ := json.Marshal(blocked)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "PromptFailure")
	delete(fields, "PromptReportAttempts")
	legacy, _ := json.Marshal(fields)
	var decoded Job
	if strictJSON(legacy, &decoded) != nil || decoded.PromptFailure != nil {
		t.Fatal("new reader cannot read old job schema")
	}
	var old legacyPromptJob
	if strictJSON(legacy, &old) != nil || old.ID != blocked.ID || strictJSON(raw, &old) == nil {
		t.Fatal("legacy reader lost compatible identity or silently ignored newer state")
	}
	// Corrupt diagnostics cannot be echoed through native status or GitHub reports.
	blocked.PromptFailure.Sections[0].Name = "private-sentinel"
	loaded.Jobs[blocked.ID] = blocked
	if store.Save(loaded) == nil {
		t.Fatal("untrusted diagnostic section accepted")
	}
}

// Frozen pre-#51 job schema. Compatibility must preserve old identities and
// force an old strict reader to refuse newer records rather than replay them.
type legacyPromptJob struct {
	BlockReported                                                                bool
	TerminalAt                                                                   time.Time
	ScopeSHA, WaitReason                                                         string
	ID, Kind, GoalURL, TaskURL, PRURL, HeadSHA, InputRevision                    string
	Checkout, Branch, SessionID, MessageID, SubmissionState, Phase, ProgressHash string
	Attempts, ReviewCycles                                                       int
	NextAttemptAt, Deadline, SubmittedAt                                         time.Time
}

func TestPlanningIndexCannotRetargetSelectedTurnToAnotherApprovedGoal(t *testing.T) {
	c, s, j := handoffFixture(t)
	other := s.Goals[0]
	other.URL = "https://github.com/orgs/planning/discussions/2"
	other.Body = "Another approved goal"
	other.BodySHA = BodyHash(other.Body)
	s.Goals = append(s.Goals, other)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Goal          Goal
		Event         Event
		PlanningGoals []struct {
			Goal     Goal
			Approval Approval
		}
	}
	parts := strings.SplitN(prompt, "Quoted GitHub input and verified job identity:\n", 2)
	if json.Unmarshal([]byte(parts[1]), &input) != nil || input.Goal.URL != j.GoalURL || input.Event.URL != j.GoalURL || len(input.PlanningGoals) != 2 {
		t.Fatal("selected planning authority changed")
	}
	if input.PlanningGoals[1].Approval.State != "approved" || input.PlanningGoals[1].Goal.Body != "" || input.PlanningGoals[1].Goal.BodySHA != other.BodySHA {
		t.Fatal("approved discovery index lost its decision or included unrelated body")
	}
	j.GoalURL = other.URL
	if ok, _ := Admit(c, j, s, time.Now()); ok {
		t.Fatal("discovery index retargeted an existing event's planning authority")
	}
}

func TestOversizeWakeNearRequestCapacityRetainsDiagnostic(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	r := testWakeRequest(e.Config)
	r.CreatedAt = clock.Now()
	r.Source.Body = ""
	raw, _ := json.Marshal(r)
	r.Source.Body = strings.Repeat("x", 32768-len(raw))
	r.Source.Revision = ConversationRevision(r.Source)
	if _, err := store.Enqueue(r); err != nil {
		t.Fatal(err)
	}
	tick(t, e, clock)
	v, err := NewWakeStore(store.Dir).Receipt(r.ID)
	if err != nil || v.Phase != "blocked" || v.Job.PromptFailure == nil || code.calls != 0 {
		t.Fatalf("large source prevented failure evidence: phase=%s err=%v", v.Phase, err)
	}
}

type promptReportGitHub struct {
	*wakeGitHub
	reports     int
	reportError error
	rootBody    string
}

func (g *promptReportGitHub) RevalidateWake(ctx context.Context, r WakeRequest) (ConversationSource, error) {
	s, err := g.wakeGitHub.RevalidateWake(ctx, r)
	if err == nil && r.Kind == "ping" && g.rootBody != "" {
		s.Body = g.rootBody
		s.Revision = ConversationRevision(s)
	}
	return s, err
}

func (g *promptReportGitHub) ReportBlock(context.Context, Job, string, string) error {
	g.reports++
	return g.reportError
}

func TestPromptFailureReportRetriesWithoutReplayingModel(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := &promptReportGitHub{wakeGitHub: e.GitHub.(*wakeGitHub), reportError: ghError("permission_denied")}
	e.GitHub, e.Wakes.GitHub = g, g
	g.snapshot.Goals[0].Body = strings.Repeat("<", 6000)
	q, err := QueuePing(e.Config, store, PingInput{Prompt: "oversized"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	if err := e.Tick(context.Background()); errorCategory(err) != "permission_denied" {
		t.Fatalf("missing first report failure: %v", err)
	}
	g.reportError = nil
	clock.now = clock.now.Add(20 * time.Minute)
	tick(t, e, clock)
	v, err := store.Receipt(q.ID)
	if err != nil || !v.Job.BlockReported || g.reports != 2 || code.calls != 0 {
		t.Fatal("blocked report was never retried without another queued wake, or model replayed")
	}
}

func TestFailedSizeReportDoesNotStarveNewSupportedWake(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := &promptReportGitHub{wakeGitHub: e.GitHub.(*wakeGitHub), reportError: ghError("permission_denied")}
	e.GitHub, e.Wakes.GitHub = g, g
	g.snapshot.Goals[0].Body = strings.Repeat("<", 6000)
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "oversized"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	_ = e.Tick(context.Background())
	g.snapshot.Goals[0].Body = "Compact current scope"
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "fresh supported input"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(20 * time.Minute)
	tick(t, e, clock)
	if code.calls != 1 || g.reports != 1 {
		t.Fatal("stale failed report displaced a fresh supported request")
	}
}

func TestPromptSizeFailuresReleaseWakeQueueCapacity(t *testing.T) {
	c, store := wakeFixture(t)
	_, err := assemblePrompt(BodyHash("example"), c.Wake.CoordinationURL, promptSection{"instructions", strings.Repeat("x", 32769)})
	var diagnostic *PromptSizeError
	if !errors.As(err, &diagnostic) {
		t.Fatal("missing size failure")
	}
	for i := range MaxWakeQueued {
		q, err := QueuePing(c, store, PingInput{RequestID: BodyHash(time.Unix(int64(i+1), 0).String()), Prompt: "bounded input"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		copy := *diagnostic
		copy.JobID = q.ID
		if err := store.SaveReceipt(WakeReceipt{Version: 1, RequestID: q.ID, Phase: "blocked", Reason: "prompt_size", Job: Job{ID: q.ID, PromptFailure: &copy}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := QueuePing(c, store, PingInput{Prompt: "fresh supported input"}, time.Now()); err != nil {
		t.Fatalf("terminal size failures permanently consumed queue capacity: %v", err)
	}
}

func TestExpandedPingSourceRetainsLosslessSizeFailureEvidence(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := &promptReportGitHub{wakeGitHub: e.GitHub.(*wakeGitHub), rootBody: strings.Repeat("<", 32768)}
	e.GitHub, e.Wakes.GitHub = g, g
	q, err := QueuePing(e.Config, store, PingInput{Prompt: "short ping"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	tick(t, e, clock)
	v, err := NewWakeStore(store.Dir).Receipt(q.ID)
	if err != nil || v.Job.PromptFailure == nil || v.Source.Body != g.rootBody || code.calls != 0 {
		t.Fatal("expanded escaped source lost failure evidence or submitted")
	}
}

func TestSizeReportAttemptsAreBoundedIndependentlyOfExecution(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := &promptReportGitHub{wakeGitHub: e.GitHub.(*wakeGitHub), reportError: ghError("permission_denied")}
	e.GitHub, e.Wakes.GitHub = g, g
	g.snapshot.Goals[0].Body = strings.Repeat("<", 6000)
	q, err := QueuePing(e.Config, store, PingInput{Prompt: "oversized"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		clock.now = clock.now.Add(20 * time.Minute)
		_ = e.Tick(context.Background())
	}
	v, err := store.Receipt(q.ID)
	if err != nil || v.Job.PromptReportAttempts != e.Config.Limits.MaxAttempts || g.reports != e.Config.Limits.MaxAttempts || v.Job.Attempts != 0 || v.Job.BlockReported || code.calls != 0 {
		t.Fatal("report retries were unbounded, consumed execution attempts or replayed input")
	}
}
