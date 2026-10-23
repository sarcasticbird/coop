package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type NativeEngineeringMaster interface {
	NativeMaster
	Self(context.Context) (c.SelfIdentity, error)
	Thread(context.Context, string) (c.Thread, error)
}
type NativeCatalogReader interface {
	NativeSnapshot(context.Context, Config, NativeMaster) (NativeCatalog, error)
	NativeTaskSnapshot(context.Context, Config, NativeMaster, Job, ...string) (NativeCatalog, error)
}
type nativeRefresh struct {
	catalog NativeCatalog
	err     error
}
type nativeStep struct {
	execution NativeExecution
	completed bool
	err       error
}
type NativePreview struct {
	MasterID string `json:"master_id"`
	AgentID  string `json:"agent_id"`
	Goals    int    `json:"goals"`
	Tasks    int    `json:"tasks"`
	Recovery bool   `json:"recovery"`
	Jobs     []Job  `json:"jobs"`
}

func (d *NativeDispatcher) Preview(ctx context.Context) (NativePreview, error) {
	out := NativePreview{MasterID: d.config.Master.MasterID, AgentID: d.config.Master.AgentID, Recovery: d.held, Jobs: []Job{}}
	if err := verifyNativePlacement(ctx, d.config, d.master); err != nil {
		return out, err
	}
	if d.coordinationOnly() {
		return out, nil
	}
	catalog, err := d.github.NativeSnapshot(ctx, d.config, d.master)
	if err != nil {
		return out, err
	}
	for _, a := range catalog.Goals {
		if _, err = nativeCurrentGoal(ctx, d.config, d.master, a.Revision.Ref); err != nil {
			return out, err
		}
	}
	out.Goals, out.Tasks = len(catalog.Goals), len(catalog.Tasks)
	if !d.held {
		for _, v := range NativeCandidates(d.config, catalog, d.history, d.clock.Now()) {
			out.Jobs = append(out.Jobs, v.Job)
		}
	}
	return out, nil
}

// NativeDispatcher owns the single engineering slot. All remote reads and native
// model calls run outside the receipt loop; results are consumed by that loop.
type NativeDispatcher struct {
	config          Config
	master          NativeEngineeringMaster
	github          NativeCatalogReader
	code            OpenCode
	checkouts       Checkouts
	clock           Clock
	history         map[string]NativeExecution
	eligible        map[string]time.Time
	catalog         NativeCatalog
	active          *NativeExecution
	refresh         chan nativeRefresh
	step            chan nativeStep
	stepCancel      context.CancelFunc
	nextRefresh     time.Time
	held            bool
	catalogError    string
	recoveryJob     string
	noticeDone      chan error
	nextNoticeCheck time.Time
}

func nativeExecutionPath(cfg Config, id string) string {
	return filepath.Join(masterDir(cfg), "engineering", id+".json")
}
func saveNativeExecution(cfg Config, x NativeExecution) error {
	if x.Version != 3 || cfg.Master == nil || x.MasterID != cfg.Master.MasterID || x.InstanceID != cfg.Master.InstanceID || x.Generation != cfg.Master.Generation || !hexSHA.MatchString(x.Job.ID) || x.Job.Master == nil || x.Job.Master.Goal == nil || *x.Job.Master.Goal != x.Goal {
		return c.ErrConflict
	}
	if x.Job.Master.AgentID != cfg.Master.AgentID || x.Job.Master.MasterID != cfg.Master.MasterID || x.Job.Master.ExecutorID != cfg.Self.ID || x.Binding != nil && (x.Binding.Goal != x.Goal || x.Binding.TaskURL != x.Job.TaskURL || NativeAssignmentSHA(*x.Binding) != x.Job.Master.AssignmentSHA) {
		return c.ErrConflict
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return err
	}
	if len(raw) > MaxSnapshot {
		return c.ErrSize
	}
	return atomicPrivate(nativeExecutionPath(cfg, x.Job.ID), raw)
}
func loadNativeExecution(cfg Config, id string) (NativeExecution, error) {
	var x NativeExecution
	if !hexSHA.MatchString(id) {
		return x, c.ErrInvalid
	}
	raw, err := readPrivate(nativeExecutionPath(cfg, id), MaxSnapshot)
	if err != nil {
		return x, err
	}
	if strictTeamJSON(raw, &x) != nil || x.Version != 3 || x.Job.ID != id || cfg.Master == nil || x.MasterID != cfg.Master.MasterID || x.Job.Master == nil || x.Job.Master.Goal == nil || *x.Job.Master.Goal != x.Goal {
		return x, c.ErrConflict
	}
	if x.Job.Master.AgentID != cfg.Master.AgentID || x.Job.Master.MasterID != cfg.Master.MasterID || x.Job.Master.ExecutorID != cfg.Self.ID || x.Binding != nil && (x.Binding.Goal != x.Goal || x.Binding.TaskURL != x.Job.TaskURL || NativeAssignmentSHA(*x.Binding) != x.Job.Master.AssignmentSHA) {
		return x, c.ErrConflict
	}
	if x.InstanceID != cfg.Master.InstanceID || x.Generation != cfg.Master.Generation {
		if _, ok, e := adoptedRecoveryRecord(cfg, "engineering", id+".json", raw); e != nil || !ok {
			if e != nil {
				return x, e
			}
			return x, c.ErrConflict
		}
	}
	return x, nil
}
func NewNativeDispatcher(cfg Config, m NativeEngineeringMaster, g NativeCatalogReader, code OpenCode, checkouts Checkouts, clock Clock) (*NativeDispatcher, error) {
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || m == nil || g == nil || code == nil || checkouts == nil {
		return nil, c.ErrInvalid
	}
	if clock == nil {
		clock = RealClock{}
	}
	d := &NativeDispatcher{config: cfg, master: m, github: g, code: code, checkouts: checkouts, clock: clock, history: map[string]NativeExecution{}}
	if err := d.loadSchedule(); err != nil {
		return nil, err
	}
	dir := filepath.Join(masterDir(cfg), "engineering")
	if err := ensurePrivateDir(dir, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id := entry.Name()
		if filepath.Ext(id) != ".json" {
			return nil, c.ErrConflict
		}
		id = id[:len(id)-5]
		x, e := loadNativeExecution(cfg, id)
		if e != nil {
			return nil, e
		}
		raw, marshalErr := json.Marshal(x)
		if marshalErr != nil {
			return nil, marshalErr
		}
		retired, retireErr := recoveryRetired(cfg, "engineering", entry.Name(), raw)
		if retireErr != nil {
			return nil, retireErr
		}
		if retired && x.Phase != "completed" && x.Phase != "cancelled" {
			x.Phase = "cancelled"
			x.Reason = "operator_restore_resolution"
		}
		d.history[id] = x
		switch x.Phase {
		case "selected", "prepared", "active":
			if d.active != nil {
				return nil, errors.New("conflicting native engineering intents")
			}
			copy := x
			d.active = &copy
		case "blocked", "completed", "cancelled":
		default:
			return nil, c.ErrConflict
		}
	}
	if err := d.updateRecoveryHold(); err != nil {
		return nil, err
	}
	return d, nil
}
func (d *NativeDispatcher) HasActive() bool {
	return d.step != nil || d.active != nil && (d.active.Phase == "selected" || d.active.Phase == "prepared" || d.active.Phase == "active")
}
func (d *NativeDispatcher) RecoveryBlocked() bool { return d.held }

// ObserveOnly cannot admit or submit a selected/prepared job while transport
// reconciliation is unavailable. It can still inspect an existing native turn.
func (d *NativeDispatcher) ObserveOnly(ctx context.Context) (bool, error) {
	if d.active == nil {
		return false, nil
	}
	if d.active.Phase != "active" {
		if d.step == nil {
			return false, nil
		}
		if d.stepCancel != nil {
			d.stepCancel()
		}
	}
	return d.Tick(ctx)
}

// A coordinator with no engineering capabilities or history has no engineering
// catalog to discover. Retained work must still reconcile after capabilities change.
func (d *NativeDispatcher) coordinationOnly() bool {
	if d.config.Team == nil || len(d.history) != 0 || d.active != nil || d.step != nil {
		return false
	}
	self, ok := TeamMemberForActor(*d.config.Team, d.config.Self)
	return ok && len(self.Capabilities) == 0
}

func (d *NativeDispatcher) poll() {
	if d.coordinationOnly() {
		return
	}
	if d.refresh != nil {
		select {
		case result := <-d.refresh:
			d.refresh = nil
			d.catalog = result.catalog
			d.catalogError = ""
			if result.err != nil {
				d.catalog.Complete = false
				d.catalogError = errorCategory(result.err)
			}
			d.nextRefresh = d.clock.Now().Add(d.config.Limits.PollInterval)
			if d.config.Limits.PollInterval <= 0 {
				d.nextRefresh = d.clock.Now().Add(30 * time.Second)
			}
			var quota *GitHubError
			if errors.As(result.err, &quota) && quota.RetryAt.After(d.nextRefresh) {
				d.nextRefresh = quota.RetryAt
			}
		default:
		}
	}
	if d.refresh == nil && !d.clock.Now().Before(d.nextRefresh) {
		d.refresh = make(chan nativeRefresh, 1)
		ch := d.refresh
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), NativeCatalogTimeout)
			defer cancel()
			catalog, err := d.github.NativeSnapshot(ctx, d.config, d.master)
			ch <- nativeRefresh{catalog, err}
		}()
	}
}
func (d *NativeDispatcher) Ready() bool {
	d.poll()
	if d.held {
		if err := d.updateRecoveryHold(); err != nil {
			return true // Tick reports the error without admitting work.
		}
		if d.held {
			return d.resumableBlocked() != nil
		}
	}
	candidates, err := d.candidates(d.catalog)
	recovery, recoveryErr := d.automaticRecoveries()
	stale, staleErr := d.staleRetirements()
	return err != nil || recoveryErr != nil || staleErr != nil || len(recovery) > 0 || len(stale) > 0 || len(candidates) > 0 || len(d.dependencyWaits()) > 0
}
func (d *NativeDispatcher) Tick(ctx context.Context) (bool, error) {
	d.poll()
	if d.step != nil {
		select {
		case result := <-d.step:
			if d.stepCancel != nil {
				d.stepCancel()
				d.stepCancel = nil
			}
			d.step = nil
			x := result.execution
			d.history[x.Job.ID] = x
			d.active = &x
			if x.Phase == "completed" || x.Phase == "blocked" || x.Phase == "cancelled" {
				d.active = nil
			}
			if err := d.updateRecoveryHold(); err != nil {
				return false, err
			}
			if result.completed || x.Phase == "cancelled" {
				d.nextRefresh = time.Time{}
				d.catalog.Complete = false
			}
			return result.completed, result.err
		default:
			return false, nil
		}
	}
	if err := d.updateRecoveryHold(); err != nil {
		return false, err
	}
	if d.active == nil {
		if d.held {
			d.active = d.resumableBlocked()
			if d.active == nil {
				return false, nil
			}
		}
	}
	if d.active == nil {
		candidates, err := d.candidates(d.catalog)
		if err != nil {
			return false, err
		}
		var counters ScheduleCounters
		var finished []NativeExecution
		for _, x := range d.history {
			if x.Phase == "completed" {
				finished = append(finished, x)
			}
		}
		sort.Slice(finished, func(i, j int) bool { return finished[i].Job.TerminalAt.Before(finished[j].Job.TerminalAt) })
		for _, x := range finished {
			counters = completedService(counters, x.Job)
		}
		jobs := []Job{}
		for _, v := range candidates {
			jobs = append(jobs, v.Job)
		}
		waiting := d.dependencyWaits()
		recovery, err := d.automaticRecoveries()
		if err != nil {
			return false, err
		}
		waiting = append(waiting, recovery...)
		stale, err := d.staleRetirements()
		if err != nil {
			return false, err
		}
		waiting = append(waiting, stale...)
		for _, x := range waiting {
			jobs = append(jobs, x.Job)
		}
		j, ok := ChooseTeamJob(jobs, counters)
		if !ok {
			return false, nil
		}
		for _, x := range waiting {
			if x.Job.ID == j.ID {
				copy := x
				d.active = &copy
				break
			}
		}
		for _, v := range candidates {
			if v.Job.ID == j.ID {
				x := NativeExecution{Version: 3, MasterID: d.config.Master.MasterID, InstanceID: d.config.Master.InstanceID, Generation: d.config.Master.Generation, Goal: v.Goal.Revision.Ref, Job: j, Phase: "selected", SelectedAt: d.clock.Now()}
				if v.Task != nil {
					b := v.Task.Binding
					x.Binding = &b
				}
				if err := saveNativeExecution(d.config, x); err != nil {
					return false, err
				}
				d.active = &x
				break
			}
		}
	}
	if d.active == nil {
		return false, nil
	}
	x := *d.active
	d.step = make(chan nativeStep, 1)
	ch := d.step
	timeout := githubRevalidationTimeout
	if d.active.Phase == "blocked" && d.active.Reason == "native_completed_without_result" {
		timeout = 5 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	d.stepCancel = cancel
	go func() {
		defer cancel()
		done, err := d.advance(stepCtx, &x)
		ch <- nativeStep{x, done, err}
	}()
	return false, nil
}
func verifyNativePlacement(ctx context.Context, cfg Config, m NativeEngineeringMaster) error {
	self, err := m.Self(ctx)
	if err != nil {
		return err
	}
	var connection masterConnectionState
	raw, err := readPrivate(filepath.Join(masterDir(cfg), "connection.json"), 4096)
	if err != nil {
		return c.ErrEpoch
	}
	if strictTeamJSON(raw, &connection) != nil || connection.Version != 1 || connection.MasterID != cfg.Master.MasterID || connection.InstanceID != cfg.Master.InstanceID || connection.Generation != cfg.Master.Generation || connection.Epoch < 1 || connection.Epoch != self.RestoreEpoch {
		return c.ErrEpoch
	}
	want := c.Placement{AgentID: cfg.Master.AgentID, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}
	if self.Principal.ID != cfg.Master.AgentID || self.Principal.Kind != "agent" || self.Principal.GitHubBotID != cfg.Self.ID || self.Placement != want || !containsExact(self.Principal.Engagements, cfg.Master.Engagement) {
		return c.ErrForbidden
	}
	return nil
}
func containsExact(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func verifyNativeThreads(ctx context.Context, cfg Config, m NativeEngineeringMaster, a c.GoalAuthority) error {
	dest := a.Revision.Proposal.QA.EvidenceDestination
	if a.Revision.Proposal.QA.Required && (dest.MasterID != cfg.Master.MasterID || dest.Engagement != cfg.Master.Engagement) {
		return c.ErrForbidden
	}
	for _, id := range []string{a.Revision.Proposal.ThreadID, dest.ThreadID} {
		if id == "" {
			continue
		}
		th, err := m.Thread(ctx, id)
		if err != nil {
			return err
		}
		if th.ID != id || th.Engagement != cfg.Master.Engagement {
			return c.ErrForbidden
		}
		for _, principal := range []string{a.Actors.Author.PrincipalID, a.Actors.Peer.PrincipalID, a.Actors.QA.PrincipalID} {
			if principal != "" && !containsExact(th.Members, principal) {
				return c.ErrForbidden
			}
		}

	}
	return nil
}
func nativeCurrentGoal(ctx context.Context, cfg Config, m NativeEngineeringMaster, ref c.GoalRef) (c.GoalAuthority, error) {
	if err := verifyNativePlacement(ctx, cfg, m); err != nil {
		return c.GoalAuthority{}, err
	}
	a, err := m.ResolveGoal(ctx, ref)
	if err != nil {
		return a, err
	}
	if !a.Valid || a.State != "approved" || a.Revision.Ref != ref || a.ActiveRevisionSHA256 != ref.RevisionSHA256 || ref.MasterID != cfg.Master.MasterID {
		return a, c.ErrForbidden
	}
	if err = verifyNativeThreads(ctx, cfg, m, a); err != nil {
		return a, err
	}
	return a, nil
}
func (d *NativeDispatcher) freshCandidate(ctx context.Context, x NativeExecution) (NativeCandidate, error) {
	authority, err := nativeCurrentGoal(ctx, d.config, d.master, x.Goal)
	if err != nil {
		// A resolved exact revision can conclusively lose approval. Placement,
		// epoch, membership and transport failures do not prove supersession.
		if errors.Is(err, c.ErrForbidden) && authority.Revision.Ref == x.Goal && (authority.ActiveRevisionSHA256 != x.Goal.RevisionSHA256 || authority.State == "completed" || authority.State == "rejected") {
			return NativeCandidate{}, errors.Join(errNativeCandidateStale, err)
		}
		return NativeCandidate{}, err
	}
	catalog, err := nativeValidationSnapshot(ctx, d.config, d.master, d.github, x.Job, nativeWaitReferences(x)...)
	if err != nil {
		return NativeCandidate{}, err
	}
	if !catalog.Complete {
		return NativeCandidate{}, errors.New("native catalog unavailable or incomplete")
	}
	if x.Wait != nil && !nativeDependenciesReady(catalog, x.Wait) {
		return NativeCandidate{}, errNativeDependencyWait
	}
	selection := catalog
	if x.Job.Kind == "plan" && (len(x.Turns) > 0 || x.Phase == "blocked" && (resumableNativeReason(x.Reason) || x.Reason == "native_completed_without_result")) {
		// Registration removes a plan from discovery, but does not finish its saved
		// turn. Revalidate continuation policy/identity against the exact approval;
		// retain unfinished rebind instructions without requiring new-plan eligibility.
		if x.Job.InputRevision != fmt.Sprintf("%s:%d", authority.Revision.Ref.RevisionSHA256, authority.ApprovalSequence) {
			return NativeCandidate{}, errNativeCandidateStale
		}
		selection.Tasks = nil
		for _, task := range catalog.Tasks {
			if task.Binding.Goal == x.Goal && task.Binding.ApprovalSequence < authority.ApprovalSequence && !task.Task.Completed {
				selection.Tasks = append(selection.Tasks, task)
			}
		}
	}
	candidates := NativeCandidates(d.config, selection, nil, d.clock.Now())
	for _, v := range candidates {
		if v.Job.ID == x.Job.ID {
			return v, nil
		}
	}
	for _, v := range candidates {
		if v.Job.Kind == x.Job.Kind && v.Job.TaskURL == x.Job.TaskURL && v.Job.PRURL == x.Job.PRURL {
			return NativeCandidate{}, errNativeCandidateStale
		}
	}
	if nativeScopeChanged(x, catalog) {
		return NativeCandidate{}, errNativeCandidateStale
	}
	if nativeIsChecksRepair(x.Job) {
		for _, task := range catalog.Tasks {
			for _, pr := range task.PRs {
				if pr.URL == x.Job.PRURL && pr.HeadSHA == x.Job.HeadSHA && nativeChecksRecovered(pr) {
					return NativeCandidate{}, errNativeCandidateStale
				}
			}
		}
	}
	return NativeCandidate{}, errors.New("native candidate temporarily unavailable")
}

// A complete read can establish a changed immutable binding or exact PR
// scope. Pending checks/readiness alone do not retire a prepared identity.
func nativeScopeChanged(x NativeExecution, catalog NativeCatalog) bool {
	if x.Binding == nil {
		for _, task := range catalog.Tasks {
			if task.Binding.Goal == x.Goal {
				return true
			}
		}
		return false
	}
	for _, task := range catalog.Tasks {
		if task.Task.URL != x.Job.TaskURL {
			continue
		}
		if NativeAssignmentSHA(task.Binding) != NativeAssignmentSHA(*x.Binding) || task.Task.BodySHA != x.Job.ScopeSHA || task.Task.Closed || task.Task.Completed {
			return true
		}
		if x.Job.PRURL == "" {
			for _, pr := range task.PRs {
				if !pr.Closed || pr.Merged {
					return true
				}
			}
			return false
		}
		for _, pr := range task.PRs {
			if pr.URL == x.Job.PRURL {
				return pr.HeadSHA != x.Job.HeadSHA || pr.ScopeRevision != x.Job.PRScopeRevision || pr.AssignmentSHA != x.Job.Master.AssignmentSHA || pr.Closed || pr.Merged
			}
		}
		return true
	}
	return true
}

var errNativeCandidateStale = errors.New("native task, PR, checks or review scope no longer current")

func (d *NativeDispatcher) rejectUnsubmitted(x *NativeExecution, cause error) (bool, error) {
	if errors.Is(cause, errNativeDependencyWait) {
		return d.parkDependencyContinuation(x)
	}
	if !errors.Is(cause, errNativeCandidateStale) {
		return false, cause
	}
	x.Phase, x.Reason = "cancelled", "native_scope_no_longer_current"
	x.Job.Phase, x.Job.TerminalAt = "cancelled", d.clock.Now()
	if err := saveNativeExecution(d.config, *x); err != nil {
		return false, err
	}
	return false, cause
}
func (d *NativeDispatcher) advance(ctx context.Context, x *NativeExecution) (bool, error) {
	// An interrupted action recovery retains a completed native job while its
	// execution is temporarily active. Restore reconciliation before the old
	// model deadline can turn it into a fresh model continuation.
	if x.Phase == "active" && x.Job.Phase == "completed" && x.Job.SubmissionState == "completed" && !x.Job.TerminalAt.IsZero() {
		if complete, err := nativeJobHasResult(d.config, *x); err != nil {
			return false, err
		} else if complete {
			x.Phase, x.Reason = "completed", ""
			return true, saveNativeExecution(d.config, *x)
		}
		x.Phase, x.Reason = "blocked", "native_completed_without_result"
		return false, saveNativeExecution(d.config, *x)
	}
	if x.Phase == "blocked" && x.Reason == "native_completed_without_result" {
		eligible, err := nativeStaleRetirementEligible(d.config, *x)
		if err != nil {
			return false, err
		}
		if eligible {
			return d.retireStaleTerminalAttempt(ctx, x)
		}
		return d.reconcileNativeAction(ctx, x)
	}
	if x.Phase == "blocked" && x.Reason == "native_dependency_wait" {
		done, err := d.resumeBlocked(ctx, x)
		if err != nil {
			if errors.Is(err, errNativeCandidateStale) {
				return d.rejectUnsubmitted(x, err)
			}
			x.Job.ReconcileFailures++
			x.Job.ReconcileError = errorCategory(err)
			x.Job.NextAttemptAt = d.clock.Now().Add(nativeDependencyBackoff(*x))
			if saveErr := saveNativeExecution(d.config, *x); saveErr != nil {
				return false, saveErr
			}
		}
		return done, err
	}
	if x.Phase == "blocked" && resumableNativeReason(x.Reason) {
		return d.resumeBlocked(ctx, x)
	}
	paused, pauseErr := NewVersionedFileStore(d.config.StateDir, 3).Paused()
	if pauseErr != nil {
		return false, pauseErr
	}
	if paused {
		if x.Phase == "active" {
			if err := d.code.Abort(ctx, x.Job); err != nil {
				return false, err
			}
		}
		x.Phase, x.Reason = "blocked", "local_pause"
		return false, saveNativeExecution(d.config, *x)
	}
	if x.Phase == "selected" || x.Phase == "prepared" {
		if len(x.Turns) > 0 {
			if err := d.code.CheckContinuation(ctx, x.Turns[len(x.Turns)-1]); err != nil {
				return false, err
			}
		}
		if x.Phase == "selected" {
			// Preparation can mutate the checkout. Revalidate before it, then
			// again before submission below. An already-prepared retry only
			// needs that final read, preserving its bounded admission budget.
			initial, err := d.freshCandidate(ctx, *x)
			if err != nil {
				return d.rejectUnsubmitted(x, err)
			}
			if prompt, e := nativePrompt(d.config, *x, initial); errors.Is(e, c.ErrSize) {
				return d.rejectPromptSize(x, len(prompt))
			} else if e != nil {
				return false, e
			}
			health, err := d.code.Health(ctx)
			if err != nil {
				return false, err
			}
			if !health.Healthy || health.Version != "1.18.18" {
				return false, errors.New("unsupported native server")
			}
			j, err := d.checkouts.Prepare(ctx, x.Job)
			if err != nil {
				return false, err
			}
			j, err = d.code.EnsureSession(ctx, d.config, j)
			if err != nil {
				return false, err
			}
			j.MessageID = "msg_" + randomID()
			j.SubmissionState = "prepared"
			x.Job = j
			x.Phase = "prepared"
			if err = saveNativeExecution(d.config, *x); err != nil {
				return false, err
			}
		}
		// Checkout/session preparation can outlive a pause or owner decision.
		if paused, err := NewVersionedFileStore(d.config.StateDir, 3).Paused(); err != nil || paused {
			if err != nil {
				return false, err
			}
			x.Phase, x.Reason = "blocked", "local_pause"
			return false, saveNativeExecution(d.config, *x)
		}
		if paused, err := NewVersionedFileStore(d.config.StateDir, 3).AdmissionPaused(); err != nil || paused {
			if err != nil {
				return false, err
			}
			return false, nil
		}
		candidate, err := d.freshCandidate(ctx, *x)
		if err != nil {
			return d.rejectUnsubmitted(x, err)
		}
		prompt, err := nativePrompt(d.config, *x, candidate)
		if errors.Is(err, c.ErrSize) {
			return d.rejectPromptSize(x, len(prompt))
		}
		if err != nil {
			return false, err
		}
		// A prepared native identity is never reallocated. Persist uncertainty before
		// crossing the async submission boundary; recovery only observes this pair.
		if len(x.Turns) > 0 {
			if err := d.code.CheckContinuation(ctx, x.Turns[len(x.Turns)-1]); err != nil {
				return false, err
			}
		}
		model, err := configuredModel(d.config)
		if err != nil {
			return false, err
		}
		x.Phase = "active"
		x.Job.SubmissionState = "submitting"
		x.Job.SubmittedModel, x.Job.ExecutedModel = model, nil
		x.Job.SubmittedAt = d.clock.Now()
		x.Job.Deadline = x.Job.SubmittedAt.Add(Defaults(d.config).Limits.TurnLimit)
		if err = saveNativeExecution(d.config, *x); err != nil {
			return false, err
		}
		turn, submitErr := d.code.Submit(ctx, d.config, x.Job, prompt)
		x.Job = observedModel(x.Job, turn)
		x.Job.SubmissionState = "unknown"
		if turn.State == "not_submitted" && !turn.Accepted {
			x.Phase = "prepared"
			x.Job.SubmissionState = "prepared"
		}
		if submitErr == nil && turn.Accepted {
			x.Job.SubmissionState = "accepted"
		}
		if err = saveNativeExecution(d.config, *x); err != nil {
			return false, err
		}
		return false, submitErr
	}
	if x.Phase != "active" {
		return false, nil
	}
	if !x.Job.Deadline.IsZero() && !d.clock.Now().Before(x.Job.Deadline) {
		if err := d.code.Abort(ctx, x.Job); err != nil {
			return false, err
		}
		x.Phase, x.Reason = "blocked", "native_turn_limit"
		return false, saveNativeExecution(d.config, *x)
	}
	// An unavailable or superseded master does not erase local work. Explicit
	// owner pause alone uses the OpenCode client's exact-turn ownership check.
	a, authorityErr := d.master.ResolveGoal(ctx, x.Goal)
	if authorityErr == nil && a.State == "paused" {
		if err := d.code.Abort(ctx, x.Job); err != nil {
			return false, err
		}
		x.Phase, x.Reason = "blocked", "goal_paused"
		x.PausedDecisionSequence = a.DecisionSequence
		return false, saveNativeExecution(d.config, *x)
	}
	turn, err := d.code.Observe(ctx, x.Job)
	if err != nil {
		x.Job.ReconcileFailures++
		x.Job.ReconcileError = errorCategory(err)
		if x.Job.ReconcileFailures >= Defaults(d.config).Limits.MaxAttempts {
			x.Phase, x.Reason = "blocked", "native_observation_unavailable"
		}
		if saveErr := saveNativeExecution(d.config, *x); saveErr != nil {
			return false, saveErr
		}
		return false, err
	}
	x.Job.ReconcileFailures = 0
	x.Job.ReconcileError = ""
	x.Job = observedModel(x.Job, turn)
	if !turn.Terminal {
		return false, saveNativeExecution(d.config, *x)
	}
	x.Job.TerminalAt = d.clock.Now()
	x.Job.SubmissionState = "completed"
	x.Job.Phase = "completed"
	if turn.ErrorCategory != "" {
		x.Phase, x.Reason = "blocked", "native_execution_failed"
	} else {
		complete, err := nativeJobHasResult(d.config, *x)
		if err != nil {
			return false, err
		}
		if !complete {
			if retired, err := d.retireRecoveredChecksRepair(ctx, x); err != nil {
				return false, err
			} else if retired {
				return false, nil
			}
			x.Phase, x.Reason = "blocked", "native_completed_without_result"
			x.Wait = nil
			wait, e := nativeTurnWait(d.config, *x)
			if e != nil {
				return false, e
			}
			if wait != nil {
				x.Wait = wait
				x.Reason = "native_dependency_wait"
			}
		} else {
			x.Phase = "completed"
		}
	}
	if err = saveNativeExecution(d.config, *x); err != nil {
		return false, err
	}
	return x.Phase == "completed", nil
}
func nativePrompt(cfg Config, x NativeExecution, v NativeCandidate) (string, error) {
	goal, _ := json.Marshal(v.Goal.Revision)
	binding, _ := json.Marshal(x.Binding)
	taskLifecycle := "Task creation and rebinding can select a configured Project with project_owner and project_number; one configured Project is selected automatically and confirmed Ready."
	if kataTaskSource(cfg) {
		taskLifecycle = "Kata is the authoritative task source for new tasks. Use canonical kata://<lowercase-project-uid>/<uppercase-issue-uid>/<owner>/<repo> task references returned by the guarded helper. Kata task creation and rebinding require no GitHub issue or Project writes and no Project Ready status. Retained GitHub task references keep their existing guarded lifecycle. Use the same native action helper for both sources."
	}
	prompt := fmt.Sprintf("You are %s, authenticated agent %s and immutable GitHub bot %d. Execute only the assigned %s stage for the approved native goal below. Preserve local work if authority becomes unavailable or superseded. Use /usr/local/libexec/coop-worker native action --config /var/lib/coop/worker/config.json --job %s --input /absolute/private/action.json for all consequential writes. Never self-review. Every action JSON requires operation and a stable request_id. When an implement/fix task needs an existing approved task to finish, checkpoint local work, call wait (depends_on canonical task URLs,body blocker reason), then end this turn. Coop resumes the same session after those exact dependencies complete. Do not wait in a loop or create a replacement task. Supported operations are wait, create-task (repository,title,body,base_branch,depends_on), rebind-task (task_url,base_branch,depends_on), publish-pr (title,body,artifact optional), review (verdict approved or changes-needed,body), qa (verdict,report_body private), deliver, and complete-plan. %s The helper derives role, goal, assignment, exact head and evidence metadata from fresh authenticated sources. Use complete-plan after all task registrations. Checkpoint local code before publish-pr; the helper pushes the assigned branch and derives the immutable source artifact when artifact is omitted. QA must test the approved acceptance and environment revision. Private QA belongs only in report_body. An uncertain helper outcome requires operator inspection; preserve its request_id and never bypass the journal with a new request.\nApproved revision: %s\nAssignment: %s\nTask: %s\nPR: %s\nExact head: %s", cfg.Name, cfg.Master.AgentID, cfg.Self.ID, x.Job.Kind, x.Job.ID, taskLifecycle, goal, binding, x.Job.TaskURL, x.Job.PRURL, x.Job.HeadSHA)
	if v.Task != nil {
		prompt += "\nTask title: " + v.Task.Task.Title + "\nTask body:\n" + v.Task.Task.Body
	}
	if nativeIsBranchRefresh(x.Job) && v.PR != nil {
		prompt += "\nThis author fix is a branch refresh. Merge the approved base branch " + v.PR.BaseBranch + " at exact commit " + v.PR.BaseSHA + " into the assigned PR branch, resolving conflicts within this task's approved scope. Fetch and verify that exact base commit before merging. Preserve the PR history and all local work; never force-push, reset away changes, or change the approved base. Checkpoint and validate the merged result, then use publish-pr with artifact omitted so the helper regenerates immutable exact-head source metadata. New CI, independent peer review and exact-artifact QA remain required before deliver. Do not request another goal approval for this routine branch maintenance. If the merge cannot be resolved within approved scope, checkpoint and report the concrete conflict rather than retrying in a loop."
	}
	if nativeIsChecksRepair(x.Job) && v.PR != nil {
		prompt += "\nThis author fix repairs failed checks for exact PR head " + x.Job.HeadSHA + ". Inspect the failed GitHub check runs and logs for that head, identify the cause, and make the smallest repair within the existing approved task. Run the failed checks locally, checkpoint a new head, and publish through publish-pr with artifact omitted to regenerate immutable source metadata. Do not create empty commits or rerun unchanged failures in a loop. Fresh CI, independent peer review and required exact-artifact QA remain mandatory. If an infrastructure or permission problem prevents a scoped code repair, checkpoint and report the concrete blocker. No new goal approval is required for this repair."
	}
	if x.ContinuationRequestID != "" && x.ContinuationSourceMessageID != "" {
		prompt += "\nCoordinator continuation: read exact message " + x.ContinuationSourceMessageID + " in the approved private thread " + x.Job.Master.ThreadID + ". Read that handoff before editing; it contains the coordination context for this retained task. Continue only the existing approved assignment and stage; this continuation grants no new goal, review, QA or merge authority."
	}
	if len(v.RebindTasks) > 0 {
		prompt += "\nRenewed approval requires explicit rebind-task for these retained task URLs before further execution: " + strings.Join(v.RebindTasks, ", ")
	}
	if len(prompt) > 32<<10 {
		return prompt, c.ErrSize
	}
	return prompt, nil
}

func (d *NativeDispatcher) rejectPromptSize(x *NativeExecution, size int) (bool, error) {
	x.Phase = "cancelled"
	x.Reason = fmt.Sprintf("prompt_size: %d bytes exceeds %d", size, 32<<10)
	x.Job.Phase = "cancelled"
	x.Job.TerminalAt = d.clock.Now()
	return false, saveNativeExecution(d.config, *x)
}

func resumableNativeReason(reason string) bool {
	return reason == "goal_paused" || reason == "native_turn_limit"
}

func (d *NativeDispatcher) resumableBlocked() *NativeExecution {
	if !d.catalog.Complete {
		return nil
	}
	for _, x := range d.history {
		if x.Phase == "blocked" && !resumableNativeReason(x.Reason) {
			if held, err := nativeExecutionRecoveryHold(d.config, x); err != nil || held {
				return nil
			}
		}
	}
	for _, x := range d.history {
		if x.Phase != "blocked" || !resumableNativeReason(x.Reason) {
			continue
		}
		for _, a := range d.catalog.Goals {
			if a.Valid && a.State == "approved" && a.Revision.Ref == x.Goal && (x.Reason == "native_turn_limit" || a.DecisionSequence > x.PausedDecisionSequence) {
				copy := x
				return &copy
			}
		}
	}
	return nil
}
func (d *NativeDispatcher) resumeBlocked(ctx context.Context, x *NativeExecution) (bool, error) {
	return d.resumeBlockedWithRequest(ctx, x, "", "", nil)
}
func (d *NativeDispatcher) resumeBlockedWithRequest(ctx context.Context, x *NativeExecution, requestID, sourceMessageID string, check func(context.Context) error) (bool, error) {
	a, e := nativeCurrentGoal(ctx, d.config, d.master, x.Goal)
	if e != nil {
		return false, e
	}
	if x.Reason == "goal_paused" && a.DecisionSequence <= x.PausedDecisionSequence {
		return false, c.ErrForbidden
	}
	paused, e := NewVersionedFileStore(d.config.StateDir, 3).Paused()
	if e != nil {
		return false, e
	}
	if paused {
		return false, nil
	}
	if paused, e = NewVersionedFileStore(d.config.StateDir, 3).AdmissionPaused(); e != nil || paused {
		return false, e
	}
	release, e := lockNativeProducer(d.config)
	if e != nil {
		return false, e
	}
	defer release()
	if requestID != "" {
		records, err := loadNativeJobActions(d.config, *x)
		if err != nil {
			return false, err
		}
		for _, r := range records {
			if r.State != "completed" {
				return false, errContinuationActions
			}
		}
	}
	if uncertain, e := nativeJobActionUncertain(d.config, x.Job.ID); e != nil {
		return false, e
	} else if uncertain {
		return false, errors.New("blocked native job has uncertain actions")
	}
	observed, e := d.code.Observe(ctx, x.Job)
	if e != nil {
		return false, e
	}
	if !observed.Terminal || observed.SessionID != x.Job.SessionID || observed.MessageID != x.Job.MessageID {
		return false, errors.New("blocked native turn lacks exact terminal evidence")
	}
	x.Job = observedModel(x.Job, observed)
	x.Job.SubmissionState = "completed"
	x.Job.Phase = "completed"
	x.Job.TerminalAt = d.clock.Now()
	if done, e := nativeJobHasResult(d.config, *x); e != nil {
		return false, e
	} else if done {
		x.Phase, x.Reason = "completed", ""
		return true, saveNativeExecution(d.config, *x)
	}
	if e = d.code.CheckContinuation(ctx, x.Job); e != nil {
		return false, e
	}
	if _, e = d.freshCandidate(ctx, *x); e != nil {
		return false, e
	}
	if pending := x.PendingContinuation; pending != nil {
		if pending.ID != x.Job.ID || pending.SessionID != x.Job.SessionID || pending.Master == nil || pending.Master.AssignmentSHA != x.Job.Master.AssignmentSHA || pending.ContinuationMessageID != x.Job.MessageID || pending.SubmissionState != "" && pending.SubmissionState != "prepared" {
			return false, c.ErrConflict
		}
		if requestID != "" {
			if check == nil {
				return false, c.ErrForbidden
			}
			if err := check(ctx); err != nil {
				return false, err
			}
			recordContinuation(x, requestID, sourceMessageID)
		}
		x.Job = *pending
		x.Job.ReconcileFailures = 0
		x.Job.ReconcileError = ""
		x.Job.NextAttemptAt = time.Time{}
		x.PendingContinuation = nil
		x.Phase, x.Reason = "selected", ""
		x.SelectedAt = d.clock.Now()
		if x.Job.SubmissionState == "prepared" {
			x.Phase = "prepared"
		}
		return false, saveNativeExecution(d.config, *x)
	}
	if len(x.Turns) >= Defaults(d.config).Limits.MaxAttempts {
		x.Reason = "native_continuation_limit"
		if e = saveNativeExecution(d.config, *x); e != nil {
			return false, e
		}
		return false, errors.New("native continuation limit reached")
	}
	if requestID != "" {
		if check == nil {
			return false, c.ErrForbidden
		}
		if err := check(ctx); err != nil {
			return false, err
		}
		recordContinuation(x, requestID, sourceMessageID)
	}
	x.Turns = append(x.Turns, x.Job)
	x.Job.ContinuationMessageID = x.Job.MessageID
	x.Job.MessageID = ""
	x.Job.SubmittedModel, x.Job.ExecutedModel = nil, nil
	x.Job.SubmissionState = ""
	x.Job.Deadline = time.Time{}
	x.Job.SubmittedAt = time.Time{}
	x.Job.TerminalAt = time.Time{}
	x.Job.ReconcileFailures = 0
	x.Job.ReconcileError = ""
	x.SelectedAt = d.clock.Now()
	x.Phase, x.Reason = "selected", ""
	return false, saveNativeExecution(d.config, *x)
}
