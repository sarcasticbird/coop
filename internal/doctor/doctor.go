// Package doctor diagnoses the host environment: the top support
// questions answered before they're asked.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

type Status int

const (
	OK Status = iota
	Warn
	Fail
)

type Check struct {
	Status Status
	Name   string
	Detail string
}

// currentName matches the post-hardening naming scheme (slug + 16-hex
// path hash). Anything else under coop- is a legacy artifact.
var currentName = regexp.MustCompile(`^coop-.*-[0-9a-f]{16}$`)

// Run executes all checks. lookPath is injectable for tests.
func Run(rt runtime.Runtime, cfg config.Config, hostHome string, lookPath func(string) (string, error), coreLock []byte) []Check {
	seeds := cfg.Seeds
	var checks []Check
	add := func(s Status, name, detail string) {
		checks = append(checks, Check{s, name, detail})
	}

	// Runtime checks are conditional, but configuration-only security checks
	// below must still run when the container CLI or apiserver is unavailable.
	runtimeReady := false

	// container CLI
	if _, err := lookPath("container"); err != nil {
		add(Fail, "container CLI", "not on PATH — brew install container")
	} else {
		add(OK, "container CLI", "found")

		// apiserver
		if _, err := rt.List(); err != nil {
			add(Fail, "container apiserver", "not responding — run: container system start (or brew services start container)")
		} else {
			add(OK, "container apiserver", "running")
			runtimeReady = true

			// image — checked via the same derived-tag logic sessions use
			imgName := session.EffectiveImageNameWithCoreLock(cfg, coreLock)
			exists, imgErr := rt.ImageExists(imgName)
			switch {
			case imgErr != nil:
				add(Fail, "sandbox image", "cannot inspect: "+imgErr.Error())
			case exists:
				add(OK, "sandbox image", imgName+" present")
			default:
				add(Fail, "sandbox image", imgName+" missing — run: coop rebuild")
			}
		}
	}

	// flox runs IN-GUEST (baked into the sandbox image) — a host PATH
	// check would be misleading either direction, so there isn't one.

	// seed sources
	missing := 0
	for _, s := range seeds {
		if _, err := statPath(config.ExpandHome(s.Src, hostHome)); err != nil {
			missing++
		}
	}
	switch {
	case len(seeds) == 0:
		add(OK, "seeds", "none configured (optional)")
	case missing > 0:
		add(Warn, "seeds", fmt.Sprintf("%d/%d sources missing on this host (skipped at entry)", missing, len(seeds)))
	default:
		add(OK, "seeds", fmt.Sprintf("%d rules, all sources present", len(seeds)))
	}

	securitySeeds := append([]config.Seed(nil), seeds...)
	for _, scope := range cfg.Projects {
		securitySeeds = append(securitySeeds, scope.Seeds...)
	}
	sensitiveSet := make(map[string]struct{})
	for _, seed := range securitySeeds {
		dest := seed.Dest
		if dest == "" {
			dest = seed.Src
		}
		for _, candidate := range []string{seed.Src, dest} {
			if sensitiveSeedPath(config.ExpandHome(candidate, hostHome)) {
				sensitiveSet[candidate] = struct{}{}
			}
		}
	}
	sensitivePaths := make([]string, 0, len(sensitiveSet))
	for candidate := range sensitiveSet {
		sensitivePaths = append(sensitivePaths, candidate)
	}
	sort.Strings(sensitivePaths)
	if len(sensitivePaths) > 0 {
		quoted := make([]string, len(sensitivePaths))
		for i, candidate := range sensitivePaths {
			quoted[i] = strconv.Quote(candidate)
		}
		add(Fail, "credential seeds", fmt.Sprintf(
			"%d sensitive seed paths detected: %s — remove them and use credential grants or provider-owned agent state",
			len(sensitivePaths), strings.Join(quoted, ", ")))
	} else {
		add(OK, "credential seeds", "none detected")
	}

	plaintextGitSources := 0
	for _, credential := range cfg.Credentials {
		if credential.Source.Type != "file" {
			continue
		}
		for _, exposure := range config.Exposures(credential) {
			if exposure.Type == "git-credential-store" {
				plaintextGitSources++
				break
			}
		}
	}
	if plaintextGitSources > 0 {
		noun := "sources"
		if plaintextGitSources == 1 {
			noun = "source"
		}
		add(Warn, "credential sources", fmt.Sprintf(
			"%d plaintext Git credential %s detected — migrate to source.type = \"git-credential\" and a host credential helper", plaintextGitSources, noun))
	} else {
		add(OK, "credential sources", "no plaintext Git credential stores configured")
	}

	// legacy artifacts from pre-hashed naming — enumeration failures
	// must not read as a clean bill of health
	if !runtimeReady {
		return checks
	}
	infos, infoErr := rt.Containers()
	vols, volErr := rt.ListVolumes()
	if infoErr != nil || volErr != nil {
		add(Warn, "legacy artifacts", "could not enumerate containers/volumes — status unknown")
		return checks
	}
	var legacy []string
	for _, i := range infos {
		projectPath := i.ProjectMount()
		if !currentName.MatchString(i.Name) || projectPath == "" || project.Name(projectPath) != i.Name {
			legacy = append(legacy, i.Name)
		}
	}
	for _, v := range vols {
		if strings.HasPrefix(v, "coop-") && !strings.Contains(v, "--") {
			legacy = append(legacy, v)
		}
	}
	if len(legacy) > 0 {
		add(Warn, "legacy artifacts", fmt.Sprintf("pre-v0 naming, no longer managed: %s — remove with container rm / container volume rm", strings.Join(legacy, ", ")))
	} else {
		add(OK, "legacy artifacts", "none")
	}

	return checks
}

// statPath is a variable for tests.
var statPath = os.Stat

func sensitiveSeedPath(seedPath string) bool {
	clean := filepath.ToSlash(filepath.Clean(seedPath))
	for _, suffix := range []string{"/.codex", "/.config/gh", "/.config/git"} {
		if strings.HasSuffix(clean, suffix) {
			return true
		}
	}
	if strings.HasSuffix(filepath.ToSlash(filepath.Dir(clean)), "/.config/git") &&
		strings.HasPrefix(filepath.Base(clean), "credentials-") {
		return true
	}
	switch filepath.Base(clean) {
	case ".git-credentials", ".netrc", ".kube", ".aws", ".docker":
		return true
	}
	for _, suffix := range []string{
		"/.codex/auth.json",
		"/.aws/credentials",
		"/.kube/config",
		"/.config/gh/hosts.yml",
		"/.docker/config.json",
	} {
		if strings.HasSuffix(clean, suffix) {
			return true
		}
	}
	return false
}
