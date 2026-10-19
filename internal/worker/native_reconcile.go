package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func loadNativeJobActions(cfg Config, x NativeExecution) ([]nativeActionRecord, error) {
	dir := filepath.Join(masterDir(cfg), "actions")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []nativeActionRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, e := readPrivate(filepath.Join(dir, entry.Name()), c.MaxJSON)
		if e != nil {
			return nil, e
		}
		var r nativeActionRecord
		if strictTeamJSON(raw, &r) != nil || r.Version != 3 || filepath.Base(nativeActionPath(cfg, r.JobID, r.RequestID)) != entry.Name() {
			return nil, c.ErrConflict
		}
		if r.JobID == x.Job.ID {
			out = append(out, r)
		}
	}

	// An intent may precede the first journal when a read fails or the process
	// exits. With no journal, no effect could have started: every effect first
	// durably writes its uncertainty marker under the producer lock.
	intentDir := filepath.Join(masterDir(cfg), "action-intents")
	intents, err := os.ReadDir(intentDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range out {
		seen[r.RequestID] = true
	}
	for _, entry := range intents {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, e := readPrivate(filepath.Join(intentDir, entry.Name()), c.MaxJSON)
		if e != nil {
			return nil, e
		}
		var intent nativeActionIntent
		if strictTeamJSON(raw, &intent) != nil || intent.Version != 1 || entry.Name() != filepath.Base(nativeActionIntentPath(cfg, intent.JobID, intent.Action.RequestID)) {
			return nil, c.ErrConflict
		}
		if intent.JobID != x.Job.ID || seen[intent.Action.RequestID] {
			continue
		}
		r := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: intent.Action.RequestID, InputSHA: intent.InputSHA, Operation: intent.Action.Operation, State: "prepared"}
		if _, e = loadNativeActionIntent(cfg, x, r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out, nil
}

// Only acknowledged checkpoints can replay their remaining guarded steps.
// An uncertain write (including an issue creation with a lost response) is
// never inferred from its input or retried automatically.
func nativePreparedAction(r nativeActionRecord) bool {
	if r.State != "prepared" {
		return false
	}
	switch r.Operation {
	case "create-task":
		return r.Step == "" || (r.Step == "create-task" || r.Step == "task-metadata" || r.Step == "project-add" || r.Step == "project-ready") && validGitHubURL(r.Result.URL)
	case "rebind-task":
		return r.Step == "" || r.Step == "task-metadata" || r.Step == "project-add" || r.Step == "project-ready"
	case "publish-pr":
		return r.Step == "" || r.Step == "push" || r.Step == "publish-pr"
	case "review", "qa":
		return r.Step == "" || r.Step == "review-published"
	case "deliver":
		return r.Step == "" || r.Step == "merge" || r.Step == "task-completed"
	case "complete-plan", "wait":
		return r.Step == ""
	}
	return false
}

func nativeTerminalCheckpoint(x NativeExecution) bool {
	return x.Phase == "blocked" && x.Reason == "native_completed_without_result" && x.PendingContinuation == nil && x.Job.Phase == "completed" && x.Job.SubmissionState == "completed" && !x.Job.TerminalAt.IsZero() && validSession(x.Job.SessionID) && validMessage(x.Job.MessageID)
}

// This is a local eligibility check, not permission to write. Recovery rechecks
// placement, goal, exact native evidence and each original effect's guard.
func nativeAutomaticRecovery(cfg Config, x NativeExecution) (bool, error) {
	if !nativeTerminalCheckpoint(x) || x.Job.ReconcileFailures >= Defaults(cfg).Limits.MaxAttempts && !nativeActionRetryable(x) {
		return false, nil
	}
	records, err := loadNativeJobActions(cfg, x)
	if err != nil {
		return false, err
	}
	retained := false
	for _, r := range records {
		_, e := loadNativeActionIntent(cfg, x, r)
		if r.State != "completed" {
			if !nativePreparedAction(r) || e != nil {
				return false, nil
			}
		} else if e != nil && !os.IsNotExist(e) {
			return false, e
		}
		retained = retained || e == nil
	}
	return retained, nil
}

func (d *NativeDispatcher) automaticRecoveries() ([]NativeExecution, error) {
	out := []NativeExecution{}
	// Recovery needs the same concrete GitHub client used by the guarded helper.
	if _, ok := d.github.(*GitHubClient); !ok {
		return out, nil
	}
	for _, x := range d.history {
		if d.clock.Now().Before(x.Job.NextAttemptAt) {
			continue
		}
		ok, err := nativeAutomaticRecovery(d.config, x)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job.ID < out[j].Job.ID })
	return out, nil
}

func nativeRecoveryBackoff(failures int) time.Duration {
	delay := time.Minute
	for i := 1; i < failures && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	return min(delay, 15*time.Minute)
}

func (d *NativeDispatcher) reconcileNativeAction(ctx context.Context, x *NativeExecution) (done bool, err error) {
	defer func() {
		if err != nil && x.Phase == "blocked" {
			x.Job.ReconcileFailures++
			x.Job.ReconcileError = errorCategory(err)
			if errors.Is(err, c.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errNativeProducerBusy) {
				x.Job.ReconcileError = "transport_failed"
			}
			if errors.Is(err, errNativePrerequisiteWait) {
				x.Job.ReconcileError = "native_prerequisite_wait"
			}
			x.Job.NextAttemptAt = d.clock.Now().Add(nativeRecoveryBackoff(x.Job.ReconcileFailures))
			var quota *GitHubError
			if errors.As(err, &quota) && quota.RetryAt.After(x.Job.NextAttemptAt) {
				x.Job.NextAttemptAt = quota.RetryAt
			}
			err = errors.Join(err, saveNativeExecution(d.config, *x))
		}
	}()
	// Retired executions are cancelled in the dispatcher's accepted history.
	if d.history[x.Job.ID].Phase != "blocked" {
		return false, c.ErrEpoch
	}
	release, e := lockNativeProducer(d.config)
	if e != nil {
		return false, e
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	saved, e := loadNativeExecution(d.config, x.Job.ID)
	if e != nil {
		return false, e
	}
	if saved.Phase != x.Phase || saved.Job.SessionID != x.Job.SessionID || saved.Job.MessageID != x.Job.MessageID {
		return false, c.ErrConflict
	}
	eligible, e := nativeAutomaticRecovery(d.config, saved)
	if e != nil {
		return false, e
	}
	if !eligible {
		return false, c.ErrConflict
	}
	paused, e := NewVersionedFileStore(d.config.StateDir, 3).Paused()
	if e != nil || paused {
		return false, e
	}
	paused, e = NewVersionedFileStore(d.config.StateDir, 3).AdmissionPaused()
	if e != nil || paused {
		return false, e
	}
	if _, e = nativeCurrentGoal(ctx, d.config, d.master, x.Goal); e != nil {
		return false, e
	}
	observed, e := d.code.Observe(ctx, x.Job)
	if e != nil {
		return false, e
	}
	if !observed.Terminal || observed.SessionID != x.Job.SessionID || observed.MessageID != x.Job.MessageID {
		return false, c.ErrConflict
	}
	if e = d.code.CheckContinuation(ctx, x.Job); e != nil {
		return false, e
	}
	records, e := loadNativeJobActions(d.config, *x)
	if e != nil {
		return false, e
	}
	if retired, e := d.reconcileDeliveryReadiness(ctx, x, records); e != nil {
		return false, e
	} else if retired {
		return false, nil
	}
	for _, r := range records {
		if r.State == "completed" {
			continue
		}
		in, e := loadNativeActionIntent(d.config, *x, r)
		if e != nil {
			return false, e
		}
		active := *x
		active.Phase, active.Reason = "active", ""
		if e = saveNativeExecution(d.config, active); e != nil {
			return false, e
		}
		_, actionErr := runNativeActionLocked(ctx, d.config, d.master, d.github.(*GitHubClient), x.Job.ID, in)
		// Persist the original terminal checkpoint even when an effect became
		// uncertain. A crash before this write is observed normally on restart.
		if e = saveNativeExecution(d.config, *x); e != nil {
			return false, errors.Join(actionErr, e)
		}
		if actionErr != nil {
			return false, actionErr
		}
		x.Job.ReconcileFailures, x.Job.ReconcileError, x.Job.NextAttemptAt = 0, "", time.Time{}
		return false, saveNativeExecution(d.config, *x)
	}
	if complete, e := nativeJobHasResult(d.config, *x); e != nil {
		return false, e
	} else if complete {
		x.Phase, x.Reason = "completed", ""
		return true, saveNativeExecution(d.config, *x)
	}
	if wait, e := nativeTurnWait(d.config, *x); e != nil {
		return false, e
	} else if wait != nil {
		x.Wait = wait
		x.Reason = "native_dependency_wait"
		return false, saveNativeExecution(d.config, *x)
	}
	release()
	released = true
	// A recovered intermediate action still needs a final model result. Continue
	// the same session once, within the existing continuation/authority limits.
	return d.resumeBlocked(ctx, x)
}

func nativeRecoveryBlocker(cfg Config, x NativeExecution) string {
	if ok, err := nativeAutomaticRecovery(cfg, x); err == nil && ok {
		return "native_action_retry"
	}
	return nativeDependencyBlocker(cfg, x)
}

var errNativePrerequisiteWait = errors.New("native prerequisites not ready")

func nativeActionRetryable(x NativeExecution) bool {
	switch x.Job.ReconcileError {
	case "transport_failed", "rate_limited", "native_prerequisite_wait":
		return true
	}
	return false
}

// A known unstarted delivery can safely wait or retire in favor of a freshly
// verified author repair. Started/uncertain effects never take this path.
func (d *NativeDispatcher) reconcileDeliveryReadiness(ctx context.Context, x *NativeExecution, records []nativeActionRecord) (bool, error) {
	if x.Job.Kind != "deliver" {
		return false, nil
	}
	pending := false
	for _, r := range records {
		if r.State == "completed" {
			continue
		}
		if r.State != "prepared" || r.Step != "" || r.Operation != "deliver" {
			return false, nil
		}
		pending = true
	}
	if !pending {
		return false, nil
	}
	catalog, err := d.github.NativeSnapshot(ctx, d.config, d.master)
	if err != nil {
		return false, err
	}
	if !catalog.Complete {
		return false, c.ErrUnavailable
	}
	obsolete := nativeScopeChanged(*x, catalog)
	for _, candidate := range NativeCandidates(d.config, catalog, nil, d.clock.Now()) {
		if candidate.Job.ID == x.Job.ID {
			return false, nil
		}
		if candidate.Job.TaskURL == x.Job.TaskURL && (candidate.Job.Kind == "fix" || candidate.Job.Kind == "deliver") {
			obsolete = true
		}
	}
	if !obsolete {
		return false, errNativePrerequisiteWait
	}
	x.Phase, x.Reason, x.Job.Phase = "cancelled", "native_delivery_superseded", "cancelled"
	return true, saveNativeExecution(d.config, *x)
}
