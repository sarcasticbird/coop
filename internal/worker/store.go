package worker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type Store interface {
	Load() (State, error)
	Save(State) error
	Lock() (func(), error)
}
type FileStore struct{ Dir string }

func NewFileStore(dir string) *FileStore { return &FileStore{Dir: dir} }
func randomID() string {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("system randomness unavailable")
	}
	return hex.EncodeToString(bytes[:])
}
func safePath(path string) error {
	if !cleanPrivatePath(path) {
		return errors.New("invalid private path")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return errors.New("cannot inspect private path")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in private path")
		}
	}
	return nil
}
func ensurePrivateDir(dir string, create bool) error {
	if err := safePath(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return errors.New("cannot create private directory")
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("worker directory must be mode 0700")
	}
	return nil
}
func readPrivate(path string, limit int) ([]byte, error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("worker file must be regular and mode 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("worker file exceeds limit")
	}
	return raw, nil
}
func atomicPrivate(path string, raw []byte) error {
	if err := ensurePrivateDir(filepath.Dir(path), true); err != nil {
		return err
	}
	if err := safePath(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
		return errors.New("unsafe existing worker file")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect worker file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".worker-")
	if err != nil {
		return errors.New("cannot stage worker file")
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err := f.Chmod(0600); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err := f.Write(raw); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("cannot replace worker file")
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func newState() State {
	return State{Version: 1, InstanceID: randomID(), Sessions: map[string]SessionBinding{}, Jobs: map[string]Job{}, Seen: map[string]string{}, Cursor: map[string]string{}, ReviewCycles: map[string]int{}}
}
func validateState(s State) error {
	if s.Version != 1 || !hexSHA.MatchString(s.InstanceID) || s.Jobs == nil || s.Sessions == nil || s.Seen == nil || s.Cursor == nil || s.ReviewCycles == nil {
		return errors.New("invalid worker state")
	}
	for key, j := range s.Jobs {
		if !hexSHA.MatchString(key) || j.ID != key {
			return errors.New("invalid worker job identity")
		}
		if err := validateJobPromptFailure(j); err != nil {
			return err
		}
	}
	return nil
}
func (s *FileStore) Load() (State, error) {
	if err := ensurePrivateDir(s.Dir, false); err != nil {
		if os.IsNotExist(err) {
			return newState(), nil
		}
		return State{}, err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20))
	if os.IsNotExist(err) {
		return newState(), nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := strictJSON(raw, &state); err != nil {
		return state, err
	}
	return state, validateState(state)
}
func (s *FileStore) Save(state State) error {
	if err := validateState(state); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > MaxSnapshot+(4<<20) {
		return errors.New("worker state exceeds limit")
	}
	return atomicPrivate(filepath.Join(s.Dir, "state.json"), raw)
}
func (s *FileStore) Lock() (func(), error) {
	if err := ensurePrivateDir(s.Dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, "worker.lock")
	if err := safePath(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open worker lock")
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = f.Close() // Preserve the primary lock error.
		return nil, errors.New("unsafe worker lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close() // Preserve the primary lock error.
		return nil, errors.New("another worker owns this state")
	}
	var once sync.Once
	return func() { once.Do(func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }) }, nil
}

type control struct {
	Paused bool `json:"paused"`
}

func (s *FileStore) SetPaused(paused bool) error {
	raw, _ := json.Marshal(control{Paused: paused})
	return atomicPrivate(filepath.Join(s.Dir, "control.json"), raw)
}
func (s *FileStore) Paused() (bool, error) {
	if err := ensurePrivateDir(s.Dir, false); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "control.json"), 1024)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var c control
	if err := strictJSON(raw, &c); err != nil {
		return false, err
	}
	return c.Paused, nil
}
