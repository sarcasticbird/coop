package master

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
)

type BackupManifest = c.BackupManifest

func privateInfo(path string, dir bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || st.Mode().Perm()&0077 != 0 || st.IsDir() != dir || !dir && !st.Mode().IsRegular() {
		return c.ErrForbidden
	}
	return nil
}
func privateRead(path string, limit int64) ([]byte, error) {
	if err := privateInfo(path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, c.ErrSize
	}
	return b, nil
}
func immutableFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, w := f.Write(b)
	if w == nil {
		w = f.Sync()
	}
	return errors.Join(w, f.Close())
}
func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func emptyPrivateDir(path string) error {
	if !filepath.IsAbs(path) {
		return c.ErrInvalid
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := privateInfo(path, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return c.ErrConflict
	}
	return nil
}
func lockFile(path string) (*os.File, error) {
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() {
			return nil, c.ErrForbidden
		}
		if e = privateInfo(path, false); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, c.ErrConflict
	}
	return f, nil
}
func checkSnapshot(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var result string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return c.ErrConflict
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return c.ErrConflict
	}
	return rows.Err()
}

// VACUUM INTO is SQLite's transactionally consistent snapshot operation. It
// copies committed WAL content without copying a live database file.
func (s *Store) Backup(ctx context.Context, dir string, config []byte) (BackupManifest, error) {
	out := BackupManifest{Version: 1, MasterID: s.id, Epoch: s.epoch}
	if len(config) > c.MaxJSON {
		return out, c.ErrSize
	}
	if err := emptyPrivateDir(dir); err != nil {
		return out, err
	}
	path := filepath.Join(dir, "master.sqlite")
	// Reserve an empty owner-only file; SQLite accepts an empty destination.
	if err := immutableFile(path, nil); err != nil {
		return out, err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return out, err
	}
	if err := checkSnapshot(ctx, path); err != nil {
		return out, err
	}
	var err error
	out.DatabaseSHA256, err = hashFile(path)
	if err != nil {
		return out, err
	}
	if err = immutableFile(filepath.Join(dir, "config.toml"), config); err != nil {
		return out, err
	}
	out.ConfigSHA256, err = hashFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return out, err
	}
	return out, immutableFile(filepath.Join(dir, "manifest.json"), encode(out))
}
func readBackup(ctx context.Context, dir string) (BackupManifest, error) {
	var m BackupManifest
	if err := privateInfo(dir, true); err != nil {
		return m, err
	}
	b, err := privateRead(filepath.Join(dir, "manifest.json"), c.MaxJSON)
	if err != nil {
		return m, err
	}
	if json.Unmarshal(b, &m) != nil || m.Version != 1 || !validID(m.MasterID) || m.Epoch < 1 {
		return m, c.ErrInvalid
	}
	for name, want := range map[string]string{"master.sqlite": m.DatabaseSHA256, "config.toml": m.ConfigSHA256} {
		if err = privateInfo(filepath.Join(dir, name), false); err != nil {
			return m, err
		}
		got, e := hashFile(filepath.Join(dir, name))
		if e != nil {
			return m, e
		}
		if got != want {
			return m, c.ErrConflict
		}
	}
	return m, checkSnapshot(ctx, filepath.Join(dir, "master.sqlite"))
}
