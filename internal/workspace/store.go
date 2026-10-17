package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

type FileStore struct{ Root string }

func DefaultStore() (FileStore, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return FileStore{}, err
		}
		root = filepath.Join(home, ".local", "state")
	}
	return FileStore{Root: filepath.Join(root, "coop", "workspaces")}, nil
}

var recordName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,254}$`)

func validKey(mode Mode, name string) error {
	if mode != Agent && mode != Interactive {
		return fmt.Errorf("invalid workspace mode %q", mode)
	}
	if !recordName.MatchString(name) || strings.Contains(name, "--") {
		return fmt.Errorf("invalid workspace identity %q", name)
	}
	if mode == Agent {
		return configName(name)
	}
	return nil
}
func configName(name string) error {
	if len(name) > 63 || strings.HasSuffix(name, "-") {
		return fmt.Errorf("invalid agent identity %q", name)
	}
	return nil
}
func ValidateRecord(r Record) error {
	if err := validKey(r.Mode, r.Identity); err != nil {
		return err
	}
	if r.Version != 1 {
		return fmt.Errorf("unsupported workspace record version %d", r.Version)
	}
	want := r.Identity
	if r.Mode == Agent {
		want = "coop-agent-" + r.Identity
	}
	if r.RuntimeName != want {
		return fmt.Errorf("workspace runtime identity mismatch")
	}
	return nil
}

// openDirectory refuses symlinks at every component, including the state root.
func openDirectory(path string, create bool) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		if create {
			err := unix.Mkdirat(fd, part, 0700)
			if err == nil {
				err = unix.Fsync(fd)
			}
			if err != nil && err != unix.EEXIST {
				_ = unix.Close(fd)
				return nil, err
			}
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("workspace state directory is owned by another user")
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func (s FileStore) dir(mode Mode, create bool) (*os.File, error) {
	return openDirectory(filepath.Join(s.Root, string(mode)), create)
}
func (s FileStore) Load(mode Mode, name string) (Record, error) {
	if err := validKey(mode, name); err != nil {
		return Record{}, err
	}
	dir, err := s.dir(mode, false)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), name+".json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Record{}, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return Record{}, fmt.Errorf("invalid workspace record file")
	}
	var r Record
	decoder := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Record{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Record{}, fmt.Errorf("trailing workspace record data")
	}
	if err := ValidateRecord(r); err != nil {
		return Record{}, err
	}
	if r.Mode != mode || r.Identity != name {
		return Record{}, fmt.Errorf("workspace record ownership mismatch")
	}
	return r, nil
}
func (s FileStore) Save(r Record) (retErr error) {
	if err := ValidateRecord(r); err != nil {
		return err
	}
	dir, err := s.dir(r.Mode, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := ".record-" + hex.EncodeToString(id[:])
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
	if err := unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), r.Identity+".json"); err != nil {
		return err
	}
	return dir.Sync()
}
func (s FileStore) Remove(mode Mode, name string) error {
	if _, err := s.Load(mode, name); err != nil {
		return err
	}
	dir, err := s.dir(mode, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return unix.Unlinkat(int(dir.Fd()), name+".json", 0)
}
func (s FileStore) List() ([]Record, error) {
	var records []Record
	for _, mode := range []Mode{Interactive, Agent} {
		rows, err := s.ListMode(mode, nil)
		if err != nil {
			return nil, err
		}
		records = append(records, rows...)
	}
	return records, nil
}

// ListMode filters ownership identities before opening record contents.
func (s FileStore) ListMode(mode Mode, allow func(string) bool) ([]Record, error) {
	dir, err := s.dir(mode, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if allow != nil && !allow(name) {
			continue
		}
		r, err := s.Load(mode, name)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}
