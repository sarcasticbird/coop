package image_test

import (
	"encoding/json"
	"testing"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/worker"
)

func TestEmbeddedOpenCodeMatchesWorkerRuntimeContract(t *testing.T) {
	var lock struct {
		Manifest struct {
			Install map[string]struct {
				Version string `json:"version"`
			} `json:"install"`
		} `json:"manifest"`
		Packages []struct {
			InstallID string `json:"install_id"`
			System    string `json:"system"`
			Version   string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(image.EmbeddedCoreLock(), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Manifest.Install["opencode"].Version != worker.SupportedOpenCodeVersion {
		t.Fatal("image manifest must pin the worker-supported native server")
	}
	found := false
	for _, pkg := range lock.Packages {
		if pkg.InstallID == "opencode" && pkg.System == "aarch64-linux" {
			found = true
			if pkg.Version != worker.SupportedOpenCodeVersion {
				t.Fatalf("image bundles OpenCode %s, worker requires %s", pkg.Version, worker.SupportedOpenCodeVersion)
			}
		}
	}
	if !found {
		t.Fatal("Linux OpenCode lock is missing")
	}
}
