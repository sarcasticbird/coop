package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

func testSession(t *testing.T, m *runtime.Mock) *Session {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // isolate per-project locks
	s := &Session{
		RT:      m,
		Project: "/Users/u/Projects/proj",
		Name:    "coop-proj",
		Cfg: config.Config{
			Image:     config.Image{Name: "coop:latest"},
			Resources: config.Resources{CPUs: 4, Memory: "8G"},
			Agents: map[string]config.Agent{
				"opencode": {State: "~/.local/share/opencode"},
				"claude":   {State: "~/.claude"},
				"codex":    {State: "~/.codex"},
			},
		},
		HostHome:  "/Users/u",
		GuestHome: "/Users/u",
	}
	m.Images = map[string]bool{EffectiveImageName(s.Cfg.Image): true}
	return s
}

func TestUpCreatesContainerWithIdenticalPathMount(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 1 {
		t.Fatalf("expected 1 run, got %d", len(m.Run_))
	}
	spec := m.Run_[0]
	if spec.Name != "coop-proj" {
		t.Errorf("bad spec: %+v", spec)
	}
	if spec.SSH {
		t.Errorf("SECURITY: ssh agent forwarding must default OFF")
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != spec.Mounts[0].Target {
		t.Errorf("identical-path mount violated: %+v", spec.Mounts)
	}
	// three per-project state volumes, targets under guest home
	if len(spec.Volumes) != 3 {
		t.Fatalf("expected 3 state volumes, got %+v", spec.Volumes)
	}
	for _, v := range spec.Volumes {
		if !strings.HasPrefix(v.Name, "coop-proj--") {
			t.Errorf("volume not project-scoped: %+v", v)
		}
		if !strings.HasPrefix(v.Target, "/Users/u/") {
			t.Errorf("volume target not under guest home: %+v", v)
		}
	}
	gotNames := []string{spec.Volumes[0].Name, spec.Volumes[1].Name, spec.Volumes[2].Name}
	wantNames := []string{"coop-proj--claude", "coop-proj--codex", "coop-proj--opencode"}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Errorf("volumes not sorted before Run: %v", gotNames)
	}
}

func TestUpHonorsGlobalSSHOptIn(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.SSH = true
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if !m.Run_[0].SSH {
		t.Errorf("explicit ssh opt-in not honored")
	}
}

// labelCurrent marks an existing mock container as matching the
// session's current spec fingerprint.
func labelCurrent(m *runtime.Mock, s *Session) {
	m.ContainerLabels = map[string]map[string]string{
		s.Name: {SpecLabel: s.SpecFingerprint()},
	}
}

func TestUpStartsExistingStoppedContainer(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	labelCurrent(m, s)

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 0 {
		t.Errorf("should not recreate existing container")
	}
	if !m.Running("coop-proj") {
		t.Errorf("container should have been started")
	}
}

func TestUpRecreatesWhenSSHRevoked(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	// container was created when ssh was on; config has since revoked it
	sshOn := *s
	sshOn.Cfg.SSH = true
	m.ContainerLabels = map[string]map[string]string{
		s.Name: {SpecLabel: sshOn.SpecFingerprint()},
	}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 || len(m.Run_) != 1 {
		t.Fatalf("ssh revocation must recreate: removed=%v runs=%d", m.Removed, len(m.Run_))
	}
	if m.Run_[0].SSH {
		t.Errorf("recreated container still has ssh")
	}
}

func TestUpRecreatesLegacyUnlabeledContainer(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	// no labels at all — pre-hardening container

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 || len(m.Run_) != 1 {
		t.Fatalf("legacy container must be recreated once: removed=%v", m.Removed)
	}
	if m.Run_[0].Labels[SpecLabel] != s.SpecFingerprint() {
		t.Errorf("recreated container not labeled")
	}
}

func TestUpAppliesSeeds(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)

	src := filepath.Join(t.TempDir(), "conf.json")
	if err := os.WriteFile(src, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Seeds = []config.Seed{{Src: src, Dest: "~/.config/x/conf.json", Policy: config.PolicyAlways}}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.config/x/conf.json"]; got != `{"x":1}` {
		t.Errorf("seed content = %q; calls:\n%s", got, m.ExecString())
	}
}

func TestEnterClampsCwdAndWrapsFlox(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	// project WITHOUT .flox: plain argv
	if err := s.Enter("/elsewhere", []string{"opencode"}); err != nil {
		t.Fatal(err)
	}
	call := m.Interactive[0]
	if call.Workdir != s.Project {
		t.Errorf("cwd outside project must clamp to root, got %q", call.Workdir)
	}
	if strings.Join(call.Argv, " ") != "opencode" {
		t.Errorf("unexpected argv: %v", call.Argv)
	}

	// project WITH .flox: wrapped in flox activate
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".flox"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Project = proj
	if err := s.Enter(proj, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	call = m.Interactive[1]
	joined := strings.Join(call.Argv, " ")
	if !strings.Contains(joined, "flox activate") || !strings.Contains(joined, "claude") {
		t.Errorf("flox wrap missing: %v", call.Argv)
	}
}

func TestEnterRejectsSiblingPrefixPath(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	if err := s.Enter(s.Project+"-other", []string{"opencode"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Interactive[0].Workdir; got != s.Project {
		t.Fatalf("sibling prefix escaped project boundary: %q", got)
	}
}

func TestEnsureImageRequiresLocalBuildWhenMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	delete(m.Images, EffectiveImageName(s.Cfg.Image))

	if err := s.EnsureImage(); err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("missing local image did not require rebuild: %v", err)
	}
}

func TestEnsureImageCustomConfigRequiresLocalBuild(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}

	err := s.EnsureImage()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Errorf("custom config must demand local build, got %v", err)
	}
}

func TestExtraPackagesNeverReuseStaleStockImage(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m) // the base local image is present
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}

	err := s.EnsureImage()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Errorf("stale stock image silently reused: %v", err)
	}
}

func TestUpRecreatesContainerOnImageChange(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	// container exists, labeled with the stock-config fingerprint
	m.Existing["coop-proj"] = true
	labelCurrent(m, s)
	// config now wants extra packages; derived image already built
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}
	derived := EffectiveImageName(s.Cfg.Image)
	m.Images[derived] = true

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 {
		t.Fatalf("stale container not recreated")
	}
	if len(m.Run_) != 1 || m.Run_[0].Image != derived {
		t.Errorf("recreated with wrong image: %+v", m.Run_)
	}
	if len(m.RemovedVol) != 0 {
		t.Errorf("recreation must preserve state volumes: %v", m.RemovedVol)
	}
}

func TestEffectiveImageNameRegistryPort(t *testing.T) {
	img := config.Image{Name: "localhost:5000/team/coop:latest", ExtraPackages: []string{"x"}}
	got := EffectiveImageName(img)
	if !strings.HasPrefix(got, "localhost:5000/team/coop:local-") {
		t.Errorf("registry port corrupted: %q", got)
	}
	digest := config.Image{Name: "repo/coop@sha256:deadbeef", ExtraPackages: []string{"x"}}
	if got := EffectiveImageName(digest); !strings.HasPrefix(got, "repo/coop:local-") {
		t.Errorf("digest ref corrupted: %q", got)
	}
}

func TestEffectiveImageNameDerivation(t *testing.T) {
	stock := config.Image{Name: "coop:latest"}
	if got := EffectiveImageName(stock); !strings.HasPrefix(got, "coop:local-") {
		t.Errorf("default embedded image tag not derived: %s", got)
	}
	a := config.Image{Name: "coop:latest", ExtraPackages: []string{"x", "y"}}
	b := config.Image{Name: "coop:latest", ExtraPackages: []string{"y", "x"}}
	if EffectiveImageName(a) != EffectiveImageName(b) {
		t.Errorf("package order should not change tag")
	}
	if EffectiveImageName(a) == EffectiveImageName(stock) || !strings.HasPrefix(EffectiveImageName(a), "coop:local-") {
		t.Errorf("derived tag wrong: %s", EffectiveImageName(a))
	}
	// base ref changes must change the derived tag even with identical packages
	v1 := config.Image{Name: "coop:v1", ExtraPackages: []string{"x"}}
	v2 := config.Image{Name: "coop:v2", ExtraPackages: []string{"x"}}
	if EffectiveImageName(v1) == EffectiveImageName(v2) {
		t.Errorf("base ref not part of derivation")
	}
}

func TestUpRefusesTeardownWhenReplacementImageMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing["coop-proj"] = true
	labelCurrent(m, s)
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"} // derived image NOT built

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("expected actionable error, got %v", err)
	}
	if len(m.Removed) != 0 {
		t.Errorf("working container torn down before replacement validated")
	}
}

func TestUpSurfacesRuntimeErrors(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.StateErr = fmt.Errorf("apiserver unreachable")

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "apiserver unreachable") {
		t.Fatalf("runtime failure must surface, not read as absent: %v", err)
	}
	if len(m.Run_) != 0 {
		t.Errorf("created a container on inconclusive state")
	}
}

func TestDestroyRemovesVolumesByPrefixNotConfig(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	// this coop's volumes, incl. one from an OLDER agent config (gemini)
	m.Volumes["coop-proj--opencode"] = true
	m.Volumes["coop-proj--gemini"] = true
	// other coops, including a name whose slug begins
	// with this coop's full name (the "--" separator must fence it)
	m.Volumes["coop-other--claude"] = true
	m.Volumes["coop-proj-worker-abcdef1234567890--opencode"] = true

	s := testSession(t, m)
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 {
		t.Errorf("container not removed")
	}
	if len(m.RemovedVol) != 2 {
		t.Errorf("prefix destroy wrong: %v", m.RemovedVol)
	}
	if !m.Volumes["coop-other--claude"] || !m.Volumes["coop-proj-worker-abcdef1234567890--opencode"] {
		t.Errorf("destroyed another coop's volume: %v", m.RemovedVol)
	}
}

func TestUpUsesConfiguredAgentSet(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Agents = map[string]config.Agent{
		"aider": {State: "~/.aider"},
	}
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	spec := m.Run_[0]
	if len(spec.Volumes) != 1 || spec.Volumes[0].Name != "coop-proj--aider" ||
		spec.Volumes[0].Target != "/Users/u/.aider" {
		t.Errorf("agent config not honored: %+v", spec.Volumes)
	}
}
