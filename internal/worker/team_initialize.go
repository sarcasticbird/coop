package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (s *FileStore) checkTeamStartup(state State) error {
	if _, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20)); err != nil {
		return err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "migration.json"), 4096)
	if err != nil {
		return errors.New("team startup requires complete verified migration evidence")
	}
	var m stateMigration
	if strictTeamJSON(raw, &m) != nil || m.Version != 1 || m.Phase != "verified" || m.InstanceID != state.InstanceID {
		return errors.New("team startup migration identity mismatch")
	}
	backup, err := readPrivate(filepath.Join(s.Dir, "migration-backup.json"), migrationLimit)
	if err != nil || BodyHash(string(backup)) != m.BackupSHA {
		return errors.New("team startup migration backup missing or changed")
	}
	var files map[string][]byte
	if json.Unmarshal(backup, &files) != nil || BodyHash(string(files["state.json"])) != m.BeforeSHA {
		return errors.New("team startup migration backup identity mismatch")
	}
	return NewVersionedWakeStore(filepath.Join(s.Dir, "wake", "v1"), 2).ensure(false)
}

// InitializeTeamState is an explicit offline operation for a first worker. It
// reuses migration's immutable backup, producer fence and verified journal.
// A failed/partial attempt is never overwritten or retried automatically.
func InitializeTeamState(c Config) (string, error) {
	if c.Version == 3 {
		if err := ValidateConfig(c); err != nil {
			return "", err
		}
		legacy := c
		legacy.Version = 2
		legacy.Master = nil
		legacy.TaskSource = nil
		id, err := initializeTeamState(legacy, c.Master.InstanceID)
		if err != nil {
			return "", err
		}
		if err = MigrateState(c, 2, 3, id); err != nil {
			return "", err
		}
		return id, nil
	}
	return initializeTeamState(c, "")
}
func initializeTeamState(c Config, instance string) (string, error) {
	if c.Version != 2 {
		return "", errors.New("init-state requires v2 configuration")
	}
	if err := ValidateConfig(c); err != nil {
		return "", err
	}
	s := NewFileStore(c.StateDir)
	release, err := s.Lock()
	if err != nil {
		return "", err
	}
	defer release()
	entries, err := os.ReadDir(c.StateDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != "worker.lock" {
			return "", errors.New("init-state requires unused storage; inspect existing evidence")
		}
	}
	state := newState()
	if instance != "" {
		state.InstanceID = instance
	}
	if err = s.Save(state); err != nil {
		return "", err
	}
	if err = migrateStateLocked(c, s, state.InstanceID, atomicPrivate); err != nil {
		return "", err
	}
	return state.InstanceID, nil
}
