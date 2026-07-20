// Package config loads coop configuration: a global file at
// ~/.config/coop/coop.toml merged with an optional per-project coop.toml
// (which doubles as the project-root marker).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// SeedPolicy controls how a seed entry is applied on session entry.
type SeedPolicy string

const (
	// PolicyAlways re-copies on every entry (config files: host edits win).
	PolicyAlways SeedPolicy = "always"
	// PolicyIfAbsent copies only when the guest file is missing
	// (credentials: the coop's own refreshes must not be clobbered).
	PolicyIfAbsent SeedPolicy = "if-absent"
	// PolicyOverlay tar-copies a directory tree over the destination
	// (adds/updates, never deletes).
	PolicyOverlay SeedPolicy = "overlay"
)

// Seed is one host->guest file or directory propagation rule.
// Host-side reads resolve symlinks (stow-managed files work); mounts
// would dangle, which is why seeding exists at all.
type Seed struct {
	Src    string     `toml:"src"`
	Dest   string     `toml:"dest,omitempty"` // defaults to Src
	Policy SeedPolicy `toml:"policy"`
}

// Agent declares one coding agent the coop hosts. State is the guest
// directory that must persist per-project (sessions, credentials,
// history) — it becomes a named volume so transcripts never leak
// across projects and token refreshes survive restarts.
type Agent struct {
	State string `toml:"state"`
}

// Config is the merged coop configuration.
type Config struct {
	Image     Image            `toml:"image"`
	Resources Resources        `toml:"resources"`
	Agents    map[string]Agent `toml:"agents"`
	Seeds     []Seed           `toml:"seed"`
	// SSH forwards the host SSH agent socket into coops. Default OFF:
	// it grants guests the ability to sign/authenticate as you, which
	// combined with network egress is an exfiltration-adjacent
	// capability. Enable deliberately, global config only.
	SSH bool `toml:"ssh"`
}

type Image struct {
	Name string `toml:"name"`
	// ExtraPackages are nixpkgs attribute names (e.g. "gemini-cli")
	// installed into the image by `coop rebuild` — pairs with
	// [agents.<name>] entries whose binaries aren't in the stock image.
	// Trusted (global) config only, like everything image-related.
	ExtraPackages []string `toml:"extra_packages"`
}

type Resources struct {
	CPUs   int    `toml:"cpus"`
	Memory string `toml:"memory"`
}

// Default returns the built-in configuration. The default agent set
// matches what the stock image ships; override or extend via
// [agents.<name>] tables (set state = "" to drop one).
func Default() Config {
	return Config{
		Image:     Image{Name: "coop:latest"},
		Resources: Resources{CPUs: 4, Memory: "8G"},
		Agents: map[string]Agent{
			"opencode": {State: "~/.local/share/opencode"},
			"claude":   {State: "~/.claude"},
			"codex":    {State: "~/.codex"},
		},
	}
}

// Load merges: defaults <- global (~/.config/coop/coop.toml) <- project
// (<projectRoot>/coop.toml). projectRoot may be empty.
//
// Trust boundary: the project file is repository-controlled — an
// untrusted checkout must not be able to seed host files (exfiltration)
// or swap the sandbox image. Seeds and image are honored from the
// GLOBAL layer only; project layers may set resources and agents.
func Load(projectRoot string) (Config, error) {
	cfg := Default()

	global := filepath.Join(configHome(), "coop", "coop.toml")
	if err := mergeFile(&cfg, global, true); err != nil {
		return cfg, fmt.Errorf("global config: %w", err)
	}
	if projectRoot != "" {
		if err := mergeFile(&cfg, filepath.Join(projectRoot, "coop.toml"), false); err != nil {
			return cfg, fmt.Errorf("project config: %w", err)
		}
	}

	for i := range cfg.Seeds {
		if cfg.Seeds[i].Dest == "" {
			cfg.Seeds[i].Dest = cfg.Seeds[i].Src
		}
		if cfg.Seeds[i].Policy == "" {
			cfg.Seeds[i].Policy = PolicyAlways
		}
	}
	if err := validateMerged(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ExpandHome rewrites a leading ~/ against the given home directory.
func ExpandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func mergeFile(cfg *Config, path string, trusted bool) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var layer Config
	md, err := toml.Decode(string(data), &layer)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// Unknown keys are config typos or probing — reject loudly.
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if err := validateLayer(&layer, trusted,
		md.IsDefined("resources", "cpus"), md.IsDefined("resources", "memory")); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if trusted && layer.SSH {
		cfg.SSH = true
	}
	if trusted && layer.Image.Name != "" {
		cfg.Image.Name = layer.Image.Name
	}
	if trusted && len(layer.Image.ExtraPackages) > 0 {
		cfg.Image.ExtraPackages = append(cfg.Image.ExtraPackages, layer.Image.ExtraPackages...)
	}
	if layer.Resources.CPUs != 0 {
		cfg.Resources.CPUs = layer.Resources.CPUs
	}
	if layer.Resources.Memory != "" {
		cfg.Resources.Memory = layer.Resources.Memory
	}
	// Agents merge per name; an entry with empty state removes the agent.
	for name, agent := range layer.Agents {
		if agent.State == "" {
			delete(cfg.Agents, name)
			continue
		}
		cfg.Agents[name] = agent
	}
	if trusted {
		cfg.Seeds = append(cfg.Seeds, layer.Seeds...)
	}
	return nil
}

var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const (
	maxAgentNameLen = 63
	maxAgents       = 32
)

// validateLayer enforces the grammar untrusted (and trusted) layers
// must satisfy. Untrusted layers additionally get resource caps —
// a repository must not be able to request the whole machine.
func validateLayer(layer *Config, trusted, hasCPUs, hasMemory bool) error {
	for name, agent := range layer.Agents {
		if !agentName.MatchString(name) || strings.Contains(name, "--") {
			return fmt.Errorf("agent name %q invalid (lowercase alphanumeric+hyphens, no '--')", name)
		}
		if len(name) > maxAgentNameLen {
			return fmt.Errorf("agent name %q exceeds %d characters", name, maxAgentNameLen)
		}
		if agent.State != "" {
			if !strings.HasPrefix(agent.State, "~/") {
				return fmt.Errorf("agent %s: state %q must be under the guest home (~/...)", name, agent.State)
			}
			if strings.Contains(agent.State, ":") {
				return fmt.Errorf("agent %s: state %q contains ':' (reserved by volume mount grammar)", name, agent.State)
			}
			// "~/" prefix alone doesn't confine: ~/../../etc cleans to
			// /etc and would become a volume target outside guest home
			rel := filepath.Clean(strings.TrimPrefix(agent.State, "~/"))
			if rel == "." {
				return fmt.Errorf("agent %s: state %q must name a directory below the guest home", name, agent.State)
			}
			if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
				return fmt.Errorf("agent %s: state %q escapes the guest home", name, agent.State)
			}
		}
	}
	if hasMemory {
		m := layer.Resources.Memory
		if !memoryFormat.MatchString(m) {
			return fmt.Errorf("memory %q: expected a positive value like 8G or 512M", m)
		}
		n, err := strconv.ParseUint(m[:len(m)-1], 10, 64)
		if err != nil || n == 0 {
			return fmt.Errorf("memory %q: must be positive", m)
		}
	}
	if hasCPUs && layer.Resources.CPUs <= 0 {
		return fmt.Errorf("cpus must be positive")
	}
	for _, seed := range layer.Seeds {
		switch seed.Policy {
		case "", PolicyAlways, PolicyIfAbsent, PolicyOverlay:
		default:
			return fmt.Errorf("seed %s: unknown policy %q", seed.Src, seed.Policy)
		}
	}
	if !trusted {
		if layer.Resources.CPUs > maxProjectCPUs {
			return fmt.Errorf("project config requests %d cpus (max %d)", layer.Resources.CPUs, maxProjectCPUs)
		}
		if layer.Resources.Memory != "" && memoryOverCap(layer.Resources.Memory) {
			return fmt.Errorf("project config requests memory %s (max %dG)", layer.Resources.Memory, maxProjectMemG)
		}
	}
	return nil
}

// validateMerged catches aliases introduced across config layers. Two agents
// targeting the same normalized directory would mount different named volumes
// at one guest path, making persistence depend on CLI argument ordering.
func validateMerged(cfg *Config) error {
	if len(cfg.Agents) > maxAgents {
		return fmt.Errorf("configured agent count %d exceeds maximum %d", len(cfg.Agents), maxAgents)
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	owners := make(map[string]string, len(names))
	for _, name := range names {
		target := filepath.Clean(strings.TrimPrefix(cfg.Agents[name].State, "~/"))
		for existing, owner := range owners {
			if target == existing || strings.HasPrefix(target, existing+string(filepath.Separator)) ||
				strings.HasPrefix(existing, target+string(filepath.Separator)) {
				return fmt.Errorf("agents %s and %s have overlapping normalized state targets %q and %q", owner, name, existing, target)
			}
		}
		owners[target] = name
	}
	return nil
}

const (
	maxProjectCPUs = 8
	maxProjectMemG = 16
)

var memoryFormat = regexp.MustCompile(`^[0-9]+[GM]$`)

func memoryOverCap(m string) bool {
	n, err := strconv.Atoi(m[:len(m)-1])
	if err != nil {
		return true
	}
	if strings.HasSuffix(m, "M") {
		return n > maxProjectMemG*1024
	}
	return n > maxProjectMemG
}

func configHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}
