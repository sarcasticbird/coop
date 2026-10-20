package worker

import (
	"context"
	"errors"
	"reflect"
	"sort"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// An action-free terminal checkpoint can release an obsolete task reservation.
// Retained actions use their existing reconciliation path instead.
func nativeStaleRetirementEligible(cfg Config, x NativeExecution) (bool, error) {
	if !nativeTerminalCheckpoint(x) || x.Binding == nil || x.Job.TaskURL == "" {
		return false, nil
	}
	records, err := loadNativeJobActions(cfg, x)
	return len(records) == 0 && err == nil, err
}

func nativeStaleReplacement(cfg Config, x NativeExecution, catalog NativeCatalog, history map[string]NativeExecution) (NativeCandidate, bool) {
	if !catalog.Complete {
		return NativeCandidate{}, false
	}
	// Ignore only this reservation. Other retained jobs and review-cycle limits
	// still constrain a replacement exactly as they constrain normal admission.
	remaining := make(map[string]NativeExecution, len(history))
	for id, retained := range history {
		if id != x.Job.ID {
			remaining[id] = retained
		}
	}
	candidates := NativeCandidates(cfg, catalog, remaining, x.Job.TerminalAt)
	for _, v := range candidates {
		if v.Job.ID == x.Job.ID {
			return NativeCandidate{}, false
		}
	}
	var replacement NativeCandidate
	found := false
	for _, v := range candidates {
		if v.Task == nil || v.Job.TaskURL != x.Job.TaskURL || v.Goal.Revision.Ref != x.Goal || BindingKey(cfg, v.Job) != BindingKey(cfg, x.Job) {
			continue
		}
		if found {
			return NativeCandidate{}, false
		}
		replacement, found = v, true
	}
	return replacement, found
}

// A cached complete catalog only nominates work for the single asynchronous
// slot. It never authorizes retirement or causes a model continuation.
func (d *NativeDispatcher) staleRetirements() ([]NativeExecution, error) {
	var out []NativeExecution
	if !d.catalog.Complete {
		return out, nil
	}
	for _, x := range d.history {
		if !nativeTerminalCheckpoint(x) || x.Binding == nil || x.Job.TaskURL == "" || d.clock.Now().Before(x.Job.NextAttemptAt) {
			continue
		}
		if _, ok := nativeStaleReplacement(d.config, x, d.catalog, d.history); !ok {
			continue
		}
		eligible, err := nativeStaleRetirementEligible(d.config, x)
		if err != nil {
			return nil, err
		}
		if eligible {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job.ID < out[j].Job.ID })
	return out, nil
}

func (d *NativeDispatcher) retireStaleTerminalAttempt(ctx context.Context, x *NativeExecution) (done bool, err error) {
	if d.history[x.Job.ID].Phase != "blocked" {
		return false, c.ErrEpoch
	}
	release, err := lockNativeProducer(d.config)
	if err != nil {
		return false, err
	}
	defer release()
	saved, err := loadNativeExecution(d.config, x.Job.ID)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(saved, *x) {
		return false, c.ErrConflict
	}
	defer func() {
		if err == nil || x.Phase != "blocked" {
			return
		}
		x.Job.ReconcileFailures++
		x.Job.ReconcileError = errorCategory(err)
		x.Job.NextAttemptAt = d.clock.Now().Add(nativeRecoveryBackoff(x.Job.ReconcileFailures))
		var quota *GitHubError
		if errors.As(err, &quota) && quota.RetryAt.After(x.Job.NextAttemptAt) {
			x.Job.NextAttemptAt = quota.RetryAt
		}
		err = errors.Join(err, saveNativeExecution(d.config, *x))
	}()
	eligible, err := nativeStaleRetirementEligible(d.config, saved)
	if err != nil {
		return false, err
	}
	if !eligible {
		return false, c.ErrConflict
	}
	store := NewVersionedFileStore(d.config.StateDir, 3)
	paused, err := store.Paused()
	if err != nil {
		return false, err
	}
	admissionPaused, err := store.AdmissionPaused()
	if err != nil {
		return false, err
	}
	if paused || admissionPaused {
		return false, c.ErrForbidden
	}
	if _, err = nativeCurrentGoal(ctx, d.config, d.master, x.Goal); err != nil {
		return false, err
	}
	observed, err := d.code.Observe(ctx, x.Job)
	if err != nil {
		return false, err
	}
	if !observed.Terminal || observed.SessionID != x.Job.SessionID || observed.MessageID != x.Job.MessageID {
		return false, c.ErrConflict
	}
	if err = d.code.CheckContinuation(ctx, x.Job); err != nil {
		return false, err
	}
	catalog, err := nativeValidationSnapshot(ctx, d.config, d.master, d.github, x.Job)
	if err != nil {
		return false, err
	}
	if !catalog.Complete {
		return false, c.ErrUnavailable
	}
	replacement, ok := nativeStaleReplacement(d.config, *x, catalog, d.history)
	if !ok {
		return false, errNativeCandidateStale
	}
	// Authority and native idleness can change during remote reads. Recheck them
	// before the local checkpoint; the producer stays exclusively held throughout.
	authority, err := nativeCurrentGoal(ctx, d.config, d.master, x.Goal)
	if err != nil {
		return false, err
	}
	if authority.ApprovalSequence != replacement.Goal.ApprovalSequence {
		return false, c.ErrConflict
	}
	if err = d.code.CheckContinuation(ctx, x.Job); err != nil {
		return false, err
	}
	paused, err = store.Paused()
	if err != nil {
		return false, err
	}
	admissionPaused, err = store.AdmissionPaused()
	if err != nil {
		return false, err
	}
	if paused || admissionPaused {
		return false, c.ErrForbidden
	}
	cancelled := *x
	cancelled.Phase, cancelled.Reason = "cancelled", "native_terminal_attempt_superseded"
	// The historical job remains the exact completed native turn. Cancelling
	// its reservation neither completes its task nor changes terminal evidence.
	if err = saveNativeExecution(d.config, cancelled); err != nil {
		return false, err
	}
	*x = cancelled
	return false, nil
}
