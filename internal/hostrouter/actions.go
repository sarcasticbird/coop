package hostrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Invocation is the versioned, bounded stdin-independent SSH wire contract.
// Paths and credential *names* are interpreted only by the receiving Coop.
type Invocation struct {
	Version   int      `json:"version"`
	Target    Target   `json:"target"`
	Directory string   `json:"directory,omitempty"`
	Args      []string `json:"args"`
}

var _ ActionClient = SSH{}

func (v Invocation) Validate() error {
	if v.Version != 1 || v.Target.Host != Local || len(v.Args) > 1024 || len(v.Args) == 0 {
		return errors.New("invalid remote invocation")
	}
	t := v.Target
	if (t.Kind != "" || t.ID != "") && ((t.Kind != Agent && t.Kind != Interactive) || !targetID.MatchString(t.ID)) {
		return errors.New("invalid remote target")
	}
	if v.Directory != "" && (!path.IsAbs(v.Directory) || !safeText(v.Directory, 4096) || !utf8.ValidString(v.Directory)) {
		return errors.New("remote directory must be an absolute server path")
	}
	for _, arg := range v.Args {
		if strings.ContainsRune(arg, 0) || len(arg) > 64<<10 || !utf8.ValidString(arg) {
			return errors.New("invalid remote argument")
		}
	}
	return nil
}

func DecodeInvocation(encoded string) (Invocation, error) {
	var v Invocation
	if len(encoded) > 128<<10 {
		return v, errors.New("remote invocation too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || validateInvocationJSON(b) != nil || decodeJSON(b, &v) != nil {
		return v, errors.New("invalid remote invocation")
	}
	return v, v.Validate()
}

// encoding/json otherwise accepts duplicate keys and null string values. The
// transport rejects ambiguous selectors instead of silently picking a winner.
func validateInvocationJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var read func(int) error
	read = func(depth int) error {
		if depth > 8 {
			return errors.New("invocation nesting limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return errors.New("null invocation value")
		}
		if delim, ok := token.(json.Delim); ok {
			seen := map[string]bool{}
			for d.More() {
				if delim == '{' {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok {
						return errors.New("invalid invocation key")
					}
					name = strings.ToLower(name)
					if seen[name] {
						return errors.New("duplicate invocation key")
					}
					seen[name] = true
				}
				if err := read(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	return read(0)
}

func actionCommand(req ActionRequest) (string, error) {
	if req.Operation != "command" || req.Target.Host == Local || req.Target.Host.Validate() != nil {
		return "", errors.New("invalid remote action")
	}
	v := Invocation{Version: 1, Target: req.Target, Directory: req.Directory, Args: req.Args}
	v.Target.Host = Local
	if err := v.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	// Leave room for the fixed command prefix below OS single-argument limits.
	if err != nil || len(b) > 95<<10 {
		return "", errors.New("remote invocation too large")
	}
	// The only shell-evaluated value is base64url, never user argv or paths.
	return "coop host-exec " + base64.RawURLEncoding.EncodeToString(b), nil
}

func sshArgs(h Host, tty bool, command string) []string {
	mode := "-T"
	if tty {
		mode = "-tt"
	}
	return []string{mode, "-x", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=3", "--", h.Address, command}
}

// Execute never retries: a lost SSH connection can leave an unknown remote
// outcome. Existing command-specific request IDs remain in Args unchanged.
func (s SSH) Execute(ctx context.Context, req ActionRequest, stdin io.Reader, stdout, stderr io.Writer) (ActionResult, error) {
	result := ActionResult{Target: req.Target}
	command, err := actionCommand(req)
	if err != nil {
		return result, err
	}
	r, err := DefaultRegistry()
	if err != nil {
		return result, err
	}
	h, err := r.Resolve(req.Target.Host.Name)
	if err != nil || h != req.Target.Host {
		return result, errors.New("stale host selection; refresh inventory")
	}
	if req.Target.Kind != "" {
		if _, err := r.ResolveTarget(req.Target); err != nil {
			return result, err
		}
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(h, false, command)...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	// SSH diagnostics may expose trusted configuration and private paths. Remote
	// command errors use the same channel, so return a safe status to the caller.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 200 * time.Millisecond
	err = cmd.Run()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		result.ExitCode = 255
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		}
		if result.ExitCode == 255 {
			return result, errors.New("SSH connection failed or disconnected; remote outcome may be unknown; no local fallback or retry")
		}
		return result, &ActionError{Code: result.ExitCode}
	}
	return result, nil
}

type ActionError struct{ Code int }

func (e *ActionError) Error() string {
	return fmt.Sprintf("remote Coop command failed (status %d; server paths, credentials and lifecycle policy apply)", e.Code)
}
func (e *ActionError) ExitCode() int       { return e.Code }
func (e *ActionError) RemoteExitCode() int { return e.Code }
