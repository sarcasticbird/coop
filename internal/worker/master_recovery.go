package worker

import (
	"context"
	"errors"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// ResolveMasterConversation never creates or resubmits a native message. The
// worker lock requires the service to be stopped while evidence is resolved.
func ResolveMasterConversation(ctx context.Context, cfg Config, client MasterTransport, code OpenCode, delivery, session, message, action string) (MasterTurn, error) {
	var turn MasterTurn
	if cfg.Version != 3 || cfg.Master == nil || (action != "observe" && action != "cancel") {
		return turn, c.ErrInvalid
	}
	release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return turn, err
	}
	defer release()
	turn, err = loadMasterTurn(cfg, delivery)
	if err != nil {
		return turn, err
	}
	if client.MasterID() != turn.MasterID || turn.Job.SessionID != session || turn.Job.MessageID != message {
		return turn, c.ErrConflict
	}
	if turn.Phase == "completed" || turn.Phase == "cancelled" {
		return turn, c.ErrConflict
	}
	if turn.Phase == "active" || turn.Phase == "blocked" && turn.Job.SubmissionState != "prepared" && turn.Job.SessionID != "" {
		observed, err := code.Observe(ctx, turn.Job)
		if err != nil {
			return turn, err
		}
		if !observed.Terminal && action == "cancel" {
			return turn, errors.New("saved native turn is still running or uncertain")
		}
		turn.Job = observedModel(turn.Job, observed)
		if observed.Terminal {
			turn.Job.SubmissionState = "completed"
		}
	}
	if action == "observe" {
		if turn.Phase == "blocked" && turn.Reason == "native_observation_unavailable" {
			if err = client.SetDelivery(ctx, c.DeliveryUpdate{RestoreEpoch: turn.RestoreEpoch, DeliveryID: delivery, InstanceID: turn.InstanceID, Generation: turn.Generation, State: "running", Intent: &c.NativeIntent{SessionID: session, MessageID: message}}); err != nil {
				return turn, err
			}
			turn.Phase, turn.Reason = "active", ""
			turn.Job.ReconcileFailures = 0
			turn.Job.ReconcileError = ""
			return turn, saveMasterTurn(cfg, &turn)
		}
		return turn, nil
	}
	var intent *c.NativeIntent
	if session != "" || message != "" {
		intent = &c.NativeIntent{SessionID: session, MessageID: message}
	}
	if err = client.SetDelivery(ctx, c.DeliveryUpdate{RestoreEpoch: turn.RestoreEpoch, DeliveryID: delivery, InstanceID: turn.InstanceID, Generation: turn.Generation, State: "cancelled", Reason: "operator_resolved_native_evidence", Intent: intent}); err != nil {
		return turn, err
	}
	turn.Phase, turn.Reason = "cancelled", "operator_resolved_native_evidence"
	return turn, saveMasterTurn(cfg, &turn)
}
