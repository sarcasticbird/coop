package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// credential and seed policy.
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

func resolveMounts(cfg *Config, projectRoot string) error {
	if len(cfg.Mounts) == 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	resolved := make([]Mount, 0, len(cfg.Mounts))
	for i, mount := range cfg.Mounts {
		mount, err := resolveMount(mount, home, projectRoot)
		if err != nil {
			return fmt.Errorf("mount[%d]: %w", i, err)
		}
		resolved = append(resolved, mount)
	}
	cfg.Mounts = canonicalMounts(resolved)
	return nil
}

func resolveMount(mount Mount, home, projectRoot string) (Mount, error) {
	source := canonicalPath(ExpandHome(mount.Source, home))
	if strings.ContainsAny(source, ",=") {
		return Mount{}, fmt.Errorf("source %q contains unsupported runtime mount characters ',' or '='", source)
	}
	info, err := os.Stat(source)
	if err != nil {
		return Mount{}, fmt.Errorf("inspect source %q: %w", source, err)
	}
	if !info.IsDir() {
		return Mount{}, fmt.Errorf("source %q is not a directory", source)
	}
	canonicalHome := canonicalPath(home)
	if sharesAncestor(canonicalHome, source) {
		return Mount{}, fmt.Errorf("refusing broad source %q", source)
	}
	if projectRoot != "" {
		canonicalProject := canonicalPath(projectRoot)
		if sharesAncestor(source, canonicalProject) || sharesAncestor(canonicalProject, source) {
			return Mount{}, fmt.Errorf("source %q overlaps project %q", source, canonicalProject)
		}
	}
	if mount.Access == "" {
		mount.Access = MountReadOnly
	}
	mount.Source = source
	return mount, nil
}

func canonicalMounts(mounts []Mount) []Mount {
	bySource := make(map[string]MountAccess, len(mounts))
	for _, mount := range mounts {
		if existing, ok := bySource[mount.Source]; !ok || (existing == MountReadOnly && mount.Access == MountReadWrite) {
			bySource[mount.Source] = mount.Access
		}
	}
	canonical := make([]Mount, 0, len(bySource))
	for source, access := range bySource {
		canonical = append(canonical, Mount{Source: source, Access: access})
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Source < canonical[j].Source })
	return canonical
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
