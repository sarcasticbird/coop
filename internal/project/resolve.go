// Package project resolves which directory a coop session is anchored to.
package project

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Resolve determines the project root for a coop, walking up from dir.
//
// Resolution order:
//  1. .coop.toml marker (nearest ancestor) — explicit pin, e.g. for
//     pseudo-monorepos where the org root is the sandbox unit
//  2. bare+worktree layout — git toplevel whose parent contains .bare/
//     resolves to that parent (the whole project incl. all worktrees)
//  3. git toplevel
//  4. dir itself
func Resolve(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	// Canonicalize so symlink aliases of the same project resolve to one
	// identity — name, mount, and workdir must all agree.
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}

	root := abs
	marker, err := findMarker(abs)
	if err != nil {
		return "", err
	}
	switch {
	case marker != "":
		root = marker
	default:
		if top := gitToplevel(abs); top != "" {
			parent := filepath.Dir(top)
			if isDir(filepath.Join(parent, ".bare")) {
				root = parent
			} else {
				root = top
			}
		}
	}

	// The resolved root becomes a read-write host mount: refuse
	// catastrophically broad ones regardless of how resolution (or a
	// malicious symlink) produced them.
	if err := guardBreadth(root); err != nil {
		return "", err
	}
	return root, nil
}

// guardBreadth rejects project roots whose mounting would expose the
// filesystem root, the home directory, or any ancestor of home (e.g.
// /Users). These are never sane sandbox units.
func guardBreadth(root string) error {
	deny := map[string]bool{"/": true}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		for dir := home; ; dir = filepath.Dir(dir) {
			deny[dir] = true
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	if deny[filepath.Clean(root)] {
		return fmt.Errorf("refusing to sandbox %s — it spans your home directory or filesystem root; run coop from inside a project (or add a .coop.toml marker)", root)
	}
	return nil
}

// Name derives the container name for a (canonical) project path.
//
// Anatomy: coop-<slug>-<hash16>. The 64-bit path hash disambiguates
// same-basename projects (work/api vs personal/api must never share a
// container or volumes). The slug collapses hyphen runs so "--" can
// never appear in a name — VolumeSep exploits that as an unambiguous
// owner/suffix boundary for volume cleanup.
func Name(projectRoot string) string {
	slug := strings.ToLower(filepath.Base(projectRoot))
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > maxProjectSlugLen {
		slug = strings.TrimRight(slug[:maxProjectSlugLen], "-")
	}
	sum := sha256.Sum256([]byte(projectRoot))
	return "coop-" + slug + "-" + hex.EncodeToString(sum[:])[:16]
}

// VolumeSep separates a coop's name from a volume suffix. Container
// names cannot contain "--" (Name collapses hyphen runs), so
// <name>-- prefix matching cannot cross into another coop's volumes.
const VolumeSep = "--"

// Reserve enough of Apple's 255-character identifier limit for the longest
// permitted agent name. The path hash is never truncated.
const maxProjectSlugLen = 255 - len(VolumeSep) - 63 - len("coop--") - 16

func findMarker(start string) (string, error) {
	for dir := start; ; dir = filepath.Dir(dir) {
		if isFile(filepath.Join(dir, ".coop.toml")) {
			if err := ValidateLocalConfig(dir); err != nil {
				return "", err
			}
			return dir, nil
		}
		if dir == filepath.Dir(dir) { // filesystem root
			return "", nil
		}
	}
}

// ValidateLocalConfig rejects a project .coop.toml tracked by Git. The file
// has host-side authority and must remain machine-local rather than arriving
// through a checkout.
func ValidateLocalConfig(root string) error {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	configPath := filepath.Join(root, ".coop.toml")
	if !isFile(configPath) {
		return nil
	}
	hasRepository, err := hasGitMetadata(root)
	if err != nil {
		return fmt.Errorf("inspect Git repository for %s: %w", configPath, err)
	}
	if !hasRepository {
		return nil
	}
	top, err := gitToplevelStrict(root)
	if err != nil {
		return fmt.Errorf("inspect Git repository for %s: %w", configPath, err)
	}
	rel, err := filepath.Rel(top, configPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("inspect Git repository for %s: file is outside reported top-level %s", configPath, top)
	}
	tracked, err := trackedByFileIdentity(top, configPath)
	if err != nil {
		return fmt.Errorf("inspect Git tracking for %s: %w", configPath, err)
	}
	if tracked {
		return fmt.Errorf("refusing tracked project configuration %s; remove .coop.toml from Git and add it to .gitignore", configPath)
	}
	return nil
}

// trackedByFileIdentity compares the actual config file with tracked paths.
// Git pathspec matching is case-sensitive even on default macOS filesystems,
// where .COOP.toml and .coop.toml can name the same inode.
func trackedByFileIdentity(top, configPath string) (bool, error) {
	target, err := os.Stat(configPath)
	if err != nil {
		return false, err
	}
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = top
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	reader := bufio.NewReader(stdout)
	tracked := false
	for {
		path, readErr := reader.ReadString(0)
		path = strings.TrimSuffix(path, "\x00")
		if path != "" && strings.EqualFold(filepath.Base(path), ".coop.toml") {
			candidate, statErr := os.Stat(filepath.Join(top, filepath.FromSlash(path)))
			switch {
			case statErr == nil && os.SameFile(target, candidate):
				tracked = true
			case statErr != nil && !os.IsNotExist(statErr):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return false, statErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return false, readErr
		}
	}
	if err := cmd.Wait(); err != nil {
		return false, err
	}
	return tracked, nil
}

// hasGitMetadata distinguishes a directory outside Git from a checkout that
// Git cannot inspect. Every ordinary checkout, linked worktree, and checkout
// using --separate-git-dir has a .git directory or control file.
func hasGitMetadata(start string) (bool, error) {
	for dir := start; ; dir = filepath.Dir(dir) {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		if dir == filepath.Dir(dir) {
			return false, nil
		}
	}
}

func gitToplevelStrict(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", errors.New("git returned an empty top-level")
	}
	if canonical, err := filepath.EvalSymlinks(top); err == nil {
		top = canonical
	}
	return top, nil
}

func gitToplevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
