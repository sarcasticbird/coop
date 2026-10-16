package worker

import (
	"errors"
	"os"
	"path/filepath"
)

// CheckInstall permits staging before explicit conversion, never conversion on
// load. The operator must keep workers, producers and manual sessions stopped.
func CheckInstall(c Config, previous string) error {
	if previous != "" {
		old, err := LoadConfig(previous)
		if err != nil {
			return err
		}
		if old.Name != c.Name || !sameActor(old.Self, c.Self) || old.StateDir != c.StateDir || old.Version > c.Version {
			return errors.New("installation cannot change identity, state directory or downgrade configuration")
		}
	}
	s := NewVersionedFileStore(c.StateDir, c.Version)
	if _, err := os.Lstat(c.StateDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	release, err := s.Lock()
	if err != nil {
		return err
	}
	defer release()
	// The immutable backup is published before the inspection hold. Its
	// presence alone is an interrupted migration, not an unused v1 store.
	if _, err := os.Lstat(filepath.Join(c.StateDir, "migration-backup.json")); err == nil {
		if _, err := os.Lstat(filepath.Join(c.StateDir, "migration.json")); os.IsNotExist(err) {
			return errors.New("migration backup requires inspection; no automatic retry or rollback")
		} else if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// A fresh ledger is allowed. Existing/malformed migration evidence still
	// must pass the versioned reader even when state.json is absent.
	state, err := s.Load()
	if err != nil {
		if c.Version != 2 {
			return err
		}
		state, err = NewFileStore(c.StateDir).Load()
		if err != nil {
			return err
		}
	}
	w := NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), state.Version)
	if _, err := os.Lstat(filepath.Join(c.StateDir, "wake")); os.IsNotExist(err) {
		if state.Version == 2 {
			// Load also returns a new in-memory state for an unused directory.
			// Only that case may lack the v2 old-producer fence; persisted
			// ledger or migration evidence means this is a partial restore.
			for _, name := range []string{"state.json", "migration.json"} {
				if _, err := os.Lstat(filepath.Join(c.StateDir, name)); !os.IsNotExist(err) {
					return errors.New("v2 installation requires complete fenced wake storage")
				}
			}
		}
		return nil
	} else if err != nil {
		return err
	}
	if err := w.ensure(false); err != nil {
		return err
	}
	unlock, err := w.producerLockMode(true)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = w.Requests()
	return err
}
