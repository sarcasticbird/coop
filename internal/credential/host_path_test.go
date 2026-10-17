package credential

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedCredentialsIgnoreInheritedHelperPATH(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	helper := filepath.Join(project, "coop-untrusted-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf untrusted\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", project+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := NewManager("")
	m.NonInteractive = true
	if _, err := m.Run(context.Background(), home, []string{"coop-untrusted-helper"}); err == nil {
		t.Fatal("named credentials executed inherited project helper")
	}
	path, err := trustedSearchPath(home, "", os.Getenv("PATH"))
	if err != nil || strings.Contains(path, project) {
		t.Fatal("named credential child PATH retained project directory", err)
	}
	// A private host configuration may deliberately select a custom helper.
	if out, err := m.Run(context.Background(), home, []string{helper}); err != nil || string(out) != "untrusted" {
		t.Fatal("explicit configured helper stopped working", err)
	}
}
