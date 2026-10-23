package masterclient

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPrivateClientSupportsTrustedStowPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	target := filepath.Join(dir, "stow", "coop")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config", "coop")); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(target, "master-client.toml")
	token := filepath.Join(target, "token")
	if err := os.WriteFile(token, []byte("private-agent-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\n", "https://master.example.invalid", "master-a", filepath.Join(dir, "config", "coop", "token"))
	if err := os.WriteFile(cfg, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(dir, "config", "coop", "master-client.toml")); err != nil {
		t.Fatalf("trusted Stow path rejected: %v", err)
	}
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(dir, "config", "coop", "master-client.toml")); err == nil {
		t.Fatal("public token accepted")
	}
	if err := os.Chmod(token, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(dir, "config", "coop", "master-client.toml")); err == nil {
		t.Fatal("public config accepted")
	}
}
