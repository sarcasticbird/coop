package hostcontrol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

// Walk from / with O_NOFOLLOW rather than checking a pathname then reopening it.
func privateDir(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fail("invalid_private_path")
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		if create {
			e = unix.Mkdirat(fd, part, 0700)
			if e == nil {
				e = unix.Fsync(fd)
			}
			if e != nil && e != unix.EEXIST {
				_ = unix.Close(fd)
				return nil, e
			}
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		_ = unix.Close(fd)
		return nil, fail("unsafe_private_directory")
	}
	return os.NewFile(uintptr(fd), path), nil
}
func readAt(dir *os.File, name string, max int64) ([]byte, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, fail("invalid_private_name")
	}
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 || st.Size > max {
		return nil, fail("unsafe_private_file")
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if len(b) > int(max) {
		return nil, fail("private_file_too_large")
	}
	return b, e
}
func readPrivate(path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fail("invalid_private_path")
	}
	d, e := privateDir(filepath.Dir(path), false)
	if e != nil {
		return nil, e
	}
	defer func() { _ = d.Close() }()
	return readAt(d, filepath.Base(path), max)
}
func writeAt(dir *os.File, name string, b []byte) error {
	if name == "" || filepath.Base(name) != name {
		return fail("invalid_private_name")
	}
	// Existing symlinks/foreign files are errors, not replaceable targets.
	if _, e := readAt(dir, name, 4<<20); e != nil && !errors.Is(e, unix.ENOENT) {
		return e
	}
	id, e := randomHex(16)
	if e != nil {
		return e
	}
	tmp := ".tmp-" + id
	fd, e := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer func() { _ = unix.Unlinkat(int(dir.Fd()), tmp, 0) }()
	if _, e = f.Write(b); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); e != nil {
		return e
	}
	return dir.Sync()
}
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fail("invalid_private_json")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return fail("invalid_private_json")
	}
	return nil
}

// UpdatePrivateFile serializes a private read-modify-write without following
// symlinks. A missing file is passed to update as nil; invalid files fail closed.
func UpdatePrivateFile(path string, max int64, update func([]byte) ([]byte, error)) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fail("invalid_private_path")
	}
	d, err := privateDir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	// Separate creation from opening an existing lock. Concurrent O_CREAT
	// opens with O_NOFOLLOW can return ENOENT on macOS before locking begins.
	fd, err := unix.Openat(int(d.Fd()), filepath.Base(path)+".lock", flags|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(d.Fd()), filepath.Base(path)+".lock", flags, 0)
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 {
		return fail("unsafe_private_lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	b, err := readAt(d, filepath.Base(path), max)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	b, err = update(b)
	if err != nil {
		return err
	}
	if int64(len(b)) > max {
		return fail("private_file_too_large")
	}
	return writeAt(d, filepath.Base(path), b)
}

func OpenHost(root, label string) (Host, func() error, error) {
	d, e := privateDir(root, true)
	if e != nil {
		return Host{}, nil, e
	}
	fd, e := unix.Openat(int(d.Fd()), "service.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		_ = d.Close()
		return Host{}, nil, e
	}
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 {
		_ = unix.Close(fd)
		_ = d.Close()
		return Host{}, nil, fail("unsafe_service_lock")
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = unix.Close(fd)
		_ = d.Close()
		return Host{}, nil, fail("host_service_already_running")
	}
	close := func() error { e1 := unix.Close(fd); e2 := d.Close(); return errors.Join(e1, e2) }
	raw, e := readAt(d, "identity.json", 1024)
	var h Host
	if errors.Is(e, unix.ENOENT) {
		id, err := randomHex(16)
		if err != nil {
			_ = close()
			return h, nil, err
		}
		h = Host{ID: "h1_" + id}
		raw, err = json.Marshal(h)
		if err == nil {
			err = writeAt(d, "identity.json", raw)
		}
		if err != nil {
			_ = close()
			return h, nil, err
		}
	} else if e != nil {
		_ = close()
		return h, nil, e
	} else if e = strictJSON(raw, &h); e != nil || len(h.ID) != 35 || !strings.HasPrefix(h.ID, "h1_") {
		_ = close()
		return Host{}, nil, fail("invalid_host_identity")
	}
	if _, e = hex.DecodeString(strings.TrimPrefix(h.ID, "h1_")); e != nil {
		_ = close()
		return Host{}, nil, fail("invalid_host_identity")
	}
	h.Label = label
	return h, close, nil
}
