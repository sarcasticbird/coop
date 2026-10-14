package agent

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type renewBackend struct {
	state                                 runtime.State
	inspected, created, started, executed int
}

func (b *renewBackend) Inspect(context.Context, workspace.Spec) (workspace.Info, error) {
	b.inspected++
	return workspace.Info{State: b.state, Fingerprint: "fp"}, nil
}
func (b *renewBackend) Create(context.Context, workspace.Spec) error { b.created++; return nil }
func (b *renewBackend) Start(context.Context, workspace.Spec) error  { b.started++; return nil }
func (b *renewBackend) Stop(context.Context, workspace.Spec) error   { return nil }
func (b *renewBackend) Remove(context.Context, workspace.Spec) error { return nil }
func (b *renewBackend) Apply(context.Context, workspace.Spec) error  { return nil }
func (b *renewBackend) Exec(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	b.executed++
	return nil
}
func (b *renewBackend) Attach(context.Context, string, string, []string) error { return nil }

func TestRefreshRunningNeverStartsStoppedAgentsOrPublishesAfterStop(t *testing.T) {
	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateRoot)
	for _, initial := range []runtime.State{runtime.StateAbsent, runtime.StateStopped, runtime.StateRunning} {
		b := &renewBackend{state: initial}
		acquisitions := 0
		manager := credential.NewManager("")
		manager.Run = func(context.Context, string, []string) ([]byte, error) {
			acquisitions++
			b.state = runtime.StateStopped
			return []byte("opaque"), nil
		}
		s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "test", RuntimeName: "coop-agent-test", Fingerprint: "fp", Config: config.Config{IncludeCredentials: []string{"app"}, Credentials: map[string]config.Credential{"app": {Source: config.CredentialSource{Type: "command", Argv: []string{"helper"}}, Expose: []config.CredentialInjection{{Type: "file", PathEnv: "COOP_GITHUB_APP_BUNDLE_FILE"}}}}}}, Controller: workspace.Controller{Backend: b}, Runner: b, CredentialManager: manager}
		store, storeErr := workspace.DefaultStore()
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		if err := store.Save(s.Spec.Record()); err != nil {
			t.Fatal(err)
		}
		s.Controller.Store = store
		err := s.RefreshRunning(context.Background(), nil)
		if !errors.Is(err, ErrAgentNotRunning) || b.created != 0 || b.started != 0 || b.executed != 0 {
			t.Fatalf("stopped machine changed: %v %+v", err, b)
		}
		if initial != runtime.StateRunning && acquisitions != 0 {
			t.Fatal("acquired credentials for stopped machine")
		}
	}
}

func TestWatchCredentialsContinuesOtherProfilesAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	waits := 0
	err := WatchCredentials(ctx, RenewConfig{Profiles: []string{"robin", "hawk"}, Interval: 15 * time.Minute, Retry: time.Minute}, func(_ context.Context, name string) error {
		calls = append(calls, name)
		if name == "robin" {
			return errors.New("safe failure")
		}
		return nil
	}, func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(calls) != 4 || calls[1] != "hawk" || calls[3] != "hawk" {
		t.Fatalf("renewal starved or ignored cancellation: %q %v", calls, err)
	}
	for _, cfg := range []RenewConfig{{Profiles: nil, Interval: time.Minute, Retry: time.Minute}, {Profiles: []string{"robin", "robin"}, Interval: time.Minute, Retry: time.Minute}, {Profiles: []string{"../other"}, Interval: time.Minute, Retry: time.Minute}} {
		if WatchCredentials(context.Background(), cfg, nil, nil) == nil {
			t.Fatal("invalid watcher configuration accepted")
		}
	}
}

func TestRenewalDetectsWallClockAdvanceAfterLaptopSleep(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	err := waitUntilRefresh(context.Background(), 15*time.Minute, func() time.Time { return now }, func(_ context.Context, delay time.Duration) error {
		calls++
		if delay > time.Minute {
			t.Fatal("renewal does not check promptly after wake")
		}
		now = now.Add(2 * time.Hour)
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatal("overdue refresh was delayed after sleep")
	}
}
