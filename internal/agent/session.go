// Package agent runs a named, self-contained Coop with machine-scoped
// credentials and a reconnectable supervised terminal.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/core"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/releasetool"
	"github.com/sarcasticbird/coop/internal/repository"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/seed"
	"github.com/sarcasticbird/coop/internal/workspace"
)

const guestControl = "/usr/local/libexec/coop-guest"

type Session struct {
	Spec              workspace.Spec
	Controller        workspace.Controller
	Runner            workspace.Runner
	CredentialManager *credential.Manager
	CoreLock          []byte
	Output            io.Writer
}

func New(name string) (*Session, error) {
	spec, err := workspace.ResolveAgent(name)
	if err != nil {
		return nil, err
	}
	if err := releasetool.HydrateConfig(&spec.Config); err != nil {
		return nil, err
	}
	coreLock, warning, err := core.Active()
	if err != nil {
		return nil, err
	}
	if warning != "" {
		spec.Config.Warnings = append(spec.Config.Warnings, warning)
	}
	spec.Image = image.MachineImageName(spec.Config, coreLock)
	spec.Fingerprint = workspace.Fingerprint(spec)
	store, err := workspace.DefaultStore()
	if err != nil {
		return nil, err
	}
	backend := workspace.MachineBackend{Runtime: runtime.AppleMachine{}, Store: store}
	return &Session{Spec: spec, Controller: workspace.Controller{Backend: backend, Store: store}, Runner: backend, CredentialManager: credential.NewManager(""), CoreLock: coreLock, Output: os.Stdout}, nil
}
func (s *Session) output() io.Writer {
	if s.Output != nil {
		return s.Output
	}
	return io.Discard
}

type executor struct{ runner workspace.Runner }

func (e executor) Exec(name string, args []string, input io.Reader) error {
	return e.ExecContext(context.Background(), name, args, input)
}

func (e executor) ExecContext(ctx context.Context, name string, args []string, input io.Reader) error {
	return e.runner.Exec(ctx, name, "/", args, input, io.Discard, io.Discard)
}
func (e executor) GuestFileExists(name, path string) (bool, error) {
	var out bytes.Buffer
	err := e.runner.Exec(context.Background(), name, "/", []string{"sh", "-c", `if [ -e "$1" ] || [ -L "$1" ]; then printf yes; else printf no; fi`, "coop-exists", path}, nil, &out, io.Discard)
	if err != nil {
		return false, err
	}
	switch out.String() {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, fmt.Errorf("invalid guest exists response")
}

func (s *Session) prepare(ctx context.Context, requested []string, provision, ensure bool) error {
	selected, err := credential.Resolve(s.Spec.Config, requested)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	acquired, err := s.CredentialManager.AcquireAll(ctx, home, selected)
	if err != nil {
		return err
	}
	if err := s.Controller.Ensure(ctx, s.Spec); err != nil {
		return fmt.Errorf("ensure agent machine: %w", err)
	}
	release, err := lock.AcquireContext(ctx, s.Spec.RuntimeName, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	info, err := s.Controller.Backend.Inspect(ctx, s.Spec)
	if err != nil {
		return err
	}
	if info.State != runtime.StateRunning {
		return fmt.Errorf("machine stopped during preparation; retry entry")
	}
	if err := workspace.CheckSpecification(info.State, info.Fingerprint, s.Spec.Fingerprint); err != nil {
		return err
	}
	exec := executor{runner: s.Runner}
	if err := exec.ExecContext(ctx, s.Spec.RuntimeName, []string{guestControl, "prepare"}, nil); err != nil {
		return fmt.Errorf("prepare guest: %w", err)
	}
	if _, err := (credential.PersistentManager{Executor: exec}).Stage(ctx, s.Spec.RuntimeName, acquired); err != nil {
		return fmt.Errorf("stage machine credentials: %w", err)
	}
	bootstrapped, err := exec.GuestFileExists(s.Spec.RuntimeName, "/var/lib/coop/bootstrapped")
	if err != nil {
		return err
	}
	if !bootstrapped {
		if err := seed.ApplyContext(ctx, exec, s.Spec.RuntimeName, home, s.Spec.Home, s.Spec.Config.Seeds); err != nil {
			return err
		}
		if err := exec.ExecContext(ctx, s.Spec.RuntimeName, []string{"touch", "/var/lib/coop/bootstrapped"}, nil); err != nil {
			return err
		}
	}
	command, err := json.Marshal(struct {
		Command  []string `json:"command"`
		Restart  string   `json:"restart"`
		Services any      `json:"services"`
	}{s.Spec.Profile.Command, s.Spec.Profile.Restart, s.Spec.Profile.Services})
	if err != nil {
		return err
	}
	if err := exec.ExecContext(ctx, s.Spec.RuntimeName, []string{guestControl, "command"}, bytes.NewReader(command)); err != nil {
		return err
	}
	if provision {
		opts := repository.Options{}
		for _, grant := range selected {
			for _, expose := range config.Exposures(grant.Spec) {
				if expose.Type == "file" && expose.PathEnv == "COOP_GITHUB_APP_BUNDLE_FILE" {
					opts.GitHubApp = true
				}
			}
		}
		result, err := repository.ProvisionWithOptions(ctx, s.Runner, s.Spec.RuntimeName, s.Spec.Profile.Orgs, opts)
		if _, writeErr := fmt.Fprintf(s.output(), "repositories: %d cloned, %d existing, %d failed\n", len(result.Cloned), len(result.Existing), len(result.Failed)); writeErr != nil {
			return writeErr
		}
		if err != nil {
			return err
		}
	}
	if ensure {
		return exec.ExecContext(ctx, s.Spec.RuntimeName, []string{guestControl, "ensure"}, nil)
	}
	return nil
}
func (s *Session) Up(ctx context.Context, grants []string) error {
	return s.prepare(ctx, grants, true, true)
}
func (s *Session) Sync(ctx context.Context, grants []string) error {
	return s.prepare(ctx, grants, true, false)
}
func (s *Session) Refresh(ctx context.Context, grants []string) error {
	if err := s.prepare(ctx, grants, false, false); err != nil {
		return err
	}
	_, err := fmt.Fprintln(s.output(), "Credentials refreshed. Existing processes retain their previous credentials until restarted.")
	return err
}
func (s *Session) Attach(ctx context.Context) error {
	return s.Runner.Attach(ctx, s.Spec.RuntimeName, s.Spec.Workdir, []string{guestControl, "attach"})
}
func (s *Session) Shell(ctx context.Context, grants []string) error {
	if err := s.prepare(ctx, grants, false, false); err != nil {
		return err
	}
	return s.Command(ctx, []string{"zsh", "-l"})
}
func (s *Session) Command(ctx context.Context, args []string) error {
	argv := append([]string{"/usr/local/libexec/coop-env"}, args...)
	for _, name := range s.Spec.Config.IncludeCredentials {
		for _, expose := range config.Exposures(s.Spec.Config.Credentials[name]) {
			if expose.Type == "file" && expose.PathEnv == "COOP_GITHUB_APP_BUNDLE_FILE" {
				argv = append([]string{"/usr/local/libexec/coop-env", "/usr/local/libexec/coop-github", "env", "--"}, args...)
				return s.Runner.Attach(ctx, s.Spec.RuntimeName, s.Spec.Workdir, argv)
			}
		}
	}
	return s.Runner.Attach(ctx, s.Spec.RuntimeName, s.Spec.Workdir, argv)
}
func (s *Session) PrepareCommand(ctx context.Context, grants []string) error {
	return s.prepare(ctx, grants, false, false)
}
func (s *Session) Down(ctx context.Context) error    { return s.Controller.Down(ctx, s.Spec) }
func (s *Session) Apply(ctx context.Context) error   { return s.Controller.Apply(ctx, s.Spec) }
func (s *Session) Destroy(ctx context.Context) error { return s.Controller.Destroy(ctx, s.Spec) }
func (s *Session) Status(ctx context.Context) error {
	info, err := s.Controller.Backend.Inspect(ctx, s.Spec)
	if err != nil {
		return err
	}
	pending := info.State != runtime.StateAbsent && info.Fingerprint != s.Spec.Fingerprint
	if _, err := fmt.Fprintf(s.output(), "mode: agent\nprofile: %s\nmachine: %s\nstate: %s\nimage: %s\nmaintenance pending: %t\n", s.Spec.Identity, s.Spec.RuntimeName, info.State, s.Spec.Image, pending); err != nil {
		return err
	}
	if info.State != runtime.StateRunning {
		return nil
	}
	var out bytes.Buffer
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", []string{guestControl, "status"}, nil, &out, io.Discard); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.output(), "agent: %s\n", strings.TrimSpace(out.String())); err != nil {
		return err
	}
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", []string{"sh", "-c", `if [ -L /run/coop/current-credentials ]; then cat /run/coop/current-credentials/metadata.json; fi`}, nil, s.output(), io.Discard); err != nil {
		return err
	}
	if _, ok := s.Spec.Profile.Services["worker"]; ok {
		if err := s.Worker(ctx, "status"); err != nil {
			_, writeErr := fmt.Fprintln(s.output(), "worker: unavailable (check staged installation and service health)")
			return writeErr
		}
	}
	return nil
}
