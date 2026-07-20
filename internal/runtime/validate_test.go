package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMountFieldRejectsGrammarInjection(t *testing.T) {
	// the documented attack: a checkout path that smuggles a second
	// source/target directive into the comma-delimited --mount grammar
	evil := "/Users/u/Projects/x,source=/Users,target=/host"
	if err := ValidateMountField(evil); err == nil {
		t.Fatal("mount grammar injection accepted")
	}
	for _, bad := range []string{"a=b", "a,b", ""} {
		if err := ValidateMountField(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidateMountField("/Users/u/Projects/normal path/repo"); err != nil {
		t.Errorf("legitimate path rejected: %v", err)
	}
}

func TestRunRejectsInjectedMounts(t *testing.T) {
	a := NewApple()
	err := a.Run(RunSpec{
		Name: "x", Image: "i", CPUs: 1, Memory: "1G",
		Mounts: []Mount{{Source: "/p,source=/Users,target=/host", Target: "/p"}},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("injected mount reached the runtime: %v", err)
	}
}

func TestRunRejectsVolumeGrammarColon(t *testing.T) {
	a := NewApple()
	err := a.Run(RunSpec{
		Name: "x", Image: "i", CPUs: 1, Memory: "1G",
		Volumes: []Volume{{Name: "coop-x", Target: "/home/u/.agent:ro"}},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved by -v grammar") {
		t.Fatalf("volume grammar colon reached the runtime: %v", err)
	}
}

func TestVolumeNamesFromListFailsClosedOnMissingID(t *testing.T) {
	for _, input := range []string{
		`[{"id":""}]`,
		`[{"name":"coop-x"}]`,
		`[{"id":"coop-x"},{}]`,
	} {
		if names, err := volumeNamesFromList([]byte(input)); err == nil || names != nil {
			t.Errorf("accepted inconclusive volume list %s: names=%v err=%v", input, names, err)
		}
	}
	names, err := volumeNamesFromList([]byte(`[{"id":"coop-x"}]`))
	if err != nil || len(names) != 1 || names[0] != "coop-x" {
		t.Fatalf("valid volume list rejected: %v %v", names, err)
	}
}

func TestRuntimeQueryErrorsIncludeStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho query-detail >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Apple{Bin: bin}
	_, err := a.State("coop-x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("query stderr not captured: %v", err)
	}
	_, err = a.GuestFileExists("coop-x", "/x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("guest query stderr not captured: %v", err)
	}
	err = a.EnsureVolume("coop-x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("volume query error collapsed into absence: %v", err)
	}
}

func TestImageExistsUsesDenormalizedQuietOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'alpine:latest\\ncoop:latest\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Apple{Bin: bin}
	exists, err := a.ImageExists("coop:latest")
	if err != nil || !exists {
		t.Fatalf("short image reference not found in quiet output: exists=%v err=%v", exists, err)
	}
	exists, err = a.ImageExists("coop:missing")
	if err != nil || exists {
		t.Fatalf("missing image reference result: exists=%v err=%v", exists, err)
	}
}
