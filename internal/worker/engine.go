package worker

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"time"
)

type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }
func (RealClock) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type Engine struct {
	asynchronous                        bool
	poll                                chan snapshotPoll
	pollCancel                          context.CancelFunc
	lastSnapshotStarted                 time.Time
	Config                              Config
	Wakes                               *WakeCoordinator
	apiNotBefore                        time.Time
	GitHub                              GitHub
	Store                               Store
	Checkouts                           Checkouts
	OpenCode                            OpenCode
	Clock                               Clock
	state                               State
	status                              Status
	initialized                         bool
	nextPoll, nextObserve, nextDispatch time.Time
}

func (e *Engine) initialize() error {
	if e.initialized {
		return nil
	}
	e.Config = Defaults(e.Config)
	if err := ValidateConfig(e.Config); err != nil {
		return err
	}
	if e.Clock == nil {
		e.Clock = RealClock{}
	}
	state, err := e.Store.Load()
	if err != nil {
		return err
	}
	e.state = state
	if e.state.Attempts == nil {
		e.state.Attempts = map[string]int{}
	}
	e.status = Status{Name: e.Config.Name, Enabled: true, State: "starting"}
	if wakeEnabled(e.Config) {
		if e.Wakes == nil {
			e.Wakes = NewWakeCoordinator(e.Config, NewWakeStore(filepath.Join(e.Config.StateDir, "wake", "v1")), e.GitHub, e.OpenCode, e.Clock)
		}
		e.Wakes.Paused = e.paused
		routing, err := e.Wakes.Store.Routing()
		if err != nil {
			return err
		}
		e.apiNotBefore = routing.APINotBefore
	}
	e.initialized = true
	return e.Store.Save(e.state)
}
func (e *Engine) save() error         { return e.Store.Save(e.state) }
func (e *Engine) saveJob(j Job) error { e.state.Jobs[j.ID] = j; return e.save() }
func (e *Engine) active() (Job, bool) {
	var ids []string
	for id, j := range e.state.Jobs {
		if j.Phase == "active" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		return e.state.Jobs[ids[0]], true
	}
	return Job{}, false
}
func (e *Engine) paused() (bool, error) {
	if control, ok := e.Store.(interface{ Paused() (bool, error) }); ok {
		return control.Paused()
	}
	return false, nil
}
func errorCategory(err error) string {
	var size *PromptSizeError
	if errors.As(err, &size) {
		return "prompt_size"
	}
	var gh *GitHubError
	if errors.As(err, &gh) {
		return gh.Category
	}
	var code *OpenCodeError
	if errors.As(err, &code) {
		return code.Category
	}
	return "execution"
}
func authorizationLost(err error) bool {
	switch errorCategory(err) {
	case "unreadable_content", "goal_discussion_not_discovered", "unexpected_bot_identity", "permission_denied", "not_found":
		return true
	}
	return false
}
func (e *Engine) refreshSynchronous(ctx context.Context) error {
	now := e.Clock.Now()
	health, healthErr := e.OpenCode.Health(ctx)
	e.status.ServerHealthy = healthErr == nil && health.Healthy
	e.status.ServerVersion = health.Version
	if healthErr != nil {
		e.nextDispatch = now.Add(e.Config.Limits.RetryBase)
	} else {
		e.nextDispatch = time.Time{}
	}
	s, err := e.GitHub.Snapshot(ctx, e.Config, e.state.SnapshotCache)
	e.nextPoll = now.Add(e.Config.Limits.PollInterval + time.Duration(rand.Int64N(int64(e.Config.Limits.PollInterval/10)+1)))
	if err != nil {
		e.noteAPIError(err, e.nextPoll)
		var gh *GitHubError
		if errors.As(err, &gh) && gh.RetryAt.After(e.apiNotBefore) {
			e.apiNotBefore = gh.RetryAt
		}
		if errors.As(err, &gh) && gh.RetryAt.After(e.nextPoll) {
			e.nextPoll = gh.RetryAt
		}
		e.status.LastError = errorCategory(err)
		return err
	}
	if !s.Complete {
		return ghError("incomplete_snapshot")
	}
	if e.Wakes != nil {
		if err := e.Wakes.Ingest(s); err != nil {
			return err
		}
	}
	e.state.SnapshotCache = s
	e.state.Cursor = s.Cursor
	e.status.LastPoll = now
	e.status.LastError = ""
	if healthErr != nil {
		e.status.LastError = errorCategory(healthErr)
	}
	return e.save()
}
func (e *Engine) continuationAllowed(j Job) (bool, string) {
	s := e.state.SnapshotCache
	if j.Kind == "plan" {
		return Admit(e.Config, j, s, e.Clock.Now())
	}
	g, ok := goalFor(s, j.GoalURL)
	if !ok {
		return false, "scope"
	}
	if EvaluateGoal(e.Config, g, e.Clock.Now()).State != "approved" {
		return false, "scope"
	}
	t, ok := taskFor(s, j.TaskURL)
	if !ok {
		return false, "scope"
	}
	if t.Closed && !t.Completed {
		return false, "task_cancelled"
	}
	if j.ScopeSHA != "" && t.BodySHA != j.ScopeSHA {
		return false, "scope"
	}
	check := t
	check.Completed = false
	if t.Completed {
		if !artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, s)) {
			return false, "task_closed"
		}
		check.Closed = false // Observe the terminal turn after verified delivery.
	}
	if !taskAdmitted(e.Config, check, g, s) {
		return false, "scope"
	}
	if j.Kind == "implement" {
		if t.Claim == nil || t.Claim.InstanceID != e.state.InstanceID {
			return false, "claim"
		}
		return true, "owned implementation"
	}
	p, ok := prFor(s, j.PRURL)
	if !ok {
		return false, "PR unavailable"
	}
	if p.HeadSHA != j.HeadSHA && !artifactCompletes(e.Config, j, Artifacts{PR: &p}) {
		return false, "head changed"
	}
	if artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, s)) {
		return true, "verified artifact"
	}
	return Admit(e.Config, j, s, e.Clock.Now())
}
func artifactCompletes(c Config, j Job, a Artifacts) bool {
	if j.Kind == "plan" {
		return a.PlanResponded
	}
	if a.PR == nil || !sameActor(a.PR.Author, c.Implementer) || a.PR.TaskURL != j.TaskURL || a.PR.GoalURL != j.GoalURL {
		return false
	}
	switch j.Kind {
	case "implement":
		return !a.PR.Closed && a.PR.Branch == j.Branch
	case "fix":
		return a.PR.HeadSHA != j.HeadSHA && !a.PR.Closed
	case "review":
		r := latestReview(c, *a.PR)
		return a.PR.HeadSHA == j.HeadSHA && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED")
	case "deliver":
		return a.PR.Merged && a.IssueCompleted
	}
	return false
}
func (e *Engine) stop(ctx context.Context, j Job, reason string) error {
	if j.WaitReason == "abort_unconfirmed" && e.Clock.Now().Before(j.NextAttemptAt) {
		return nil
	}
	if j.SessionID != "" {
		if err := e.OpenCode.Abort(ctx, j); err != nil {
			j.WaitReason = "abort_unconfirmed"
			j.NextAttemptAt = e.Clock.Now().Add(e.Config.Limits.RetryBase)
			e.status.LastError = "abort_unconfirmed"
			_ = e.saveJob(j)
			return err
		}
	}
	j.Phase = "paused"
	j.WaitReason = reason
	j.SubmissionState = "interrupted"
	e.nextObserve = time.Time{}
	return e.saveJob(j)
}
func (e *Engine) failed(ctx context.Context, j Job, category string) error {
	key := BindingKey(e.Config, j)
	e.state.Attempts[key]++
	j.Attempts = e.state.Attempts[key]
	j.WaitReason = category
	j.MessageID = ""
	j.SubmissionState = ""
	j.Deadline = time.Time{}
	j.SubmittedAt = time.Time{}
	if j.Attempts >= e.Config.Limits.MaxAttempts {
		j.Phase = "blocked"
		if err := e.saveJob(j); err != nil {
			return err
		}
		return e.reportBlock(ctx, j, category)
	}
	delay := e.Config.Limits.RetryBase
	for i := 1; i < j.Attempts; i++ {
		delay *= 2
		if delay >= e.Config.Limits.RetryCap {
			delay = e.Config.Limits.RetryCap
			break
		}
	}
	j.NextAttemptAt = e.Clock.Now().Add(delay)
	j.Phase = "waiting"
	return e.saveJob(j)
}
func (e *Engine) observe(ctx context.Context, j Job) error {
	turn, err := e.OpenCode.Observe(ctx, j)
	e.nextObserve = e.Clock.Now().Add(e.Config.OpenCode.ReconcileInterval)
	if err != nil {
		e.status.LastError = errorCategory(err)
		if j.SubmissionState == "unknown" {
			j.WaitReason = "submission_unknown"
			if saveErr := e.saveJob(j); saveErr != nil {
				return saveErr
			}
			if blockErr := e.reportBlock(ctx, j, "submission_unknown"); blockErr != nil {
				e.status.LastError = errorCategory(blockErr)
			}
		}
		return nil
	}
	j.SubmissionState = turn.State
	if turn.Accepted && turn.State != "blocked" {
		j.WaitReason = ""
	}
	if turn.State == "blocked" {
		if err := e.OpenCode.Abort(ctx, j); err != nil {
			j.WaitReason = "abort_unconfirmed"
			j.NextAttemptAt = e.Clock.Now().Add(e.Config.Limits.RetryBase)
			return e.saveJob(j)
		}
		j.Phase = "blocked"
		j.WaitReason = "permission"
		if err := e.saveJob(j); err != nil {
			return err
		}
		return e.reportBlock(ctx, j, "permission")
	}
	if !turn.Terminal {
		return e.saveJob(j)
	}
	if j.TerminalAt.IsZero() {
		j.TerminalAt = e.Clock.Now()
	}
	if e.asynchronous && !artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, e.state.SnapshotCache)) && (e.poll != nil || e.lastSnapshotStarted.Before(j.TerminalAt)) {
		j.WaitReason = "reconciling"
		e.nextPoll = time.Time{}
		return e.saveJob(j)
	}
	a, err := e.reconcile(ctx, j)
	if err != nil {
		return err
	}
	if artifactCompletes(e.Config, j, a) {
		j.Phase = "completed"
		j.WaitReason = ""
		e.state.Attempts[BindingKey(e.Config, j)] = 0
		return e.saveJob(j)
	}
	if pendingReviewHandoff(e.Config, j, e.state.SnapshotCache, e.Clock.Now()) {
		// A PR can become reviewable while a planning turn is running. Yield
		// its slot without treating a normal handoff as a failed model attempt.
		j.Phase, j.WaitReason = "waiting", "review_handoff"
		j.MessageID, j.SubmissionState = "", ""
		j.Deadline, j.SubmittedAt, j.TerminalAt = time.Time{}, time.Time{}, time.Time{}
		j.NextAttemptAt = e.Clock.Now()
		e.nextObserve = time.Time{}
		return e.saveJob(j)
	}
	progress, err := e.Checkouts.Progress(ctx, j)
	if err != nil {
		return e.failed(ctx, j, "execution")
	}
	if progress != "" && progress != j.ProgressHash {
		j.ProgressHash = progress
		j.Phase = "waiting"
		j.MessageID = ""
		j.SubmissionState = ""
		j.Deadline = time.Time{}
		j.SubmittedAt = time.Time{}
		j.NextAttemptAt = e.Clock.Now()
		e.state.Attempts[BindingKey(e.Config, j)] = 0
		return e.saveJob(j)
	}
	return e.failed(ctx, j, "execution")
}
func (e *Engine) dispatch(ctx context.Context, j Job) error {
	now := e.Clock.Now()
	if health, err := e.OpenCode.Health(ctx); err != nil {
		e.status.LastError = errorCategory(err)
		e.status.ServerHealthy = false
		e.nextDispatch = now.Add(e.Config.Limits.RetryBase)
		return err
	} else {
		e.status.ServerHealthy = health.Healthy
		e.status.ServerVersion = health.Version
		e.nextDispatch = time.Time{}
	}
	if t, ok := taskFor(e.state.SnapshotCache, j.TaskURL); ok {
		j.ScopeSHA = t.BodySHA
	}
	j.Phase = "preparing"
	if err := e.saveJob(j); err != nil {
		return err
	}
	if j.Kind == "implement" {
		t, _ := taskFor(e.state.SnapshotCache, j.TaskURL)
		if t.Claim == nil {
			claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			claim, err := e.GitHub.Claim(claimCtx, j, e.state.InstanceID)
			cancel()
			if err != nil {
				return e.failed(ctx, j, "execution")
			}
			for i := range e.state.SnapshotCache.Tasks {
				if e.state.SnapshotCache.Tasks[i].URL == j.TaskURL {
					e.state.SnapshotCache.Tasks[i].Claim = &claim
				}
			}
		} else if t.Claim.InstanceID != e.state.InstanceID {
			j.Phase = "blocked"
			j.WaitReason = "manual_claim"
			return e.saveJob(j)
		}
	}
	if j.Kind == "fix" {
		p, _ := prFor(e.state.SnapshotCache, j.PRURL)
		review := latestReview(e.Config, p)
		seenKey := "review:" + review.ID
		if e.state.Seen[seenKey] == "" {
			e.state.ReviewCycles[j.TaskURL]++
			e.state.Seen[seenKey] = "counted"
		}
		if e.state.ReviewCycles[j.TaskURL] > e.Config.Limits.MaxReviewCycles {
			j.Phase = "blocked"
			j.WaitReason = "review_limit"
			if err := e.saveJob(j); err != nil {
				return err
			}
			return e.reportBlock(ctx, j, "review_limit")
		}
	}
	key := BindingKey(e.Config, j)
	if binding, ok := e.state.Sessions[key]; ok {
		j.Checkout = binding.Checkout
		j.Branch = binding.Branch
		j.SessionID = binding.SessionID
	}
	if checker, ok := e.OpenCode.(interface {
		CheckIdle(context.Context, Job) error
	}); ok && j.SessionID != "" {
		if err := checker.CheckIdle(ctx, j); err != nil {
			if errorCategory(err) == "session_busy" {
				return e.holdBusy(j)
			}
			return e.failed(ctx, j, errorCategory(err))
		}
	}
	prepared, err := e.Checkouts.Prepare(ctx, j)
	if err != nil {
		return e.failed(ctx, j, "execution")
	}
	j = prepared
	if j.ProgressHash == "" {
		j.ProgressHash, err = e.Checkouts.Progress(ctx, j)
		if err != nil {
			return e.failed(ctx, j, "execution")
		}
	}
	prepared, err = e.OpenCode.EnsureSession(ctx, e.Config, j)
	if err != nil {
		if errorCategory(err) == "session_busy" {
			return e.holdBusy(j)
		}
		return e.failed(ctx, j, "execution")
	}
	j = prepared
	e.state.Sessions[key] = SessionBinding{TaskURL: j.TaskURL, GoalURL: j.GoalURL, Role: e.Config.Role, Kind: j.Kind, Checkout: j.Checkout, Branch: j.Branch, SessionID: j.SessionID}
	// Checkout/session preparation can take time. Re-read the selected inputs,
	// then the local control, immediately before creating submission intent.
	if reader, ok := e.GitHub.(interface {
		Revalidate(context.Context, Job, Snapshot) (Snapshot, error)
	}); ok {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		snapshot, checkErr := reader.Revalidate(checkCtx, j, e.state.SnapshotCache)
		cancel()
		if checkErr != nil {
			return e.failed(ctx, j, errorCategory(checkErr))
		}
		e.state.SnapshotCache = snapshot
		if allowed, reason := e.continuationAllowed(j); !allowed {
			j.Phase, j.WaitReason = "paused", reason
			return e.saveJob(j)
		}
	}
	paused, err := e.paused()
	if err != nil {
		return err
	}
	if paused {
		j.Phase, j.WaitReason = "paused", "local_pause"
		return e.saveJob(j)
	}
	now = e.Clock.Now()
	policy := ""
	if j.Kind == "plan" {
		if !e.state.LastProposalAt.IsZero() && now.Sub(e.state.LastProposalAt) < 24*time.Hour {
			policy = "\nDo not create a new-goal proposal in this turn; the daily proposal allowance has been used. Continue planning within approved goals and responding to existing conversations.\n"
		}
	}
	prompt, err := buildPrompt(e.Config, j, e.state.SnapshotCache, policy, now)
	if err != nil {
		var size *PromptSizeError
		if errors.As(err, &size) {
			j.Phase, j.WaitReason, j.PromptFailure = "blocked", "prompt_size", size
			if err := e.saveJob(j); err != nil {
				return err
			}
			return e.reportBlock(ctx, j, "prompt_size")
		}
		return e.failed(ctx, j, "prompt_build")
	}
	if j.Kind == "plan" && policy == "" {
		e.state.LastProposalAt = now
	}
	j.MessageID = "msg_" + randomID()
	j.TerminalAt = time.Time{}
	j.BlockReported = false
	j.SubmittedAt = now
	j.Deadline = now.Add(e.Config.Limits.TurnLimit)
	j.Phase = "active"
	j.SubmissionState = "submitting"
	j.WaitReason = ""
	if err := e.saveJob(j); err != nil {
		return err
	}
	turn, err := e.OpenCode.Submit(ctx, e.Config, j, prompt)
	if err != nil {
		if errorCategory(err) == "session_busy" {
			return e.holdBusy(j)
		}
		j.SubmissionState = "unknown"
		j.WaitReason = "submission_unknown"
		e.status.LastError = errorCategory(err)
	} else {
		j.SubmissionState = turn.State
	}
	e.nextObserve = now.Add(e.Config.OpenCode.ReconcileInterval)
	return e.saveJob(j)
}
func (e *Engine) syncBoard(ctx context.Context) error {
	if e.Config.Role != "planner" {
		return nil
	}
	for i, t := range e.state.SnapshotCache.Tasks {
		if t.ProjectID == "" || t.Claim == nil || !sameActor(t.Claim.Author, e.Config.Implementer) {
			continue
		}
		g, ok := goalFor(e.state.SnapshotCache, t.GoalURL)
		if !ok || !goalInScope(e.Config, g) {
			continue
		}
		state := "in_progress"
		for _, p := range e.state.SnapshotCache.PRs {
			if p.TaskURL == t.URL && p.Merged && t.Completed && sameActor(p.Author, e.Config.Implementer) {
				state = "done"
			}
		}
		option := t.InProgressID
		if state == "done" {
			option = t.DoneID
		}
		if option == t.CurrentStatusID {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := e.GitHub.SyncTask(writeCtx, Job{TaskURL: t.URL, GoalURL: t.GoalURL}, state, "")
		cancel()
		if err != nil {
			return err
		}
		e.state.SnapshotCache.Tasks[i].CurrentStatusID = option
		return e.save() // Bound mutations to one item per completed poll.
	}
	return nil
}
func (e *Engine) Tick(ctx context.Context) (result error) {
	if err := e.initialize(); err != nil {
		return err
	}
	e.status.WaitReason = ""
	e.status.PromptFailure = nil
	e.status.NextRetry = time.Time{}
	defer func() {
		e.noteAPIError(result, e.Clock.Now().Add(e.Config.Limits.RetryBase))
		if err := e.persistWakeDeadline(); err != nil {
			result = errors.Join(result, err)
		}
		e.status.Heartbeat = e.Clock.Now()
		if e.Wakes != nil {
			e.status.WakeEnabled = true
			routing, err := e.Wakes.Store.Routing()
			if err != nil {
				result = errors.Join(result, err)
			} else {
				e.status.WakeIntakeDeferred = routing.IntakeDeferred
			}
			e.status.APINotBefore = e.apiNotBefore
			summaries, err := e.Wakes.Summaries()
			if err != nil && result == nil {
				result = err
			}
			if e.status.Paused {
				for i := range summaries {
					if summaries[i].Phase == "queued" && summaries[i].Reason == "" {
						summaries[i].Reason = "local_pause"
					}
				}
			}
			e.status.Wakes = summaries
		}
		if result != nil {
			e.status.LastError = errorCategory(result)
		}
		e.status.ActiveTask = ""
		e.status.SessionID = ""
		e.status.SubmissionState = ""
		if j, ok := e.active(); ok {
			e.status.ActiveTask = j.TaskURL
			e.status.SessionID = j.SessionID
			e.status.SubmissionState = j.SubmissionState
			e.status.WaitReason = j.WaitReason
		} else if e.Wakes != nil {
			if v, active, err := e.Wakes.Active(); err != nil {
				if result == nil {
					result = err
				}
			} else if active {
				e.status.SessionID = v.Job.SessionID
				e.status.SubmissionState = v.Job.SubmissionState
				e.status.WaitReason = v.Reason
			} else if result == nil {
				e.idleStatus()
			}
		} else if result == nil {
			e.idleStatus()
		}
		if writer, ok := e.Store.(interface{ WriteStatus(Status) error }); ok {
			if err := writer.WriteStatus(e.status); result == nil {
				result = err
			}
		}
	}()
	paused, err := e.paused()
	if err != nil {
		return err
	}
	e.status.Paused = paused
	var hasWake bool
	if e.Wakes != nil {
		_, hasWake, err = e.Wakes.Active()
		if err != nil {
			return err
		}
		if _, hasTask := e.active(); hasTask && hasWake {
			return ghError("conflicting_active")
		}
	}
	if paused && e.Wakes != nil && hasWake {
		e.status.State = "paused"
		return e.Wakes.Pause(ctx)
	}
	if paused && e.Wakes != nil {
		if j, ok := e.active(); ok {
			e.status.State = "paused"
			return e.stop(ctx, j, "local_pause")
		}
	}
	if paused && e.Wakes == nil {
		if e.pollCancel != nil {
			e.pollCancel()
		}
		e.status.State = "paused"
		if j, ok := e.active(); ok {
			return e.stop(ctx, j, "local_pause")
		}
		return nil
	}
	if e.Wakes != nil && !paused && !hasWake {
		if r, ok, err := e.Wakes.Next(e.Clock.Now()); err != nil {
			return err
		} else if ok && e.lastSnapshotStarted.Before(r.CreatedAt) && !e.Clock.Now().Before(e.apiNotBefore) {
			e.nextPoll = time.Time{}
		}
	}
	fresh := false
	pending := false
	if e.poll != nil || !e.Clock.Now().Before(e.nextPoll) {
		if err := e.refresh(ctx); errors.Is(err, errPollPending) {
			pending = true
			e.status.State = "syncing"
		} else if err != nil {
			if authorizationLost(err) {
				if j, ok := e.active(); ok {
					if stopErr := e.stop(ctx, j, "scope"); stopErr != nil {
						return stopErr
					}
				}
			}
			e.status.State = "degraded"
			return err
		} else {
			fresh = true
			if err := e.syncBoard(ctx); err != nil {
				return err
			}
		}
	}
	if paused {
		e.status.State = "paused"
		if hasWake {
			return e.Wakes.Pause(ctx)
		}
		if j, ok := e.active(); ok {
			return e.stop(ctx, j, "local_pause")
		}
		return nil
	}
	if hasWake {
		e.status.State = "working"
		snapshot := e.state.SnapshotCache
		if pending || e.Clock.Now().Before(e.apiNotBefore) {
			snapshot.Complete = false
		}
		return e.Wakes.Tick(ctx, snapshot)
	}
	if j, ok := e.active(); ok {
		e.status.State = "working"
		if j.WaitReason == "abort_unconfirmed" && e.Clock.Now().Before(j.NextAttemptAt) {
			e.status.WaitReason = j.WaitReason
			e.status.NextRetry = j.NextAttemptAt
			return nil
		}
		if allowed, reason := e.continuationAllowed(j); !allowed {
			return e.stop(ctx, j, reason)
		}
		if j.TerminalAt.IsZero() && !e.Clock.Now().Before(j.Deadline) {
			if err := e.OpenCode.Abort(ctx, j); err != nil {
				j.WaitReason = "abort_unconfirmed"
				j.NextAttemptAt = e.Clock.Now().Add(e.Config.Limits.RetryBase)
				return e.saveJob(j)
			}
			return e.observe(ctx, j)
		}
		if !e.Clock.Now().Before(e.nextObserve) {
			return e.observe(ctx, j)
		}
		return nil
	}
	if pending {
		return nil
	}
	if !e.Clock.Now().Before(e.nextDispatch) {
		if handled, err := e.scheduleWake(ctx, fresh); handled || err != nil {
			return err
		}
	}
	for _, j := range e.state.Jobs {
		if j.Phase == "blocked" && !j.BlockReported {
			return e.reportBlock(ctx, j, j.WaitReason)
		}
	}
	if e.Clock.Now().Before(e.nextDispatch) {
		e.status.State = "waiting"
		e.status.WaitReason = "server_unavailable"
		e.status.NextRetry = e.nextDispatch
		return nil
	}
	var waiting []Job
	for _, j := range e.state.Jobs {
		if j.Phase == "preparing" || j.Phase == "waiting" || j.Phase == "paused" {
			waiting = append(waiting, j)
		}
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].ID < waiting[j].ID })
	for _, j := range waiting {
		if e.Clock.Now().Before(j.NextAttemptAt) {
			continue
		}
		if !fresh {
			if err := e.refresh(ctx); errors.Is(err, errPollPending) {
				return nil
			} else if err != nil {
				return err
			}
			fresh = true
		}
		a, err := e.reconcile(ctx, j)
		if err != nil {
			return err
		}
		if artifactCompletes(e.Config, j, a) {
			j.Phase = "completed"
			if err := e.saveJob(j); err != nil {
				return err
			}
			continue
		}
		if pendingReviewHandoff(e.Config, j, e.state.SnapshotCache, e.Clock.Now()) {
			continue
		}
		if allowed, _ := e.continuationAllowed(j); !allowed {
			continue
		}
		return e.dispatch(ctx, j)
	}
	jobs := Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
	if len(jobs) > 0 {
		if !fresh {
			if err := e.refresh(ctx); errors.Is(err, errPollPending) {
				return nil
			} else if err != nil {
				return err
			}
			jobs = Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
		}
		if len(jobs) > 0 {
			e.status.State = "working"
			return e.dispatch(ctx, jobs[0])
		}
	}
	e.status.State = "idle"
	return nil
}
func (e *Engine) Run(ctx context.Context) error {
	e.asynchronous = true
	release, err := e.Store.Lock()
	if err != nil {
		return err
	}
	defer release()
	defer func() {
		if e.pollCancel != nil {
			e.pollCancel()
			if e.poll != nil {
				select {
				case <-e.poll:
				case <-time.After(2 * time.Second):
				}
			}
		}
	}()
	if err := e.initialize(); err != nil {
		return err
	}
	lastError := ""
	wake := make(chan struct{}, 1)
	type watchResult struct{ id, category string }
	watchDone := make(chan watchResult, 2)
	watchID := ""
	var watchCancel context.CancelFunc
	var nextWatch time.Time
	defer func() {
		if watchCancel != nil {
			watchCancel()
		}
	}()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.Tick(ctx); err != nil {
			category := errorCategory(err)
			if category != lastError {
				slog.Warn("worker tick failed", "category", category)
				lastError = category
			}
		} else {
			lastError = ""
		}
		if watcher, ok := e.OpenCode.(interface {
			Watch(context.Context, Job, chan<- struct{}) error
		}); ok {
			job, active := e.active()
			if !active || job.SessionID != watchID {
				if watchCancel != nil {
					watchCancel()
					watchCancel = nil
				}
				watchID = ""
			}
			if active && watchID == "" && !e.Clock.Now().Before(nextWatch) {
				watchCtx, cancel := context.WithCancel(ctx)
				watchCancel = cancel
				watchID = job.SessionID
				go func(job Job) {
					err := watcher.Watch(watchCtx, job, wake)
					category := ""
					if err != nil {
						category = errorCategory(err)
					}
					select {
					case watchDone <- watchResult{id: job.SessionID, category: category}:
					case <-ctx.Done():
					}
				}(job)
			}
		}
		select {
		case <-wake:
			e.nextObserve = time.Time{}
		default:
		}
		select {
		case done := <-watchDone:
			if done.id == watchID {
				watchID = ""
				nextWatch = e.Clock.Now().Add(15 * time.Second)
				if done.category != "" {
					slog.Warn("OpenCode event stream disconnected", "category", done.category)
				}
			}
		default:
		}
		if err := e.Clock.Wait(ctx, time.Second); err != nil {
			return err
		}
	}
}
