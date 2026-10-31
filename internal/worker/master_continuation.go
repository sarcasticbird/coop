package worker

import (
	"context"
	"errors"
	"maps"
	"os"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type continuationCheck struct {
	execution NativeExecution
	err       error
}
type continuationAuthorityReader interface {
	Continuation(context.Context, string) (c.Message, error)
}

func continuationRefusal(err error) string {
	switch {
	case errors.Is(err, errContinuationTarget), errors.Is(err, errNativeCandidateStale), errors.Is(err, os.ErrNotExist):
		return "stale_target"
	case errors.Is(err, errContinuationActions):
		return "unfinished_actions"
	case errors.Is(err, c.ErrForbidden):
		return "authority_changed"
	case errors.Is(err, errNativeDependencyWait):
		return "dependency_not_ready"
	case errors.Is(err, c.ErrUnsupported):
		return "unsupported_worker"
	default:
		return "unsafe_or_changed_evidence"
	}
}
func continuationRetryable(err error) bool {
	if errors.Is(err, errNativeProducerBusy) || errors.Is(err, errContinuationSlot) || errors.Is(err, c.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var oc *OpenCodeError
	var gh *GitHubError
	return errors.As(err, &oc) && (oc.Category == "transport_failed" || oc.Category == "request_failed") || errors.As(err, &gh) && (gh.Category == "transport_failed" || gh.Category == "rate_limited" || gh.Category == "request_failed" && gh.StatusCode >= 500 && gh.StatusCode <= 599)
}
func (m *MasterComms) continuationReceipt(ctx context.Context, turn *MasterTurn, state, reason string, intent *c.NativeIntent) error {
	cfg := m.Config.Master
	if err := m.Client.SetDelivery(ctx, c.DeliveryUpdate{RestoreEpoch: turn.RestoreEpoch, DeliveryID: turn.Delivery.ID, InstanceID: cfg.InstanceID, Generation: cfg.Generation, State: state, Reason: reason, Intent: intent}); err != nil {
		return err
	}
	turn.Delivery.State, turn.Delivery.Reason, turn.Delivery.Intent = state, reason, intent
	turn.Phase = "continuation"
	if state == "resumed" || state == "refused" || state == "cancelled" {
		turn.Phase = "completed"
	}
	return saveMasterTurn(m.Config, turn)
}

// tickContinuation handles typed controls on the event loop, never by prompting
// an agent to interpret chat. Native checkpoint and receipt replay are separate.
func (r *MasterRuntime) tickContinuation(ctx context.Context) (bool, error) {
	m := r.Comms
	if r.continuationStep != nil {
		select {
		case result := <-r.continuationStep:
			r.continuationStep = nil
			turn := r.continuationTurn
			r.continuationTurn = nil
			if d, ok := r.Engineering.(*NativeDispatcher); ok && result.execution.Job.ID != "" {
				x := result.execution
				d.history[x.Job.ID] = x
				if x.Phase == "selected" || x.Phase == "prepared" {
					d.active = &x
				}
			}
			return r.finishContinuationCheck(ctx, turn, result.err)
		default:
			return true, nil
		}
	}
	turns, err := m.pendingTurns()
	if err != nil {
		return false, err
	}
	recovery := masterRecoveryView{cfg: m.Config}
	for _, turn := range turns {
		if turn.Message.Continuation == nil || turn.Phase == "completed" || turn.Phase == "cancelled" {
			continue
		}
		retired, e := recovery.retiredTurn(turn)
		if e != nil {
			return false, e
		}
		if retired {
			continue
		}
		if m.Clock.Now().Before(turn.Job.NextAttemptAt) {
			continue
		}
		reader, ok := m.Client.(interface {
			OwnDelivery(context.Context, string) (c.Delivery, error)
		})
		if !ok {
			return false, c.ErrUnsupported
		}
		delivery, e := reader.OwnDelivery(ctx, turn.Delivery.ID)
		if e != nil {
			return false, e
		}
		if delivery.ID != turn.Delivery.ID || delivery.MessageID != turn.Message.ID || delivery.AgentID != m.Config.Master.AgentID {
			return false, c.ErrConflict
		}
		turn.Delivery = delivery
		if turn.Delivery.State == "resumed" || turn.Delivery.State == "refused" || turn.Delivery.State == "cancelled" {
			turn.Phase = "completed"
			return true, saveMasterTurn(m.Config, &turn)
		}
		d, ok := r.Engineering.(*NativeDispatcher)
		if !ok {
			return true, m.continuationReceipt(ctx, &turn, "refused", "unsupported_worker", nil)
		}
		target := turn.Message.Continuation.Target
		state, intent, stateErr := d.ContinuationState(turn.Delivery.ID, target)
		if state == "resumed" {
			return true, m.continuationReceipt(ctx, &turn, "resumed", "", intent)
		}
		if state == "refused" {
			return true, m.continuationReceipt(ctx, &turn, "refused", continuationRefusal(stateErr), nil)
		}
		if stateErr != nil {
			return true, m.continuationReceipt(ctx, &turn, "refused", continuationRefusal(stateErr), nil)
		}
		if state == "admitted" {
			continue
		} // normal engineering submission/observation owns progress
		if m.Paused || m.Recovery || m.Busy || d.HasActive() {
			continue
		}
		authority, ok := m.Client.(continuationAuthorityReader)
		if !ok {
			return true, m.continuationReceipt(ctx, &turn, "refused", "unsupported_worker", nil)
		}
		if turn.Delivery.State == "received" {
			if e = m.continuationReceipt(ctx, &turn, "admitted", "", nil); e != nil {
				return r.finishContinuationCheck(ctx, &turn, e)
			}
		}
		// Keep all GitHub/native validation off the receipt/heartbeat loop. Only
		// this retained task check owns admission until its bounded result arrives.
		snapshot := *d
		snapshot.history = maps.Clone(d.history)
		stepCtx, cancel := context.WithTimeout(ctx, githubRevalidationTimeout)
		ch := make(chan continuationCheck, 1)
		r.continuationStep = ch
		r.continuationTurn = &turn
		go func(saved MasterTurn) {
			defer cancel()
			check := func(checkCtx context.Context) error {
				current, e := authority.Continuation(checkCtx, saved.Message.ID)
				if e == nil && digestMessage(current) != digestMessage(saved.Message) {
					return c.ErrConflict
				}
				return e
			}
			e := snapshot.RequestContinuation(stepCtx, saved.Delivery.ID, saved.Message.ThreadID, target, saved.Message.Continuation.SourceMessageID, check)
			x, readErr := loadNativeExecution(snapshot.config, target.JobID)
			if readErr != nil {
				e = errors.Join(e, readErr)
				x = NativeExecution{}
			}
			ch <- continuationCheck{execution: x, err: e}
		}(turn)
		return true, nil
	}
	return false, nil
}

func (r *MasterRuntime) finishContinuationCheck(ctx context.Context, turn *MasterTurn, err error) (bool, error) {
	m := r.Comms
	if err != nil {
		if continuationRetryable(err) {
			turn.Job.ReconcileFailures++
			turn.Job.NextAttemptAt = m.Clock.Now().Add(nativeRecoveryBackoff(turn.Job.ReconcileFailures))
			return true, saveMasterTurn(m.Config, turn)
		}
		return true, m.continuationReceipt(ctx, turn, "refused", continuationRefusal(err), nil)
	}
	turn.Phase = "continuation"
	turn.Job.NextAttemptAt = time.Time{}
	return true, saveMasterTurn(m.Config, turn)
}
