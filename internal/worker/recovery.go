package worker

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"
)

// Production artifact reconciliation depends on the snapshot poll. A failed
// poll must not bypass the recovery budget for a turn that has already ended.
func (e *Engine) reconcilePollFailure(ctx context.Context, pollErr error) (bool, error) {
	j, active := e.active()
	var receipt WakeReceipt
	if !active && e.Wakes != nil {
		var err error
		receipt, active, err = e.Wakes.Active()
		if err != nil {
			return false, err
		}
		j = receipt.Job
	}
	if !active {
		return false, nil
	}
	if j.ReconcileFailures > 0 && e.Clock.Now().Before(j.NextAttemptAt) {
		return true, nil
	}
	if j.TerminalAt.IsZero() {
		turn, err := e.OpenCode.Observe(ctx, j)
		if err != nil {
			// Native uncertainty still uses its own categorized failure, rather
			// than treating a catalog outage as evidence that the turn ended.
			pollErr = err
		} else if !turn.Terminal {
			j.SubmissionState, j.WaitReason = turn.State, ""
			j.ReconcileFailures, j.ReconcileError = 0, ""
			j.NextAttemptAt = time.Time{}
			if receipt.RequestID != "" {
				receipt.Job, receipt.Reason = j, ""
				return false, e.Wakes.save(receipt)
			}
			return false, e.saveJob(j)
		} else {
			j.TerminalAt, j.SubmissionState = e.Clock.Now(), turn.State
		}
	}
	j.WaitReason = "reconciling"
	j = reconciliationFailure(j, pollErr, e.Config.Limits, e.Clock.Now())
	if receipt.RequestID != "" {
		receipt.Job, receipt.Reason = j, j.WaitReason
		if j.Phase == "recovery" {
			receipt.Phase = "recovery"
		}
		return true, e.Wakes.save(receipt)
	}
	return true, e.saveJob(j)
}

// A missing native record says nothing about external effects. Escalation
// preserves the intent and holds all dispatch, rather than retrying the prompt.
func reconciliationFailure(j Job, err error, limits Limits, now time.Time) Job {
	j.ReconcileFailures++
	j.NextAttemptAt = now.Add(limits.RetryBase)
	j.ReconcileError = errorCategory(err)
	var native *OpenCodeError
	if errors.As(err, &native) && native.Status == http.StatusNotFound {
		j.ReconcileError = "native_record_missing"
	}
	if j.ReconcileFailures >= limits.MaxAttempts {
		j.Phase, j.WaitReason = "recovery", "reconciliation_required"
		j.NextAttemptAt = time.Time{}
	}
	return j
}

func (w *WakeCoordinator) reconciliationFailure(v WakeReceipt, err error) error {
	v.Job = reconciliationFailure(v.Job, err, w.Config.Limits, w.Clock.Now())
	v.Reason = v.Job.ReconcileError
	if v.Job.Phase == "recovery" {
		v.Phase, v.Reason = "recovery", "reconciliation_required"
	}
	return w.save(v)
}

// RecoveryIntent requires the worker lock: the service must be stopped, not
// merely paused. Exact identity guards against operating on a newer turn.
// It only resumes observation of the original message; it never submits input.
func RecoveryIntent(c Config, id, session, message string) error {
	if !hexSHA.MatchString(id) || !validSession(session) || !validMessage(message) {
		return errors.New("recovery requires exact job, session and message IDs")
	}
	s := NewVersionedFileStore(c.StateDir, c.Version)
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.Load()
	if err != nil {
		return err
	}
	resume := func(j Job) (Job, error) {
		if j.ID != id || j.Phase != "recovery" || j.SessionID != session || j.MessageID != message || !validSession(session) || !validMessage(message) {
			return j, errors.New("recovery requires exact saved recovery intent")
		}
		j.Phase, j.WaitReason = "active", ""
		j.ReconcileFailures, j.ReconcileError = 0, ""
		return j, nil
	}
	if j, ok := state.Jobs[id]; ok {
		j, err = resume(j)
		if err != nil {
			return err
		}
		state.Jobs[id] = j
		return s.Save(state)
	}
	w := NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), c.Version)
	v, err := w.Receipt(id)
	if err != nil {
		return err
	}
	if v.Phase != "recovery" {
		return errors.New("wake is not awaiting recovery")
	}
	v.Job, err = resume(v.Job)
	if err != nil {
		return err
	}
	v.Phase, v.Reason = "running", ""
	v.UpdatedAt = time.Now().UTC()
	return w.SaveReceipt(v)
}
