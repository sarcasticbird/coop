package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// A candidate manifest lives with the installed private profile. Installation
// writes these pins after building; entry never installs or switches binaries.
var sourceRevision, sourceModified, sourceTreeSHA256 string

var sourceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sourceTreePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type candidateManifest struct {
	SourceTreeSHA256 string `json:"source_tree_sha256,omitempty"`
	SourceModified   bool   `json:"source_modified,omitempty"`
	SourceRevision   string `json:"source_revision"`
	ArtifactSHA256   string `json:"artifact_sha256"`
	Profile          string `json:"profile"`
}

func candidateProvenance(revision string, dirty bool, binary, manifestPath, profile string, tree ...string) string {
	source := revision
	if source == "" {
		source = "unknown"
	}
	if dirty {
		source += "+dirty"
	}
	prefix := "source " + source + "; candidate "
	file, err := os.Open(manifestPath)
	if err != nil {
		return prefix + "unknown (no readable manifest)"
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return prefix + "unknown (manifest unreadable)"
	}
	var manifest candidateManifest
	if len(raw) > 8192 || json.Unmarshal(raw, &manifest) != nil {
		return prefix + "mismatch (invalid manifest)"
	}
	if !sourceRevisionPattern.MatchString(revision) {
		return prefix + "unknown (source revision unavailable)"
	}
	if dirty || manifest.SourceModified {
		return prefix + "mismatch (dirty preview; not accepted for installation)"
	}
	if len(tree) > 0 && tree[0] != "" && manifest.SourceTreeSHA256 != tree[0] {
		return prefix + "mismatch (source tree)"
	}
	if manifest.SourceRevision != revision || manifest.Profile != profile || !filepath.IsAbs(profile) {
		return prefix + "mismatch (source or profile)"
	}
	digest, err := hex.DecodeString(manifest.ArtifactSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(manifest.ArtifactSHA256) != manifest.ArtifactSHA256 {
		return prefix + "mismatch (invalid artifact SHA)"
	}
	f, err := os.Open(binary)
	if err != nil {
		return prefix + "unknown (artifact unreadable)"
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return prefix + "unknown (artifact unreadable)"
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.ArtifactSHA256 {
		return prefix + "mismatch (artifact SHA)"
	}
	return prefix + "verified; SHA256 " + manifest.ArtifactSHA256 + "; profile " + profile
}

// Go may omit VCS metadata for a .git pointer file in a wt checkout. The
// candidate builder supplies explicit stamps without changing Git metadata.
func buildSource(info *debug.BuildInfo, revision, modified, tree string) (string, bool) {
	vcsRevision, vcsModified := "", ""
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRevision = setting.Value
			case "vcs.modified":
				vcsModified = setting.Value
			}
		}
	}
	stamped := sourceRevisionPattern.MatchString(revision) && (modified == "true" || modified == "false") && sourceTreePattern.MatchString(tree)
	if vcsRevision != "" {
		if !sourceRevisionPattern.MatchString(vcsRevision) || (vcsModified != "true" && vcsModified != "false") {
			return "", false
		}
		if stamped && (revision != vcsRevision || modified != vcsModified) {
			return "", false
		}
		return vcsRevision, vcsModified == "true"
	}
	if stamped {
		return revision, modified == "true"
	}
	return "", false
}
func currentBuildProvenance() string {
	info, _ := debug.ReadBuildInfo()
	revision, dirty := buildSource(info, sourceRevision, sourceModified, sourceTreeSHA256)
	binary, err := os.Executable()
	if err != nil {
		return "source unknown; candidate unknown (artifact unavailable)"
	}
	profile := filepath.Dir(masterclient.DefaultConfigPath("master-client.toml"))
	return candidateProvenance(revision, dirty, binary, masterclient.DefaultConfigPath("candidate.json"), profile, sourceTreeSHA256)
}

func workDestination() string {
	cfg, err := config.LoadWorkTools()
	if err != nil {
		return "unconfigured (private work-tools.toml required)"
	}
	registry, err := hostrouter.DefaultRegistry()
	if err != nil {
		return "unavailable (host registry)"
	}
	host, err := registry.Resolve(cfg.HostName)
	if err != nil {
		return "unavailable (registered host missing)"
	}
	destination := host.Name
	if host.Address != "" {
		destination += " (" + host.Address + ")"
	}
	return fmt.Sprintf("%s; master %s; Kata project %s", destination, cfg.MasterID, cfg.ProjectUID)
}
