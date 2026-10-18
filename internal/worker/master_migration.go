package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type masterMigration struct {
	Version, From, To                                 int
	InstanceID, BeforeSHA, AfterSHA, BackupSHA, Phase string
}

func (s *FileStore) masterCompatible() error {
	raw, err := readPrivate(filepath.Join(s.Dir, "master-migration.json"), 4096)
	if err != nil {
		return errors.New("v3 requires explicit verified master migration")
	}
	var m masterMigration
	if strictTeamJSON(raw, &m) != nil || m.Version != 1 || m.From != 2 || m.To != 3 || m.Phase != "verified" || !hexSHA.MatchString(m.InstanceID) || !hexSHA.MatchString(m.BeforeSHA) || !hexSHA.MatchString(m.AfterSHA) || !hexSHA.MatchString(m.BackupSHA) {
		return errors.New("master migration requires inspection")
	}
	backup, err := readPrivate(filepath.Join(s.Dir, "master-migration-backup.json"), migrationLimit)
	if err != nil || BodyHash(string(backup)) != m.BackupSHA {
		return errors.New("master migration backup missing or changed")
	}
	var files map[string][]byte
	if json.Unmarshal(backup, &files) != nil || BodyHash(string(files["state.json"])) != m.BeforeSHA {
		return errors.New("master migration backup identity mismatch")
	}
	var before State
	if strictTeamJSON(files["state.json"], &before) != nil || before.Version != 2 || before.InstanceID != m.InstanceID {
		return errors.New("master migration instance mismatch")
	}
	return nil
}
func migrateMasterState(c Config, expected string, write func(string, []byte) error) error {
	if c.Version != 3 || ValidateConfig(c) != nil || c.Master.InstanceID != expected || !hexSHA.MatchString(expected) {
		return errors.New("master migration requires v3 config and exact instance")
	}
	s := NewVersionedFileStore(c.StateDir, 2)
	release, err := s.Lock()
	if err != nil {
		return err
	}
	defer release()
	for _, name := range []string{"master-migration.json", "master-migration-backup.json", "master"} {
		if _, err = os.Lstat(filepath.Join(s.Dir, name)); !os.IsNotExist(err) {
			return errors.New("master migration evidence exists; inspect before proceeding")
		}
	}
	w := NewVersionedWakeStore(filepath.Join(s.Dir, "wake", "v1"), 2)
	unlock, err := w.producerLockMode(true)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.Load()
	if err != nil {
		return err
	}
	if state.InstanceID != expected {
		return errors.New("master migration instance mismatch")
	}
	if err = s.checkTeamStartup(state); err != nil {
		return err
	}
	// Old authority is drained before native admission. Keep every record intact.
	for _, j := range state.Jobs {
		if j.Phase != "completed" || migrationJob(j, false) != nil {
			return errors.New("finish or explicitly resolve legacy engineering work before master migration")
		}
	}
	requests, err := w.Requests()
	if err != nil {
		return err
	}
	for _, r := range requests {
		v, e := w.Receipt(r.ID)
		if e != nil {
			return e
		}
		if !wakeReceiptTerminal(v) || v.Job.Phase == "active" || v.Job.MessageID != "" && v.Job.TerminalAt.IsZero() {
			return errors.New("master migration refuses unresolved wake turn")
		}
	}
	files := map[string][]byte{}
	total := 0
	err = filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(s.Dir, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return ensurePrivateDir(path, false)
		}
		raw, e := readPrivate(path, migrationLimit/2)
		if e != nil {
			return e
		}
		if rel == "worker.lock" || rel == filepath.Join("wake", "v1", "producer.lock") {
			return nil
		}
		total += len(raw)
		if total > migrationLimit/2 {
			return errors.New("master migration backup capacity exceeded")
		}
		files[rel] = raw
		return nil
	})
	if err != nil {
		return err
	}
	backup, err := json.Marshal(files)
	if err != nil || len(backup) > migrationLimit {
		return errors.New("master backup exceeds limit")
	}
	if err = writeWakeImmutable(filepath.Join(s.Dir, "master-migration-backup.json"), backup); err != nil {
		return err
	}
	state.Version = 3
	after, err := json.Marshal(state)
	if err != nil {
		return err
	}
	m := masterMigration{Version: 1, From: 2, To: 3, InstanceID: expected, BeforeSHA: BodyHash(string(files["state.json"])), AfterSHA: BodyHash(string(after)), BackupSHA: BodyHash(string(backup)), Phase: "prepared"}
	raw, _ := json.Marshal(m)
	if err = write(filepath.Join(s.Dir, "master-migration.json"), raw); err != nil {
		return err
	}
	if err = write(filepath.Join(s.Dir, "state.json"), after); err != nil {
		return err
	}
	m.Phase = "verified"
	raw, _ = json.Marshal(m)
	if err = write(filepath.Join(s.Dir, "master-migration.json"), raw); err != nil {
		return err
	}
	_, err = NewVersionedFileStore(s.Dir, 3).Load()
	return err
}
func checkMasterInstall(c Config, previous string) error {
	if err := ValidateConfig(c); err != nil {
		return err
	}
	if previous != "" {
		old, err := LoadConfig(previous)
		if err != nil {
			return err
		}
		if old.Version > 3 || old.Name != c.Name || !sameActor(old.Self, c.Self) || old.StateDir != c.StateDir {
			return errors.New("master installation identity changed")
		}
		if old.Version == 3 && (old.Master == nil || old.Master.MasterID != c.Master.MasterID || old.Master.AgentID != c.Master.AgentID || old.Master.Engagement != c.Master.Engagement || old.Master.HostID != c.Master.HostID || old.Master.InstanceID != c.Master.InstanceID || old.Master.Generation != c.Master.Generation) {
			if !masterInstallTransition(old, c) {
				return errors.New("master installation cannot silently change placement")
			}
		}
	}
	s := NewVersionedFileStore(c.StateDir, 3)
	release, err := s.Lock()
	if err != nil {
		return err
	}
	defer release()
	state, err := s.Load()
	if err != nil {
		return err
	}
	if !masterStateInstanceAllowed(c, state.InstanceID) {
		return errors.New("master installation instance mismatch")
	}
	return nil
}
