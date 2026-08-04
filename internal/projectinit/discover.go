package projectinit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sarcasticbird/coop/internal/config"
)

const MaxCandidates = 64

var ignoredTraversalDirs = map[string]bool{
	".git":         true,
	".flox":        true,
	"node_modules": true,
	"target":       true,
	".venv":        true,
	"venv":         true,
}

func Discover(root string, existing []config.Volume) ([]string, error) {
	excluded := make(map[string]bool, len(existing))
	for _, volume := range existing {
		excluded[filepath.ToSlash(volume.Path)] = true
	}
	candidates := make(map[string]bool)
	add := func(path string) error {
		path = filepath.ToSlash(path)
		if path == "." || path == "" || excluded[path] || candidates[path] {
			return nil
		}
		candidates[path] = true
		if len(candidates) > MaxCandidates {
			return fmt.Errorf("project init found more than %d volume candidates", MaxCandidates)
		}
		return nil
	}

	var cargoDirs, workspaceDirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return filepath.SkipDir
			}
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if rel != "." && excluded[rel] {
				return filepath.SkipDir
			}
			name := entry.Name()
			if name == ".venv" || name == "venv" {
				if err := add(rel); err != nil {
					return err
				}
			}
			if rel != "." && ignoredTraversalDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "package.json":
			return add(filepath.ToSlash(filepath.Join(filepath.Dir(rel), "node_modules")))
		case "Cargo.toml":
			dir := filepath.ToSlash(filepath.Dir(rel))
			cargoDirs = append(cargoDirs, dir)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var decoded map[string]any
			metadata, err := toml.Decode(string(data), &decoded)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			if metadata.IsDefined("workspace") {
				workspaceDirs = append(workspaceDirs, dir)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, cargoDir := range cargoDirs {
		targetDir := cargoDir
		bestLength := -1
		for _, workspaceDir := range workspaceDirs {
			if pathWithin(cargoDir, workspaceDir) && len(workspaceDir) > bestLength {
				targetDir = workspaceDir
				bestLength = len(workspaceDir)
			}
		}
		if err := add(filepath.ToSlash(filepath.Join(targetDir, "target"))); err != nil {
			return nil, err
		}
	}

	result := make([]string, 0, len(candidates))
	for candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Strings(result)
	return result, nil
}

func pathWithin(path, parent string) bool {
	if parent == "." {
		return true
	}
	return path == parent || strings.HasPrefix(path, parent+"/")
}
