package worker

import (
	"encoding/json"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeActionIntent struct {
	Version                                        int
	MasterID, AgentID, InstanceID, JobID, InputSHA string
	Generation                                     int64
	Goal                                           c.GoalRef
	Action                                         NativeAction
}

func nativeActionIntentPath(cfg Config, id, request string) string {
	return filepath.Join(masterDir(cfg), "action-intents", BodyHash(id+"\x00"+request)+".json")
}
func persistNativeActionIntent(cfg Config, x NativeExecution, in NativeAction) error {
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > c.MaxJSON {
		return c.ErrSize
	}
	saved := nativeActionIntent{Version: 1, MasterID: x.MasterID, AgentID: cfg.Master.AgentID, InstanceID: x.InstanceID, Generation: x.Generation, Goal: x.Goal, JobID: x.Job.ID, InputSHA: BodyHash(string(raw)), Action: in}
	data, err := json.Marshal(saved)
	if err != nil || len(data) > c.MaxJSON {
		return c.ErrSize
	}
	path := nativeActionIntentPath(cfg, x.Job.ID, in.RequestID)
	if old, e := readPrivate(path, c.MaxJSON); e == nil {
		if string(old) != string(data) {
			return c.ErrConflict
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = ensurePrivateDir(filepath.Dir(path), true); err != nil {
		return err
	}
	return writeWakeImmutable(path, data)
}
func loadNativeActionIntent(cfg Config, x NativeExecution, r nativeActionRecord) (NativeAction, error) {
	var saved nativeActionIntent
	raw, err := readPrivate(nativeActionIntentPath(cfg, x.Job.ID, r.RequestID), c.MaxJSON)
	if err != nil {
		return NativeAction{}, err
	}
	if strictTeamJSON(raw, &saved) != nil || saved.Version != 1 || saved.MasterID != cfg.Master.MasterID || saved.AgentID != cfg.Master.AgentID || saved.InstanceID != cfg.Master.InstanceID || saved.Generation != cfg.Master.Generation || saved.Goal != x.Goal || saved.JobID != x.Job.ID || saved.InputSHA != r.InputSHA || saved.Action.Operation != r.Operation || saved.Action.RequestID != r.RequestID {
		return NativeAction{}, c.ErrConflict
	}
	encoded, err := json.Marshal(saved.Action)
	if err != nil || BodyHash(string(encoded)) != r.InputSHA {
		return NativeAction{}, c.ErrConflict
	}
	return saved.Action, nil
}

// RetainNativeAction seeds an exact legacy intent without changing its journal
// or contacting a remote service. Future actions retain this automatically.
func RetainNativeAction(cfg Config, id string, in NativeAction) error {
	if cfg.Version != 3 || cfg.Master == nil || in.RequestID == "" {
		return c.ErrInvalid
	}
	release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return err
	}
	defer release()
	producer, err := lockNativeProducer(cfg)
	if err != nil {
		return err
	}
	defer producer()
	x, err := loadNativeExecution(cfg, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return err
	}
	if retired, e := recoveryRetired(cfg, "engineering", id+".json", raw); e != nil || retired {
		if e != nil {
			return e
		}
		return c.ErrEpoch
	}
	raw, err = readPrivate(nativeActionPath(cfg, id, in.RequestID), c.MaxJSON)
	if err != nil {
		return err
	}
	var record nativeActionRecord
	encoded, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if strictTeamJSON(raw, &record) != nil || record.Version != 3 || record.JobID != id || record.RequestID != in.RequestID || record.Operation != in.Operation || record.InputSHA != BodyHash(string(encoded)) {
		return c.ErrConflict
	}
	return persistNativeActionIntent(cfg, x, in)
}
