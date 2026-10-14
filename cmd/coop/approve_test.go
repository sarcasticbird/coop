package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostApprovalLoadsPrivateStowConfig(t *testing.T) {
	raw, err := os.ReadFile("../../examples/worker.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	actual := filepath.Join(root, "dotfiles", "worker.json")
	if err = os.MkdirAll(filepath.Dir(actual), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(actual, raw, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "coop")
	if err = os.Symlink(filepath.Dir(actual), link); err != nil {
		t.Fatal(err)
	}
	if _, err = loadHostApprovalConfig(filepath.Join(link, "worker.json")); err != nil {
		t.Fatalf("trusted stow source rejected: %v", err)
	}
	if err = os.Chmod(actual, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadHostApprovalConfig(filepath.Join(link, "worker.json")); err == nil {
		t.Fatal("insecure source permissions accepted")
	}
}
