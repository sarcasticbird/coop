package worker

import (
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// PortableBinding is read-only and deliberately does not initialize state,
// reconcile a journal, contact a provider or change placement.
func PortableBinding(cfg Config) (c.ExecutionBinding, error) {
	if cfg.Version != 3 || cfg.Master == nil {
		return c.ExecutionBinding{}, c.ErrUnsupported
	}
	if !portableLocations(cfg) {
		return c.ExecutionBinding{}, c.ErrInvalid
	}
	for _, path := range []string{cfg.StateDir, cfg.WorkRoot} {
		if err := safePath(path); err != nil {
			return c.ExecutionBinding{}, err
		}
	}
	return c.ExecutionBinding{MasterID: cfg.Master.MasterID, Placement: configuredPlacement(cfg)}, nil
}

func portableLocations(cfg Config) bool {
	inside := func(name, root string) bool { return filepath.Clean(name) == name && strings.HasPrefix(name, root+"/") }
	return inside(cfg.StateDir, "/var/lib/coop") && inside(cfg.WorkRoot, "/workspace")
}
