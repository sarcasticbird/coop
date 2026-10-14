package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// These audit probes assert the observed failure modes, not desired behavior.
type auditChangingStorage struct {
	fixtureStorage
	change string
	calls  int
}

func (s *auditChangingStorage) Snapshot(ctx context.Context, r workspace.Record) (Snapshot, error) {
	snap, err := s.fixtureStorage.Snapshot(ctx, r)
	s.calls++
	if s.calls == 2 {
		switch s.change {
		case "added-volume":
			snap.Payloads = append(snap.Payloads, Payload{Name: "volume-0", Size: 4})
		case "removed-root":
			snap.Payloads = nil
		case "changed-size":
			snap.Payloads[0].Size++
		case "changed-metadata":
			snap.Metadata = json.RawMessage(`{"changed":true}`)
		}
	}
	return snap, err
}

func TestAuditBackupPublishesDespiteChangedPostcopySnapshot(t *testing.T) {
	for _, change := range []string{"added-volume", "removed-root", "changed-size", "changed-metadata"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			s := &auditChangingStorage{fixtureStorage: fixtureStorage{source: filepath.Join(dir, "disk")}, change: change}
			if err := os.WriteFile(s.source, []byte("disk"), 0600); err != nil {
				t.Fatal(err)
			}
			r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}
			archive := filepath.Join(dir, "backup.zip")
			if err := Create(context.Background(), s, r, archive); err != nil {
				t.Fatal(err)
			}
			m, err := ReadManifest(archive)
			if err != nil {
				t.Fatal(err)
			}
			if s.calls != 2 || len(m.Files) != 1 || m.Files[0].Name != "rootfs" || m.Files[0].Size != 4 || len(m.Metadata) != 0 {
				t.Fatalf("unexpected published snapshot: calls=%d manifest=%+v", s.calls, m)
			}
			t.Logf("CONFIRMED: %s ignored; archive published with pre-copy manifest", change)
		})
	}
}

func TestAuditMaintenanceFailureLeavesOldRecordAndBlocksRetries(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	r := workspace.Record{Version: 1, Mode: workspace.Interactive, Identity: "coop-audit", RuntimeName: "coop-audit", Image: "test", Fingerprint: "old"}
	store := workspace.FileStore{Root: filepath.Join(root, "records")}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "staging")
	bin := filepath.Join(root, "bin")
	bundle := filepath.Join(root, "containers", r.RuntimeName)
	for _, path := range []string{dir, bin, bundle} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := json.Marshal(diskMetadata{Source: r, RuntimeVersion: "1.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, ".runtime-metadata.json"), string(meta), 0600)
	write(filepath.Join(dir, "rootfs"), "archived disk", 0600)
	write(filepath.Join(bundle, "rootfs.ext4"), "original disk", 0600)
	// Only the hard-coded Apple CLI listing/removal is faked; Publish is real.
	write(filepath.Join(bin, "container"), `#!/bin/sh
case "$1" in
list) printf '%s\n' '[{"configuration":{"id":"coop-audit"},"status":{"state":"stopped"}}]' ;;
rm) printf '%s\n' removed >> "$AUDIT_REMOVALS" ;;
*) exit 91 ;;
esac
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	removals := filepath.Join(root, "removals")
	t.Setenv("AUDIT_REMOVALS", removals)
	fingerprint := "old"
	created := 0
	s := AppleStorage{Store: store, Maintenance: true, Target: &workspace.Spec{Mode: r.Mode, Identity: r.Identity, RuntimeName: r.RuntimeName, Image: r.Image, Fingerprint: "new"}}
	s.Machine = runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		switch strings.Join(args, " ") {
		case "system status --format json":
			return json.NewEncoder(out).Encode(map[string]any{"paths": map[string]string{"appRoot": root}, "server": map[string]string{"version": "1.5.0"}})
		case "inspect coop-audit":
			_, err := fmt.Fprintf(out, `[{"configuration":{"id":"coop-audit","labels":{"coop.spec":%q}},"status":{"state":"stopped"}}]`, fingerprint)
			return err
		default:
			t.Fatalf("unexpected command %q", args)
			return nil
		}
	}}
	s.CreateInteractive = func(_ context.Context, target workspace.Record) error {
		created++
		fingerprint = target.Fingerprint
		write(filepath.Join(bundle, "rootfs.ext4"), "fresh empty disk", 0600)
		// Simulate missing runtime metadata after recreation, before disk restore.
		return nil
	}
	err = s.Publish(context.Background(), r, dir)
	if !os.IsNotExist(err) || created != 1 {
		t.Fatalf("expected post-recreation metadata failure, created=%d err=%v", created, err)
	}
	have, err := store.Load(r.Mode, r.Identity)
	if err != nil || !reflect.DeepEqual(have, r) {
		t.Fatalf("old record not preserved: %+v %v", have, err)
	}
	disk, err := os.ReadFile(filepath.Join(bundle, "rootfs.ext4"))
	if err != nil || string(disk) != "fresh empty disk" {
		t.Fatalf("unexpected disk after failure: %q %v", disk, err)
	}
	s.Maintenance = false
	if err := s.Publish(context.Background(), r, dir); err == nil || err.Error() != "destination ownership already exists" {
		t.Fatalf("expected ordinary restore record refusal: %v", err)
	}
	s.Maintenance = true
	if err := s.Publish(context.Background(), r, dir); err == nil || err.Error() != "workspace changed during maintenance" {
		t.Fatalf("expected maintenance fingerprint refusal: %v", err)
	}
	removed, err := os.ReadFile(removals)
	if err != nil || string(removed) != "removed\n" || created != 1 {
		t.Fatalf("unexpected retry mutation: removed=%q created=%d err=%v", removed, created, err)
	}
	t.Log("CONFIRMED: old record + recreated empty disk; normal retry refuses record; maintenance retry refuses new fingerprint")
}
