package hostmcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagingStageHasNoActivation(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "coop")
	if err := os.WriteFile(bin, []byte("fixture-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "server.toml")
	if err := os.WriteFile(config, []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeBin := filepath.Join(root, "container")
	if err := os.WriteFile(runtimeBin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "stage")
	cmd := exec.Command("/bin/sh", "../../scripts/deploy-mcp.sh", "--stage", "--host", "local", "--binary", bin, "--config", config, "--destination", dest, "--runtime-bin", runtimeBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v %s", err, out)
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 3 {
		t.Fatal("stage artifacts missing")
	}
	if _, err = os.Stat(filepath.Join(dest, "coop")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dest, "activation.json")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("/bin/sh", "../../scripts/deploy-mcp.sh", "--activate")
	if err = cmd.Run(); err == nil {
		t.Fatal("unreviewed activation accepted")
	}
}
func TestPackagingUATRefusesProductionBirds(t *testing.T) {
	for _, agent := range []string{"robin", "hawk", "wren"} {
		cmd := exec.Command("/bin/sh", "../../scripts/uat-mcp.sh", "--agent", agent)
		if err := cmd.Run(); err == nil {
			t.Fatal("production agent accepted as test fixture")
		}
	}
}

func TestPackagingRuntimePathSurvivesMinimalLaunchdEnvironment(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "homebrew", "bin")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeBin := filepath.Join(runtimeDir, "container")
	if err := os.WriteFile(runtimeBin, []byte("#!/bin/sh\nprintf runtime-fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "coop")
	if err := os.WriteFile(bin, []byte("fixture-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "stage")
	cmd := exec.Command("/bin/sh", "../../scripts/deploy-mcp.sh", "--stage", "--host", "local", "--binary", bin, "--config", filepath.Join(root, "server.toml"), "--destination", dest, "--runtime-bin", runtimeBin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage: %v %s", err, out)
	}
	// Read the actual staged plist and execute the same bare lookup as both adapters.
	cmd = exec.Command("python3", "-c", `import os,plistlib,subprocess,sys
with open(sys.argv[1],'rb') as f:p=plistlib.load(f)
env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin'}
env.update(p.get('EnvironmentVariables',{}))
r=subprocess.run(['/bin/sh','-c','command -v container; container'],env=env,capture_output=True,text=True)
print(r.stdout,end='')
sys.exit(r.returncode)`, filepath.Join(dest, "coop-mcp.plist"))
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != runtimeBin+"\nruntime-fixture" {
		t.Fatalf("minimal launchd runtime lookup: %v %s", err, out)
	}
	template, err := os.ReadFile("../../scripts/coop-mcp.launchd.plist.in")
	if err != nil || !strings.Contains(string(template), "@RUNTIME_DIRECTORY@") {
		t.Fatal("template omits deployment runtime path")
	}
}
