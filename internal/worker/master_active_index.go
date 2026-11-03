package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// These are rebuildable hints, never execution or recovery evidence. Canonical
// records stay in their original locations for idempotency and reconciliation.
// Inbox writers own the worker lock (offline recovery owns it too); outbox
// writers and readers own the producer lock. Rebuild once per worker lifetime.
func masterIndexDir(cfg Config, kind string) string {
	return filepath.Join(masterDir(cfg), "indexes", kind)
}

func markMasterPending(cfg Config, kind, key, id string) error {
	return atomicPrivate(filepath.Join(masterIndexDir(cfg, kind), key), []byte(id))
}

func unmarkMasterPending(cfg Config, kind, key string) error {
	path := filepath.Join(masterIndexDir(cfg, kind), key)
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func clearMasterIndex(cfg Config, kind string) error {
	return prepareMasterIndex(cfg, kind, true)
}

func prepareMasterIndex(cfg Config, kind string, clear bool) error {
	dir := masterIndexDir(cfg, kind)
	if err := ensurePrivateDir(dir, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		staging := strings.HasPrefix(entry.Name(), ".worker-")
		if entry.IsDir() || (!staging && filepath.Ext(entry.Name()) != ".json") {
			return errors.New("invalid master index entry")
		}
		if clear || staging {
			// atomicPrivate can leave a private staging file after a process is
			// killed. It is a derived hint, not execution evidence.
			if _, err := readPrivate(filepath.Join(dir, entry.Name()), 4096); err != nil {
				return err
			}
			if err := unmarkMasterPending(cfg, kind, entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func terminalMasterTurn(turn MasterTurn) bool {
	return turn.Phase == "completed" || turn.Phase == "cancelled"
}

// Called under the producer lock on both cold and warm scans. A hint without a
// canonical outbox record can result from a crash before QueueMasterReply's
// immutable write. No publication is possible before that write succeeds.
func pruneMissingOutboxHints(cfg Config) error {
	entries, err := os.ReadDir(masterIndexDir(cfg, "outbox"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, err := readPrivate(filepath.Join(masterIndexDir(cfg, "outbox"), entry.Name()), 4096)
		if err != nil {
			return err
		}
		if !hexSHA.Match(id) || entry.Name() != string(id)+".json" {
			return errors.New("invalid master outbox index identity")
		}
		_, err = os.Lstat(filepath.Join(masterDir(cfg), "outbox", entry.Name()))
		if os.IsNotExist(err) {
			if err := unmarkMasterPending(cfg, "outbox", entry.Name()); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (m *MasterComms) pendingTurns() ([]MasterTurn, error) {
	if !m.inboxIndexed {
		turns, err := masterTurns(m.Config)
		if err != nil {
			return nil, err
		}
		if err := clearMasterIndex(m.Config, "inbox"); err != nil {
			return nil, err
		}
		recovery := masterRecoveryView{cfg: m.Config}
		for _, turn := range turns {
			if terminalMasterTurn(turn) {
				continue
			}
			retired, err := recovery.retiredTurn(turn)
			if err != nil {
				return nil, err
			}
			if !retired {
				if err := markMasterPending(m.Config, "inbox", BodyHash(turn.Delivery.ID)+".json", turn.Delivery.ID); err != nil {
					return nil, err
				}
			}
		}
		m.inboxIndexed = true
	}
	entries, err := os.ReadDir(masterIndexDir(m.Config, "inbox"))
	if err != nil {
		return nil, err
	}
	if len(entries) > c.MaxMessages {
		return nil, c.ErrCapacity
	}
	turns := []MasterTurn{}
	for _, entry := range entries {
		id, err := readPrivate(filepath.Join(masterIndexDir(m.Config, "inbox"), entry.Name()), 4096)
		if err != nil {
			return nil, err
		}
		if entry.Name() != BodyHash(string(id))+".json" {
			return nil, errors.New("invalid master inbox index identity")
		}
		turn, err := loadMasterTurn(m.Config, string(id))
		if os.IsNotExist(err) {
			// A failed initial save can commit the hint without the turn. Under
			// the worker lock no writer can still be completing that save.
			// Confirm the canonical file itself is absent: missing recovery
			// evidence must not discard a real turn.
			if _, statErr := os.Lstat(masterRecordPath(m.Config, string(id))); os.IsNotExist(statErr) {
				if err := unmarkMasterPending(m.Config, "inbox", entry.Name()); err != nil {
					return nil, err
				}
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if !terminalMasterTurn(turn) {
			turns = append(turns, turn)
		}
	}
	sortMasterTurns(turns)
	return turns, nil
}
