package masterclient

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateMasterTokenReference(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	cfg := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(token, []byte("private-agent-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(extra string) {
		t.Helper()
		if err := os.WriteFile(cfg, fmt.Appendf(nil, "url = %q\nmaster_id = %q\ntoken_file = %q\n%s", "https://master.example.invalid", "stable-master", token, extra), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	client, err := LoadConfig(cfg)
	if err != nil || client.cfg.Token != "private-agent-test-token" {
		t.Fatal("private token reference failed", err)
	}
	write("token_env = \"COOP_TEST_TOKEN\"\n")
	if _, err = LoadConfig(cfg); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	write("")
	if err = os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(cfg); err == nil {
		t.Fatal("public credential file accepted")
	}
}
