package worker

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type masterOutbox struct {
	Version              int
	MasterID, InstanceID string
	Epoch                int64
	Reply                c.PublishReply
}

// Shared with native actions, stopped reconciliation and placement adoption.
func lockMasterProducer(cfg Config) (func(), error) {
	dir := filepath.Join(masterDir(cfg), "actions")
	if e := ensurePrivateDir(dir, true); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "producer.lock")
	if e := safePath(path); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		if errors.Is(e, unix.EWOULDBLOCK) {
			return nil, errors.Join(c.ErrConflict, errNativeProducerBusy)
		}
		return nil, e
	}
	return func() { _ = f.Close() }, nil
}

// A periodic flush can briefly hold the shared producer while a native turn
// queues its prepared reply. Wait only for that contention, before any writes.
func lockMasterReplyProducer(cfg Config) (func(), error) {
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	retry := time.NewTicker(50 * time.Millisecond)
	defer retry.Stop()
	for {
		release, err := lockMasterProducer(cfg)
		if !errors.Is(err, errNativeProducerBusy) {
			return release, err
		}
		select {
		case <-timeout.C:
			return nil, err
		case <-retry.C:
		}
	}
}

func QueueMasterReply(cfg Config, deliveryID, body string) error {
	if cfg.Version != 3 || cfg.Master == nil || strings.TrimSpace(body) == "" || len(body) > c.MaxText {
		return c.ErrInvalid
	}
	release, e := lockMasterReplyProducer(cfg)
	if e != nil {
		return e
	}
	defer release()
	if _, err := NewVersionedFileStore(cfg.StateDir, 3).Load(); err != nil {
		return err
	}
	turn, err := loadMasterTurn(cfg, deliveryID)
	if err != nil {
		return err
	}
	retired, retireErr := retiredTurn(cfg, turn)
	if retireErr != nil {
		return retireErr
	}
	if retired {
		return c.ErrEpoch
	}
	if turn.Phase != "active" || turn.Job.Master == nil || turn.Job.SessionID == "" || turn.Job.MessageID == "" {
		return errors.New("reply requires a saved running master turn")
	}
	id := BodyHash("master-reply\x00" + turn.MasterID + "\x00" + deliveryID + "\x00" + turn.Job.MessageID)
	in := masterOutbox{Version: 1, MasterID: turn.MasterID, InstanceID: turn.InstanceID, Epoch: turn.RestoreEpoch, Reply: c.PublishReply{RestoreEpoch: turn.RestoreEpoch, DeliveryID: deliveryID, InstanceID: turn.InstanceID, Generation: turn.Generation, Intent: c.NativeIntent{SessionID: turn.Job.SessionID, MessageID: turn.Job.MessageID}, Body: body, RequestID: id}}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	dir := filepath.Join(masterDir(cfg), "outbox")
	if err = ensurePrivateDir(dir, true); err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	if old, e := readPrivate(path, c.MaxJSON); e == nil {
		if string(old) == string(raw) {
			return nil
		}
		return c.ErrConflict
	} else if !os.IsNotExist(e) {
		return e
	}
	return writeWakeImmutable(path, raw)
}
func (m *MasterComms) FlushOutbox(ctx context.Context) error {
	release, e := lockMasterProducer(m.Config)
	if errors.Is(e, errNativeProducerBusy) {
		// A native continuation or action owns the producer. Leave the durable
		// outbox for the next tick; this is not a transport outage and must not
		// cancel the native operation that currently holds the shared lock.
		return nil
	}
	if e != nil {
		return e
	}
	defer release()

	dir := filepath.Join(masterDir(m.Config), "outbox")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			return errors.New("invalid outbox entry")
		}
		raw, err := readPrivate(filepath.Join(dir, e.Name()), c.MaxJSON)
		if err != nil {
			return err
		}
		if _, ok, e := adoptedRecoveryRecord(m.Config, "outbox", e.Name(), raw); e != nil {
			return e
		} else if ok {
			continue
		}
		var in masterOutbox
		if strictTeamJSON(raw, &in) != nil || in.Version != 1 || in.MasterID != m.Config.Master.MasterID || in.InstanceID != m.Config.Master.InstanceID || e.Name() != in.Reply.RequestID+".json" || !hexSHA.MatchString(in.Reply.RequestID) {
			return errors.New("outbox ownership mismatch")
		}
		retired, retireErr := recoveryRetired(m.Config, "outbox", e.Name(), raw)
		if retireErr != nil {
			return retireErr
		}
		if retired {
			continue
		}
		if in.Epoch != m.connection.Epoch {
			return c.ErrEpoch
		}
		result := filepath.Join(masterDir(m.Config), "published", e.Name())
		if saved, err := readPrivate(result, c.MaxJSON); err == nil {
			var receipt c.MessageReceipt
			if strictTeamJSON(saved, &receipt) != nil || receipt.MasterID != in.MasterID || receipt.MessageID == "" {
				return errors.New("invalid saved reply receipt")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		receipt, err := m.Client.PublishReply(ctx, in.Reply)
		if err != nil {
			if errors.Is(err, c.ErrConflict) {
				canceled, proofErr := m.masterReplyCanceled(ctx, in)
				if proofErr != nil {
					return errors.Join(err, proofErr)
				}
				if canceled {
					continue
				}
			}
			return err
		}
		if receipt.RestoreEpoch != m.connection.Epoch {
			return c.ErrEpoch
		}
		data, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if err = ensurePrivateDir(filepath.Dir(result), true); err != nil {
			return err
		}
		if err = writeWakeImmutable(result, data); err != nil {
			return err
		}
	}
	return nil
}

// Explicit cancellation retires dispatch, not the saved reply or an unknown
// native write. Require matching local recovery and current master evidence;
// never fabricate a published receipt or weaken other publication failures.
func (m *MasterComms) masterReplyCanceled(ctx context.Context, in masterOutbox) (bool, error) {
	turn, err := loadMasterTurn(m.Config, in.Reply.DeliveryID)
	if err != nil {
		return false, err
	}
	if turn.Phase != "cancelled" || turn.RestoreEpoch != in.Epoch || in.Reply.RestoreEpoch != in.Epoch || turn.InstanceID != in.InstanceID || in.Reply.InstanceID != in.InstanceID || turn.Generation != in.Reply.Generation || turn.Job.SessionID != in.Reply.Intent.SessionID || turn.Job.MessageID != in.Reply.Intent.MessageID {
		return false, nil
	}
	status, err := m.Client.MessageStatus(ctx, turn.Message.ID)
	if err != nil {
		return false, err
	}
	if status.MasterID != in.MasterID || status.RestoreEpoch != in.Epoch || status.MessageID != turn.Message.ID {
		return false, c.ErrConflict
	}
	for _, d := range status.Deliveries {
		if d.ID == in.Reply.DeliveryID && d.MessageID == turn.Message.ID && d.AgentID == m.Config.Master.AgentID && d.State == "cancelled" && d.Intent != nil && *d.Intent == in.Reply.Intent {
			return true, nil
		}
	}
	return false, nil
}
