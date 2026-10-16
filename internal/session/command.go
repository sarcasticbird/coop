package session

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// GuestCommand treats cwd as a guest path. Host Flox discovery is restricted to
// the already-owned project binding. It never canonicalizes an arbitrary guest path.
func (s *Session) GuestCommand(cwd string, argv []string) (string, []string, error) {
	if cwd == "" {
		cwd = s.Project
	}
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return "", nil, fmt.Errorf("invalid guest cwd")
	}
	cwd = filepath.Clean(cwd)
	wrapper := []string{"flox", "activate", "--dir", "/opt/coop-core", "--", "/usr/local/bin/coop-user-env"}
	if withinProject(s.Project, cwd) {
		if dir := s.floxDir(cwd); dir != "" {
			wrapper = append(wrapper, "--project-flox", dir)
		}
	}
	wrapper = append(wrapper, "--")
	return cwd, append(wrapper, slices.Clone(argv)...), nil
}
