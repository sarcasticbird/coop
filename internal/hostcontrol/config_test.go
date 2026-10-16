package hostcontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func fixtureConfig(t *testing.T) Config {
	t.Helper()
	root := privateTemp(t)
	sum := sha256.Sum256([]byte("test-token-with-32-random-bytes-in-real-use"))
	raw, e := json.Marshal(map[string]string{"chief": hex.EncodeToString(sum[:])})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "verifiers.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	return Config{Version: 1, Label: "test-host", Listen: "127.0.0.1:0", StateDir: filepath.Join(root, "state"), VerifierFile: path, AllowedHosts: []string{"127.0.0.1"}, Callers: []CallerConfig{{ID: "chief", Workspaces: []string{"agent:robin"}, Capabilities: []string{"read", "exec", "lifecycle", "ping", "worker_control"}}}}
}
func TestConfigRejectsUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"remote listener", func(c *Config) { c.Listen = "0.0.0.0:9876" }},
		{"hostname listener", func(c *Config) { c.Listen = "localhost:9876" }},
		{"relative state", func(c *Config) { c.StateDir = "relative" }},
		{"unknown capability", func(c *Config) { c.Callers[0].Capabilities = []string{"host_shell"} }},
		{"duplicate caller", func(c *Config) { c.Callers = append(c.Callers, c.Callers[0]) }},
		{"oversized limit", func(c *Config) { c.Limits.Active = 100000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureConfig(t)
			tc.change(&c)
			if e := c.Validate(); e == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}
func TestConfigStrictPrivateFile(t *testing.T) {
	c := fixtureConfig(t)
	path := filepath.Join(filepath.Dir(c.VerifierFile), "mcp.toml")
	raw := "version=1\nlabel='test'\nlisten='127.0.0.1:0'\nstate_dir='" + c.StateDir + "'\nverifier_file='" + c.VerifierFile + "'\nallowed_hosts=['127.0.0.1']\n"
	if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(path); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(raw+"inline_token='private-sentinel'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(path); e == nil || strings.Contains(e.Error(), "private-sentinel") {
		t.Fatal("unknown config leaked or accepted")
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(path); e == nil {
		t.Fatal("public config accepted")
	}
	link := path + ".link"
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(link); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestPolicyRevokesAuthenticatedCaller(t *testing.T) {
	c := fixtureConfig(t)
	p, e := NewPolicy(c)
	if e != nil {
		t.Fatal(e)
	}
	caller, e := p.Authenticate("test-token-with-32-random-bytes-in-real-use")
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Authorize(caller, "agent:robin", "exec"); e != nil {
		t.Fatal(e)
	}
	if e = p.Authorize(caller, "agent:hawk", "exec"); e == nil {
		t.Fatal("workspace grant escaped")
	}
	bad := c
	bad.Listen = "0.0.0.0:80"
	if e = p.Reload(bad); e == nil {
		t.Fatal("bad reload accepted")
	}
	if e = p.Authorize(caller, "agent:robin", "exec"); e != nil {
		t.Fatal("invalid reload changed policy")
	}
	c.Callers = nil
	if e = p.Reload(c); e != nil {
		t.Fatal(e)
	}
	if e = p.Authorize(caller, "agent:robin", "exec"); e == nil {
		t.Fatal("caller retained revoked authority")
	}
	if _, e = p.Authenticate("wrong-secret-sentinel"); e == nil || strings.Contains(e.Error(), "sentinel") {
		t.Fatal("bad credential accepted or leaked")
	}
}
func TestHostIdentityAndExclusiveStore(t *testing.T) {
	root := filepath.Join(privateTemp(t), "state")
	h, close, e := OpenHost(root, "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e := OpenHost(root, "test"); e == nil {
		t.Fatal("second service acquired store")
	}
	if e = close(); e != nil {
		t.Fatal(e)
	}
	again, close, e := OpenHost(root, "test")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = close() }()
	if h.ID != again.ID || !strings.HasPrefix(h.ID, "h1_") {
		t.Fatal("host identity changed")
	}
}

func TestConfigReturnsValidatedDefaults(t *testing.T) {
	c := fixtureConfig(t)
	path := filepath.Join(filepath.Dir(c.VerifierFile), "defaults.toml")
	raw := "version=1\nlabel='test'\nlisten='127.0.0.1:0'\nstate_dir='" + c.StateDir + "'\nverifier_file='" + c.VerifierFile + "'\nallowed_hosts=['127.0.0.1']\n"
	if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := LoadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	if got.Limits.Active != 4 || got.Limits.OutputBytes != 256<<10 {
		t.Fatal("validated defaults were discarded")
	}
}
func TestPolicyRejectsSharedCredential(t *testing.T) {
	c := fixtureConfig(t)
	b, e := os.ReadFile(c.VerifierFile)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]string
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	v["other"] = v["chief"]
	b, e = json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(c.VerifierFile, b, 0600); e != nil {
		t.Fatal(e)
	}
	c.Callers = append(c.Callers, CallerConfig{ID: "other", Capabilities: []string{"read"}})
	if _, e = NewPolicy(c); e == nil {
		t.Fatal("one credential maps to multiple caller identities")
	}
}
func TestPrivateRejectsParentSymlinkAndCorruptIdentity(t *testing.T) {
	parent := privateTemp(t)
	actual := filepath.Join(parent, "actual")
	if e := os.Mkdir(actual, 0700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(parent, "link")
	if e := os.Symlink(actual, link); e != nil {
		t.Fatal(e)
	}
	if _, _, e := OpenHost(filepath.Join(link, "state"), "test"); e == nil {
		t.Fatal("symlink parent accepted")
	}
	h, close, e := OpenHost(actual, "test")
	if e != nil {
		t.Fatal(e)
	}
	if e = close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(actual, "identity.json"), []byte(`{"host_id":"bad"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if next, close, e := OpenHost(actual, "test"); e == nil {
		_ = close()
		t.Fatalf("corrupt identity silently replaced %s with %s", h.ID, next.ID)
	}
}
