package worker

import (
	"context"
	"errors"
	"sort"
	"time"
)

type WakeCoordinator struct {
	Config      Config
	Store       WakeStore
	GitHub      GitHub
	OpenCode    OpenCode
	Clock       Clock
	nextObserve time.Time
	Paused      func() (bool, error)
}

func NewWakeCoordinator(c Config, s WakeStore, g GitHub, code OpenCode, clock Clock) *WakeCoordinator {
	if clock == nil {
		clock = RealClock{}
	}
	return &WakeCoordinator{Config: Defaults(c), Store: s, GitHub: g, OpenCode: code, Clock: clock}
}
func (w *WakeCoordinator) Ingest(s Snapshot) error {
	return IngestDiscussionWakes(w.Config, s, w.Store, w.Clock.Now())
}
func (w *WakeCoordinator) Active() (WakeReceipt, bool, error) {
	requests, err := w.Store.Requests()
	if err != nil {
		return WakeReceipt{}, false, err
	}
	var active WakeReceipt
	found := false
	for _, r := range requests {
		v, err := w.Store.Receipt(r.ID)
		if err != nil {
			return v, false, err
		}
		if v.Phase == "running" {
			if found {
				return v, false, ghError("conflicting_active")
			}
			if v.Job.Phase != "active" || !validSession(v.Job.SessionID) || !validMessage(v.Job.MessageID) || v.Job.ID != r.ID {
				return v, false, errors.New("invalid active wake intent")
			}
			active = v
			found = true
		}
	}
	return active, found, nil
}
func (w *WakeCoordinator) Next(now time.Time) (WakeRequest, bool, error) {
	records, err := w.Store.Requests()
	if err != nil {
		return WakeRequest{}, false, err
	}
	var report *WakeRequest
	for _, r := range records {
		v, err := w.Store.Receipt(r.ID)
		if err != nil {
			return WakeRequest{}, false, err
		}
		if v.Phase == "recovery" {
			return WakeRequest{}, false, nil
		}
	}
	for _, r := range records {
		v, err := w.Store.Receipt(r.ID)
		if err != nil {
			return r, false, err
		}
		if v.Phase == "queued" && !now.Before(v.Job.NextAttemptAt) {
			return r, true, nil
		}
		if report == nil && v.Phase == "blocked" && v.Reason == "prompt_size" && !v.Job.BlockReported && v.Job.PromptReportAttempts < w.Config.Limits.MaxAttempts && !now.Before(v.Job.NextAttemptAt) {
			copy := r
			report = &copy
		}
	}
	if report != nil {
		return *report, true, nil
	}
	return WakeRequest{}, false, nil
}
func (w *WakeCoordinator) Summaries() ([]WakeSummary, error) { return WakeSummaries(w.Store, 20, "") }
func WakeSummaries(store WakeStore, limit int, id string) ([]WakeSummary, error) {
	records, err := store.Requests()
	if err != nil {
		return nil, err
	}
	var summaries []WakeSummary
	for _, r := range records {
		if id != "" && r.ID != id {
			continue
		}
		v, err := store.Receipt(r.ID)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, WakeSummary{ID: r.ID, Kind: r.Kind, Phase: v.Phase, Reason: v.Reason, PromptFailure: v.Job.PromptFailure, ReportAttempts: v.Job.PromptReportAttempts})
	}
	if id != "" && len(summaries) == 0 {
		return nil, errors.New("unknown wake request")
	}
	if len(summaries) > limit {
		summaries = summaries[len(summaries)-limit:]
	}
	return summaries, nil
}
func (w *WakeCoordinator) save(v WakeReceipt) error {
	v.UpdatedAt = w.Clock.Now()
	if err := w.Store.SaveReceipt(v); err != nil {
		return err
	}
	if wakeReceiptTerminal(v) {
		routing, err := w.Store.Routing()
		if err != nil {
			return err
		}
		if routing.UrgentBeforeWake == v.RequestID {
			routing.UrgentBeforeWake = ""
			return w.Store.SaveRouting(routing)
		}
	}
	return nil
}

func (w *WakeCoordinator) reportPromptBlock(ctx context.Context, v WakeReceipt) error {
	// Use the existing idempotent Discussion report, not a wake reply marker:
	// the failed request has not been answered or executed by the model.
	v.Job.PromptReportAttempts++
	delay := w.Config.Limits.RetryBase
	for i := 1; i < v.Job.PromptReportAttempts && delay < w.Config.Limits.RetryCap; i++ {
		delay *= 2
	}
	if delay > w.Config.Limits.RetryCap {
		delay = w.Config.Limits.RetryCap
	}
	v.Job.NextAttemptAt = w.Clock.Now().Add(delay)
	// Persist a bounded reporting attempt before its external effect. This is
	// independent of execution attempts and never requeues the model input.
	if err := w.save(v); err != nil {
		return err
	}
	j := v.Job
	j.Kind, j.GoalURL = "plan", v.Source.DiscussionURL
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.GitHub.ReportBlock(ctx, j, "prompt_size", ""); err != nil {
		return err
	}
	v.Job.BlockReported = true
	v.Job.NextAttemptAt = time.Time{}
	return w.save(v)
}
func (w *WakeCoordinator) retry(v WakeReceipt, reason string) error {
	v.Job.Attempts++
	v.Reason = reason
	v.Phase = "queued"
	v.Job.Phase = "waiting"
	v.Job.MessageID = ""
	v.Job.SubmissionState = ""
	v.Job.Deadline = time.Time{}
	if v.Job.Attempts >= w.Config.Limits.MaxAttempts {
		v.Phase = "blocked"
	} else {
		delay := w.Config.Limits.RetryBase
		for i := 1; i < v.Job.Attempts && delay < w.Config.Limits.RetryCap; i++ {
			delay *= 2
		}
		if delay > w.Config.Limits.RetryCap {
			delay = w.Config.Limits.RetryCap
		}
		v.Job.NextAttemptAt = w.Clock.Now().Add(delay)
	}
	return w.save(v)
}
func (w *WakeCoordinator) request(id string) (WakeRequest, error) {
	records, err := w.Store.Requests()
	if err != nil {
		return WakeRequest{}, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, nil
		}
	}
	return WakeRequest{}, errors.New("unknown active wake")
}
func (w *WakeCoordinator) Pause(ctx context.Context) error {
	v, active, err := w.Active()
	if err != nil || !active {
		return err
	}
	if w.Clock.Now().Before(v.Job.NextAttemptAt) && v.Reason == "abort_unconfirmed" {
		return nil
	}
	if err := w.OpenCode.Abort(ctx, v.Job); err != nil {
		v.Job = reconciliationFailure(v.Job, err, w.Config.Limits, w.Clock.Now())
		if v.Job.Phase == "recovery" {
			v.Phase, v.Reason = "recovery", "reconciliation_required"
			return w.save(v)
		}
		v.Reason = "abort_unconfirmed"
		v.Job.NextAttemptAt = w.Clock.Now().Add(w.Config.Limits.RetryBase)
		if saveErr := w.save(v); saveErr != nil {
			return saveErr
		}
		return err
	}
	v.Phase = "queued"
	v.Reason = "local_pause"
	v.Job.ReconcileFailures, v.Job.ReconcileError = 0, ""
	v.Job.Phase = "paused"
	v.Job.MessageID = ""
	v.Job.SubmissionState = ""
	v.Job.NextAttemptAt = time.Time{}
	return w.save(v)
}
func (w *WakeCoordinator) Tick(ctx context.Context, snapshot Snapshot) error {
	conversation, ok := w.GitHub.(WakeConversation)
	if !ok {
		return errors.New("wake backend unavailable")
	}
	v, active, err := w.Active()
	if err != nil {
		return err
	}
	if active {
		if v.Job.ReconcileFailures > 0 && w.Clock.Now().Before(v.Job.NextAttemptAt) {
			return nil
		}
		if w.Clock.Now().Before(w.nextObserve) {
			return nil
		}
		w.nextObserve = w.Clock.Now().Add(w.Config.OpenCode.ReconcileInterval)
		r, err := w.request(v.RequestID)
		if err != nil {
			return err
		}
		if v.Source.DiscussionID != "" {
			r.Source = v.Source
		}
		turn, observeErr := w.OpenCode.Observe(ctx, v.Job)
		if observeErr != nil {
			return w.reconciliationFailure(v, observeErr)
		}
		expired := !w.Clock.Now().Before(v.Job.Deadline)
		if !turn.Terminal {
			if !expired && turn.State != "blocked" {
				v.Job.SubmissionState = turn.State
				v.Job.ReconcileFailures, v.Job.ReconcileError = 0, ""
				return w.save(v)
			}
			if w.Clock.Now().Before(v.Job.NextAttemptAt) && v.Reason == "abort_unconfirmed" {
				return nil
			}
			if err := w.OpenCode.Abort(ctx, v.Job); err != nil {
				v.Job = reconciliationFailure(v.Job, err, w.Config.Limits, w.Clock.Now())
				if v.Job.Phase == "recovery" {
					v.Phase, v.Reason = "recovery", "reconciliation_required"
					return w.save(v)
				}
				v.Reason = "abort_unconfirmed"
				v.Job.NextAttemptAt = w.Clock.Now().Add(w.Config.Limits.RetryBase)
				if saveErr := w.save(v); saveErr != nil {
					return saveErr
				}
				return err
			}
		}
		if v.Job.TerminalAt.IsZero() {
			v.Job.TerminalAt = w.Clock.Now()
		}
		// A native terminal result frees no slot until GitHub writes are reconciled.
		if !snapshot.Complete {
			v.Reason = "reconciling"
			return w.save(v)
		}
		reply, found, err := conversation.WakeReply(ctx, r)
		if err != nil && errorCategory(err) != "duplicate_reply" {
			if errorCategory(err) == "source_deleted" || errorCategory(err) == "source_superseded" {
				v.Phase = "superseded"
				v.Job.Phase = "completed"
				v.Reason = errorCategory(err)
				return w.save(v)
			}
			return w.reconciliationFailure(v, err)
		}
		v.Job.ReconcileFailures, v.Job.ReconcileError = 0, ""
		if found {
			v.Phase = "completed"
			v.Job.Phase = "completed"
			v.ReplyNodeID = reply.NodeID
			v.ReplyURL = reply.URL
			v.Reason = ""
			if err != nil {
				v.Reason = "duplicate_reply"
			}
			return w.save(v)
		}
		return w.retry(v, "reply_missing")
	}
	r, found, err := w.Next(w.Clock.Now())
	if err != nil || !found {
		return err
	}
	v, err = w.Store.Receipt(r.ID)
	if err != nil {
		return err
	}
	if v.Phase == "blocked" && v.Reason == "prompt_size" {
		return w.reportPromptBlock(ctx, v)
	}
	source, err := conversation.RevalidateWake(ctx, r)
	if err != nil {
		switch errorCategory(err) {
		case "source_deleted", "source_superseded":
			v.Phase = "superseded"
			v.Reason = errorCategory(err)
			return w.save(v)
		}
		return w.failure(v, err)
	}
	r.Source = source
	v.Source = source
	reply, found, err := conversation.WakeReply(ctx, r)
	if err != nil && errorCategory(err) != "duplicate_reply" {
		return w.failure(v, err)
	}
	if found {
		v.Phase = "completed"
		v.ReplyNodeID = reply.NodeID
		v.ReplyURL = reply.URL
		v.Reason = ""
		if err != nil {
			v.Reason = "duplicate_reply"
		}
		return w.save(v)
	}
	routing, err := w.Store.Routing()
	if err != nil {
		return err
	}
	key := WakeBindingKey(w.Config, r)
	binding := routing.Bindings[key]
	// Check the saved session before any directory preparation or permission mutation.
	if binding.SessionID != "" {
		if checker, ok := w.OpenCode.(interface {
			CheckIdle(context.Context, Job) error
		}); ok {
			if err := checker.CheckIdle(ctx, Job{Checkout: binding.Directory, SessionID: binding.SessionID}); err != nil {
				if errorCategory(err) == "session_busy" {
					v.Reason = "session_busy"
					v.Job.NextAttemptAt = w.Clock.Now().Add(w.Config.OpenCode.ReconcileInterval)
					return w.save(v)
				}
				return w.failure(v, err)
			}
		}
	}
	job, err := PrepareWakeJob(w.Config, r, binding)
	if err != nil {
		return w.retry(v, "wake_directory")
	}
	job.Attempts = v.Job.Attempts
	job, err = w.OpenCode.EnsureSession(ctx, w.Config, job)
	if err != nil {
		if errorCategory(err) == "session_busy" {
			v.Reason = "session_busy"
			v.Job.NextAttemptAt = w.Clock.Now().Add(w.Config.OpenCode.ReconcileInterval)
			return w.save(v)
		}
		return w.failure(v, err)
	}
	routing.Bindings[key] = WakeBinding{source.DiscussionID, source.RootID, job.Checkout, job.SessionID}
	if err := w.Store.SaveRouting(routing); err != nil {
		return err
	}
	// Revalidate after session preparation and re-read local pause just before intent.
	current, err := conversation.RevalidateWake(ctx, r)
	if err != nil {
		if errorCategory(err) == "source_deleted" || errorCategory(err) == "source_superseded" {
			v.Phase = "superseded"
			v.Reason = errorCategory(err)
			return w.save(v)
		}
		return w.failure(v, err)
	}
	if ConversationRevision(current) != ConversationRevision(source) {
		v.Phase = "superseded"
		v.Reason = "source_superseded"
		return w.save(v)
	}
	if w.Paused != nil {
		paused, err := w.Paused()
		if err != nil {
			return err
		}
		if paused {
			v.Reason = "local_pause"
			return w.save(v)
		}
	}
	prompt, err := BuildWakePrompt(w.Config, r, source, snapshot)
	if err != nil {
		var size *PromptSizeError
		if errors.As(err, &size) {
			job.Phase, job.WaitReason, job.PromptFailure = "blocked", "prompt_size", size
			v.Job, v.Phase, v.Reason = job, "blocked", "prompt_size"
			if err := w.save(v); err != nil {
				return err
			}
			return w.reportPromptBlock(ctx, v)
		}
		return w.retry(v, "wake_prompt")
	}
	job.Phase = "active"
	job.SubmissionState = "submitting"
	job.MessageID = "msg_" + randomID()
	job.SubmittedAt = w.Clock.Now()
	job.Deadline = w.Clock.Now().Add(w.Config.Limits.TurnLimit)
	v.Job = job
	v.Phase = "running"
	v.Reason = ""
	if err := w.save(v); err != nil {
		return err
	}
	turn, err := w.OpenCode.Submit(ctx, w.Config, job, prompt)
	if err != nil {
		if errorCategory(err) == "session_busy" {
			v.Phase = "queued"
			v.Reason = "session_busy"
			v.Job.Phase = "waiting"
			v.Job.MessageID = ""
			v.Job.NextAttemptAt = w.Clock.Now().Add(w.Config.OpenCode.ReconcileInterval)
		} else {
			v.Job.SubmissionState = "unknown"
			v.Reason = "submission_unknown"
		}
	} else {
		v.Job.SubmissionState = turn.State
	}
	w.nextObserve = w.Clock.Now().Add(w.Config.OpenCode.ReconcileInterval)
	return w.save(v)
}
func urgentWakeJob(j Job) bool { return j.Kind == "fix" || j.Kind == "review" || j.Kind == "deliver" }
func (e *Engine) scheduleWake(ctx context.Context, fresh bool) (bool, error) {
	if e.Wakes == nil {
		return false, nil
	}
	r, found, err := e.Wakes.Next(e.Clock.Now())
	if err != nil || !found {
		return false, err
	}
	if !fresh || e.lastSnapshotStarted.Before(r.CreatedAt) {
		if e.Clock.Now().Before(e.apiNotBefore) {
			return true, nil
		}
		e.nextPoll = time.Time{}
		if err := e.refresh(ctx); err != nil {
			if errors.Is(err, errPollPending) {
				return true, nil
			}
			return true, err
		}
		if e.lastSnapshotStarted.Before(r.CreatedAt) {
			return true, nil
		}
	}
	routing, err := e.Wakes.Store.Routing()
	if err != nil {
		return true, err
	}
	if routing.UrgentBeforeWake != r.ID {
		jobs := Select(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
		for _, j := range e.state.Jobs {
			if urgentWakeJob(j) && (j.Phase == "waiting" || j.Phase == "paused" || j.Phase == "preparing") && !e.Clock.Now().Before(j.NextAttemptAt) {
				if allowed, _ := e.continuationAllowed(j); allowed && !artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, e.state.SnapshotCache)) {
					jobs = append(jobs, j)
				}
			}
		}
		sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
		for _, j := range jobs {
			if urgentWakeJob(j) {
				routing.UrgentBeforeWake = r.ID
				if err := e.Wakes.Store.SaveRouting(routing); err != nil {
					return true, err
				}
				return true, e.dispatch(ctx, j)
			}
		}
	}
	return true, e.Wakes.Tick(ctx, e.state.SnapshotCache)
}

func (w *WakeCoordinator) failure(v WakeReceipt, cause error) error {
	if err := w.retry(v, errorCategory(cause)); err != nil {
		return err
	}
	return cause
}

func (e *Engine) noteAPIError(cause error, fallback time.Time) {
	var gh *GitHubError
	if !errors.As(cause, &gh) {
		return
	}
	if fallback.After(e.apiNotBefore) {
		e.apiNotBefore = fallback
	}
	if gh.RetryAt.After(e.apiNotBefore) {
		e.apiNotBefore = gh.RetryAt
	}
	if e.apiNotBefore.After(e.nextPoll) {
		e.nextPoll = e.apiNotBefore
	}
}
func (e *Engine) persistWakeDeadline() error {
	if e.Wakes == nil {
		return nil
	}
	routing, err := e.Wakes.Store.Routing()
	if err != nil {
		return err
	}
	if routing.APINotBefore.Equal(e.apiNotBefore) {
		return nil
	}
	routing.APINotBefore = e.apiNotBefore
	return e.Wakes.Store.SaveRouting(routing)
}
