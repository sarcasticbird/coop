package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type epochLineage struct {
	MasterID   string        `json:"master_id"`
	Epoch      int64         `json:"epoch"`
	Principals []c.Principal `json:"principals,omitempty"`
	Placements []c.Placement `json:"placements,omitempty"`
}

func readLineage(path string) (epochLineage, error) {
	var v epochLineage
	b, e := privateRead(path, 16<<20)
	if e != nil {
		return v, e
	}
	if json.Unmarshal(b, &v) != nil || !validID(v.MasterID) || v.Epoch < 1 {
		return v, c.ErrConflict
	}
	return v, nil
}
func writeLineage(path string, v epochLineage) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".epoch-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, e = f.Write(encode(v))
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}

// Restore reserves a new epoch in the independent lineage before publishing
// the snapshot. A failed attempt consumes the epoch, never reuses it.
func Restore(ctx context.Context, backup, target, lineage string, dryRun bool) (out BackupManifest, err error) {
	m, err := readBackup(ctx, backup)
	if err != nil {
		return m, err
	}
	snapshotEpoch := m.Epoch
	if !filepath.IsAbs(lineage) || !filepath.IsAbs(target) || filepath.Clean(lineage) == filepath.Join(filepath.Clean(target), "master.sqlite.epoch") {
		return m, c.ErrInvalid
	}
	lock, err := lockFile(lineage + ".lock")
	if err != nil {
		return m, err
	}
	defer func() { _ = lock.Close() }()
	old, err := readLineage(lineage)
	if err != nil {
		return m, err
	}
	if old.MasterID != m.MasterID || old.Epoch < m.Epoch || old.Epoch == math.MaxInt64 {
		return m, c.ErrConflict
	}
	m.Epoch = old.Epoch + 1
	if entries, e := os.ReadDir(target); e == nil {
		if e = privateInfo(target, true); e != nil {
			return m, e
		}
		if len(entries) != 0 {
			return m, c.ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return m, e
	}
	if dryRun {
		return m, nil
	}
	if err = emptyPrivateDir(target); err != nil {
		return m, err
	}
	targetLock, err := lockFile(filepath.Join(target, "master.sqlite.lock"))
	if err != nil {
		return m, err
	}
	defer func() { _ = targetLock.Close() }()
	next := old
	next.Epoch = m.Epoch
	if err = writeLineage(lineage, next); err != nil {
		return m, err
	}
	src, err := os.Open(filepath.Join(backup, "master.sqlite"))
	if err != nil {
		return m, err
	}
	defer func() { _ = src.Close() }()
	path := filepath.Join(target, "master.sqlite")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Sync()
	}
	err = errors.Join(err, dst.Close())
	if err != nil {
		return m, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return m, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer func() { _ = tx.Rollback() }()
	var id, epoch string
	if err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='master_id'").Scan(&id); err != nil {
		return m, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='epoch'").Scan(&epoch); err != nil {
		return m, err
	}
	if id != m.MasterID || epoch != strconv.FormatInt(snapshotEpoch, 10) {
		return m, c.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='epoch'", strconv.FormatInt(m.Epoch, 10)); err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value='1' WHERE key='quiescent'"); err != nil {
		return m, err
	}
	if err = restoreRegistry(ctx, tx, old); err != nil {
		return m, err
	}
	if err = tx.Commit(); err != nil {
		return m, err
	}
	if err = checkSnapshot(ctx, path); err != nil {
		return m, err
	}
	config, err := privateRead(filepath.Join(backup, "config.toml"), c.MaxJSON)
	if err != nil {
		return m, err
	}
	if err = immutableFile(filepath.Join(target, "config.original.toml"), config); err != nil {
		return m, err
	}
	return m, immutableFile(filepath.Join(target, "restore.json"), encode(m))
}
func admission(ctx context.Context, q queryer) error {
	var quiet string
	if err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='quiescent'").Scan(&quiet); err != nil {
		return err
	}
	if quiet != "0" {
		return c.ErrEpoch
	}
	return nil
}
