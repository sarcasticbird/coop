package image

import (
	"os"
	"path/filepath"
	"testing"
)

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
