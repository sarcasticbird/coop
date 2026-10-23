package pilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveIsolationRejectsUnsafeConfiguration(t *testing.T) {
	for _, extra := range []string{`[[remote_hosts]]
host="elsewhere"
url="https://remote.invalid"
`, `[agents.claude]
dirs=["/private/history"]
`} {
		cfg := Config{StateDir: t.TempDir(), AgentsViewData: t.TempDir(), OpenCodeDir: "/tmp/fixture"}
		disabled, _ := json.Marshal(archiveDisabledProviders)
		raw := "disabled_agents=" + string(disabled) + "\nsync_include_cwd_prefixes=[\"/fixture\"]\n[agents.opencode]\ndirs=[\"/tmp/fixture\"]\n" + extra
		if e := os.WriteFile(filepath.Join(cfg.AgentsViewData, "config.toml"), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if e := isolateArchive(cfg); e == nil {
			t.Fatal("unsafe archive accepted")
		}
	}
}

func TestArchiveEnvironmentCannotInheritHostHistoryLocations(t *testing.T) {
	cfg := Config{StateDir: t.TempDir(), AgentsViewData: t.TempDir(), OpenCodeDir: "/tmp/fixture"}
	disabled, _ := json.Marshal(archiveDisabledProviders)
	raw := "disabled_agents=" + string(disabled) + "\nsync_include_cwd_prefixes=[\"/fixture\"]\n[agents.opencode]\ndirs=[\"/tmp/fixture\"]\n"
	if e := os.WriteFile(filepath.Join(cfg.AgentsViewData, "config.toml"), []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CODEX_HOME", "/private/unrelated-history")
	t.Setenv("GH_TOKEN", "not-forwarded")
	env, e := archiveEnvironment(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range env {
		if strings.Contains(entry, "unrelated-history") || strings.HasPrefix(entry, "GH_TOKEN=") {
			t.Fatal("host environment forwarded")
		}
	}
}
