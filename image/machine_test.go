package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineLauncherRunsWithoutFlox(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "coop-user-env")
	if err := os.WriteFile(helper, []byte("#!/bin/bash\n[[ $1 == -- ]] || exit 64\nshift\nexec \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := machineFiles.ReadFile("machine/coop-env")
	if err != nil {
		t.Fatal(err)
	}
	// No Flox executable or Nix store exists in this isolated launcher fixture.
	script := strings.ReplaceAll(string(raw), "/usr/local/bin/coop-user-env", helper)
	launcher := filepath.Join(dir, "coop-env")
	if err = os.WriteFile(launcher, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", launcher, "/bin/bash", "-c", `printf '%s' "$1"; exit 17`, "--", "argument with spaces")
	cmd.Env = append(os.Environ(), "COOP_RUNTIME_DIR="+filepath.Join(dir, "runtime"))
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 17 || string(out) != "argument with spaces" {
		t.Fatalf("launcher lost arguments or exit status: %q, %v", out, err)
	}
}

func TestMachineContextContainsBootableGuestWithoutChangingInteractive(t *testing.T) {
	before := Fingerprint()
	dir, err := MaterializeMachineWithCoreLock(nil, nil, EmbeddedCoreLock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"Containerfile", "coop-guest", "coop-env", "coop-agent-run", "coop-agent.service", "configured-packages.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if Fingerprint() != before {
		t.Fatal("machine build changed interactive image identity")
	}
	if MachineFingerprintWithCoreLock(EmbeddedCoreLock()) == "" {
		t.Fatal("missing machine image identity")
	}
}
