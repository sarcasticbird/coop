package worker

import (
	"context"
	"errors"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

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
	if x.Job.SessionID != session || x.Job.MessageID != message || x.Phase == "completed" || x.Phase == "cancelled" {
		return x, c.ErrConflict
	}
	if x.Job.SubmissionState != "prepared" && x.Job.SessionID != "" {
		observed, e := code.Observe(ctx, x.Job)
		if e != nil {
			return x, e
		}
		if !observed.Terminal {
			return x, errors.New("saved native engineering turn is still running or uncertain")
		}
		x.Job = observedModel(x.Job, observed)
		x.Job.SubmissionState = "completed"
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
