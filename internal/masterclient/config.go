package masterclient

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
)

type FileConfig struct {
	URL               string `toml:"url"`
	MasterID          string `toml:"master_id"`
	TokenEnv          string `toml:"token_env"`
	TokenFile         string `toml:"token_file"`
	AllowLoopbackHTTP bool   `toml:"allow_loopback_http"`
}

func LoadConfig(path string) (*Client, error) {
	raw, err := ReadPrivateConfig(path)
	if err != nil {
		return nil, c.ErrForbidden
	}
	if len(raw) > c.MaxJSON {
		return nil, c.ErrSize
	}
	var cfg FileConfig
	md, err := toml.Decode(string(raw), &cfg)
	if err != nil || len(md.Undecoded()) > 0 || (cfg.TokenEnv == "") == (cfg.TokenFile == "") {
		return nil, c.ErrInvalid
	}
	token := os.Getenv(cfg.TokenEnv)
	if cfg.TokenFile != "" {
		raw, err := ReadPrivateConfig(cfg.TokenFile)
		if err != nil {
			return nil, c.ErrForbidden
		}
		if len(raw) > 4096 {
			return nil, c.ErrSize
		}
		token = strings.TrimSpace(string(raw))
	}
	return New(ClientConfig{URL: cfg.URL, MasterID: cfg.MasterID, Token: token, AllowHTTP: cfg.AllowLoopbackHTTP})
}

// DefaultConfigPath names the installed profile, including XDG overrides.
func DefaultConfigPath(name string) string {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(root, "coop", name)
}

// ReadPrivateConfig permits Stow references only inside the user-selected Coop
// profile. Other paths retain no-follow traversal. The opened target must still
// belong to this user and have mode 0600; resolving a reference grants no access
// to public files or credentials owned by someone else.
func ReadPrivateConfig(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, c.ErrForbidden
	}
	path = filepath.Clean(path)
	root := filepath.Dir(DefaultConfigPath("master-client.toml"))
	rel, err := filepath.Rel(root, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) {
		return nil, c.ErrForbidden
	}
	if stat.Size > c.MaxJSON {
		return nil, c.ErrSize
	}
	b, err := io.ReadAll(io.LimitReader(f, c.MaxJSON+1))
	if err != nil {
		return nil, err
	}
	if len(b) > c.MaxJSON {
		return nil, c.ErrSize
	}
	if len(b) == 0 {
		return nil, errors.New("private configuration is empty")
	}
	return b, nil
}
