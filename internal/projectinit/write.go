package projectinit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	readConfigFile   = os.ReadFile
	renameConfigFile = os.Rename
	writeConfigTemp  = writeSyncedTemp
)

func EnsureLocalExclude(root string) error {
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	topOutput, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(strings.ToLower(string(topOutput)), "not a git repository") {
			return nil
		}
		return fmt.Errorf("resolve Git repository for %s: %w: %s", root, err, strings.TrimSpace(string(topOutput)))
	}
	top := strings.TrimSpace(string(topOutput))
	if canonical, err := filepath.EvalSymlinks(top); err == nil {
		top = canonical
	}
	configPath := filepath.Join(root, ".coop.toml")
	relative, err := filepath.Rel(top, configPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resolve repository-relative config path %s from %s", configPath, top)
	}
	relative = filepath.ToSlash(relative)

	trackedOutput, trackedErr := gitOutput(top, "ls-files", "--error-unmatch", "--", relative)
	if trackedErr == nil {
		return fmt.Errorf("refusing tracked project configuration %s", configPath)
	}
	if exitCode(trackedErr) != 1 {
		return fmt.Errorf("inspect Git tracking for %s: %w: %s", configPath, trackedErr, strings.TrimSpace(string(trackedOutput)))
	}

	ignoredOutput, ignoredErr := gitOutput(top, "check-ignore", "-q", "--no-index", "--", relative)
	if ignoredErr == nil {
		return nil
	}
	if exitCode(ignoredErr) != 1 {
		return fmt.Errorf("inspect Git ignore rules for %s: %w: %s", configPath, ignoredErr, strings.TrimSpace(string(ignoredOutput)))
	}

	excludeOutput, err := gitOutput(top, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve Git local exclude for %s: %w: %s", configPath, err, strings.TrimSpace(string(excludeOutput)))
	}
	excludePath := strings.TrimSpace(string(excludeOutput))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(top, excludePath)
	}
	existing, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Git local exclude %s: %w", excludePath, err)
	}
	pattern, err := literalGitIgnorePattern(relative)
	if err != nil {
		return fmt.Errorf("encode Git local exclude for %s: %w", configPath, err)
	}
	var addition strings.Builder
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		addition.WriteByte('\n')
	}
	addition.WriteString(pattern)
	addition.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create Git local exclude directory for %s: %w", excludePath, err)
	}
	file, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open Git local exclude %s: %w", excludePath, err)
	}
	if _, err := file.WriteString(addition.String()); err != nil {
		_ = file.Close()
		return fmt.Errorf("append Git local exclude %s: %w", excludePath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Git local exclude %s: %w", excludePath, err)
	}
	verifyOutput, err := gitOutput(top, "check-ignore", "-q", "--no-index", "--", relative)
	if err != nil {
		return fmt.Errorf("verify Git local exclude for %s: %w: %s", configPath, err, strings.TrimSpace(string(verifyOutput)))
	}
	return nil
}

func literalGitIgnorePattern(relative string) (string, error) {
	if strings.ContainsAny(relative, "\r\n") {
		return "", fmt.Errorf("repository-relative path contains a line break")
	}
	var pattern strings.Builder
	pattern.Grow(len(relative) + 1)
	pattern.WriteByte('/')
	for _, r := range relative {
		switch r {
		case '\\', '*', '?', '[', ']':
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(r)
	}
	return pattern.String(), nil
}

func AppendConfig(root string, block []byte) error {
	if len(block) == 0 {
		return nil
	}
	path := filepath.Join(root, ".coop.toml")
	mode := fs.FileMode(0o600)
	var existing []byte
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("project configuration %s is not a regular file", path)
		}
		mode = info.Mode().Perm()
		existing, err = readConfigFile(path)
		if err != nil {
			return fmt.Errorf("read project configuration %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("inspect project configuration %s: %w", path, err)
	}
	if bytes.Contains(existing, block) {
		return nil
	}

	content := bytes.Clone(existing)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, block...)
	temporary, err := writeConfigTemp(root, content, mode)
	if err != nil {
		return fmt.Errorf("write temporary project configuration for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := renameConfigFile(temporary, path); err != nil {
		return fmt.Errorf("replace project configuration %s: %w", path, err)
	}
	return nil
}

func writeSyncedTemp(root string, content []byte, mode fs.FileMode) (path string, retErr error) {
	file, err := os.CreateTemp(root, ".coop.toml.tmp-*")
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		if retErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return "", err
	}
	if err := file.Chmod(mode); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.CombinedOutput()
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
