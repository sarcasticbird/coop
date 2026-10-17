package backup

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func TestPublishRejectsMissingPayloadsBeforeRuntime(t *testing.T) {
	for _, kind := range []string{"missing-root", "missing-volume", "duplicate-volume", "unexpected-payload"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			r := workspace.Record{Version: 1, Mode: workspace.Interactive, Identity: "coop-uat", RuntimeName: "coop-uat", Image: "test", Fingerprint: "x"}
			meta := diskMetadata{Source: r, RuntimeVersion: "1.5.0"}
			if kind != "missing-root" {
				if err := os.WriteFile(filepath.Join(dir, "rootfs"), []byte("disk"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing-volume" || kind == "duplicate-volume" {
				meta.Volumes = []volumeRecord{{Name: "coop-uat--deps", Payload: "volume-0"}}
			}
			if kind == "duplicate-volume" {
				meta.Volumes = append(meta.Volumes, meta.Volumes[0])
				if err := os.WriteFile(filepath.Join(dir, "volume-0"), []byte("disk"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unexpected-payload" {
				if err := os.WriteFile(filepath.Join(dir, "other"), []byte("disk"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".runtime-metadata.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			storage := AppleStorage{Store: workspace.FileStore{Root: dir}, Machine: runtime.AppleMachine{Command: func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { called = true; return nil }}}
			if err := storage.Publish(context.Background(), r, dir); err == nil {
				t.Fatal("incomplete payload set accepted")
			}
			if called {
				t.Fatal("runtime reached before complete payload validation")
			}
		})
	}
}

func TestAppleArchiveRejectsForeignSourceMetadataBeforeRetargeting(t *testing.T) {
	r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Image: "test"}
	foreign := r
	foreign.Identity = "other"
	foreign.RuntimeName = "coop-agent-other"
	data, err := json.Marshal(diskMetadata{Source: foreign, RuntimeVersion: "1.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: 1, Record: r, RuntimeVersion: "1.5.0", Metadata: data}
	if err := (AppleStorage{}).ValidateArchive(m); err == nil {
		t.Fatal("foreign source metadata accepted")
	}
}

func TestPayloadSetAcceptsExistingLongInteractiveVolumeNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"rootfs", "volume-0", ".runtime-metadata.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	name := "coop-" + strings.Repeat("a", 150) + "-0123456789abcdef"
	meta := diskMetadata{Source: workspace.Record{Mode: workspace.Interactive, RuntimeName: name}, Volumes: []volumeRecord{{Name: name + "--deps", Payload: "volume-0"}}}
	if err := validatePayloadSet(dir, meta); err != nil {
		t.Fatal(err)
	}
}

func TestColdStorageRejectsDiskOutsideOwnedBundle(t *testing.T) {
	testMachineSnapshot(t, true)
}

func TestColdStoragePreservesNativeInitializationMarker(t *testing.T) {
	testMachineSnapshot(t, false)
}

func testMachineSnapshot(t *testing.T, foreign bool) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "plugin-state", "machine-apiserver", "machines", "coop-agent-robin")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(bundle, "rootfs.ext4")
	if foreign {
		source = "/etc/passwd"
	}
	fs, _ := json.Marshal(map[string]string{"source": source, "destination": "/"})
	for name, data := range map[string]string{"config.json": `{"configuration":{"id":"coop-agent-robin"}}`, "rootfs.json": string(fs), "rootfs.ext4": "fixture", "machine.initialized": "1"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Image: "test"}
	m := runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		switch strings.Join(args, " ") {
		case "system status --format json":
			_, err := io.WriteString(out, `{"paths":{"appRoot":"`+root+`"},"server":{"version":"1.5.0"}}`)
			return err
		case "machine list --format json":
			_, err := io.WriteString(out, `[{"id":"coop-agent-robin"}]`)
			return err
		case "machine inspect coop-agent-robin":
			_, err := io.WriteString(out, `[{"id":"coop-agent-robin","status":"stopped","homeMount":"none","cpus":1,"memory":1073741824,"image":{"reference":"test"}}]`)
			return err
		default:
			t.Fatalf("unexpected command %q", args)
			return nil
		}
	}}
	s := AppleStorage{Machine: m}
	snap, err := s.Snapshot(context.Background(), r)
	if foreign && err == nil {
		t.Fatal("foreign disk accepted")
	}
	if !foreign {
		if err != nil {
			t.Fatal(err)
		}
		var meta diskMetadata
		if err := json.Unmarshal(snap.Metadata, &meta); err != nil {
			t.Fatal(err)
		}
		if !meta.Initialized {
			t.Fatal("native initialization state lost")
		}
	}
}
