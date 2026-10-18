package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// NativeEngineering shares the existing worker's single native slot. Catalog
// refresh belongs to its asynchronous reader and cannot hold the slot.
type NativeEngineering interface {
	HasActive() bool
	Ready() bool
	Tick(context.Context) (bool, error)
}
type MasterRuntime struct {
	Comms       *MasterComms
	Engineering NativeEngineering
}

func (e *Engine) initMaster() error {
	if e.MasterRuntime != nil {
		return nil
	}
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
	if state.Version != 3 || !masterStateInstanceAllowed(e.Config, state.InstanceID) {
		return errors.New("master worker state identity mismatch")
	}
	for _, job := range state.Jobs {
		if job.Phase != "completed" {
			return errors.New("legacy work must finish under its original authority")
		}
	}
	client, err := masterclient.LoadConfig(e.Config.Master.ClientConfig)
	if err != nil {
		return err
	}
	comms, err := NewMasterComms(e.Config, client, e.OpenCode, e.Clock)
	if err != nil {
		return err
	}
	reader, ok := e.GitHub.(NativeCatalogReader)
	if !ok {
		return errors.New("native GitHub catalog reader unavailable")
	}
	if e.Checkouts == nil {
		return errors.New("native checkouts unavailable")
	}
	engineering, err := NewNativeDispatcher(e.Config, client, reader, e.OpenCode, e.Checkouts, e.Clock)
	if err != nil {
		return err
	}
	e.MasterRuntime = &MasterRuntime{Comms: comms, Engineering: engineering}
	return nil
}
func (e *Engine) tickMaster(ctx context.Context) (result error) {
	if err := e.initMaster(); err != nil {
		return err
	}
	r := e.MasterRuntime
	m := r.Comms
	defer func() {
		if writer, ok := e.Store.(interface{ WriteStatus(Status) error }); ok {
			state := "idle"
			if result != nil {
				state = "blocked"
			}
			if m.Busy {
				state = "working"
			}
			if m.Paused {
				state = "paused"
			}
			status := Status{Name: e.Config.Name, Enabled: true, Actor: e.Config.Self, State: state, WakeEnabled: true, Heartbeat: m.Clock.Now()}
			if result != nil {
				status.LastError = errorCategory(result)
			}
			if err := writer.WriteStatus(status); result == nil {
				result = err
			}
		}
	}()
	paused, err := e.paused()
	if err != nil {
		return err
	}
	admission, err := e.admissionPaused()
	if err != nil {
		return err
	}
	m.Paused = paused || admission
	engineeringHeld := false
	if recovery, ok := r.Engineering.(interface{ RecoveryBlocked() bool }); ok {
		engineeringHeld = recovery.RecoveryBlocked()
	}
	conversation, err := m.Active()
	if err != nil {
		return err
	}
	nativeActive := r.Engineering != nil && r.Engineering.HasActive()
	if conversation != nil && nativeActive {
		return errors.New("conflicting native work")
	}
	m.Busy = nativeActive || conversation != nil
	// Receipts and outbox progress always precede GitHub-dependent work.
	if err = m.Tick(ctx); err != nil {
		if conversation != nil {
			if localErr := m.observeConversationLocal(ctx, conversation, paused); localErr != nil {
				return errors.Join(err, localErr)
			}
		}
		// Local native observation/checkpoint can continue during a master
		// outage. Action helpers independently require fresh master authority.
		if observer, ok := r.Engineering.(interface {
			ObserveOnly(context.Context) (bool, error)
		}); nativeActive && ok {
			_, localErr := observer.ObserveOnly(ctx)
			if localErr != nil {
				return errors.Join(err, localErr)
			}
		}
		return err
	}
	if engineeringHeld && !nativeActive {
		if m.Paused || m.Recovery {
			return nil
		}
		if r.Engineering.Ready() {
			_, err = r.Engineering.Tick(ctx)
			return err
		}
		return nil
	}
	if conversation != nil {
		m.Busy = false
		if paused {
			if err = m.Code.Abort(ctx, conversation.Job); err != nil {
				return err
			}
			conversation.Phase, conversation.Reason = "blocked", "local_pause"
			if err = saveMasterTurn(e.Config, conversation); err != nil {
				return err
			}
			return m.Client.SetDelivery(ctx, coordination.DeliveryUpdate{RestoreEpoch: conversation.RestoreEpoch, DeliveryID: conversation.Delivery.ID, InstanceID: conversation.InstanceID, Generation: conversation.Generation, State: "blocked", Reason: "local_pause", Intent: &coordination.NativeIntent{SessionID: conversation.Job.SessionID, MessageID: conversation.Job.MessageID}})
		}
		return m.Advance(ctx, conversation)
	}
	if nativeActive {
		completed, err := r.Engineering.Tick(ctx)
		if completed {
			m.connection.Conversations = 0
			if saveErr := m.saveConnection(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if m.Paused || m.Recovery {
		return nil
	}
	ready := r.Engineering != nil && r.Engineering.Ready()
	if !ready || m.connection.Conversations < 2 {
		m.Busy = false
		turn, err := m.Next(ctx)
		if err != nil {
			return err
		}
		if turn != nil {
			return m.Advance(ctx, turn)
		}
	}
	if ready {
		completed, err := r.Engineering.Tick(ctx)
		if completed {
			m.connection.Conversations = 0
			if saveErr := m.saveConnection(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	return nil
}
func (e *Engine) runMaster(ctx context.Context) error {
	release, err := e.Store.Lock()
	if err != nil {
		return err
	}
	defer release()
	if err = e.initMaster(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = e.tickMaster(ctx); err != nil {
			category := errorCategory(err)
			if category != last {
				slog.Warn("master worker waiting", "category", category)
				last = category
			}
		} else {
			last = ""
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
