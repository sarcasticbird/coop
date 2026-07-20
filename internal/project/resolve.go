// Package project resolves which directory a coop session is anchored to.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Resolve determines the project root for a coop, walking up from dir.
//
// Resolution order:
//  1. coop.toml marker (nearest ancestor) — explicit pin, e.g. for
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
	switch {
	case findMarker(abs) != "":
		root = findMarker(abs)
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
		return fmt.Errorf("refusing to sandbox %s — it spans your home directory or filesystem root; run coop from inside a project (or add a coop.toml marker)", root)
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

func findMarker(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if isFile(filepath.Join(dir, "coop.toml")) {
			return dir
		}
		if dir == filepath.Dir(dir) { // filesystem root
			return ""
		}
	}
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
