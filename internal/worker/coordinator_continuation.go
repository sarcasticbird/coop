package worker

import (
	"context"
	"errors"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

var (
	errContinuationTarget  = errors.New("continuation target changed")
	errContinuationActions = errors.New("continuation has unfinished actions")
	errContinuationSlot    = errors.New("continuation slot occupied")
)

func (d *NativeDispatcher) continuationExecution(target c.TaskContinuationTarget) (NativeExecution, error) {
	var x NativeExecution
	if c.ValidateContinuationTarget(target) != nil || target.InstanceID != d.config.Master.InstanceID || target.Generation != d.config.Master.Generation {
		return x, errContinuationTarget
	}
	x, err := loadNativeExecution(d.config, target.JobID)
	if err != nil {
		return x, err
	}
	// Retired restore records are authoritative even if their original bytes remain.
	retained, ok := d.history[target.JobID]
	if !ok || retained.Phase == "cancelled" && x.Phase != "cancelled" {
		return x, errContinuationTarget
	}
	if x.Job.TaskURL != target.TaskURL {
		return x, errContinuationTarget
	}
	return x, nil
}
func continuationOwnsTarget(x NativeExecution, delivery string, target c.TaskContinuationTarget) bool {
	for _, request := range x.Continuations {
		if request.RequestID == delivery && request.SessionID == target.SessionID && request.MessageID == target.MessageID {
			return true
		}
	}
	return false
}

func recordContinuation(x *NativeExecution, requestID, sourceMessageID string) {
	x.Continuations = append(x.Continuations, NativeContinuation{RequestID: requestID, SessionID: x.Job.SessionID, MessageID: x.Job.MessageID})
	x.ContinuationRequestID, x.ContinuationSourceMessageID = requestID, sourceMessageID
}

// ContinuationState observes a durable checkpoint; admitted does not mean a
// model prompt was accepted. A resumed receipt names its actual new message.
func (d *NativeDispatcher) ContinuationState(delivery string, target c.TaskContinuationTarget) (string, *c.NativeIntent, error) {
	x, err := d.continuationExecution(target)
	if err != nil {
		return "", nil, err
	}
	if !continuationOwnsTarget(x, delivery, target) {
		return "", nil, nil
	}
	// The first successor belongs to this request. A later request must not
	// replace its accepted identity with the currently selected model turn.
	successor := x.Job
	for i, turn := range x.Turns {
		if turn.SessionID == target.SessionID && turn.MessageID == target.MessageID {
			if i+1 < len(x.Turns) {
				successor = x.Turns[i+1]
			}
			break
		}
	}
	if successor.SessionID == target.SessionID && validMessage(successor.MessageID) && successor.MessageID != target.MessageID && (successor.SubmissionState == "accepted" || successor.SubmissionState == "completed") {
		return "resumed", &c.NativeIntent{SessionID: successor.SessionID, MessageID: successor.MessageID}, nil
	}
	if x.Phase == "cancelled" || x.Phase == "blocked" && (successor.SubmissionState == "" || successor.SubmissionState == "prepared") {
		return "refused", nil, c.ErrConflict
	}
	return "admitted", nil, nil
}

// RequestContinuation runs on an isolated dispatcher snapshot while the
// worker owns its lock. It checkpoints a retained task; submission remains the normal
// dispatcher's responsibility and keeps all fresh authority/action checks.
func (d *NativeDispatcher) RequestContinuation(ctx context.Context, delivery, thread string, target c.TaskContinuationTarget, sourceMessageID string, check func(context.Context) error) error {
	x, err := d.continuationExecution(target)
	if err != nil {
		return err
	}
	if continuationOwnsTarget(x, delivery, target) {
		return nil
	}
	for _, request := range x.Continuations {
		if request.SessionID == target.SessionID && request.MessageID == target.MessageID {
			return errContinuationTarget
		}
	}
	if d.HasActive() {
		return errContinuationSlot
	}
	if x.Job.Master == nil || x.Job.Master.ThreadID != thread || x.Job.SessionID != target.SessionID || x.Job.MessageID != target.MessageID || x.Phase != "blocked" || (x.Reason != "native_completed_without_result" && x.Reason != "native_dependency_wait") {
		return errContinuationTarget
	}
	for id, other := range d.history {
		if id == x.Job.ID {
			continue
		}
		if held, e := nativeExecutionRecoveryCheck(d.config, other); e != nil {
			return e
		} else if held {
			return c.ErrConflict
		}
	}
	store := NewVersionedFileStore(d.config.StateDir, 3)
	paused, err := store.Paused()
	if err != nil {
		return err
	}
	admission, err := store.AdmissionPaused()
	if err != nil {
		return err
	}
	if paused || admission {
		return c.ErrForbidden
	}
	_, err = d.resumeBlockedWithRequest(ctx, &x, delivery, sourceMessageID, check)
	// Preserve any bounded-limit or successful checkpoint in event-loop history.
	saved, readErr := loadNativeExecution(d.config, x.Job.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	d.history[x.Job.ID] = saved
	if err != nil {
		return err
	}
	if !continuationOwnsTarget(saved, delivery, target) {
		return errContinuationTarget
	}
	if saved.Phase == "selected" || saved.Phase == "prepared" {
		copy := saved
		d.active = &copy
	}
	return nil
}
