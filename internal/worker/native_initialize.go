package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

// Native workers start with their pinned identity. No legacy ledger, wake
// queue, synthetic migration or backup is needed for previously unused storage.
func initializeNativeState(cfg Config) (string, error) {
	if err := ValidateConfig(cfg); err != nil {
		return "", err
	}
	s := NewVersionedFileStore(cfg.StateDir, 3)
	release, err := s.Lock()
	if err != nil {
		return "", err
	}
	defer release()
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != "worker.lock" {
			return "", errors.New("init-state requires unused storage; inspect existing evidence")
		}
	}
	state := newState()
	state.Version, state.InstanceID = 3, cfg.Master.InstanceID
	if err = validateState(state); err != nil {
		return "", err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if err = atomicPrivate(filepath.Join(s.Dir, "state.json"), raw); err != nil {
		return "", err
	}
	_, err = s.Load()
	return state.InstanceID, err
}

// A migrated store must still validate its migration evidence. The native
// alternative is only for an identity ledger with no legacy operational data.
func (s *FileStore) nativeCompatible() error {
	for _, name := range []string{"migration.json", "migration-backup.json", "master-migration-backup.json", "master-legacy-retirement.json", "wake"} {
		if _, err := os.Lstat(filepath.Join(s.Dir, name)); !os.IsNotExist(err) {
			return errors.New("legacy state requires verified master migration")
		}
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20))
	if err != nil {
		return errors.New("native worker requires explicit initialization")
	}
	var state State
	if strictTeamJSON(raw, &state) != nil || state.Version != 3 || validateState(state) != nil {
		return errors.New("invalid native worker identity ledger")
	}
	if len(state.Jobs)+len(state.Sessions)+len(state.Attempts)+len(state.Seen)+len(state.Cursor)+len(state.ReviewCycles) != 0 || state.Schedule != nil || !state.LastProposalAt.IsZero() || !reflect.DeepEqual(state.SnapshotCache, Snapshot{}) {
		return errors.New("legacy operational state requires verified master migration")
	}
	return nil
}
