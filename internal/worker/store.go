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
type FileStore struct {
	Dir     string
	Version int
}

func NewFileStore(dir string) *FileStore { return NewVersionedFileStore(dir, 1) }
func NewVersionedFileStore(dir string, version int) *FileStore {
	return &FileStore{Dir: dir, Version: version}
}
func (s *FileStore) version() int {
	if s.Version == 0 {
		return 1
	}
	return s.Version
}
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
func validateOwnershipBinding(b *JobBinding, kind string) error {
	if b == nil || b.Version != 2 || !hexSHA.MatchString(b.AssignmentSHA) || b.ExecutorID <= 0 {
		return errors.New("invalid worker ownership binding")
	}
	role := ""
	switch kind {
	case "plan":
		role = "planner"
	case "implement", "fix", "deliver":
		role = "implementer"
	case "review":
		role = "engineering-review"
	case "qa":
		role = "customer-qa"
	}
	if role == "" || b.Role != role {
		return errors.New("worker ownership role mismatch")
	}
	return nil
}
func validateState(s State) error {
	if s.Schedule != nil {
		c := s.Schedule.Counters
		if s.Version != 2 || c.Handoffs < 0 || c.Handoffs > 2 || c.Foreground < 0 || c.Foreground > 3 {
			return errors.New("invalid scheduling counters")
		}
		for id, age := range s.Schedule.Eligible {
			if !hexSHA.MatchString(id) || age.IsZero() {
				return errors.New("invalid scheduling age")
			}
		}
		for id, counted := range s.Schedule.CompletedWakes {
			if !hexSHA.MatchString(id) || !counted {
				return errors.New("invalid scheduling wake receipt")
			}
		}
	}
	if (s.Version != 1 && s.Version != 2) || !hexSHA.MatchString(s.InstanceID) || s.Jobs == nil || s.Sessions == nil || s.Seen == nil || s.Cursor == nil || s.ReviewCycles == nil {
		return errors.New("invalid worker state")
	}
	for key, j := range s.Jobs {
		if !hexSHA.MatchString(key) || j.ID != key {
			return errors.New("invalid worker job identity")
		}
		if err := validateJobPromptFailure(j); err != nil {
			return err
		}
		if j.Binding != nil {
			if s.Version != 2 || validateOwnershipBinding(j.Binding, j.Kind) != nil || j.ID != V2JobKey(j, j.Binding.AssignmentSHA, j.Binding.ExecutorID) {
				return errors.New("invalid bound worker job identity")
			}
			if j.SessionID != "" {
				b, ok := s.Sessions[BindingKey(Config{}, j)]
				if !ok || b.Binding == nil || *b.Binding != *j.Binding || b.TaskURL != j.TaskURL || b.GoalURL != j.GoalURL || b.SessionID != j.SessionID || b.Checkout != j.Checkout || b.Branch != j.Branch {
					return errors.New("worker job/session ownership mismatch")
				}
			}
		} else if s.Version == 2 {
			// Migration preserves drained v1 evidence, never promotes it to a v2 assignment.
			if err := migrationJob(j, false); err != nil {
				return err
			}
		}
	}
	sessionOwners, checkoutOwners := map[string]SessionBinding{}, map[string]SessionBinding{}
	for key, b := range s.Sessions {
		if b.Binding != nil {
			j := Job{Kind: b.Kind, TaskURL: b.TaskURL, GoalURL: b.GoalURL, Binding: b.Binding}
			if s.Version != 2 || validateOwnershipBinding(b.Binding, b.Kind) != nil || b.Role != b.Binding.Role || key != BindingKey(Config{}, j) || !validSession(b.SessionID) || !cleanPrivatePath(b.Checkout) {
				return errors.New("invalid bound worker session")
			}
		} else if s.Version == 2 && (!validSession(b.SessionID) || !cleanPrivatePath(b.Checkout)) {
			return errors.New("invalid historical worker session")
		}
		if s.Version == 2 {
			if owner, ok := sessionOwners[b.SessionID]; ok && (owner.Binding != nil || b.Binding != nil) {
				return errors.New("worker session shared across ownership bindings")
			}
			if owner, ok := checkoutOwners[b.Checkout]; ok && (owner.Binding != nil || b.Binding != nil) {
				return errors.New("worker checkout shared across ownership bindings")
			}
			sessionOwners[b.SessionID], checkoutOwners[b.Checkout] = b, b
		}
	}
	return nil
}
func (s *FileStore) Load() (State, error) {
	if err := s.compatible(); err != nil {
		return State{}, err
	}
	if err := ensurePrivateDir(s.Dir, false); err != nil {
		if os.IsNotExist(err) {
			state := newState()
			state.Version = s.version()
			return state, nil
		}
		return State{}, err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20))
	if os.IsNotExist(err) {
		state := newState()
		state.Version = s.version()
		return state, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	decode := strictJSON
	if s.version() == 2 {
		decode = strictTeamJSON
	}
	if err := decode(raw, &state); err != nil {
		return state, err
	}
	if state.Version != s.version() {
		return State{}, errors.New("worker state/config version mismatch; explicit migration required")
	}
	return state, validateState(state)
}
func (s *FileStore) Save(state State) error {
	if err := s.compatible(); err != nil {
		return err
	}
	if state.Version != s.version() {
		return errors.New("worker state/config version mismatch")
	}
	if raw, err := readPrivate(filepath.Join(s.Dir, "state.json"), MaxSnapshot+(4<<20)); err == nil {
		var current State
		if strictJSON(raw, &current) != nil || current.Version != s.version() {
			return errors.New("cannot overwrite incompatible worker state")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
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
	if _, err := s.Load(); err != nil {
		return err
	}
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
