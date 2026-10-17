package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// RecoveryStore keeps an interrupted disk transaction separate from ownership.
type RecoveryStore interface {
	Store
	LoadRecovery(Mode, string) ([]byte, error)
	SaveRecovery(Mode, string, []byte) error
	ClearRecovery(Mode, string) error
}

func (s FileStore) recoveryDir(mode Mode, name string, create bool) (*os.File, error) {
	if err := validKey(mode, name); err != nil {
		return nil, err
	}
	return openDirectory(filepath.Join(s.Root, "recovery", string(mode)), create)
}

func (s FileStore) LoadRecovery(mode Mode, name string) ([]byte, error) {
	dir, err := s.recoveryDir(mode, name, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), name+".json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid recovery journal")
	}
	return io.ReadAll(io.LimitReader(f, (1<<20)+1))
}

func (s FileStore) SaveRecovery(mode Mode, name string, data []byte) error {
	if len(data) > 1<<20 {
		return fmt.Errorf("oversized recovery journal")
	}
	dir, err := s.recoveryDir(mode, name, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := ".recovery-" + hex.EncodeToString(id[:])
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer func() { _ = unix.Unlinkat(int(dir.Fd()), tmp, 0) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name+".json"); err != nil {
		return err
	}
	return dir.Sync()
}

func (s FileStore) ClearRecovery(mode Mode, name string) error {
	dir, err := s.recoveryDir(mode, name, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := unix.Unlinkat(int(dir.Fd()), name+".json", 0); err != nil {
		return err
	}
	return dir.Sync()
}
