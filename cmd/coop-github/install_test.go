package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerStagesVerifiedHelperWithoutRestart(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	cli := filepath.Join(root, "container")
	record := filepath.Join(root, "record")
	payload := make([]byte, 64)
	copy(payload, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	payload[18] = 183
	if err := os.WriteFile(helper, payload, 0700); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
if [ "$1" = machine ] && [ "$2" = inspect ]; then
 printf '[{"id":"coop-agent-example","status":"running","homeMount":"none","containerId":"child-example"}]'
 exit 0
fi
if [ "$1" = exec ]; then
 cat > "$TEST_RECORD.payload"
 printf '%s\n' "$@" > "$TEST_RECORD.args"
 exit 0
fi
exit 91
`
	if err := os.WriteFile(cli, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../scripts/install-github-auth.sh")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script, "example", helper, "123")
	command.Env = append(os.Environ(), "COOP_CONTAINER_BIN="+cli, "TEST_RECORD="+record)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install failed: %v %s", err, out)
	}
	got, err := os.ReadFile(record + ".payload")
	if err != nil || string(got) != string(payload) {
		t.Fatal("helper bytes were not staged over stdin")
	}
	args, err := os.ReadFile(record + ".args")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if !strings.Contains(string(args), hex.EncodeToString(digest[:])) || !strings.Contains(string(args), "child-example") {
		t.Fatal("inspection identity or checksum missing")
	}
	for _, bad := range []string{"../example", "example;touch bad"} {
		if exec.Command("bash", script, bad, helper, "123").Run() == nil {
			t.Fatal("unsafe agent identity accepted")
		}
	}
}
