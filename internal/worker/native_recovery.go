package worker

import (
	"context"
	"errors"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// ContinueNativeEngineering checkpoints a new turn for a retained terminal task.
// It requires the stopped worker lock and never submits a model prompt itself.
func ContinueNativeEngineering(ctx context.Context, cfg Config, m NativeEngineeringMaster, g NativeCatalogReader, code OpenCode, id, session, message string) (NativeExecution, error) {
	var x NativeExecution
	if cfg.Version != 3 || cfg.Master == nil {
		return x, c.ErrInvalid
	}
	release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return x, err
	}
	defer release()
	x, err = loadNativeExecution(cfg, id)
	if err != nil {
		return x, err
	}
	if x.Phase != "blocked" || x.Reason != "native_completed_without_result" || x.Job.SessionID != session || x.Job.MessageID != message {
		return x, c.ErrConflict
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, NewCheckouts(cfg, ExecRunner{}), RealClock{})
	if err != nil {
		return x, err
	}
	if d.HasActive() || d.RecoveryBlocked() || d.history[id].Phase != "blocked" {
		return x, errors.New("native recovery must finish before task continuation")
	}
	turns, err := masterTurns(cfg)
	if err != nil {
		return x, err
	}
	for _, turn := range turns {
		retired, e := retiredTurn(cfg, turn)
		if e != nil {
			return x, e
		}
		if !retired && (turn.Phase == "active" || turn.Phase == "prepared" || turn.Phase == "blocked") {
			return x, errors.New("native conversation still owns the worker slot")
		}
	}
	_, err = d.resumeBlocked(ctx, &x)
	return x, err
}

// ResolveNativeEngineering requires the stopped worker's lock and the exact
// saved native identity. It never submits or replays an engineering action.
// Cancellation preserves action journals for remote artifact reconciliation.
func ResolveNativeEngineering(ctx context.Context, cfg Config, code OpenCode, id, session, message, action string) (NativeExecution, error) {
	var x NativeExecution
	if cfg.Version != 3 || cfg.Master == nil || (action != "observe" && action != "cancel") {
		return x, c.ErrInvalid
	}
	release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return x, err
	}
	defer release()
	x, err = loadNativeExecution(cfg, id)
	if err != nil {
		return x, err
	}
	checkpoint := x.Job
	unsubmitted := (x.Phase == "selected" || x.Phase == "blocked") && x.Job.SubmissionState == "" && x.Job.MessageID == "" && len(x.Turns) > 0
	if unsubmitted {
		if action != "cancel" {
			return x, c.ErrConflict
		}
		checkpoint = x.Turns[len(x.Turns)-1]
		if checkpoint.SessionID != x.Job.SessionID || checkpoint.Phase != "completed" || checkpoint.SubmissionState != "completed" || checkpoint.TerminalAt.IsZero() {
			return x, c.ErrConflict
		}
	}
	if checkpoint.SessionID != session || checkpoint.MessageID != message || x.Phase == "completed" || x.Phase == "cancelled" {
		return x, c.ErrConflict
	}
	if checkpoint.SubmissionState != "prepared" && checkpoint.SessionID != "" {
		observed, e := code.Observe(ctx, checkpoint)
		if e != nil {
			return x, e
		}
		if !observed.Terminal || observed.SessionID != checkpoint.SessionID || observed.MessageID != checkpoint.MessageID {
			return x, errors.New("saved native engineering turn is still running or uncertain")
		}
		if !unsubmitted {
			x.Job = observedModel(x.Job, observed)
			x.Job.SubmissionState = "completed"
			x.Job.Phase = "completed"
			if x.Job.TerminalAt.IsZero() {
				x.Job.TerminalAt = time.Now().UTC()
			}
		}
	}
	if action == "cancel" {
		x.Phase, x.Reason = "cancelled", "operator_resolved_native_evidence"
	} else {
		complete, e := nativeJobHasResult(cfg, x)
		if e != nil {
			return x, e
		}
		if complete {
			x.Phase, x.Reason = "completed", ""
		} else {
			x.Phase, x.Reason = "blocked", "native_completed_without_result"
		}
	}
	if err = saveNativeExecution(cfg, x); err != nil {
		return x, err
	}
	return x, nil
}
