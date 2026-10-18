package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type masterMigration struct {
	Version, From, To                                 int
	InstanceID, BeforeSHA, AfterSHA, BackupSHA, Phase string
}

// MigrateMasterStateWithRetirements explicitly retires named, drained legacy
// records without changing their historical status or native identities.
func MigrateMasterStateWithRetirements(c Config, expected string, jobs, wakes []string) error {
	return migrateMasterStateRetiring(c, expected, jobs, wakes, atomicPrivate)
}

type masterLegacyRetirement struct {
	Version                          int
	InstanceID, StateSHA             string
	RetiredJobs, AcceptedLegacyWakes []string
}

func drainedLegacyRetirement(j Job) bool {
	return j.Binding == nil && j.Phase == "blocked" && j.MessageID == "" && j.SubmissionState == "" && j.SubmittedAt.IsZero() && migrationJob(j, false) == nil
}
func validateMasterLegacyJobs(c Config, state State) error {
	needsAudit := false
	for _, j := range state.Jobs {
		needsAudit = needsAudit || j.Phase != "completed"
	}
	if !needsAudit {
		return nil
	}
	store := NewVersionedFileStore(c.StateDir, 3)
	if err := store.masterCompatible(); err != nil {
		return err
	}
	raw, err := readPrivate(filepath.Join(store.Dir, "master-migration-backup.json"), migrationLimit)
	if err != nil {
		return err
	}
	var files map[string][]byte
	if json.Unmarshal(raw, &files) != nil {
		return errors.New("invalid retirement backup")
	}
	auditRaw, err := readPrivate(filepath.Join(store.Dir, "master-legacy-retirement.json"), MaxSnapshot)
	if err != nil || !bytes.Equal(auditRaw, files["master-legacy-retirement.json"]) {
		return errors.New("retirement audit changed or missing")
	}
	var audit masterLegacyRetirement
	var before State
	if strictTeamJSON(auditRaw, &audit) != nil || audit.Version != 1 || audit.InstanceID != state.InstanceID || audit.StateSHA != BodyHash(string(files["state.json"])) || strictTeamJSON(files["state.json"], &before) != nil || before.InstanceID != state.InstanceID {
		return errors.New("retirement audit identity mismatch")
	}
	retired := map[string]bool{}
	for _, id := range audit.RetiredJobs {
		j, ok := before.Jobs[id]
		if !ok || retired[id] || !drainedLegacyRetirement(j) {
			return errors.New("invalid retired legacy job")
		}
		retired[id] = true
	}
	for _, j := range state.Jobs {
		if j.Phase == "completed" {
			continue
		}
		previous, _ := json.Marshal(before.Jobs[j.ID])
		current, _ := json.Marshal(j)
		if !retired[j.ID] || !bytes.Equal(previous, current) {
			return errors.New("legacy work must finish under its original authority")
		}
	}
	return nil
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
	return migrateMasterStateRetiring(c, expected, nil, nil, write)
}

func migrateMasterStateRetiring(c Config, expected string, jobs, wakes []string, write func(string, []byte) error) error {
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
	jobIDs, wakeIDs := map[string]bool{}, map[string]bool{}
	for _, group := range []struct {
		ids      []string
		selected map[string]bool
	}{{jobs, jobIDs}, {wakes, wakeIDs}} {
		for _, id := range group.ids {
			if !hexSHA.MatchString(id) || group.selected[id] {
				return errors.New("invalid or duplicate legacy retirement identity")
			}
			group.selected[id] = true
		}
	}
	// Old authority is drained before native admission. Keep every record intact.
	for _, j := range state.Jobs {
		if jobIDs[j.ID] {
			if !drainedLegacyRetirement(j) {
				return errors.New("retirement requires a blocked unbound legacy job with no pending native submission")
			}
			delete(jobIDs, j.ID)
			continue
		}
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
		if wakeIDs[r.ID] {
			if v.Phase != "completed" || v.Job.Phase != "completed" || v.Job.SubmissionState != "running" || !v.Job.TerminalAt.IsZero() || migrationWakeReceipt(c, r, v) != nil {
				return errors.New("legacy wake acceptance requires an exact completed historical receipt")
			}
			delete(wakeIDs, r.ID)
			continue
		}
		if !wakeReceiptTerminal(v) || v.Job.Phase == "active" || v.Job.MessageID != "" && v.Job.TerminalAt.IsZero() {
			return errors.New("master migration refuses unresolved wake turn")
		}
	}
	if len(jobIDs) != 0 || len(wakeIDs) != 0 {
		return errors.New("legacy retirement identity not found")
	}
	if len(jobs) != 0 || len(wakes) != 0 {
		before, e := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20))
		if e != nil {
			return e
		}
		audit, e := json.Marshal(masterLegacyRetirement{1, expected, BodyHash(string(before)), jobs, wakes})
		if e != nil {
			return e
		}
		auditPath := filepath.Join(s.Dir, "master-legacy-retirement.json")
		existing, e := readPrivate(auditPath, MaxSnapshot)
		if os.IsNotExist(e) {
			e = writeWakeImmutable(auditPath, audit)
		} else if e == nil && !bytes.Equal(existing, audit) {
			return errors.New("retirement audit changed; inspect before retrying migration")
		}
		if e != nil {
			return e
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
