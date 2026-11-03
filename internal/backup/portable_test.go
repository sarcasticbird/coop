package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func TestPortableAgentColdBackupAndRestoreIncludesEveryDataVolume(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Image: "test-image", Fingerprint: "fingerprint", Applied: workspace.AppliedSpec{Runtime: "container", Resources: config.Resources{CPUs: 1, Memory: "1G"}}}
	rows := map[string]any{}
	volumes := map[string]string{}
	materialize := func(r workspace.Record, initial string) {
		bundle := filepath.Join(root, "containers", r.RuntimeName)
		if err := os.MkdirAll(bundle, 0700); err != nil {
			t.Fatal(err)
		}
		write := func(path string, value any) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(bundle, "rootfs.ext4"), []byte(initial+" root"), 0600); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(bundle, "rootfs.json"), map[string]string{"source": filepath.Join(bundle, "rootfs.ext4"), "destination": "/"})
		mounts := []any{}
		for _, v := range workspace.AgentVolumes(r.RuntimeName) {
			dir := filepath.Join(root, "volumes", v.Name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "volume.img")
			if err := os.WriteFile(path, []byte(initial+" "+v.Target), 0600); err != nil {
				t.Fatal(err)
			}
			volumes[v.Name] = path
			mounts = append(mounts, map[string]any{"destination": v.Target, "type": map[string]any{"volume": map[string]string{"name": v.Name}}})
		}
		rows[r.RuntimeName] = map[string]any{"configuration": map[string]any{"id": r.RuntimeName, "image": map[string]string{"reference": r.Image}, "labels": map[string]string{"coop.agent": r.Identity, "coop.storage": "agent-volumes-v1", "coop.incarnation": "incarnation", "coop.spec": r.Fingerprint}, "resources": map[string]any{"cpus": 1, "memoryInBytes": 1 << 30}, "mounts": mounts}, "status": map[string]string{"state": "stopped"}}
	}
	materialize(record, "source")
	machine := runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		write := func(v any) error { return json.NewEncoder(out).Encode(v) }
		switch strings.Join(args, " ") {
		case "system status --format json":
			return write(map[string]any{"paths": map[string]string{"appRoot": root}, "server": map[string]string{"version": "1.5.0"}})
		case "list --all --format json":
			values := []any{}
			for _, v := range rows {
				values = append(values, v)
			}
			return write(values)
		case "volume ls --format json":
			values := []any{}
			for name := range volumes {
				values = append(values, map[string]string{"id": name})
			}
			return write(values)
		}
		if args[0] == "inspect" {
			return write([]any{rows[args[1]]})
		}
		if len(args) > 2 && args[0] == "volume" && args[1] == "inspect" {
			return write([]any{map[string]any{"configuration": map[string]string{"name": args[2], "source": volumes[args[2]]}}})
		}
		return fmt.Errorf("unexpected runtime mutation/command %v", args)
	}}
	store := workspace.FileStore{Root: filepath.Join(root, "ownership")}
	storage := AppleStorage{Machine: machine, Store: store}
	archive := filepath.Join(root, "robin.zip")
	if err := Create(t.Context(), storage, record, archive); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(archive)
	if err != nil {
		t.Fatal(err)
	}
	var meta diskMetadata
	if err := json.Unmarshal(manifest.Metadata, &meta); err != nil || len(meta.Volumes) != 4 {
		t.Fatal("data omitted from backup", meta, err)
	}
	target := record
	target.Identity = "robin-copy"
	target.RuntimeName = "coop-agent-robin-copy"
	storage.CreateAgentContainer = func(_ context.Context, r workspace.Record) error { materialize(r, "empty target"); return nil }
	if err := Restore(t.Context(), storage, archive, target.Identity); err != nil {
		t.Fatal(err)
	}
	for _, v := range workspace.AgentVolumes(target.RuntimeName) {
		raw, err := os.ReadFile(volumes[v.Name])
		if err != nil || string(raw) != "source "+v.Target {
			t.Fatal("portable data not restored", v, string(raw), err)
		}
	}
	if got, err := store.Load(workspace.Agent, target.Identity); err != nil || got.Applied.Runtime != "container" {
		t.Fatal(got, err)
	}
	for _, v := range workspace.AgentVolumes(record.RuntimeName) {
		raw, err := os.ReadFile(volumes[v.Name])
		if err != nil || string(raw) != "source "+v.Target {
			t.Fatal("original data changed", err)
		}
	}
}

func TestPortableBackupRejectsIncompleteVolumeManifest(t *testing.T) {
	dir := t.TempDir()
	r := workspace.Record{Mode: workspace.Agent, RuntimeName: "coop-agent-robin", Applied: workspace.AppliedSpec{Runtime: "container"}}
	meta := diskMetadata{Source: r, Volumes: []volumeRecord{{Name: "coop-agent-robin--home", Payload: "volume-0"}}}
	for _, name := range []string{"rootfs", "volume-0", ".runtime-metadata.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validatePayloadSet(dir, meta); err == nil {
		t.Fatal("partial bird state accepted")
	}
}
