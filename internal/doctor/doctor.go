// Package doctor diagnoses the host environment: the top support
// questions answered before they're asked.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
func Run(rt runtime.Runtime, cfg config.Config, hostHome string, lookPath func(string) (string, error)) []Check {
	seeds := cfg.Seeds
	var checks []Check
	add := func(s Status, name, detail string) {
		checks = append(checks, Check{s, name, detail})
	}

	// container CLI
	if _, err := lookPath("container"); err != nil {
		add(Fail, "container CLI", "not on PATH — brew install container")
		return checks // everything else depends on it
	}
	add(OK, "container CLI", "found")

	// apiserver
	if _, err := rt.List(); err != nil {
		add(Fail, "container apiserver", "not responding — run: container system start (or brew services start container)")
		return checks
	}
	add(OK, "container apiserver", "running")

	// image — checked via the same derived-tag logic sessions use
	imgName := session.EffectiveImageName(cfg)
	exists, imgErr := rt.ImageExists(imgName)
	switch {
	case imgErr != nil:
		add(Fail, "sandbox image", "cannot inspect: "+imgErr.Error())
	case exists:
		add(OK, "sandbox image", imgName+" present")
	default:
		add(Fail, "sandbox image", imgName+" missing — run: coop rebuild")
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

	sensitive := 0
	for _, seed := range seeds {
		if sensitiveSeedPath(config.ExpandHome(seed.Src, hostHome)) {
			sensitive++
		}
	}
	if sensitive > 0 {
		add(Warn, "credential seeds", fmt.Sprintf(
			"%d sensitive seed paths detected — migrate session credentials to trusted [credentials] grants", sensitive))
	} else {
		add(OK, "credential seeds", "none detected")
	}

	// legacy artifacts from pre-hashed naming — enumeration failures
	// must not read as a clean bill of health
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
	switch filepath.Base(clean) {
	case ".git-credentials", ".netrc", ".kube", ".aws", ".docker":
		return true
	}
	for _, suffix := range []string{
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
