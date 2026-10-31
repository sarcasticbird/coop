package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type MasterTurn struct {
	Version                  int
	MasterID, InstanceID     string
	Generation, RestoreEpoch int64
	Delivery                 c.Delivery
	Message                  c.Message
	BodySHA                  string
	Job                      Job
	Prompt                   string
	Phase, Reason            string
}
type masterConnectionState struct {
	Version              int
	MasterID, InstanceID string
	Generation, Epoch    int64
	Conversations        int
	LastConversationID   string
}

func masterDir(cfg Config) string { return filepath.Join(cfg.StateDir, "master", "v1") }
func masterRecordPath(cfg Config, id string) string {
	return filepath.Join(masterDir(cfg), "inbox", BodyHash(id)+".json")
}
func loadMasterTurn(cfg Config, id string) (MasterTurn, error) {
	var out MasterTurn
	raw, err := readPrivate(masterRecordPath(cfg, id), 256<<10)
	if err != nil {
		return out, err
	}
	return decodeMasterTurn(cfg, id, raw, &masterRecoveryView{cfg: cfg})
}

func decodeMasterTurn(cfg Config, id string, raw []byte, recovery *masterRecoveryView) (MasterTurn, error) {
	var out MasterTurn
	if strictTeamJSON(raw, &out) != nil || out.Version != 1 || out.MasterID != cfg.Master.MasterID || out.Delivery.ID != id || out.Message.ID != out.Delivery.MessageID || out.BodySHA != BodyHash(out.Message.Body) {
		return out, errors.New("master inbox ownership mismatch")
	}
	if out.InstanceID != cfg.Master.InstanceID || out.Generation != cfg.Master.Generation {
		if _, ok, e := recovery.adoptedRecord("inbox", filepath.Base(masterRecordPath(cfg, id)), raw); e != nil || !ok {
			if e != nil {
				return out, e
			}
			return out, c.ErrConflict
		}
	}
	return out, nil
}
func saveMasterTurn(cfg Config, in *MasterTurn) error {
	if in.MasterID != cfg.Master.MasterID || in.InstanceID != cfg.Master.InstanceID || in.Generation != cfg.Master.Generation {
		return c.ErrConflict
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 256<<10 {
		return errors.New("master inbox record too large")
	}
	return atomicPrivate(masterRecordPath(cfg, in.Delivery.ID), raw)
}
func masterTurns(cfg Config) ([]MasterTurn, error) {
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "inbox"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > c.MaxMessages {
		return nil, c.ErrCapacity
	}
	out := []MasterTurn{}
	recovery := masterRecoveryView{cfg: cfg}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			return nil, errors.New("unknown master inbox record")
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "inbox", e.Name()), 256<<10)
		if err != nil {
			return nil, err
		}
		var item MasterTurn
		if strictTeamJSON(raw, &item) != nil || e.Name() != BodyHash(item.Delivery.ID)+".json" {
			return nil, errors.New("invalid master inbox identity")
		}
		item, err = decodeMasterTurn(cfg, item.Delivery.ID, raw, &recovery)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Message.AuthorKind == "owner") != (b.Message.AuthorKind == "owner") {
			return a.Message.AuthorKind == "owner"
		}
		return a.Message.Sequence < b.Message.Sequence
	})
	return out, nil
}
