package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/image"
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

func TestMissingContainerCLIDoesNotSkipCredentialAudit(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{{Src: "~/.git-credentials"}}
	checks := Run(runtime.NewMock(), cfg, "/h", notFound, image.EmbeddedCoreLock())
	if c := get(checks, "container CLI"); c == nil || c.Status != Fail {
		t.Fatalf("container CLI finding = %+v", c)
	}
	if c := get(checks, "credential seeds"); c == nil || c.Status != Fail {
		t.Fatalf("credential seed audit was skipped: %+v", checks)
	}
	if get(checks, "container apiserver") != nil || get(checks, "sandbox image") != nil || get(checks, "legacy artifacts") != nil {
		t.Fatalf("runtime-dependent checks ran without container CLI: %+v", checks)
	}
}

func TestUnavailableContainerAPIDoesNotSkipCredentialAudit(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{{Src: "~/.codex/auth.json"}}
	m := runtime.NewMock()
	m.ListErr = errors.New("apiserver unavailable")
	checks := Run(m, cfg, "/h", found, image.EmbeddedCoreLock())
	if c := get(checks, "container apiserver"); c == nil || c.Status != Fail {
		t.Fatalf("container apiserver finding = %+v", c)
	}
	if c := get(checks, "credential seeds"); c == nil || c.Status != Fail {
		t.Fatalf("credential seed audit was skipped: %+v", checks)
	}
	if get(checks, "sandbox image") != nil || get(checks, "legacy artifacts") != nil {
		t.Fatalf("runtime-dependent checks ran without apiserver: %+v", checks)
	}
}

func TestImageMissingRequiresLocalBuild(t *testing.T) {
	m := runtime.NewMock()
	checks := Run(m, config.Default(), "/h", found, image.EmbeddedCoreLock())
	c := get(checks, "sandbox image")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, "rebuild") {
		t.Errorf("missing image should require a local build: %+v", c)
	}
}

func TestDoctorUsesActiveCoreLockForImageCheck(t *testing.T) {
	m := runtime.NewMock()
	cfg := config.Default()
	lock := append(image.EmbeddedCoreLock(), '\n')
	desired := session.EffectiveImageNameWithCoreLock(cfg, lock)
	m.Images = map[string]bool{desired: true}
	check := get(Run(m, cfg, "/h", found, lock), "sandbox image")
	if check == nil || check.Status != OK || !strings.Contains(check.Detail, desired) {
		t.Fatalf("sandbox image check = %+v", check)
	}
}

func TestNoSeedsIsOptional(t *testing.T) {
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(config.Default()): true}
	c := get(Run(m, config.Default(), "/h", found, image.EmbeddedCoreLock()), "seeds")
	if c == nil || c.Status != OK {
		t.Fatalf("empty optional seed config should be healthy: %+v", c)
	}
}

func TestSensitiveSeedPathsFail(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{
		{Src: "~/.git-credentials"},
		{Src: "~/.codex/auth.json"},
		{Src: "~/.aws/credentials"},
		{Src: "~/.aws", Policy: config.PolicyOverlay},
		{Src: "~/.config/gh/hosts.yml"},
		{Src: "~/.docker", Policy: config.PolicyOverlay},
		{Src: "~/.docker/config.json"},
		{Src: "~/.netrc"},
		{Src: "~/.kube", Policy: config.PolicyOverlay},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, "9 sensitive") ||
		!strings.Contains(c.Detail, `"~/.git-credentials"`) ||
		!strings.Contains(c.Detail, `"~/.codex/auth.json"`) ||
		!strings.Contains(c.Detail, `"~/.config/gh/hosts.yml"`) {
		t.Fatalf("failure = %+v", c)
	}
}

func TestSensitiveSeedParentDirectoriesFail(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{
		{Src: "~/.codex"},
		{Src: "~/.config/gh"},
		{Src: "~/.config/git"},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, "3 sensitive") ||
		!strings.Contains(c.Detail, `"~/.codex"`) ||
		!strings.Contains(c.Detail, `"~/.config/gh"`) ||
		!strings.Contains(c.Detail, `"~/.config/git"`) {
		t.Fatalf("sensitive parent directory finding = %+v", c)
	}
}

func TestSensitiveSeedDestinationFails(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{{
		Src:  "~/.config/work/netrc",
		Dest: "~/.netrc",
	}}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, `"~/.netrc"`) {
		t.Fatalf("sensitive destination finding = %+v", c)
	}
	if strings.Contains(c.Detail, `"~/.config/work/netrc"`) {
		t.Fatalf("ordinary source was mislabeled sensitive: %+v", c)
	}
}

func TestCustomGitCredentialSeedDestinationFails(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{{
		Src:  "~/.config/coop/seeds/git-credentials/sarcasticbird",
		Dest: "~/.config/git/credentials-sarcasticbird",
	}}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, `"~/.config/git/credentials-sarcasticbird"`) {
		t.Fatalf("custom Git credential destination finding = %+v", c)
	}
	if strings.Contains(c.Detail, `"~/.config/coop/seeds/git-credentials/sarcasticbird"`) {
		t.Fatalf("ordinary source was mislabeled sensitive: %+v", c)
	}
}

func TestProjectScopedSensitiveSeedFailsWithoutSelectedProject(t *testing.T) {
	cfg := config.Default()
	cfg.Projects = []config.ProjectScope{{
		Match: "~/Projects/work",
		Seeds: []config.Seed{{Src: "~/.config/work/netrc", Dest: "~/.netrc"}},
	}}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != Fail || !strings.Contains(c.Detail, `"~/.netrc"`) {
		t.Fatalf("project-scoped sensitive seed finding = %+v", c)
	}
}

func TestPlaintextGitCredentialSourceReportsMigration(t *testing.T) {
	cfg := config.Default()
	cfg.Credentials = map[string]config.Credential{
		"legacy": {
			Source: config.CredentialSource{Type: "file", Path: "~/.config/git/credentials-acme"},
			Inject: config.CredentialInjection{Type: "git-credential-store"},
		},
		"keychain": {
			Source: config.CredentialSource{Type: "git-credential", URL: "https://github.com/acme"},
			Expose: []config.CredentialInjection{{Type: "git-credential-store"}},
		},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential sources")
	if c == nil || c.Status != Warn || !strings.Contains(c.Detail, "1 plaintext Git credential source") || !strings.Contains(c.Detail, "git-credential") {
		t.Fatalf("migration finding = %+v", c)
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
	c := get(Run(m, cfg, "/Users/u", found, image.EmbeddedCoreLock()), "credential seeds")
	if c == nil || c.Status != OK {
		t.Fatalf("ordinary seeds flagged as credentials: %+v", c)
	}
}

func TestCustomImageMissingIsFail(t *testing.T) {
	m := runtime.NewMock()
	cfg := config.Default()
	cfg.Tools.Packages = []string{"gemini-cli"}
	c := get(Run(m, cfg, "/h", found, image.EmbeddedCoreLock()), "sandbox image")
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

	c := get(Run(m, config.Default(), "/h", found, image.EmbeddedCoreLock()), "legacy artifacts")
	if c == nil || c.Status != Warn {
		t.Fatalf("expected legacy warn: %+v", c)
	}
	if !strings.Contains(c.Detail, "coop-legacyapp") ||
		strings.Contains(c.Detail, project.Name("/work/app")) {
		t.Errorf("wrong legacy set: %s", c.Detail)
	}
}
