package hostcontrol

import (
	"context"
	"fmt"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPWorkspaceReferencesHostBound(t *testing.T) {
	c := fixtureConfig(t)
	h, close, e := OpenHost(c.StateDir, c.Label)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = close() }()
	control, e := NewControl(c)
	if e != nil {
		t.Fatal(e)
	}
	ref := control.reference("agent:robin")
	if !strings.HasPrefix(ref, "w1."+h.ID+".") {
		t.Fatal("reference lacks host")
	}
	if selector, e := control.selector(ref); e != nil || selector != "agent:robin" {
		t.Fatal("reference failed verification")
	}
	if _, e = control.selector(strings.Replace(ref, "robin", "hawk", 1) + "x"); e == nil {
		t.Fatal("forged reference accepted")
	}
	foreign := strings.Replace(ref, h.ID, "h1_00000000000000000000000000000000", 1)
	if _, e = control.Resolve(context.Background(), foreign); e == nil {
		t.Fatal("foreign host accepted")
	}
}

type inventoryBackend struct {
	state  runtime.State
	starts int
	broken bool
}

func (f *inventoryBackend) Inspect(_ context.Context, s workspace.Spec) (workspace.Info, error) {
	if f.broken {
		return workspace.Info{}, fmt.Errorf("offline")
	}
	return workspace.Info{Name: s.RuntimeName, State: f.state}, nil
}
func (f *inventoryBackend) Create(context.Context, workspace.Spec) error { f.starts++; return nil }
func (f *inventoryBackend) Start(context.Context, workspace.Spec) error  { f.starts++; return nil }
func (f *inventoryBackend) Stop(context.Context, workspace.Spec) error   { f.starts++; return nil }
func (f *inventoryBackend) Remove(context.Context, workspace.Spec) error { f.starts++; return nil }
func (f *inventoryBackend) Apply(context.Context, workspace.Spec) error  { f.starts++; return nil }
func (f *inventoryBackend) Exec(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	f.starts++
	return nil
}
func (f *inventoryBackend) Attach(context.Context, string, string, []string) error {
	f.starts++
	return nil
}
func TestMCPAbsentAgentInventoryHasNoSideEffects(t *testing.T) {
	root := privateTemp(t)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", root)
	if e := os.Mkdir(filepath.Join(root, "coop"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "coop", "coop.toml"), []byte("[profiles.robin]\ncommand=['true']\n"), 0600); e != nil {
		t.Fatal(e)
	}
	cfg := fixtureConfig(t)
	_, close, e := OpenHost(cfg.StateDir, cfg.Label)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = close() }()
	c, e := NewControl(cfg)
	if e != nil {
		t.Fatal(e)
	}
	backend := &inventoryBackend{state: runtime.StateAbsent}
	c.loadAgent = func(name string) (*agent.Session, error) {
		return &agent.Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: name, RuntimeName: "coop-agent-" + name}, Controller: workspace.Controller{Backend: backend, Store: c.store}, Runner: backend}, nil
	}
	rows, e := c.List(context.Background())
	if e != nil || len(rows) != 1 || rows[0].Name != "robin" || rows[0].State != "absent" || backend.starts != 0 {
		t.Fatalf("inventory mutated or hid absent agent: %v", e)
	}
	backend.broken = true
	if _, e = c.List(context.Background()); e == nil {
		t.Fatal("offline runtime reported healthy inventory")
	}
}

func TestMCPInteractiveRegistrationRequiresOwnedProjectLabels(t *testing.T) {
	e, _, _ := fixtureEngine(t)
	root := privateTemp(t)
	t.Setenv("XDG_CONFIG_HOME", privateTemp(t))
	rt := runtime.NewMock()
	s, err := session.New(rt, root)
	if err != nil {
		t.Fatal(err)
	}
	rt.Infos = []runtime.ContainerInfo{{Name: s.Name, State: "running", Labels: map[string]string{runtime.ProjectLabel: root, session.SpecLabel: s.SpecFingerprint()}, Mounts: []runtime.MountInfo{{Source: root, Destination: root, Bind: true}}}}
	rt.ContainerImages = map[string]string{s.Name: s.DesiredImageName()}
	e.control.rt = rt
	got, err := e.control.RegisterInteractive(context.Background(), root)
	if err != nil || !got.Owned || got.Mode != "interactive" {
		t.Fatalf("registration: %+v %v", got, err)
	}
	if len(rt.Started)+len(rt.Stopped)+len(rt.ExecCalls)+len(rt.Run_) != 0 {
		t.Fatal("registration mutated runtime")
	}
	rt.Infos[0].Labels[runtime.ProjectLabel] = "/foreign"
	if _, err = e.control.RegisterInteractive(context.Background(), root); err == nil {
		t.Fatal("foreign project labels accepted")
	}
}
