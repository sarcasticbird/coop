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
	// (guest-created or guest-updated state must not be clobbered).
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

// Credential is a trusted, named host credential grant. Source controls how
// Coop acquires secret material; Inject controls how an interactive guest
// process receives it. Credential configuration is honored only globally.
type Credential struct {
	Source            CredentialSource    `toml:"source"`
	Inject            CredentialInjection `toml:"inject"`
	RequireExpiration bool                `toml:"require_expiration"`
}

// CredentialSource describes host-side acquisition without containing the
// acquired secret itself.
type CredentialSource struct {
	Type    string   `toml:"type"`
	Path    string   `toml:"path"`
	Argv    []string `toml:"argv"`
	Profile string   `toml:"profile"`
}

// CredentialInjection describes the guest-visible shape of a credential.
type CredentialInjection struct {
	Type    string `toml:"type"`
	Name    string `toml:"name"`
	PathEnv string `toml:"path_env"`
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
	Image              Image                 `toml:"image"`
	Tools              Tools                 `toml:"tools"`
	Resources          Resources             `toml:"resources"`
	Agents             map[string]Agent      `toml:"agents"`
	Seeds              []Seed                `toml:"seed"`
	IncludeCredentials []string              `toml:"include_credentials"`
	Credentials        map[string]Credential `toml:"credentials"`
	// SSH forwards the host SSH agent socket into coops. Default OFF:
	// it grants guests the ability to sign/authenticate as you, which
	// combined with network egress is an exfiltration-adjacent
	// capability. Enable deliberately, global config only.
	SSH bool `toml:"ssh"`
	// Warnings contains non-fatal migration guidance discovered while loading.
	// It is runtime metadata, never TOML input.
	Warnings []string `toml:"-"`
}

const (
	MaxCredentialGrants    = 32
	MaxSelectedCredentials = 16
)

type Image struct {
	Name string `toml:"name"`
	// ExtraPackages is decoded only for the one-beta compatibility alias.
	// New configuration uses [tools].packages.
	ExtraPackages []string `toml:"extra_packages"`
}

// Tools declares additive packages for the guest user-command plane.
// GlobalPackages and ProjectPackages retain canonical provenance for rebuild
// output; they are derived metadata and cannot be set through TOML.
type Tools struct {
	Packages        []string `toml:"packages"`
	GlobalPackages  []string `toml:"-"`
	ProjectPackages []string `toml:"-"`
}

const (
	MaxToolPackages   = 64
	maxToolPackageLen = 128
)

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
	cfg.Tools.GlobalPackages = canonicalPackages(cfg.Tools.GlobalPackages)
	cfg.Tools.ProjectPackages = canonicalPackages(cfg.Tools.ProjectPackages)
	cfg.Tools.Packages = canonicalPackages(append(
		append([]string(nil), cfg.Tools.GlobalPackages...),
		cfg.Tools.ProjectPackages...,
	))
	if len(cfg.Tools.Packages) > MaxToolPackages {
		return cfg, fmt.Errorf("configured tool package count %d exceeds maximum %d", len(cfg.Tools.Packages), MaxToolPackages)
	}
	// Keep the current image/session consumers working while they migrate to
	// Tools in the following image-construction change. This is derived output,
	// not an additional merge source.
	cfg.Image.ExtraPackages = append([]string(nil), cfg.Tools.Packages...)

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
	toolsDefined := md.IsDefined("tools", "packages")
	legacyToolsDefined := md.IsDefined("image", "extra_packages")
	if toolsDefined && legacyToolsDefined {
		return fmt.Errorf("%s: cannot define both tools.packages and deprecated image.extra_packages", path)
	}
	if !trusted && legacyToolsDefined {
		return fmt.Errorf("%s: project image.extra_packages is not supported; use tools.packages", path)
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
	packages := layer.Tools.Packages
	if trusted && legacyToolsDefined {
		packages = layer.Image.ExtraPackages
		cfg.Warnings = append(cfg.Warnings, "image.extra_packages is deprecated; use tools.packages")
	}
	if trusted {
		cfg.Tools.GlobalPackages = append(cfg.Tools.GlobalPackages, packages...)
	} else {
		cfg.Tools.ProjectPackages = append(cfg.Tools.ProjectPackages, packages...)
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
		if cfg.Credentials == nil {
			cfg.Credentials = make(map[string]Credential)
		}
		for name, grant := range layer.Credentials {
			cfg.Credentials[name] = grant
		}
		cfg.IncludeCredentials = append(cfg.IncludeCredentials, layer.IncludeCredentials...)
		cfg.Seeds = append(cfg.Seeds, layer.Seeds...)
	}
	return nil
}

var (
	configuredName  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolPackagePath = regexp.MustCompile(`^[A-Za-z0-9_+-]+(?:\.[A-Za-z0-9_+-]+)*$`)
)

const (
	maxAgentNameLen = 63
	maxAgents       = 32
)

// validateLayer enforces the grammar untrusted (and trusted) layers
// must satisfy. Untrusted layers additionally get resource caps —
// a repository must not be able to request the whole machine.
func validateLayer(layer *Config, trusted, hasCPUs, hasMemory bool) error {
	if err := validateToolPackages(layer.Tools.Packages); err != nil {
		return err
	}
	if err := validateToolPackages(layer.Image.ExtraPackages); err != nil {
		return fmt.Errorf("image.extra_packages: %w", err)
	}
	for name, agent := range layer.Agents {
		if !configuredName.MatchString(name) || strings.Contains(name, "--") {
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
	if err := validateCredentialLayer(layer); err != nil {
		return err
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

func validateToolPackages(packages []string) error {
	for _, pkg := range packages {
		if len(pkg) == 0 {
			return fmt.Errorf("tool package must not be empty")
		}
		if len(pkg) > maxToolPackageLen {
			return fmt.Errorf("tool package %q exceeds %d bytes", pkg, maxToolPackageLen)
		}
		if !toolPackagePath.MatchString(pkg) {
			return fmt.Errorf("tool package %q must be a simple Nixpkgs attribute path", pkg)
		}
	}
	return nil
}

func canonicalPackages(packages []string) []string {
	if len(packages) == 0 {
		return nil
	}
	unique := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		unique[pkg] = struct{}{}
	}
	canonical := make([]string, 0, len(unique))
	for pkg := range unique {
		canonical = append(canonical, pkg)
	}
	sort.Strings(canonical)
	return canonical
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

	seen := make(map[string]struct{}, len(cfg.IncludeCredentials))
	included := cfg.IncludeCredentials[:0]
	for _, name := range cfg.IncludeCredentials {
		if _, ok := cfg.Credentials[name]; !ok {
			return fmt.Errorf("included credential %q is not defined", name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		included = append(included, name)
	}
	cfg.IncludeCredentials = included
	return nil
}

func validateCredentialLayer(layer *Config) error {
	if len(layer.Credentials) > MaxCredentialGrants {
		return fmt.Errorf("configured credential count %d exceeds maximum %d", len(layer.Credentials), MaxCredentialGrants)
	}

	names := make([]string, 0, len(layer.Credentials))
	for name := range layer.Credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !configuredName.MatchString(name) || strings.Contains(name, "--") {
			return fmt.Errorf("credential name %q invalid (lowercase alphanumeric+hyphens, no '--')", name)
		}
		if len(name) > maxAgentNameLen {
			return fmt.Errorf("credential name %q exceeds %d characters", name, maxAgentNameLen)
		}
		if err := validateCredential(name, layer.Credentials[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateCredential(name string, credential Credential) error {
	source := credential.Source
	switch source.Type {
	case "file":
		if source.Path == "" {
			return fmt.Errorf("credential %q: file source requires path", name)
		}
		if !filepath.IsAbs(source.Path) && !strings.HasPrefix(source.Path, "~/") {
			return fmt.Errorf("credential %q: file source path must be absolute or start with ~/", name)
		}
		if len(source.Argv) > 0 || source.Profile != "" {
			return fmt.Errorf("credential %q: file source contains fields for another source type", name)
		}
	case "command":
		if len(source.Argv) == 0 || source.Argv[0] == "" {
			return fmt.Errorf("credential %q: command source requires argv", name)
		}
		if strings.ContainsRune(source.Argv[0], filepath.Separator) && !filepath.IsAbs(source.Argv[0]) {
			return fmt.Errorf("credential %q: command executable path must be absolute or resolved through PATH", name)
		}
		for _, arg := range source.Argv {
			if strings.IndexByte(arg, 0) >= 0 {
				return fmt.Errorf("credential %q: command argv contains a NUL byte", name)
			}
		}
		if source.Path != "" || source.Profile != "" {
			return fmt.Errorf("credential %q: command source contains fields for another source type", name)
		}
	case "aws-profile":
		if source.Profile == "" {
			return fmt.Errorf("credential %q: aws-profile source requires profile", name)
		}
		if source.Path != "" || len(source.Argv) > 0 {
			return fmt.Errorf("credential %q: aws-profile source contains fields for another source type", name)
		}
	default:
		return fmt.Errorf("credential %q: unknown source type %q", name, source.Type)
	}

	inject := credential.Inject
	if source.Type == "aws-profile" && inject.Type != "aws" {
		return fmt.Errorf("credential %q: aws-profile source requires aws injection", name)
	}
	switch inject.Type {
	case "environment":
		if !environmentName.MatchString(inject.Name) {
			return fmt.Errorf("credential %q: environment injection requires a valid name", name)
		}
		if inject.PathEnv != "" {
			return fmt.Errorf("credential %q: environment injection contains path_env", name)
		}
	case "file":
		if !environmentName.MatchString(inject.PathEnv) {
			return fmt.Errorf("credential %q: file injection requires a valid path_env", name)
		}
		if inject.Name != "" {
			return fmt.Errorf("credential %q: file injection contains name", name)
		}
	case "git-credential-store":
		if inject.Name != "" || inject.PathEnv != "" {
			return fmt.Errorf("credential %q: git-credential-store injection contains unused fields", name)
		}
		if source.Type != "file" {
			return fmt.Errorf("credential %q: git-credential-store injection requires a file source", name)
		}
	case "aws":
		if inject.Name != "" || inject.PathEnv != "" {
			return fmt.Errorf("credential %q: aws injection contains unused fields", name)
		}
		if source.Type != "aws-profile" {
			return fmt.Errorf("credential %q: aws injection requires an aws-profile source", name)
		}
	default:
		return fmt.Errorf("credential %q: unknown injection type %q", name, inject.Type)
	}

	if credential.RequireExpiration && source.Type != "aws-profile" {
		return fmt.Errorf("credential %q: require_expiration requires a source that reports expiration", name)
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
