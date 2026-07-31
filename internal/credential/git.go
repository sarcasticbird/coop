package credential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

type gitCredentialTarget struct {
	protocol string
	host     string
	path     string
}

func (m *Manager) acquireGitCredential(ctx context.Context, home string, selected Selected) (Acquired, error) {
	target, err := parseGitCredentialTarget(selected.Spec.Source.URL)
	if err != nil {
		return Acquired{}, err
	}
	if m.GitCredential == nil {
		return Acquired{}, errors.New("git credential runner is not configured")
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	response, err := m.GitCredential(commandCtx, home, formatGitCredentialRequest(target))
	if err != nil {
		if cause := context.Cause(commandCtx); cause != nil {
			return Acquired{}, cause
		}
		if errors.Is(err, ErrPayloadTooLarge) {
			return Acquired{}, ErrPayloadTooLarge
		}
		return Acquired{}, errors.New("git credential fill failed")
	}
	material, err := parseGitCredentialResponse(target, response)
	if err != nil {
		return Acquired{}, err
	}
	return Acquired{
		Selected: selected,
		userPass: material,
		metadata: Metadata{Provider: selected.Spec.Source.Type},
	}, nil
}

func parseGitCredentialTarget(rawURL string) (gitCredentialTarget, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return gitCredentialTarget{}, errors.New("invalid git-credential source URL")
	}
	if strings.ContainsAny(parsed.Path, "\x00\r\n") {
		return gitCredentialTarget{}, errors.New("invalid git-credential source URL path")
	}
	return gitCredentialTarget{
		protocol: parsed.Scheme,
		host:     parsed.Host,
		path:     strings.TrimPrefix(parsed.Path, "/"),
	}, nil
}

func formatGitCredentialRequest(target gitCredentialTarget) []byte {
	var request strings.Builder
	fmt.Fprintf(&request, "protocol=%s\nhost=%s\n", target.protocol, target.host)
	if target.path != "" {
		fmt.Fprintf(&request, "path=%s\n", target.path)
	}
	request.WriteByte('\n')
	return []byte(request.String())
}

func parseGitCredentialResponse(target gitCredentialTarget, response []byte) (*userPasswordMaterial, error) {
	if len(response) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	if bytes.IndexByte(response, 0) >= 0 || bytes.IndexByte(response, '\r') >= 0 {
		return nil, errors.New("invalid git credential response structure")
	}
	if !bytes.HasSuffix(response, []byte("\n")) {
		return nil, errors.New("git credential response is not terminated")
	}
	body := response[:len(response)-1]
	if len(body) == 0 || bytes.HasSuffix(body, []byte("\n")) || bytes.Contains(body, []byte("\n\n")) {
		return nil, errors.New("invalid git credential response structure")
	}

	fields := make(map[string]string, 5)
	seen := make(map[string]struct{}, 7)
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid git credential response line")
		}
		if _, exists := seen[key]; exists {
			return nil, errors.New("git credential response repeats a field")
		}
		seen[key] = struct{}{}
		switch key {
		case "protocol", "host", "path", "username", "password":
			fields[key] = value
		case "password_expiry_utc", "oauth_refresh_token":
			// Git credential extensions do not change this broker's material
			// type. In particular, never retain the refresh token.
		default:
			return nil, errors.New("git credential response contains an unsupported field")
		}
	}

	for _, required := range []string{"protocol", "host", "username", "password"} {
		if fields[required] == "" {
			return nil, fmt.Errorf("git credential response is missing field %q", required)
		}
	}
	if fields["protocol"] != target.protocol {
		return nil, errors.New("git credential response protocol does not match the trusted source")
	}
	if fields["host"] != target.host {
		return nil, errors.New("git credential response host does not match the trusted source")
	}
	if target.path != "" && fields["path"] == "" {
		return nil, errors.New("git credential response omits the trusted path; enable credential.useHttpPath for path-scoped grants")
	}
	if fields["path"] != target.path {
		return nil, errors.New("git credential response path does not match the trusted source")
	}
	return &userPasswordMaterial{
		protocol: fields["protocol"],
		host:     fields["host"],
		path:     target.path,
		username: fields["username"],
		password: fields["password"],
	}, nil
}

func runGitCredential(ctx context.Context, home, projectRoot string, request []byte) ([]byte, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("credential command home must be absolute")
	}
	trustedPath, err := trustedSearchPath(home, projectRoot, os.Getenv("PATH"))
	if err != nil {
		return nil, err
	}
	executable, err := resolveExecutable(home, projectRoot, "git", trustedPath)
	if err != nil {
		return nil, err
	}
	commandEnv, err := trustedCommandEnvironment(os.Environ(), home, projectRoot, trustedPath)
	if err != nil {
		return nil, err
	}
	commandEnv = append(commandEnv, "GIT_TERMINAL_PROMPT=0")

	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, executable, "credential", "fill")
	cmd.Args = []string{"git", "credential", "fill"}
	cmd.Dir = home
	cmd.Env = commandEnv
	cmd.Stdin = bytes.NewReader(slices.Clone(request))
	stdout := &boundedBuffer{limit: MaxPayloadBytes, onLimit: cancel}
	stderr := &boundedBuffer{limit: MaxPayloadBytes, onLimit: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = commandWaitDelay
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
	if err := cmd.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return nil, ErrPayloadTooLarge
		}
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, errors.New("git credential helper process failed")
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrPayloadTooLarge
	}
	return bytes.Clone(stdout.data), nil
}

type boundedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
	onLimit  func()
}

func (buffer *boundedBuffer) Write(input []byte) (int, error) {
	written := len(input)
	if buffer.exceeded {
		return written, ErrPayloadTooLarge
	}
	remaining := buffer.limit - len(buffer.data)
	if len(input) > remaining {
		if remaining > 0 {
			buffer.data = append(buffer.data, input[:remaining]...)
		}
		buffer.exceeded = true
		if buffer.onLimit != nil {
			buffer.onLimit()
		}
		return written, ErrPayloadTooLarge
	}
	buffer.data = append(buffer.data, input...)
	return written, nil
}
