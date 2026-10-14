package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
)

var ErrAgentNotRunning = errors.New("agent is not running")

// RefreshRunning never starts a machine, seeds files or changes its services.
// Acquisition happens outside the lifecycle lock; publication rechecks state.
func (s *Session) RefreshRunning(ctx context.Context, grants []string) error {
	inspect := func() error {
		info, err := s.Controller.Backend.Inspect(ctx, s.Spec)
		if err != nil {
			return err
		}
		if info.State != runtime.StateRunning {
			return ErrAgentNotRunning
		}
		if s.Controller.Store == nil {
			return errors.New("agent ownership store is not configured")
		}
		record, err := s.Controller.Store.Load(s.Spec.Mode, s.Spec.Identity)
		if err != nil {
			return fmt.Errorf("verify agent ownership: %w", err)
		}
		if record.RuntimeName != s.Spec.RuntimeName || record.Fingerprint != s.Spec.Fingerprint || (info.Fingerprint != "" && info.Fingerprint != s.Spec.Fingerprint) {
			return errors.New("agent configuration requires maintenance before renewal")
		}
		return nil
	}
	if err := inspect(); err != nil {
		return err
	}
	selected, err := credential.Resolve(s.Spec.Config, grants)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	s.CredentialManager.NonInteractive = true
	acquired, err := s.CredentialManager.AcquireAll(ctx, home, selected)
	if err != nil {
		return err
	}
	release, err := lock.AcquireContext(ctx, s.Spec.RuntimeName, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	if err := inspect(); err != nil {
		return err
	}
	_, err = (credential.PersistentManager{Executor: executor{runner: s.Runner}}).Stage(ctx, s.Spec.RuntimeName, acquired)
	return err
}

type RenewConfig struct {
	Profiles        []string
	Interval, Retry time.Duration
}

func WatchCredentials(ctx context.Context, cfg RenewConfig, refresh func(context.Context, string) error, wait func(context.Context, time.Duration) error) error {
	if len(cfg.Profiles) == 0 || cfg.Interval < time.Minute || cfg.Interval > 30*time.Minute || cfg.Retry < time.Second || cfg.Retry > cfg.Interval {
		return errors.New("invalid credential renewal schedule")
	}
	seen := map[string]bool{}
	for _, profile := range cfg.Profiles {
		if config.ValidateProfileName(profile) != nil || seen[profile] {
			return errors.New("invalid or duplicate renewal profile")
		}
		seen[profile] = true
	}
	if refresh == nil {
		return errors.New("credential renewal callback is required")
	}
	if wait == nil {
		wait = func(ctx context.Context, d time.Duration) error {
			return waitUntilRefresh(ctx, d, time.Now, func(ctx context.Context, tick time.Duration) error {
				timer := time.NewTimer(tick)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			})
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := cfg.Interval
		for _, profile := range cfg.Profiles {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := refresh(ctx, profile); err != nil && !errors.Is(err, ErrAgentNotRunning) {
				delay = cfg.Retry
			}
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

func waitUntilRefresh(ctx context.Context, d time.Duration, now func() time.Time, pause func(context.Context, time.Duration) error) error {
	// UTC drops the monotonic reading, which can stop advancing during sleep.
	deadline := now().UTC().Add(d)
	for {
		remaining := deadline.Sub(now().UTC())
		if remaining <= 0 {
			return nil
		}
		if remaining > time.Minute {
			remaining = time.Minute
		}
		if err := pause(ctx, remaining); err != nil {
			return err
		}
	}
}
