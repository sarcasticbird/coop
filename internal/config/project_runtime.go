package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxProjectVolumes = 64
	MaxPublishedPorts = 32
)

// ValidateProjectRuntime canonicalizes and validates project-local volume and
// publication declarations before they can affect container state.
func ValidateProjectRuntime(cfg *Config, projectRoot string) error {
	if len(cfg.Volumes) > MaxProjectVolumes {
		return fmt.Errorf("configured project volume count %d exceeds maximum %d", len(cfg.Volumes), MaxProjectVolumes)
	}
	if len(cfg.Publishes) > MaxPublishedPorts {
		return fmt.Errorf("configured published port count %d exceeds maximum %d", len(cfg.Publishes), MaxPublishedPorts)
	}

	volumes := make([]Volume, 0, len(cfg.Volumes))
	seenVolumes := make(map[string]struct{}, len(cfg.Volumes))
	for _, volume := range cfg.Volumes {
		clean, err := validateProjectVolumePath(projectRoot, volume.Path)
		if err != nil {
			return err
		}
		if _, ok := seenVolumes[clean]; ok {
			continue
		}
		seenVolumes[clean] = struct{}{}
		volumes = append(volumes, Volume{Path: clean})
	}
	cfg.Volumes = volumes
	sort.Slice(cfg.Volumes, func(i, j int) bool {
		return cfg.Volumes[i].Path < cfg.Volumes[j].Path
	})
	for i := 0; i < len(cfg.Volumes); i++ {
		for j := i + 1; j < len(cfg.Volumes); j++ {
			if pathsOverlap(cfg.Volumes[i].Path, cfg.Volumes[j].Path) {
				return fmt.Errorf("project volume %q overlaps %q", cfg.Volumes[i].Path, cfg.Volumes[j].Path)
			}
		}
	}

	publishes := make([]Publish, 0, len(cfg.Publishes))
	byHost := make(map[int]Publish, len(cfg.Publishes))
	for _, publish := range cfg.Publishes {
		if publish.GuestPort < 1 || publish.GuestPort > 65535 {
			return fmt.Errorf("publish guest_port %d must be between 1 and 65535", publish.GuestPort)
		}
		if publish.HostPort == 0 {
			publish.HostPort = publish.GuestPort
		}
		if publish.HostPort < 1 || publish.HostPort > 65535 {
			return fmt.Errorf("publish host_port %d must be between 1 and 65535", publish.HostPort)
		}
		if existing, ok := byHost[publish.HostPort]; ok {
			if existing.GuestPort != publish.GuestPort {
				return fmt.Errorf("host port %d maps to both guest ports %d and %d", publish.HostPort, existing.GuestPort, publish.GuestPort)
			}
			continue
		}
		byHost[publish.HostPort] = publish
		publishes = append(publishes, publish)
	}
	cfg.Publishes = publishes
	sort.Slice(cfg.Publishes, func(i, j int) bool {
		if cfg.Publishes[i].HostPort != cfg.Publishes[j].HostPort {
			return cfg.Publishes[i].HostPort < cfg.Publishes[j].HostPort
		}
		return cfg.Publishes[i].GuestPort < cfg.Publishes[j].GuestPort
	})
	return nil
}

func validateProjectVolumePath(projectRoot, authored string) (string, error) {
	if authored == "" {
		return "", fmt.Errorf("project volume path is required")
	}
	if filepath.IsAbs(authored) {
		return "", fmt.Errorf("project volume path %q must be relative", authored)
	}
	for _, r := range authored {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("project volume path %q contains a control character", authored)
		}
	}
	if strings.ContainsAny(authored, ":,=") {
		return "", fmt.Errorf("project volume path %q contains reserved runtime grammar", authored)
	}
	clean := filepath.Clean(authored)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean != authored {
		return "", fmt.Errorf("project volume path %q must be a normalized relative path below the project", authored)
	}
	if projectRoot == "" {
		return "", fmt.Errorf("project volume %q requires a project root", authored)
	}

	current := projectRoot
	parts := strings.Split(clean, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if i != len(parts)-1 {
				return "", fmt.Errorf("project volume %q parent %q does not exist", authored, filepath.Dir(current))
			}
			break
		}
		if err != nil {
			return "", fmt.Errorf("inspect project volume %q component %q: %w", authored, current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("project volume %q component %q is a symlink", authored, current)
		}
		if !info.IsDir() {
			if i == len(parts)-1 {
				return "", fmt.Errorf("project volume %q target is not a directory", authored)
			}
			return "", fmt.Errorf("project volume %q parent %q is not a directory", authored, current)
		}
	}
	return clean, nil
}
