package credential

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
)

// These tests record observed audit behavior, not the desired fixed behavior.
func TestAuditNamedAgentExecutesProjectPATHHelper(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	helper := filepath.Join(project, "audit-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf audit-project-code-executed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("PATH", project+":/usr/bin:/bin")
	selected := []Selected{{Name: "audit", Spec: config.Credential{Source: config.CredentialSource{Type: "command", Argv: []string{"audit-helper"}}}}}
	named := NewManager("")
	named.NonInteractive = true
	items, err := named.AcquireAll(context.Background(), home, selected)
	if err != nil || len(items) != 1 || string(items[0].payload) != "audit-project-code-executed" {
		t.Fatalf("named acquisition did not execute project helper: %v", err)
	}
	interactive := NewManager(project)
	interactive.NonInteractive = true
	if _, err := interactive.AcquireAll(context.Background(), home, selected); err == nil {
		t.Fatal("interactive acquisition unexpectedly allowed the same helper")
	}
}
