package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile is a host-owned named machine, separate from coding-tool state mounts.
type Profile struct {
	Command            []string           `toml:"command"`
	Orgs               []string           `toml:"orgs"`
	IncludeCredentials []string           `toml:"include_credentials"`
	Resources          Resources          `toml:"resources"`
	Seeds              []Seed             `toml:"seed"`
	Restart            string             `toml:"restart"`
	Services           map[string]Service `toml:"services"`
}

type Service struct {
	Command []string `toml:"command"`
}

var profileName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var orgName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func ValidateProfileName(name string) error {
	if !profileName.MatchString(name) || strings.Contains(name, "--") || name == "buildkit" {
		return fmt.Errorf("profile name %q must be 1-63 lowercase letters, digits, or internal single hyphens", name)
	}
	return nil
}

func validateProfiles(cfg *Config) error {
	if len(cfg.Profiles) > 64 {
		return fmt.Errorf("at most 64 profiles are supported")
	}
	for name, p := range cfg.Profiles {
		if err := ValidateProfileName(name); err != nil {
			return err
		}
		if len(p.Command) == 0 || p.Command[0] == "" {
			return fmt.Errorf("profile %s requires a nonempty command argv", name)
		}
		if p.Restart != "" && p.Restart != "no" && p.Restart != "on-failure" && p.Restart != "always" {
			return fmt.Errorf("profile %s has invalid restart policy", name)
		}
		if len(p.Services) > 16 {
			return fmt.Errorf("profile %s has more than 16 helper services", name)
		}
		for service, definition := range p.Services {
			if err := ValidateProfileName(service); err != nil {
				return fmt.Errorf("profile %s helper: %w", name, err)
			}
			if len(definition.Command) == 0 || definition.Command[0] == "" {
				return fmt.Errorf("profile %s helper %s requires a command", name, service)
			}
			for _, arg := range definition.Command {
				if strings.ContainsAny(arg, "\x00\r\n") {
					return fmt.Errorf("profile %s helper %s has invalid command arguments", name, service)
				}
			}
		}
		for _, arg := range p.Command {
			if strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("profile %s command contains control characters", name)
			}
		}
		for _, org := range p.Orgs {
			if !orgName.MatchString(org) {
				return fmt.Errorf("profile %s: invalid GitHub organization %q", name, org)
			}
		}
		layer := Config{Resources: p.Resources, Seeds: p.Seeds}
		if err := validateLayer(&layer, p.Resources.CPUs != 0, p.Resources.Memory != ""); err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
		for _, grant := range p.IncludeCredentials {
			if _, ok := cfg.Credentials[grant]; !ok {
				return fmt.Errorf("profile %s: credential %q is not defined", name, grant)
			}
		}
	}
	return nil
}

// LoadProfile never resolves a host project, mount source, or path-scoped grant.
func LoadProfile(name string) (Config, Profile, error) {
	cfg := Default()
	if err := ValidateProfileName(name); err != nil {
		return cfg, Profile{}, err
	}
	if err := mergeFile(&cfg, filepath.Join(configHome(), "coop", "coop.toml"), false); err != nil {
		return cfg, Profile{}, fmt.Errorf("global config: %w", err)
	}
	if err := validateProfiles(&cfg); err != nil {
		return cfg, Profile{}, err
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return cfg, Profile{}, fmt.Errorf("agent profile %q is not configured", name)
	}
	cfg.Mounts = nil
	cfg.SSH = false
	cfg.Projects = nil
	cfg.Volumes = nil
	cfg.Publishes = nil
	cfg.Agents = nil
	cfg.IncludeCredentials = append([]string(nil), p.IncludeCredentials...)
	cfg.Seeds = append([]Seed(nil), p.Seeds...)
	for i := range cfg.Seeds {
		if cfg.Seeds[i].Dest == "" {
			cfg.Seeds[i].Dest = cfg.Seeds[i].Src
		}
		if cfg.Seeds[i].Policy == "" {
			cfg.Seeds[i].Policy = PolicyIfAbsent
		}
	}
	if p.Resources.CPUs != 0 {
		cfg.Resources.CPUs = p.Resources.CPUs
	}
	if p.Resources.Memory != "" {
		cfg.Resources.Memory = p.Resources.Memory
	}
	cfg.Tools.GlobalPackages = canonicalPackages(cfg.Tools.GlobalPackages)
	cfg.Tools.Packages = append([]string(nil), cfg.Tools.GlobalPackages...)
	if len(cfg.Tools.Packages) > MaxToolPackages {
		return cfg, p, fmt.Errorf("too many configured packages")
	}
	return cfg, p, validateMerged(&cfg)
}
