package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestWorkUIBuildProvenance(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "coop")
	manifest := filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(binary, []byte("candidate binary"), 0700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	profile := filepath.Join(dir, "profile")
	if got := candidateProvenance("", false, binary, manifest, profile); !strings.Contains(got, "unknown") {
		t.Fatalf("unknown build claimed known: %s", got)
	}
	sum := sha256.Sum256([]byte("candidate binary"))
	pins := candidateManifest{SourceRevision: revision, ArtifactSHA256: hex.EncodeToString(sum[:]), Profile: profile}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(pins)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if got := candidateProvenance(revision, false, binary, manifest, profile); !strings.Contains(got, "verified") || !strings.Contains(got, revision) {
		t.Fatalf("valid candidate unverified: %s", got)
	}
	for _, which := range []string{"revision", "sha", "profile", "dirty"} {
		t.Run(which, func(t *testing.T) {
			r, p, dirty := revision, profile, false
			switch which {
			case "revision":
				r = strings.Repeat("b", 40)
			case "sha":
				pins.ArtifactSHA256 = strings.Repeat("b", 64)
				write()
			case "profile":
				p = filepath.Join(dir, "another")
			case "dirty":
				dirty = true
			}
			if got := candidateProvenance(r, dirty, binary, manifest, p); !strings.Contains(got, "mismatch") {
				t.Fatalf("false verified provenance: %s", got)
			}
			pins.ArtifactSHA256 = hex.EncodeToString(sum[:])
			write()
		})
	}
}

func TestInstalledProfileSelectsOneExactBuild(t *testing.T) {
	revision := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 64)
	for _, test := range []struct {
		name, revision, modified, tree string
		info                           *debug.BuildInfo
		want                           string
		dirty                          bool
	}{
		{name: "clean explicit worktree stamp", revision: revision, modified: "false", tree: tree, want: revision},
		{name: "dirty explicit worktree stamp", revision: revision, modified: "true", tree: tree, want: revision, dirty: true},
		{name: "unknown unstamped"},
		{name: "missing modified stamp", revision: revision, tree: tree},
		{name: "invalid source", revision: "dev", modified: "false", tree: tree},
		{name: "VCS clean", info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}}, want: revision},
		{name: "VCS modified unknown", info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}}},
		{name: "contradictory metadata", revision: revision, modified: "false", tree: tree, info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: strings.Repeat("c", 40)}, {Key: "vcs.modified", Value: "false"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, dirty := buildSource(test.info, test.revision, test.modified, test.tree)
			if r != test.want || dirty != test.dirty {
				t.Fatalf("source %q dirty %v", r, dirty)
			}
		})
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "coop")
	profile := filepath.Join(dir, "selected")
	manifest := filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(binary, []byte("exact artifact"), 0700); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte("exact artifact"))
	pins := candidateManifest{SourceRevision: revision, SourceTreeSHA256: tree, ArtifactSHA256: hex.EncodeToString(sha[:]), Profile: profile}
	raw, err := json.Marshal(pins)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got := candidateProvenance(revision, false, binary, manifest, profile, tree); !strings.Contains(got, "verified;") {
		t.Fatal(got)
	}
	for _, which := range []string{"profile", "artifact", "tree", "dirty", "unknown"} {
		t.Run(which, func(t *testing.T) {
			r, p, h, dirty := revision, profile, tree, false
			switch which {
			case "profile":
				p = filepath.Join(dir, "competing-profile")
			case "artifact":
				if err = os.WriteFile(binary, []byte("other artifact"), 0700); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.WriteFile(binary, []byte("exact artifact"), 0700); err != nil {
						t.Error(err)
					}
				}()
			case "tree":
				h = strings.Repeat("c", 64)
			case "dirty":
				dirty = true
			case "unknown":
				r = ""
			}
			if got := candidateProvenance(r, dirty, binary, manifest, p, h); strings.Contains(got, "verified;") {
				t.Fatal("false installed acceptance", got)
			}
		})
	}
}
