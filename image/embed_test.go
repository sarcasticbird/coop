package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedBuildContext(t *testing.T) {
	if len(Fingerprint()) != 12 {
		t.Fatalf("unexpected image fingerprint %q", Fingerprint())
	}
	dir, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"Containerfile", "zshrc"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("materialized %s: %v", name, err)
		}
	}
	containerfile, err := files.ReadFile("Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(containerfile), "ARG NIXPKGS_REF="+NixpkgsRef) {
		t.Fatal("Go and Containerfile nixpkgs pins differ")
	}
}
