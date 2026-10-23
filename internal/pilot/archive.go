package pilot

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
)

// Non-OpenCode provider names from the tested AgentsView v0.46.1 registry.
var archiveDisabledProviders = []string{"aider", "amp", "antigravity", "antigravity-cli", "augure-code", "augure-desktop", "claude", "cline", "codebuddy", "codebuff", "codex", "codex-cloud", "commandcode", "copilot", "cortex", "cowork", "crush", "cursor", "cursor-ide", "deepseek-harness", "deepseek-tui", "devin", "evener", "forge", "fx", "gemini", "goose", "gptme", "grok", "hermes", "icodemate", "iflow", "junie", "kilo", "kilo-legacy", "kimi", "kimi-work", "kiro", "kiro-ide", "mimocode", "muse", "omnigent", "omo", "omp", "openclaude", "openclaw", "opencodereview", "openhands", "pi", "piebald", "poolside", "posit-assistant", "positron", "prime-agent", "qclaw", "qoder", "qwen", "qwenpaw", "reasonix", "roocode", "shelley", "stepcode", "tau", "trae", "traex", "vibe", "visualstudio-copilot", "vscode-copilot", "warp", "windsurf", "workbuddy", "zcode", "zed", "zencoder"}

// isolateArchive refuses configured remote or alternate-agent sources. Commands
// also run with an empty private HOME and XDG roots so default providers cannot
// discover the operator's histories, even if the provider registry grows.
func isolateArchive(cfg Config) error {
	raw, e := credential.ReadPrivateFile(filepath.Join(cfg.AgentsViewData, "config.toml"))
	if e != nil {
		return fmt.Errorf("scoped archive configuration required: %w", c.ErrForbidden)
	}
	var settings struct {
		Disabled    []string         `toml:"disabled_agents"`
		RemoteHosts []map[string]any `toml:"remote_hosts"`
		Agents      map[string]struct {
			Dirs []string `toml:"dirs"`
		} `toml:"agents"`
		Prefixes []string `toml:"sync_include_cwd_prefixes"`
	}
	if _, e = toml.Decode(string(raw), &settings); e != nil {
		return c.ErrInvalid
	}
	for _, name := range archiveDisabledProviders {
		if !slices.Contains(settings.Disabled, name) {
			return c.ErrForbidden
		}
	}
	source, ok := settings.Agents["opencode"]
	if len(settings.RemoteHosts) != 0 || len(settings.Agents) != 1 || !ok || len(source.Dirs) != 1 || source.Dirs[0] != cfg.OpenCodeDir || len(settings.Prefixes) != 1 || settings.Prefixes[0] == "" || settings.Prefixes[0] == "/" {
		return c.ErrForbidden
	}
	if cfg.Native != nil && settings.Prefixes[0] != cfg.Native.Directory {
		return c.ErrForbidden
	}
	return nil
}
func archiveEnvironment(cfg Config) ([]string, error) {
	if e := isolateArchive(cfg); e != nil {
		return nil, e
	}
	home := filepath.Join(cfg.StateDir, "archive-home")
	if e := os.MkdirAll(home, 0700); e != nil {
		return nil, e
	}
	info, e := os.Lstat(home)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, c.ErrForbidden
	}
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TMPDIR=" + os.TempDir(), "AGENTSVIEW_DATA_DIR=" + cfg.AgentsViewData, "OPENCODE_DIR=" + cfg.OpenCodeDir, "AGENTSVIEW_TELEMETRY_ENABLED=0", "AGENTSVIEW_NO_DAEMON=1"}, nil
}
