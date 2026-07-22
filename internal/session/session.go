// Package session orchestrates coop lifecycles: resolve project, ensure
// container, seed configs, exec in.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/seed"
)

type Session struct {
	RT        runtime.Runtime
	Project   string // resolved project root
	Name      string // container name
	Cfg       config.Config
	HostHome  string
	GuestHome string // == HostHome: the identical-path property
}

// New resolves a session from a working directory.
func New(rt runtime.Runtime, cwd string) (*Session, error) {
	root, err := project.Resolve(cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Session{
		RT: rt, Project: root, Name: project.Name(root),
		Cfg: cfg, HostHome: home, GuestHome: home,
	}, nil
}

// DefaultImageName is the base local tag used by the embedded image build.
const DefaultImageName = "coop:latest"

// EffectiveImageName is the tag a session actually runs. Embedded builds use
// a tag derived from their definition and package list, so changed build inputs
// cannot silently reuse a stale image.
func EffectiveImageName(img config.Image) string {
	if len(img.ExtraPackages) == 0 && img.Name != DefaultImageName {
		return img.Name
	}
	pkgs := append([]string(nil), img.ExtraPackages...)
	sort.Strings(pkgs)
	// Hash includes the full source reference: changing the base tag or
	// digest with identical packages must produce a distinct derived tag.
	sum := sha256.Sum256([]byte(img.Name + "\x00" + strings.Join(pkgs, " ") + "\x00" + image.Fingerprint()))
	// Strip a digest suffix, then only a tag colon after the final
	// slash — registry ports (localhost:5000/team/coop:latest) and
	// digest refs (repo@sha256:...) must both survive.
	base := img.Name
	if i := strings.Index(base, "@"); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base = base[:i]
	}
	return base + ":local-" + hex.EncodeToString(sum[:])[:12]
}

// EnsureImage requires a local build for every embedded image. The public beta
// does not redistribute its third-party image contents.
func (s *Session) EnsureImage() error {
	name := EffectiveImageName(s.Cfg.Image)
	exists, err := s.RT.ImageExists(name)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", name, err)
	}
	if exists {
		return nil
	}
	return fmt.Errorf("image %s not found: build it locally with `coop rebuild`", name)
}

// SpecLabel is the container label carrying the spec fingerprint.
const SpecLabel = "coop.spec"

// SpecFingerprint canonically hashes everything a running container
// must agree with config about: image, resources, SSH capability,
// project mount, HOME, and volume layout. Containers are reconciled
// against this label — never by parsing runtime state — so ANY config
// change (including revoking ssh) forces recreation. Legacy containers
// without the label always mismatch and get recreated once.
func (s *Session) SpecFingerprint() string {
	var vols []string
	for agent, spec := range s.Cfg.Agents {
		vols = append(vols, agent+"="+config.ExpandHome(spec.State, s.GuestHome))
	}
	sort.Strings(vols)
	canonical := fmt.Sprintf("v1|img=%s|cpus=%d|mem=%s|ssh=%t|proj=%s|home=%s|vols=%s",
		EffectiveImageName(s.Cfg.Image), s.Cfg.Resources.CPUs, s.Cfg.Resources.Memory,
		s.Cfg.SSH, s.Project, s.GuestHome, strings.Join(vols, ","))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16]
}

// Up ensures the container exists and is running, then applies seeds.
// An existing container whose spec label disagrees with current config
// (image, resources, ssh, volumes...) is recreated — state volumes
// survive recreation by design. Serialized per project by a host lock.
func (s *Session) Up() error {
	release, err := lock.Acquire(s.Name, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	return s.up()
}

func (s *Session) up() error {
	want := s.SpecFingerprint()
	state, err := s.RT.State(s.Name)
	if err != nil {
		return fmt.Errorf("state of %s: %w", s.Name, err)
	}
	if state != runtime.StateAbsent {
		have, err := s.RT.ContainerLabel(s.Name, SpecLabel)
		if err != nil {
			return fmt.Errorf("spec check for %s: %w", s.Name, err)
		}
		if have != want {
			// Validate the replacement image exists BEFORE tearing down
			// a working container.
			if err := s.EnsureImage(); err != nil {
				return fmt.Errorf("%s config changed but replacement not ready: %w", s.Name, err)
			}
			fmt.Printf("%s config changed (spec %s -> %s) — recreating, state volumes preserved...\n",
				s.Name, valueOrLegacy(have), want)
			_ = s.RT.Stop(s.Name)
			if err := s.RT.Remove(s.Name); err != nil {
				return fmt.Errorf("recreate %s: %w", s.Name, err)
			}
			state = runtime.StateAbsent
		}
	}
	switch state {
	case runtime.StateRunning:
	case runtime.StateStopped:
		if err := s.RT.Start(s.Name); err != nil {
			return fmt.Errorf("start %s: %w", s.Name, err)
		}
	default:
		if err := s.EnsureImage(); err != nil {
			return err
		}
		// Per-project agent state volumes: sessions/history stay isolated
		// per coop (no cross-project transcript leakage), credentials
		// refreshed in-guest persist across restarts.
		var vols []runtime.Volume
		for agent, spec := range s.Cfg.Agents {
			v := s.Name + project.VolumeSep + agent
			if err := s.RT.EnsureVolume(v); err != nil {
				return fmt.Errorf("volume %s: %w", v, err)
			}
			vols = append(vols, runtime.Volume{
				Name:   v,
				Target: config.ExpandHome(spec.State, s.GuestHome),
			})
		}
		sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
		err := s.RT.Run(runtime.RunSpec{
			Name:   s.Name,
			Image:  EffectiveImageName(s.Cfg.Image),
			CPUs:   s.Cfg.Resources.CPUs,
			Memory: s.Cfg.Resources.Memory,
			SSH:    s.Cfg.SSH, // default off — deliberate, global-config-only capability
			Labels: map[string]string{SpecLabel: want},
			// HOME enforced at run time too, so custom images without a
			// baked GUEST_HOME still honor the identical-path property.
			Env:     map[string]string{"HOME": s.GuestHome},
			Mounts:  []runtime.Mount{{Source: s.Project, Target: s.Project}},
			Volumes: vols,
		})
		if err != nil {
			return fmt.Errorf("create %s: %w", s.Name, err)
		}
		// The image is built with a default GUEST_HOME; the real home is
		// decided here at run time and must exist.
		if err := s.RT.Exec(s.Name, []string{"mkdir", "-p", s.GuestHome}, nil); err != nil {
			return fmt.Errorf("ensure guest home: %w", err)
		}
	}
	if err := seed.Apply(s.RT, s.Name, s.HostHome, s.GuestHome, s.Cfg.Seeds); err != nil {
		return fmt.Errorf("seeding %s: %w", s.Name, err)
	}
	return nil
}

// canonicalizeCwd aligns a working directory with the canonical
// project identity (symlink aliases must not produce unmounted paths).
func canonicalizeCwd(cwd string) string {
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		return r
	}
	return cwd
}

// Enter runs inside the session at cwd (clamped to the project) — a shell
// when argv is empty, otherwise the given agent/command, wrapped in
// `flox activate` when a manifest applies. Argv is passed as a vector
// end-to-end: arguments must never be re-parsed as shell syntax.
func (s *Session) Enter(cwd string, argv []string) error {
	ctx, stopSignals := runtime.NotifyContext(context.Background())
	defer stopSignals()

	cwd = canonicalizeCwd(cwd)
	if !withinProject(s.Project, cwd) {
		cwd = s.Project
	}
	if len(argv) == 0 {
		return s.RT.ExecInteractive(ctx, s.Name, cwd, []string{"zsh", "-l"})
	}
	if dir := s.floxDir(cwd); dir != "" {
		wrapped := append([]string{"flox", "activate", "--dir", dir, "--"}, argv...)
		return s.RT.ExecInteractive(ctx, s.Name, cwd, wrapped)
	}
	return s.RT.ExecInteractive(ctx, s.Name, cwd, argv)
}

// floxDir finds the manifest governing cwd, walking up to the project
// boundary. In a bare+worktree layout the manifest lives in the active
// worktree, not the project root — anchoring at cwd finds it.
func (s *Session) floxDir(cwd string) string {
	for dir := cwd; withinProject(s.Project, dir); dir = filepath.Dir(dir) {
		if isDir(filepath.Join(dir, ".flox")) {
			return dir
		}
		if dir == s.Project {
			break
		}
	}
	return ""
}

func withinProject(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Session) Down() error {
	release, err := lock.Acquire(s.Name, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.RT.State(s.Name)
	if err != nil {
		return fmt.Errorf("state of %s: %w", s.Name, err)
	}
	if state == runtime.StateAbsent {
		return fmt.Errorf("%s does not exist", s.Name)
	}
	return s.RT.Stop(s.Name)
}

// Destroy removes the container and its state volumes. Volumes are
// matched by name prefix (not the configured agent set) so volumes
// created under an older agent config are not orphaned.
func (s *Session) Destroy() error {
	release, err := lock.Acquire(s.Name, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.RT.State(s.Name)
	if err != nil {
		return fmt.Errorf("state of %s: %w", s.Name, err)
	}
	if state != runtime.StateAbsent {
		_ = s.RT.Stop(s.Name)
		if err := s.RT.Remove(s.Name); err != nil {
			return fmt.Errorf("remove %s: %w", s.Name, err)
		}
	}
	return DestroyVolumes(s.RT, s.Name)
}

// DestroyVolumes removes every volume prefixed by a coop's name.
// Failures are aggregated so destroy cannot report success while leaving
// credential or transcript volumes behind.
func DestroyVolumes(rt runtime.Runtime, name string) error {
	vols, err := rt.ListVolumes()
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	var errs []error
	for _, v := range vols {
		if strings.HasPrefix(v, name+project.VolumeSep) {
			if err := rt.RemoveVolume(v); err != nil {
				errs = append(errs, fmt.Errorf("volume %s: %w", v, err))
			}
		}
	}
	return errors.Join(errs...)
}

func valueOrLegacy(s string) string {
	if s == "" {
		return "unlabeled/legacy"
	}
	return s
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
