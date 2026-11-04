package agent

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type attachBackend struct {
	renewBackend
	fingerprint string
	attached    int
	argv        []string
}

func (b *attachBackend) Inspect(context.Context, workspace.Spec) (workspace.Info, error) {
	b.inspected++
	return workspace.Info{State: b.state, Fingerprint: b.fingerprint}, nil
}

func (b *attachBackend) Attach(_ context.Context, _, _ string, args []string) error {
	b.attached++
	b.argv = args
	return nil
}

func TestAttachRunningPreservesInstalledLegacyGeneration(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       runtime.State
		fingerprint string
		portable    bool
		current     bool
		missing     bool
		want        bool
	}{
		{name: "legacy desired image changed", state: runtime.StateRunning, fingerprint: "installed", want: true},
		{name: "actual runtime drift", state: runtime.StateRunning, fingerprint: "runtime-drift"},
		{name: "absent", state: runtime.StateAbsent, fingerprint: "installed"},
		{name: "stopped", state: runtime.StateStopped, fingerprint: "installed"},
		{name: "unowned", state: runtime.StateRunning, fingerprint: "installed", missing: true},
		{name: "portable generation changed", state: runtime.StateRunning, fingerprint: "installed", portable: true},
		{name: "portable current generation", state: runtime.StateRunning, fingerprint: "installed", portable: true, current: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", root)
			store := workspace.FileStore{Root: filepath.Join(root, "workspaces")}
			backend := &attachBackend{renewBackend: renewBackend{state: tc.state}, fingerprint: tc.fingerprint}
			s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "attach-test", RuntimeName: "coop-agent-attach-test", Home: "/root", Workdir: "/workspace", Image: "desired-image", Fingerprint: "desired", Config: config.Config{IncludeCredentials: []string{"must-not-acquire"}}}, Controller: workspace.Controller{Backend: backend, Store: store}, Runner: backend}
			if tc.portable {
				s.Spec.Profile.Runtime = "container"
			}
			if tc.current {
				s.Spec.Fingerprint = "installed"
			}
			record := s.Spec.Record()
			record.Image, record.Fingerprint = "installed-image", "installed"
			if !tc.missing {
				if err = store.Save(record); err != nil {
					t.Fatal(err)
				}
			}
			err = s.AttachRunning(t.Context())
			if (err == nil) != tc.want || backend.attached != map[bool]int{true: 1, false: 0}[tc.want] {
				t.Fatal("unexpected attachment outcome", err, backend.attached)
			}
			if backend.created != 0 || backend.started != 0 || backend.executed != 0 {
				t.Fatal("attachment prepared or changed the guest")
			}
			if tc.want && !reflect.DeepEqual(backend.argv, []string{guestControl, "attach"}) {
				t.Fatal("attachment did not reconnect to the installed terminal", backend.argv)
			}
			if !tc.missing {
				after, err := store.Load(workspace.Agent, s.Spec.Identity)
				if err != nil || !reflect.DeepEqual(after, record) {
					t.Fatal("attachment changed ownership or applied configuration", err)
				}
			}
		})
	}
}
