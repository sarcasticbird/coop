package hostcontrol

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Control struct {
	cfg       Config
	host      Host
	key       []byte
	store     workspace.Store
	rt        runtime.Runtime
	machine   runtime.AppleMachine
	loadAgent func(string) (*agent.Session, error)
}
type Binding struct {
	Workspace                          Workspace
	Selector, RuntimeName, Incarnation string
	Instance                           string
	Spec                               workspace.Spec
	Agent                              *agent.Session
	Interactive                        *session.Session
	Runner                             workspace.Runner
}
type WorkspaceStatus struct {
	Workspace
	DesiredFingerprint string `json:"desired_fingerprint"`
	Maintenance        string `json:"maintenance,omitempty"`
}
type HostInfo struct {
	Host
	RuntimeVersion   string   `json:"runtime_version,omitempty"`
	RuntimeAvailable bool     `json:"runtime_available"`
	Capabilities     []string `json:"capabilities"`
}

func NewControl(cfg Config) (*Control, error) {
	if e := cfg.Validate(); e != nil {
		return nil, e
	}
	d, e := privateDir(cfg.StateDir, false)
	if e != nil {
		return nil, e
	}
	defer func() { _ = d.Close() }()
	b, e := readAt(d, "identity.json", 1024)
	if e != nil {
		return nil, e
	}
	var h Host
	if e = strictJSON(b, &h); e != nil {
		return nil, e
	}
	h.Label = cfg.Label
	key, e := readAt(d, "references.key", 32)
	if errors.Is(e, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		if e = writeAt(d, "references.key", key); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if len(key) != 32 {
		return nil, fail("invalid_reference_key")
	}
	store, e := workspace.DefaultStore()
	if e != nil {
		return nil, e
	}
	return &Control{cfg: cfg, host: h, key: key, store: store, rt: runtime.NewApple(), machine: runtime.AppleMachine{}, loadAgent: agent.New}, nil
}
func (c *Control) reference(selector string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(selector))
	m := hmac.New(sha256.New, c.key)
	_, _ = io.WriteString(m, c.host.ID+"."+encoded)
	return "w1." + c.host.ID + "." + encoded + "." + hex.EncodeToString(m.Sum(nil))
}
func (c *Control) selector(ref string) (string, error) {
	p := strings.Split(ref, ".")
	if len(p) != 4 || p[0] != "w1" || p[1] != c.host.ID || len(ref) > 1024 {
		return "", fail("invalid_workspace")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil {
		return "", fail("invalid_workspace")
	}
	selector := string(b)
	if !hmac.Equal([]byte(c.reference(selector)), []byte(ref)) {
		return "", fail("invalid_workspace")
	}
	p = strings.Split(selector, ":")
	if len(p) != 2 || (p[0] != "agent" && p[0] != "interactive") || !selectorName(p[1]) {
		return "", fail("invalid_workspace")
	}
	return selector, nil
}
func (c *Control) Resolve(ctx context.Context, ref string) (Binding, error) {
	selector, e := c.selector(ref)
	if e != nil {
		return Binding{}, e
	}
	return c.resolveSelector(ctx, selector)
}
func (c *Control) resolveSelector(ctx context.Context, selector string) (Binding, error) {
	p := strings.SplitN(selector, ":", 2)
	b := Binding{Selector: selector}
	b.Workspace = Workspace{Ref: c.reference(selector), Mode: p[0], Name: p[1]}
	if p[0] == "agent" {
		s, e := c.loadAgent(p[1])
		if e != nil {
			return b, fail("profile_unavailable")
		}
		s.Output = io.Discard
		b.Agent = s
		b.Spec = s.Spec
		b.Runner = s.Runner
		b.RuntimeName = s.Spec.RuntimeName
		info, e := s.Controller.Backend.Inspect(ctx, s.Spec)
		if e != nil {
			return b, fail("runtime_unavailable")
		}
		b.Workspace.State = info.State.String()
		b.Workspace.Fingerprint = info.Fingerprint
		b.Incarnation = info.BootIdentity
		if b.Incarnation == "" {
			b.Incarnation = info.ChildContainerID
		}
		b.Instance = info.RuntimeInstance
		_, b.Workspace.WorkerConfigured = s.Spec.Profile.Services["worker"]
	} else {
		r, e := c.store.Load(workspace.Interactive, p[1])
		if e != nil {
			return b, fail("unowned_workspace")
		}
		root := r.Applied.Project
		for _, bind := range c.cfg.Bindings {
			if bind.Mode == "interactive" && bind.Name == p[1] {
				if root != "" && root != bind.Project {
					return b, fail("binding_mismatch")
				}
				root = bind.Project
			}
		}
		if !filepath.IsAbs(root) {
			return b, fail("invalid_project_binding")
		}
		rt := c.rt
		if apple, ok := rt.(*runtime.Apple); ok {
			rt = apple.WithContext(ctx)
		}
		s, e := session.New(rt, root)
		if e != nil || s.Project != root || s.Name != p[1] || project.Name(root) != p[1] {
			return b, fail("invalid_project_binding")
		}
		s.Output = io.Discard
		b.Interactive = s
		b.Spec = workspace.Spec{Mode: workspace.Interactive, Identity: s.Name, RuntimeName: s.Name, Project: s.Project, Home: s.GuestHome, Workdir: s.Project, Image: s.DesiredImageName(), Fingerprint: s.SpecFingerprint(), Config: s.Cfg}
		b.RuntimeName = s.Name
		rows, e := rt.Containers()
		if e != nil {
			return b, fail("runtime_unavailable")
		}
		b.Workspace.State = "absent"
		for _, row := range rows {
			if row.Name != s.Name {
				continue
			}
			if row.ProjectMount() != root || row.Labels[runtime.ProjectLabel] != root {
				return b, fail("runtime_ownership_mismatch")
			}
			b.Workspace.State = row.State
			b.Workspace.Fingerprint = row.Labels[session.SpecLabel]
			if !row.Started.IsZero() {
				b.Incarnation = row.Name + "/" + row.Started.UTC().Format(time.RFC3339Nano)
			}
			if !row.Created.IsZero() {
				b.Instance = row.Name + "/" + row.Created.UTC().Format(time.RFC3339Nano)
			}
		}
		apple, ok := rt.(*runtime.Apple)
		if !ok {
			return b, fail("unsupported_runtime")
		}
		b.Runner = interactiveRunner{apple}
	}
	r, e := c.store.Load(b.Spec.Mode, b.Spec.Identity)
	if e == nil {
		b.Workspace.Owned = r.RuntimeName == b.RuntimeName && r.Mode == b.Spec.Mode && (b.Spec.Mode != workspace.Interactive || r.Applied.Project == b.Spec.Project)
	} else if !os.IsNotExist(e) {
		return b, fail("ownership_unavailable")
	}
	return b, nil
}

type interactiveRunner struct{ apple *runtime.Apple }

func (r interactiveRunner) Exec(ctx context.Context, n, cwd string, args []string, in io.Reader, out, err io.Writer) error {
	return r.apple.ExecStreams(ctx, n, cwd, args, in, out, err)
}
func (r interactiveRunner) Attach(ctx context.Context, n, cwd string, args []string) error {
	return r.apple.ExecInteractive(ctx, n, cwd, args)
}

func (c *Control) Host() Host { return c.host }

// ReadCredentialFile reads a private, host-owned token source without following symlinks.
func ReadCredentialFile(path string) (string, error) {
	b, err := readPrivate(path, 512)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Selector verifies an opaque reference before policy resolution; it never opens a caller path.
func (c *Control) Selector(ref string) (string, error) { return c.selector(ref) }

func ReadPrivateFile(path string, max int64) ([]byte, error) { return readPrivate(path, max) }

func (c *Control) SetVersion(version string) { c.host.Version = version }
