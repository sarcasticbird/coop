package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

func found(string) (string, error)    { return "/bin/x", nil }
func notFound(string) (string, error) { return "", errors.New("not found") }

func get(checks []Check, name string) *Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func TestMissingContainerCLIShortCircuits(t *testing.T) {
	checks := Run(runtime.NewMock(), config.Default(), "/h", notFound)
	if len(checks) != 1 || checks[0].Status != Fail {
		t.Errorf("expected single fail, got %+v", checks)
	}
}

func TestImageMissingRequiresLocalBuild(t *testing.T) {
	m := runtime.NewMock()
	checks := Run(m, config.Default(), "/h", found)
	c := get(checks, "sandbox image")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, "rebuild") {
		t.Errorf("missing image should require a local build: %+v", c)
	}
}

func TestNoSeedsIsOptional(t *testing.T) {
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(config.Default()): true}
	c := get(Run(m, config.Default(), "/h", found), "seeds")
	if c == nil || c.Status != OK {
		t.Fatalf("empty optional seed config should be healthy: %+v", c)
	}
}

func TestSensitiveSeedPathsWarn(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{
		{Src: "~/.git-credentials"},
		{Src: "~/.aws/credentials"},
		{Src: "~/.aws", Policy: config.PolicyOverlay},
		{Src: "~/.docker", Policy: config.PolicyOverlay},
		{Src: "~/.netrc"},
		{Src: "~/.kube", Policy: config.PolicyOverlay},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found), "credential seeds")
	if c == nil || c.Status != Warn || !strings.Contains(c.Detail, "6 sensitive") {
		t.Fatalf("warning = %+v", c)
	}
}

func TestOrdinaryConfigAndSkillSeedsDoNotWarn(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{
		{Src: "~/.claude/skills", Policy: config.PolicyOverlay},
		{Src: "~/.config/opencode/opencode.jsonc"},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found), "credential seeds")
	if c == nil || c.Status != OK {
		t.Fatalf("ordinary seeds flagged as credentials: %+v", c)
	}
}

func TestCustomImageMissingIsFail(t *testing.T) {
	m := runtime.NewMock()
	cfg := config.Default()
	cfg.Tools.Packages = []string{"gemini-cli"}
	c := get(Run(m, cfg, "/h", found), "sandbox image")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, "rebuild") {
		t.Errorf("custom missing image should fail toward rebuild: %+v", c)
	}
}

func TestLegacyArtifactsDetected(t *testing.T) {
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(config.Default()): true}
	m.Infos = []runtime.ContainerInfo{
		{Name: "coop-legacyapp"}, // pre-hash container
		{Name: project.Name("/work/app"), Mounts: []runtime.MountInfo{{
			Source: "/work/app", Destination: "/work/app", Bind: true,
		}}},
	}
	m.Volumes["coop-legacyapp-opencode"] = true // old separator
	m.Volumes["coop-app-0123456789abcdef--opencode"] = true

	c := get(Run(m, config.Default(), "/h", found), "legacy artifacts")
	if c == nil || c.Status != Warn {
		t.Fatalf("expected legacy warn: %+v", c)
	}
	if !strings.Contains(c.Detail, "coop-legacyapp") ||
		strings.Contains(c.Detail, project.Name("/work/app")) {
		t.Errorf("wrong legacy set: %s", c.Detail)
	}
}
