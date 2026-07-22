package credential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/jobcontrol"
	coopruntime "github.com/sarcasticbird/coop/internal/runtime"
	"golang.org/x/sys/unix"
)

// Runner executes one acquisition command directly from its argv.
type Runner func(context.Context, string, []string) ([]byte, error)

// Manager contains host dependencies used during credential acquisition.
type Manager struct {
	OpenFile func(string) (*os.File, error)
	Run      Runner
	Now      func() time.Time

	projectRoot string
	acquire     func(context.Context, string, Selected) (Acquired, error)
}

// NewManager returns a manager backed by the host operating system. Commands
// cannot resolve executables from the untrusted project tree.
func NewManager(projectRoot string) *Manager {
	return &Manager{
		OpenFile: openCredentialFile,
		Run: func(ctx context.Context, home string, argv []string) ([]byte, error) {
			return runCommand(ctx, home, projectRoot, argv)
		},
		Now:         time.Now,
		projectRoot: projectRoot,
	}
}

// AcquireAll acquires grants in order and rolls back earlier grants if a later
// acquisition fails.
func (m *Manager) AcquireAll(ctx context.Context, home string, selected []Selected) ([]Acquired, error) {
	acquired := make([]Acquired, 0, len(selected))
	for _, grant := range selected {
		if cause := context.Cause(ctx); cause != nil {
			rollbackErr := RevokeAll(context.WithoutCancel(ctx), acquired)
			return nil, errors.Join(cause, rollbackErr)
		}
		var item Acquired
		var err error
		if m.acquire != nil {
			item, err = m.acquire(ctx, home, grant)
		} else {
			item, err = m.acquireOne(ctx, home, grant)
		}
		if err != nil {
			rollbackErr := RevokeAll(context.WithoutCancel(ctx), acquired)
			return nil, errors.Join(fmt.Errorf("acquire credential %q: %w", grant.Name, err), rollbackErr)
		}
		acquired = append(acquired, item)
		if cause := context.Cause(ctx); cause != nil {
			rollbackErr := RevokeAll(context.WithoutCancel(ctx), acquired)
			return nil, errors.Join(cause, rollbackErr)
		}
	}
	return acquired, nil
}

func (m *Manager) acquireOne(ctx context.Context, home string, selected Selected) (Acquired, error) {
	var (
		payload []byte
		err     error
	)
	switch selected.Spec.Source.Type {
	case "file":
		payload, err = m.acquireFile(home, selected.Spec.Source.Path)
	case "command":
		payload, err = m.acquireCommand(ctx, home, selected.Spec.Source.Argv)
	case "aws-profile":
		return m.acquireAWS(ctx, home, selected)
	default:
		return Acquired{}, fmt.Errorf("unsupported source type %q", selected.Spec.Source.Type)
	}
	if err != nil {
		return Acquired{}, err
	}
	return Acquired{
		Selected: selected,
		payload:  payload,
		metadata: Metadata{Provider: selected.Spec.Source.Type},
	}, nil
}

func (m *Manager) acquireFile(home, path string) (payload []byte, retErr error) {
	path = config.ExpandHome(path, home)
	if !filepath.IsAbs(path) {
		return nil, errors.New("credential file path must resolve to an absolute path")
	}
	path = filepath.Clean(path)
	path, err := trustedCredentialFilePath(m.projectRoot, path)
	if err != nil {
		return nil, err
	}
	file, err := m.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open credential file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close credential file: %w", err))
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential source is not a regular file")
	}
	if info.Size() > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	payload, err = io.ReadAll(io.LimitReader(file, int64(MaxPayloadBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	if len(payload) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return bytes.Clone(payload), nil
}

func openCredentialFile(path string) (*os.File, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, errors.New("credential file path must be absolute")
	}

	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 1 && parts[0] == "" {
		return os.NewFile(uintptr(fd), path), nil
	}
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func trustedCredentialFilePath(projectRoot, path string) (string, error) {
	projectLexicalRoot := filepath.Clean(projectRoot)
	projectCanonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root for credential file: %w", err)
	}
	projectCanonicalRoot = filepath.Clean(projectCanonicalRoot)
	if withinRoot(projectLexicalRoot, path) || withinRoot(projectCanonicalRoot, path) {
		return "", errors.New("credential file path enters the project")
	}

	prefix := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		prefix = filepath.Join(prefix, part)
		resolvedPrefix, resolveErr := filepath.EvalSymlinks(prefix)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve credential file path: %w", resolveErr)
		}
		if withinRoot(projectCanonicalRoot, filepath.Clean(resolvedPrefix)) {
			return "", errors.New("credential file path enters the project")
		}
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve credential file path: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if withinRoot(projectCanonicalRoot, resolved) {
		return "", errors.New("credential file path enters the project")
	}
	return resolved, nil
}

func (m *Manager) acquireCommand(ctx context.Context, home string, argv []string) ([]byte, error) {
	payload, err := m.runAcquisitionCommand(ctx, home, argv)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(payload), nil
}

func (m *Manager) runAcquisitionCommand(ctx context.Context, home string, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty credential command")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	payload, err := m.Run(cmdCtx, home, slices.Clone(argv))
	if err != nil {
		return nil, fmt.Errorf("credential command %q failed: %w", argv[0], err)
	}
	if len(payload) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return payload, nil
}

func runCommand(ctx context.Context, home, projectRoot string, argv []string) (payload []byte, retErr error) {
	if len(argv) == 0 {
		return nil, errors.New("empty credential command")
	}
	if !filepath.IsAbs(home) {
		return nil, errors.New("credential command home must be absolute")
	}
	trustedPath, err := trustedSearchPath(home, projectRoot, os.Getenv("PATH"))
	if err != nil {
		return nil, err
	}
	executable, err := resolveExecutable(home, projectRoot, argv[0], trustedPath)
	if err != nil {
		return nil, err
	}
	commandEnv, err := trustedCommandEnvironment(os.Environ(), home, projectRoot, trustedPath)
	if err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, executable, argv[1:]...)
	cmd.Args = slices.Clone(argv)
	cmd.Dir = home
	cmd.Env = commandEnv
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create credential command stdout pipe: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	cmd.Stdout = childStdout
	cmd.WaitDelay = commandWaitDelay
	terminal, err := jobcontrol.Configure(cmd, os.Stdin)
	if err != nil {
		_ = childStdout.Close()
		return nil, fmt.Errorf("configure credential command job control: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, terminal.Restore())
	}()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		_ = childStdout.Close()
		return nil, err
	}
	if err := childStdout.Close(); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("close parent credential stdout writer: %w", err)
	}
	done := make(chan struct{})
	monitorDone := terminal.Monitor(cmd.Process.Pid, done)
	type readResult struct {
		payload []byte
		err     error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdout, int64(MaxPayloadBytes)+1))
		readDone <- readResult{payload: data, err: err}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var read readResult
	var waitErr error
	select {
	case read = <-readDone:
		if len(read.payload) > MaxPayloadBytes {
			_ = cmd.Cancel()
			cancel()
		}
		waitErr = <-waitDone
	case waitErr = <-waitDone:
		timer := time.NewTimer(commandWaitDelay)
		select {
		case read = <-readDone:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			_ = cmd.Cancel()
			cancel()
			_ = stdout.Close()
			read = <-readDone
			waitErr = errors.Join(waitErr, exec.ErrWaitDelay)
		}
	}
	if len(read.payload) > MaxPayloadBytes {
		_ = cmd.Cancel()
		cancel()
	}
	close(done)
	monitorErr := <-monitorDone
	if len(read.payload) > MaxPayloadBytes {
		return nil, errors.Join(ErrPayloadTooLarge, monitorErr)
	}
	if read.err != nil || waitErr != nil || monitorErr != nil {
		if cause := context.Cause(ctx); cause != nil {
			waitErr = errors.Join(cause, waitErr)
		} else {
			var processExit *exec.ExitError
			if errors.As(waitErr, &processExit) {
				if status, ok := processExit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					waitErr = &coopruntime.SignalError{Signal: status.Signal()}
				}
			}
		}
		return nil, errors.Join(read.err, waitErr, monitorErr)
	}
	return bytes.Clone(read.payload), nil
}

func resolveExecutable(home, projectRoot, name, path string) (string, error) {
	projectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root for credential command: %w", err)
	}
	projectRoot = filepath.Clean(projectRoot)
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			return "", fmt.Errorf("credential executable path %q is relative", name)
		}
		resolved, ok := executableFile(name)
		if !ok {
			return "", fmt.Errorf("credential executable %q: %w", name, exec.ErrNotFound)
		}
		if withinRoot(projectRoot, resolved) {
			return "", fmt.Errorf("credential executable %q resolves inside the project", name)
		}
		return resolved, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = home
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, dir)
		}
		resolved, ok := executableFile(filepath.Join(dir, name))
		if ok && !withinRoot(projectRoot, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("credential executable %q: %w", name, exec.ErrNotFound)
}

func trustedSearchPath(home, projectRoot, search string) (string, error) {
	projectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root for credential PATH: %w", err)
	}
	projectRoot = filepath.Clean(projectRoot)
	dirs := make([]string, 0, len(filepath.SplitList(search)))
	seen := make(map[string]struct{})
	for _, dir := range filepath.SplitList(search) {
		if dir == "" {
			dir = home
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, dir)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		resolved = filepath.Clean(resolved)
		if withinRoot(projectRoot, resolved) {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		dirs = append(dirs, resolved)
	}
	return strings.Join(dirs, string(os.PathListSeparator)), nil
}

func executableFile(candidate string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", false
	}
	return filepath.Clean(resolved), true
}

func trustedCommandEnvironment(env []string, home, projectRoot, trustedPath string) ([]string, error) {
	projectLexicalRoot := filepath.Clean(projectRoot)
	projectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root for credential environment: %w", err)
	}
	homeRoot, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home for credential environment: %w", err)
	}
	if withinRoot(filepath.Clean(projectRoot), filepath.Clean(homeRoot)) {
		return nil, errors.New("credential command home resolves inside the project")
	}

	allowed := map[string]bool{
		"COLORTERM":               true,
		"FORCE_COLOR":             true,
		"LANG":                    true,
		"LANGUAGE":                true,
		"LOGNAME":                 true,
		"NO_COLOR":                true,
		"TERM":                    true,
		"USER":                    true,
		"__CF_USER_TEXT_ENCODING": true,
	}
	pathValues := map[string]bool{
		"GPG_TTY":         true,
		"SSH_AUTH_SOCK":   true,
		"TMPDIR":          true,
		"XDG_CACHE_HOME":  true,
		"XDG_CONFIG_HOME": true,
		"XDG_DATA_HOME":   true,
		"XDG_RUNTIME_DIR": true,
		"XDG_STATE_HOME":  true,
	}
	clean := []string{"HOME=" + home, "PWD=" + home, "PATH=" + trustedPath}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "HOME" || name == "PWD" || name == "PATH" {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			clean = append(clean, entry)
			continue
		}
		if !pathValues[name] || !filepath.IsAbs(value) {
			continue
		}
		// Reject project-contained aliases before resolving them. Otherwise a
		// symlink in the checkout could pass validation while targeting a safe
		// path, then be retargeted before the helper consumes the variable.
		cleanValue := filepath.Clean(value)
		if withinRoot(projectLexicalRoot, cleanValue) || withinRoot(filepath.Clean(projectRoot), cleanValue) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(value)
		if err == nil && !withinRoot(filepath.Clean(projectRoot), filepath.Clean(resolved)) {
			clean = append(clean, name+"="+filepath.Clean(resolved))
		}
	}
	return clean, nil
}

func withinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}

	// filepath.Rel is lexical and therefore case-sensitive even on the
	// case-insensitive filesystems commonly used by macOS. Compare the project
	// directory with each existing candidate ancestor by filesystem identity so
	// differently cased aliases cannot bypass the trust boundary.
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	for current := filepath.Clean(candidate); ; current = filepath.Dir(current) {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(rootInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}
