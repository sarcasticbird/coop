package image

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
)

//go:embed machine/*
var machineFiles embed.FS

func MachineImageName(cfg config.Config, lock []byte) string {
	data, _ := json.Marshal(struct {
		Packages []string
		Releases []config.ResolvedReleaseTool
	}{CanonicalPackages(cfg.Tools.Packages), cfg.Tools.ResolvedReleases})
	sum := sha256.Sum256(data)
	return "coop-machine:local-" + MachineFingerprintWithCoreLock(lock) + "-" + hex.EncodeToString(sum[:])[:12]
}

func MachineFingerprintWithCoreLock(lock []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(FingerprintWithCoreLock(lock)))
	entries, err := machineFiles.ReadDir("machine")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		data, err := machineFiles.ReadFile("machine/" + e.Name())
		if err != nil {
			panic(err)
		}
		_, _ = h.Write([]byte(e.Name() + "\x00"))
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
func MaterializeMachineWithCoreLock(packages []string, releases []config.ResolvedReleaseTool, lock []byte) (dir string, retErr error) {
	dir, err := MaterializeWithCoreLock(packages, releases, lock)
	if err != nil {
		return "", err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	base, err := os.ReadFile(filepath.Join(dir, "Containerfile"))
	if err != nil {
		return "", err
	}
	workbench := strings.Replace(string(base), "FROM "+FloxBaseImage, "FROM "+FloxBaseImage+" AS workbench", 1)
	workbench += "\nRUN ln -s \"$(readlink -f \"$(command -v flox)\")\" /opt/coop-flox\n"
	entries, err := machineFiles.ReadDir("machine")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		data, err := machineFiles.ReadFile("machine/" + e.Name())
		if err != nil {
			return "", err
		}
		if e.Name() == "Containerfile" {
			data = append([]byte(workbench+"\n"), data...)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0644); err != nil {
			return "", fmt.Errorf("materialize machine %s: %w", e.Name(), err)
		}
	}
	return dir, nil
}
