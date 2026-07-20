// Package image embeds the sandbox image definition so `coop rebuild`
// works from the installed binary alone.
package image

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed Containerfile zshrc
var files embed.FS

// NixpkgsRef is the immutable package source shared by the embedded image and
// local extra-package builds.
const NixpkgsRef = "github:flox/nixpkgs/d407951447dcd00442e97087bf374aad70c04cea"

// Fingerprint identifies the embedded build inputs. Local image tags include
// it so a changed Containerfile or shell setup cannot silently reuse an older
// image under the same configuration.
func Fingerprint() string {
	h := sha256.New()
	for _, name := range []string{"Containerfile", "zshrc"} {
		data, err := files.ReadFile(name)
		if err != nil {
			panic(fmt.Sprintf("embedded %s: %v", name, err))
		}
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Materialize writes the build context to a temp directory and returns
// its path. Caller removes it.
func Materialize() (string, error) {
	dir, err := os.MkdirTemp("", "coop-image-")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"Containerfile", "zshrc"} {
		data, err := files.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("embedded %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
