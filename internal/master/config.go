package master

import (
	"net"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type Config struct {
	Tasks       *taskstore.Config `toml:"tasks"`
	GitHubCache GitHubCacheConfig `toml:"github_cache"`
	MasterID    string            `toml:"master_id"`
	EpochPath   string            `toml:"epoch_path"`
	Database    string            `toml:"database"`
	Listen      string            `toml:"listen"`
	TLSCertFile string            `toml:"tls_cert_file"`
	TLSKeyFile  string            `toml:"tls_key_file"`
	Policy      Policy            `toml:"policy"`
	Principals  []c.Principal     `toml:"principals"`
	Placements  []c.Placement     `toml:"placements"`
}

func LoadConfig(path string) (Config, error) {
	var out Config
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return out, c.ErrForbidden
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if len(raw) > c.MaxJSON {
		return out, c.ErrSize
	}
	md, err := toml.Decode(string(raw), &out)
	if err != nil || len(md.Undecoded()) > 0 {
		return out, c.ErrInvalid
	}
	if !validID(out.MasterID) || !filepath.IsAbs(out.Database) || len(out.Policy.AllowedHosts) == 0 || len(out.Policy.Tokens) == 0 {
		return out, c.ErrInvalid
	}
	if err := validateGitHubCacheConfig(out.GitHubCache, out.Principals); err != nil {
		return out, err
	}
	if out.Tasks != nil {
		if _, err := taskstore.New(*out.Tasks); err != nil {
			return out, err
		}
	}
	host, _, err := net.SplitHostPort(out.Listen)
	if err != nil {
		return out, c.ErrInvalid
	}
	ip := net.ParseIP(host)
	if out.TLSCertFile == "" || out.TLSKeyFile == "" {
		if out.TLSCertFile != "" || out.TLSKeyFile != "" || ip == nil || !ip.IsLoopback() {
			return out, c.ErrInvalid
		}
	}
	return out, nil
}
