package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// matchesProject reports whether projectRoot is match or lies beneath it.
//
// Comparison happens on canonicalized paths at path-component boundaries, so
// ~/Projects/foo does not match ~/Projects/foobar. Bare-repo layouts nest a
// worktree below <org>/<repo>/, which is why this is a prefix test rather than
// a glob: filepath.Match's * does not cross a separator and would miss every
// worktree.
func matchesProject(match, hostHome, projectRoot string) bool {
	if projectRoot == "" {
		return false
	}
	want := canonicalPath(ExpandHome(match, hostHome))
	got := canonicalPath(projectRoot)
	if want == got || strings.HasPrefix(got, want+string(filepath.Separator)) {
		return true
	}
	return sharesAncestor(got, want)
}

// applyProjectScopes folds every scope matching projectRoot into effective
// policy. Credentials and seeds from every matching scope union.
func applyProjectScopes(cfg *Config, projectRoot string) error {
	if len(cfg.Projects) == 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	for i, scope := range cfg.Projects {
		// Checked for every scope, matching or not, so a typo surfaces in any
		// directory rather than only inside the project it targets.
		for _, name := range scope.IncludeCredentials {
			if _, ok := cfg.Credentials[name]; !ok {
				return fmt.Errorf("projects[%d] (match %q): unknown credential grant %q", i, scope.Match, name)
			}
		}
		if !matchesProject(scope.Match, home, projectRoot) {
			continue
		}
		cfg.IncludeCredentials = append(cfg.IncludeCredentials, scope.IncludeCredentials...)
		cfg.Seeds = append(cfg.Seeds, scope.Seeds...)
	}
	return nil
}

// canonicalPath resolves symlinks when the path exists and falls back to a
// lexical absolute form otherwise. A single trusted config may describe more
// than one machine, so a match entry naming an absent path must not fail the
// load — it simply does not match.
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// sharesAncestor walks projectRoot upward comparing by inode. macOS is
// case-insensitive, so /Users/CDolan and /Users/cdolan name the same directory
// while comparing unequal as strings.
func sharesAncestor(projectRoot, match string) bool {
	wantInfo, err := os.Stat(match)
	if err != nil {
		return false
	}
	for dir := projectRoot; ; {
		if info, err := os.Stat(dir); err == nil && os.SameFile(info, wantInfo) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
