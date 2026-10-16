package hostcontrol

import (
	"github.com/BurntSushi/toml"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type Limits struct {
	Active      int `toml:"active"`
	Unresolved  int `toml:"unresolved"`
	Records     int `toml:"records"`
	Terminal    int `toml:"terminal"`
	OutputBytes int `toml:"output_bytes"`
}

func (l *Limits) defaults() {
	if l.Active == 0 {
		l.Active = 4
	}
	if l.Unresolved == 0 {
		l.Unresolved = 256
	}
	if l.Records == 0 {
		l.Records = 4096
	}
	if l.Terminal == 0 {
		l.Terminal = 256
	}
	if l.OutputBytes == 0 {
		l.OutputBytes = 256 << 10
	}
}

type CallerConfig struct {
	ID           string   `toml:"id"`
	Workspaces   []string `toml:"workspaces"`
	Capabilities []string `toml:"capabilities"`
}
type WorkspaceConfig struct {
	Mode    string `toml:"mode"`
	Name    string `toml:"name"`
	Project string `toml:"project,omitempty"`
}
type Config struct {
	Version        int               `toml:"version"`
	Label          string            `toml:"label"`
	Listen         string            `toml:"listen"`
	StateDir       string            `toml:"state_dir"`
	VerifierFile   string            `toml:"verifier_file"`
	AllowedHosts   []string          `toml:"allowed_hosts"`
	AllowedOrigins []string          `toml:"allowed_origins"`
	Limits         Limits            `toml:"limits"`
	Callers        []CallerConfig    `toml:"callers"`
	Bindings       []WorkspaceConfig `toml:"workspaces"`
}

var localName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (c *Config) Validate() error {
	c.Limits.defaults()
	h, port, e := net.SplitHostPort(c.Listen)
	if e != nil || port == "" || net.ParseIP(h) == nil || !net.ParseIP(h).IsLoopback() {
		return fail("invalid_listener")
	}
	if c.Version != 1 || c.Label == "" || len(c.Label) > 128 || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || !filepath.IsAbs(c.VerifierFile) {
		return fail("invalid_host_config")
	}
	l := c.Limits
	if l.Active < 1 || l.Active > 4 || l.Unresolved < 1 || l.Unresolved > 256 || l.Records < 1 || l.Records > 4096 || l.Terminal < 1 || l.Terminal > 256 || l.OutputBytes < 4 || l.OutputBytes > 256<<10 {
		return fail("invalid_limits")
	}
	if len(c.AllowedHosts) == 0 || len(c.AllowedHosts) > 16 || len(c.AllowedOrigins) > 16 || len(c.Callers) > 64 || len(c.Bindings) > 256 {
		return fail("invalid_config_capacity")
	}
	for _, v := range c.AllowedHosts {
		if v == "" || strings.ContainsAny(v, "/\\\r\n\x00@*") {
			return fail("invalid_allowed_host")
		}
	}
	for _, v := range c.AllowedOrigins {
		u, e := url.Parse(v)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fail("invalid_allowed_origin")
		}
	}
	seen := map[string]bool{}
	for _, v := range c.Callers {
		if !localName.MatchString(v.ID) || seen[v.ID] || len(v.Workspaces) > 256 || len(v.Capabilities) > 5 {
			return fail("invalid_caller")
		}
		seen[v.ID] = true
		for _, cap := range v.Capabilities {
			if !validCapability(cap) {
				return fail("invalid_capability")
			}
		}
		for _, key := range v.Workspaces {
			parts := strings.Split(key, ":")
			if len(parts) != 2 || (parts[0] != "agent" && parts[0] != "interactive") || !selectorName(parts[1]) {
				return fail("invalid_workspace_grant")
			}
		}
	}
	for _, b := range c.Bindings {
		if b.Mode != "interactive" || !selectorName(b.Name) || !filepath.IsAbs(b.Project) {
			return fail("invalid_workspace_binding")
		}
	}
	return nil
}
func selectorName(s string) bool {
	return s != "" && len(s) <= 255 && !strings.ContainsAny(s, "/\\:\x00\r\n")
}
func validCapability(s string) bool {
	switch s {
	case "read", "ping", "worker_control", "lifecycle", "exec":
		return true
	}
	return false
}
func LoadConfig(path string) (Config, error) {
	var c Config
	if filepath.Base(path) == ".coop.toml" {
		return c, fail("project_config_not_allowed")
	}
	b, e := readPrivate(path, 1<<20)
	if e != nil {
		return c, e
	}
	m, e := toml.Decode(string(b), &c)
	if e != nil || len(m.Undecoded()) > 0 {
		return c, fail("invalid_host_config")
	}
	e = c.Validate()
	return c, e
}
