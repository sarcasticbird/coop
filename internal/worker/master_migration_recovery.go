package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

// RecoverMasterMigration is an explicit stopped-store operation. Startup never
// calls it. It completes only the exact immutable before/after publication.
func RecoverMasterMigration(cfg Config, expected string) error {
	if cfg.Version != 3 || ValidateConfig(cfg) != nil || cfg.Master.InstanceID != expected {
		return errors.New("recovery requires exact v3 placement")
	}
	s := NewVersionedFileStore(cfg.StateDir, 3)
	release, err := s.Lock()
	if err != nil {
		return err
	}
	defer release()
	lockPath := filepath.Join(s.Dir, "wake", "v1", "producer.lock")
	if err = safePath(lockPath); err != nil {
		return err
	}
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 {
		return errors.New("unsafe migration producer lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	backup, err := readPrivate(filepath.Join(s.Dir, "master-migration-backup.json"), migrationLimit)
	if err != nil {
		return err
	}
	var files map[string][]byte
	if json.Unmarshal(backup, &files) != nil {
		return errors.New("invalid master migration backup")
	}
	for rel := range files {
		if rel == "." || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("invalid backup member path")
		}
	}
	before := files["state.json"]
	var state State
	if strictTeamJSON(before, &state) != nil || state.Version != 2 || state.InstanceID != expected || validateState(state) != nil {
		return errors.New("migration backup identity mismatch")
	}
	state.Version = 3
	after, err := json.Marshal(state)
	if err != nil {
		return err
	}
	m := masterMigration{Version: 1, From: 2, To: 3, InstanceID: expected, BeforeSHA: BodyHash(string(before)), AfterSHA: BodyHash(string(after)), BackupSHA: BodyHash(string(backup)), Phase: "prepared"}
	if raw, e := readPrivate(filepath.Join(s.Dir, "master-migration.json"), 4096); e == nil {
		var saved masterMigration
		if strictTeamJSON(raw, &saved) != nil || (saved.Phase != "prepared" && saved.Phase != "verified") {
			return errors.New("migration journal requires inspection")
		}
		saved.Phase = "prepared"
		if saved != m {
			return errors.New("migration journal mismatch")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	current, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20))
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) && !bytes.Equal(current, after) {
		return errors.New("migration state changed after snapshot")
	}
	// The snapshot contains private old wake/claim evidence. No changed or new
	// producer record is accepted while the transition is incomplete.
	err = filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(s.Dir, path)
		if e != nil {
			return e
		}
		switch rel {
		case "worker.lock", "state.json", "master-migration.json", "master-migration-backup.json", filepath.Join("wake", "v1", "producer.lock"):
			return nil
		}
		raw, e := readPrivate(path, migrationLimit/2)
		if e != nil {
			return e
		}
		if !bytes.Equal(files[rel], raw) {
			return errors.New("migration evidence changed")
		}
		if _, ok := files[rel]; !ok {
			return errors.New("new migration evidence requires inspection")
		}
		return nil
	})
	if err != nil {
		return err
	}
	for rel, raw := range files {
		if rel == "state.json" {
			continue
		}
		actual, e := readPrivate(filepath.Join(s.Dir, rel), migrationLimit/2)
		if e != nil || !bytes.Equal(actual, raw) {
			return errors.New("migration evidence missing or changed")
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = atomicPrivate(filepath.Join(s.Dir, "master-migration.json"), data); err != nil {
		return err
	}
	if err = atomicPrivate(filepath.Join(s.Dir, "state.json"), after); err != nil {
		return err
	}
	m.Phase = "verified"
	data, err = json.Marshal(m)
	if err != nil {
		return err
	}
	if err = atomicPrivate(filepath.Join(s.Dir, "master-migration.json"), data); err != nil {
		return err
	}
	_, err = s.Load()
	return err
}
