package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/workspace"
)

type sizeObservingReader struct {
	f        *os.File
	want     int64
	observed bool
	t        *testing.T
}

func (r *sizeObservingReader) Read(p []byte) (int, error) {
	info, err := r.f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() != r.want {
		r.t.Errorf("sparse disk grows during extent writes: size=%d want=%d", info.Size(), r.want)
	}
	r.observed = true
	return bytes.NewReader([]byte("data")).Read(p)
}

func TestSparseRestoreSetsLogicalSizeBeforeWritingExtents(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	want := int64(512) << 30
	var header, tail bytes.Buffer
	for _, v := range []uint64{uint64(want), 0, 4} {
		if err := binary.Write(&header, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := binary.Write(&tail, binary.LittleEndian, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	body := &sizeObservingReader{f: f, want: want, t: t}
	// Limit the observing reader to the single four-byte extent.
	reader := io.MultiReader(&header, io.LimitReader(body, 4), &tail)
	if err := readSparse(context.Background(), reader, f, want); err != nil {
		t.Fatal(err)
	}
	if !body.observed {
		t.Fatal("extent not written")
	}
}

func TestRestoreRejectsUnsafeArchivesBeforePublication(t *testing.T) {
	for _, kind := range []string{"checksum", "ownership", "absolute", "traversal", "duplicate", "symlink", "manifest-size", "manifest-trailing", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			s := &fixtureStorage{source: filepath.Join(dir, "source"), published: filepath.Join(dir, "target")}
			if err := os.WriteFile(s.source, []byte("binary\x00fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(dir, "valid.zip")
			r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}
			if err := Create(context.Background(), s, r, archive); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.OpenReader(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = zr.Close() }()
			bad := filepath.Join(dir, "bad.zip")
			f, err := os.Create(bad)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(f)
			for _, entry := range zr.File {
				rc, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(rc)
				if err := rc.Close(); err != nil {
					t.Fatal(err)
				}
				if err != nil {
					t.Fatal(err)
				}
				header := entry.FileHeader
				if entry.Name == "manifest.json" {
					var m Manifest
					if err := json.Unmarshal(data, &m); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "checksum":
						m.Files[0].SHA256 = strings.Repeat("0", 64)
					case "ownership":
						m.Record.RuntimeName = "coop-agent-someone-else"
					}
					data, err = json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					if kind == "manifest-size" {
						data = []byte(strings.Repeat(" ", (1<<20)+1))
					}
					if kind == "manifest-trailing" {
						data = append(data, []byte("{}")...)
					}
				} else {
					switch kind {
					case "absolute":
						header.Name = "/rootfs"
					case "traversal":
						header.Name = "../rootfs"
					case "symlink":
						header.SetMode(os.ModeSymlink | 0700)
					case "truncated":
						data = data[:len(data)-1]
					}
				}
				w, err := zw.CreateHeader(&header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(data); err != nil {
					t.Fatal(err)
				}
				if kind == "duplicate" && entry.Name == "rootfs" {
					duplicateHeader := header
					w, err := zw.CreateHeader(&duplicateHeader)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write(data); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := Restore(context.Background(), s, bad, "copy"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(s.published); !os.IsNotExist(err) {
				t.Fatal("unsafe archive published")
			}
		})
	}
}

type fixtureStorage struct{ source, published string }

type changingStorage struct {
	fixtureStorage
	calls  int
	change func(*Snapshot) error
}

func (s *changingStorage) Snapshot(ctx context.Context, r workspace.Record) (Snapshot, error) {
	snapshot, err := s.fixtureStorage.Snapshot(ctx, r)
	if err != nil {
		return snapshot, err
	}
	volume := snapshot.Payloads[0]
	volume.Name = "volume-0"
	snapshot.Payloads = append(snapshot.Payloads, volume)
	snapshot.Metadata = json.RawMessage(`{"initialized":true,"volumes":[{"name":"data","payload":"volume-0"}]}`)
	s.calls++
	if s.calls == 2 {
		err = s.change(&snapshot)
	}
	return snapshot, err
}

func TestBackupComparesPostcopySnapshot(t *testing.T) {
	for _, kind := range []string{"added", "removed", "renamed", "size", "runtime", "record", "metadata", "duplicate", "snapshot-error", "replacement", "mtime", "unchanged", "reordered"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			s := &changingStorage{fixtureStorage: fixtureStorage{source: filepath.Join(dir, "source")}}
			if err := os.WriteFile(s.source, []byte("disk-data"), 0600); err != nil {
				t.Fatal(err)
			}
			s.change = func(snapshot *Snapshot) error {
				switch kind {
				case "added":
					p := snapshot.Payloads[0]
					p.Name = "volume-1"
					snapshot.Payloads = append(snapshot.Payloads, p)
				case "removed":
					snapshot.Payloads = snapshot.Payloads[1:]
				case "renamed":
					snapshot.Payloads[0].Name = "new-rootfs"
				case "size":
					snapshot.Payloads[0].Size++
				case "runtime":
					snapshot.RuntimeVersion = "1.5.1"
				case "record":
					snapshot.Record.Applied.Command = []string{"changed"}
				case "metadata":
					snapshot.Metadata = json.RawMessage(`{"initialized":false}`)
				case "duplicate":
					snapshot.Payloads[1].Name = snapshot.Payloads[0].Name
				case "snapshot-error":
					return fmt.Errorf("runtime is running")
				case "replacement":
					info, err := os.Stat(s.source)
					if err != nil {
						return err
					}
					replacement := filepath.Join(dir, "replacement")
					if err := os.WriteFile(replacement, []byte("disk-data"), 0600); err != nil {
						return err
					}
					if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
						return err
					}
					return os.Rename(replacement, s.source)
				case "mtime":
					changed := time.Now().Add(time.Hour)
					return os.Chtimes(s.source, changed, changed)
				case "reordered":
					snapshot.Payloads[0], snapshot.Payloads[1] = snapshot.Payloads[1], snapshot.Payloads[0]
					snapshot.Metadata = json.RawMessage(`{ "volumes": [{"payload":"volume-0","name":"data"}], "initialized": true }`)
				}
				return nil
			}
			archive := filepath.Join(dir, "backup.zip")
			r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}
			err := Create(context.Background(), s, r, archive)
			if s.calls != 2 {
				t.Fatalf("snapshot calls = %d, want 2", s.calls)
			}
			if kind == "unchanged" || kind == "reordered" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ReadManifest(archive); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "workspace changed during backup") {
				t.Fatalf("changed snapshot error = %v", err)
			}
			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("changed snapshot published: %v", err)
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".coop-backup-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("temporary archives remain: %v, %v", leftovers, err)
			}
		})
	}
}

func (f *fixtureStorage) Snapshot(_ context.Context, r workspace.Record) (Snapshot, error) {
	info, err := os.Stat(f.source)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Record: r, RuntimeVersion: "1.5.0", Payloads: []Payload{{Name: "rootfs", Size: info.Size(), Open: func() (io.ReadCloser, error) { return os.Open(f.source) }}}}, nil
}
func (f *fixtureStorage) Publish(_ context.Context, _ workspace.Record, dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "rootfs"))
	if err != nil {
		return err
	}
	return os.WriteFile(f.published, data, 0600)
}
func TestBackupRoundTripPreservesSparseDiskAndSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("start")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Seek(8<<20, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("end")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	s := &fixtureStorage{source: source, published: filepath.Join(dir, "restored")}
	r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Image: "test", Fingerprint: "x"}
	archive := filepath.Join(dir, "backup.zip")
	if err := Create(context.Background(), s, r, archive); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("sparse disk expanded into %d bytes", info.Size())
	}
	if err := Restore(context.Background(), s, archive, "copy"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(s.published)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(restored) {
		t.Fatal("restored disk differs")
	}
	if err := Create(context.Background(), s, r, archive); err == nil {
		t.Fatal("existing archive overwritten")
	}
}
func TestRestoreRejectsCorruptionBeforePublication(t *testing.T) {
	dir := t.TempDir()
	s := &fixtureStorage{source: filepath.Join(dir, "source"), published: filepath.Join(dir, "target")}
	if err := os.WriteFile(s.source, []byte("disk-data"), 0600); err != nil {
		t.Fatal(err)
	}
	r := workspace.Record{Version: 1, Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}
	archive := filepath.Join(dir, "backup.zip")
	if err := Create(context.Background(), s, r, archive); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), s, archive, "copy"); err == nil {
		t.Fatal("corruption accepted")
	}
	if _, err := os.Stat(s.published); !os.IsNotExist(err) {
		t.Fatal("published corrupted disk")
	}
}
