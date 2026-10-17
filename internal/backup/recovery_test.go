package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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

var errRecoverySave = errors.New("injected record Save failure")

type recoveryFailSaveStore struct{ workspace.FileStore }

func (recoveryFailSaveStore) Save(workspace.Record) error { return errRecoverySave }

type recoveryFailJournalStore struct {
	workspace.FileStore
	calls, failAt int
	err           error
}

func (s *recoveryFailJournalStore) SaveRecovery(mode workspace.Mode, name string, data []byte) error {
	s.calls++
	if s.calls == s.failAt {
		return s.err
	}
	return s.FileStore.SaveRecovery(mode, name, data)
}

type recoveryFailClearStore struct {
	workspace.FileStore
	err error
}

func (s recoveryFailClearStore) ClearRecovery(workspace.Mode, string) error { return s.err }

type recoveryAmbiguousSaveStore struct{ workspace.FileStore }

func (s recoveryAmbiguousSaveStore) Save(r workspace.Record) error {
	if err := s.FileStore.Save(r); err != nil {
		return err
	}
	return errRecoverySave
}

type recoverySnapshotStorage struct {
	AppleStorage
	snapshot Snapshot
}

func (s recoverySnapshotStorage) Snapshot(context.Context, workspace.Record) (Snapshot, error) {
	return s.snapshot, nil
}

type recoveryFixture struct {
	t                    *testing.T
	root, stage, archive string
	archiveData          []byte
	archiveInfo          os.FileInfo
	source               workspace.Record
	target               workspace.Spec
	store                workspace.FileStore
	current              *containerInspection
	volumes              []volumeRecord
	targetVolumes        []string
	creates, removes     int
	commandFault         func([]string) error
	createFault          func() error
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryFixture{t: t, root: filepath.Join(base, "runtime"), stage: filepath.Join(base, "stage"), archive: filepath.Join(base, "original.coop"), archiveData: []byte("original cold archive evidence\x00do not replace"), store: workspace.FileStore{Root: filepath.Join(base, "state")}}
	f.target = workspace.Spec{Mode: workspace.Interactive, Identity: "coop-recovery", RuntimeName: "coop-recovery", Image: "test:1", Fingerprint: "changed-spec", Home: "/home/coop", Workdir: "/work"}
	f.source = f.target.Record()
	f.source.Fingerprint = "old-spec"
	f.volumes = []volumeRecord{{Name: "coop-recovery--deps", Payload: "volume-0"}, {Name: "coop-recovery--cache", Payload: "volume-1"}}
	for _, dir := range []string{f.stage, f.bundle()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.write(f.archive, f.archiveData)
	f.archiveInfo = f.stat(f.archive)
	f.write(filepath.Join(f.bundle(), "rootfs.ext4"), []byte("old runtime root"))
	f.writeJSON(filepath.Join(f.bundle(), "rootfs.json"), map[string]string{"source": filepath.Join(f.bundle(), "rootfs.ext4"), "destination": "/"})
	f.setCurrent(f.source, "")
	for _, v := range f.volumes {
		path := f.volume(v)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		f.write(path, []byte("old volume contents longer than payload"))
		f.write(filepath.Join(f.stage, v.Payload), f.payload(v.Payload))
	}
	f.write(filepath.Join(f.stage, "rootfs"), f.payload("rootfs"))
	f.writeJSON(filepath.Join(f.stage, ".runtime-metadata.json"), diskMetadata{Source: f.source, RuntimeVersion: "1.5.0", Volumes: f.volumes})
	if err := f.store.Save(f.source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.assertContents(f.archive, f.archiveData)
		if !os.SameFile(f.archiveInfo, f.stat(f.archive)) {
			t.Error("original archive was replaced")
		}
	})
	return f
}

func (f *recoveryFixture) bundle() string {
	return filepath.Join(f.root, "containers", f.source.RuntimeName)
}
func (f *recoveryFixture) volume(v volumeRecord) string {
	return filepath.Join(f.root, "volumes", v.Name, "volume.img")
}
func (f *recoveryFixture) payload(name string) []byte {
	return []byte("saved " + name + "\x00\x00payload")
}
func (f *recoveryFixture) write(path string, data []byte) {
	f.t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *recoveryFixture) writeJSON(path string, value any) {
	f.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(path, data)
}
func (f *recoveryFixture) stat(path string) os.FileInfo {
	f.t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return info
}
func (f *recoveryFixture) assertContents(path string, want []byte) {
	f.t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		f.t.Errorf("contents of %s = %q, %v; want %q", path, got, err, want)
	}
}
func (f *recoveryFixture) setCurrent(r workspace.Record, token string) {
	f.current = &containerInspection{}
	f.current.Configuration.ID = r.RuntimeName
	f.current.Configuration.Image.Reference = r.Image
	f.current.Configuration.Labels = map[string]string{"coop.spec": r.Fingerprint}
	if token != "" {
		f.current.Configuration.Labels[RestoreLabel] = token
	}
	f.current.Status.State = "stopped"
}

func (f *recoveryFixture) storage(maintenance bool, store workspace.Store) AppleStorage {
	sum := sha256.Sum256(f.archiveData)
	return AppleStorage{
		Store: store, Target: &f.target, Maintenance: maintenance, Archive: f.archive, ArchiveSHA256: hex.EncodeToString(sum[:]),
		Machine: runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
			if f.commandFault != nil {
				if err := f.commandFault(args); err != nil {
					return err
				}
			}
			switch strings.Join(args, " ") {
			case "system status --format json":
				return json.NewEncoder(out).Encode(map[string]any{"paths": map[string]string{"appRoot": f.root}, "server": map[string]string{"version": "1.5.0"}})
			case "list --all --format json":
				rows := []containerInspection{}
				if f.current != nil {
					rows = append(rows, *f.current)
				}
				return json.NewEncoder(out).Encode(rows)
			case "inspect " + f.source.RuntimeName:
				if f.current == nil {
					return fmt.Errorf("container absent")
				}
				return json.NewEncoder(out).Encode([]containerInspection{*f.current})
			case "rm " + f.source.RuntimeName:
				f.removes++
				f.current = nil
				return os.RemoveAll(f.bundle())
			case "volume ls --format json":
				rows := []map[string]string{}
				for _, name := range f.targetVolumes {
					if _, err := os.Stat(f.volume(volumeRecord{Name: name})); err == nil {
						rows = append(rows, map[string]string{"id": name})
					} else if !os.IsNotExist(err) {
						return err
					}
				}
				for _, v := range f.volumes {
					rows = append(rows, map[string]string{"id": v.Name})
				}
				return json.NewEncoder(out).Encode(rows)
			default:
				for _, name := range f.targetVolumes {
					path := f.volume(volumeRecord{Name: name})
					switch strings.Join(args, " ") {
					case "volume create " + name:
						j := f.journal()
						if id, ok := j.Volumes[name]; !ok || id != (diskIdentity{}) {
							f.t.Fatalf("volume create without durable zero intent: %#v", j.Volumes)
						}
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							return err
						}
						return os.WriteFile(path, []byte("fresh target-only volume"), 0600)
					case "volume inspect " + name:
						if _, err := os.Stat(path); err != nil {
							return err
						}
						return json.NewEncoder(out).Encode([]any{map[string]any{"configuration": map[string]string{"name": name, "source": path}}})
					}
				}
				for _, v := range f.volumes {
					if strings.Join(args, " ") == "volume inspect "+v.Name {
						return json.NewEncoder(out).Encode([]any{map[string]any{"configuration": map[string]string{"name": v.Name, "source": f.volume(v)}}})
					}
				}
				f.t.Fatalf("unexpected machine command %q", args)
				return nil
			}
		}},
		CreateInteractive: func(_ context.Context, r workspace.Record, token string, ensureVolume func(string) error) error {
			if token == "" || !reflect.DeepEqual(r, f.target.Record()) {
				f.t.Fatalf("create record/token = %#v, %q", r, token)
			}
			for _, name := range f.targetVolumes {
				if err := ensureVolume(name); err != nil {
					return err
				}
				got, err := identity(f.volume(volumeRecord{Name: name}))
				if err != nil || got == (diskIdentity{}) || f.journal().Volumes[name] != got {
					f.t.Fatalf("target volume %s not checkpointed before runtime creation: %#v, %v", name, got, err)
				}
			}
			f.creates++
			if err := os.MkdirAll(f.bundle(), 0700); err != nil {
				return err
			}
			f.write(filepath.Join(f.bundle(), "rootfs.ext4"), []byte("fresh bootstrap root"))
			f.writeJSON(filepath.Join(f.bundle(), "rootfs.json"), map[string]string{"source": filepath.Join(f.bundle(), "rootfs.ext4"), "destination": "/"})
			f.setCurrent(r, token)
			if f.createFault != nil {
				return f.createFault()
			}
			return nil
		},
	}
}

func (f *recoveryFixture) journal() maintenanceJournal {
	f.t.Helper()
	data, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
	if err != nil {
		f.t.Fatal(err)
	}
	var j maintenanceJournal
	if err := json.Unmarshal(data, &j); err != nil {
		f.t.Fatal(err)
	}
	if j.Token == "" || j.Archive != f.archive || !reflect.DeepEqual(j.Source, f.source) || !reflect.DeepEqual(j.Target, f.target.Record()) {
		f.t.Fatalf("unexpected recovery journal: %#v", j)
	}
	return j
}

func (f *recoveryFixture) retry() {
	f.t.Helper()
	// Reconstruct storage rather than retaining any transaction state in memory.
	s := f.storage(false, f.store)
	if pending, err := s.PendingMaintenance(); err != nil || pending != f.archive {
		f.t.Fatalf("PendingMaintenance = %q, %v", pending, err)
	}
	if err := s.Publish(context.Background(), f.source, f.stage); err != nil {
		f.t.Fatal(err)
	}
	f.assertRecovered()
}

func (f *recoveryFixture) assertRecovered() {
	f.t.Helper()
	f.assertContents(filepath.Join(f.bundle(), "rootfs.ext4"), f.payload("rootfs"))
	for _, v := range f.volumes {
		f.assertContents(f.volume(v), f.payload(v.Payload))
	}
	got, err := f.store.Load(f.source.Mode, f.source.Identity)
	if err != nil || !reflect.DeepEqual(got, f.target.Record()) {
		f.t.Fatalf("saved record = %#v, %v", got, err)
	}
	if _, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity); !os.IsNotExist(err) {
		f.t.Fatalf("journal not cleared: %v", err)
	}
}

func (f *recoveryFixture) createArchive() {
	f.t.Helper()
	metadata, err := os.ReadFile(filepath.Join(f.stage, ".runtime-metadata.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot := Snapshot{Record: f.source, RuntimeVersion: "1.5.0", Metadata: metadata}
	for _, name := range []string{"rootfs", "volume-0", "volume-1"} {
		path := filepath.Join(f.stage, name)
		snapshot.Payloads = append(snapshot.Payloads, Payload{Name: name, Size: f.stat(path).Size(), Open: func() (io.ReadCloser, error) { return os.Open(path) }})
	}
	if err := os.Remove(f.archive); err != nil {
		f.t.Fatal(err)
	}
	if err := Create(context.Background(), recoverySnapshotStorage{snapshot: snapshot}, f.source, f.archive); err != nil {
		f.t.Fatal(err)
	}
	f.archiveData, err = os.ReadFile(f.archive)
	if err != nil {
		f.t.Fatal(err)
	}
	f.archiveInfo = f.stat(f.archive)
}

func TestMaintenanceRecoveryRestoreArchive(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.createArchive()
			s := f.storage(true, recoveryFailSaveStore{f.store})
			// Restore must derive the archive path and digest from its open file,
			// not rely on fields supplied by the direct-Publish fixture.
			s.Archive, s.ArchiveSHA256 = "", ""
			if err := Restore(context.Background(), s, f.archive, f.source.Identity); !errors.Is(err, errRecoverySave) {
				t.Fatalf("Restore = %v; want injected Save failure", err)
			}
			j := f.journal()
			sum := sha256.Sum256(f.archiveData)
			if j.SHA256 != hex.EncodeToString(sum[:]) || j.Disks["rootfs"] == (diskIdentity{}) {
				t.Fatalf("Restore did not journal archive digest and root identity: %#v", j)
			}
			before, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
			if err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{"rootfs": filepath.Join(f.bundle(), "rootfs.ext4")}
			for _, v := range f.volumes {
				paths[v.Payload] = f.volume(v)
			}
			if changed {
				// Add an EOCD comment without changing any entries or checksums.
				comment := []byte("changed archive metadata")
				data := append([]byte(nil), f.archiveData...)
				binary.LittleEndian.PutUint16(data[len(data)-2:], uint16(len(comment)))
				data = append(data, comment...)
				f.write(f.archive, data)
				f.archiveData = data
				if _, err := ReadManifest(f.archive); err != nil {
					t.Fatalf("modified archive is not valid: %v", err)
				}
			}
			s = f.storage(false, f.store)
			s.Archive, s.ArchiveSHA256 = "", ""
			if pending, err := s.PendingMaintenance(); err != nil || pending != f.archive {
				t.Fatalf("PendingMaintenance = %q, %v", pending, err)
			}
			err = Restore(context.Background(), s, f.archive, f.source.Identity)
			if changed {
				if err == nil || !strings.Contains(err.Error(), "archive or destination changed") {
					t.Fatalf("Restore changed archive = %v", err)
				}
				after, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
				if err != nil || string(after) != string(before) {
					t.Fatalf("rejected archive changed journal: %v", err)
				}
				got, err := f.store.Load(f.source.Mode, f.source.Identity)
				if err != nil || !reflect.DeepEqual(got, f.source) {
					t.Fatalf("rejected archive changed record: %#v, %v", got, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				f.assertRecovered()
			}
			for name, path := range paths {
				f.assertContents(path, f.payload(name))
				if got, err := identity(path); err != nil || got != j.Disks[name] {
					t.Errorf("%s identity = %#v, %v; want %#v", name, got, err, j.Disks[name])
				}
			}
			if f.creates != 1 || f.removes != 1 {
				t.Fatalf("retry recreated runtime: creates=%d removes=%d", f.creates, f.removes)
			}
		})
	}
}

func TestMaintenanceRecoveryJournalSaveBoundaries(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("save-%d", failAt), func(t *testing.T) {
			f := newRecoveryFixture(t)
			original := *f.current
			rootInfo := f.stat(filepath.Join(f.bundle(), "rootfs.ext4"))
			volumeInfos := map[string]os.FileInfo{}
			for _, v := range f.volumes {
				volumeInfos[v.Payload] = f.stat(f.volume(v))
			}
			injected := errors.New("injected SaveRecovery failure")
			store := &recoveryFailJournalStore{FileStore: f.store, failAt: failAt, err: injected}
			s := f.storage(true, store)
			if err := s.Publish(context.Background(), f.source, f.stage); !errors.Is(err, injected) || store.calls != failAt {
				t.Fatalf("Publish = %v, journal saves=%d; want failure at %d", err, store.calls, failAt)
			}
			prior, err := f.store.Load(f.source.Mode, f.source.Identity)
			if err != nil || !reflect.DeepEqual(prior, f.source) {
				t.Fatalf("journal failure changed record: %#v, %v", prior, err)
			}
			for _, v := range f.volumes {
				f.assertContents(f.volume(v), []byte("old volume contents longer than payload"))
				if !os.SameFile(volumeInfos[v.Payload], f.stat(f.volume(v))) {
					t.Fatal("journal failure replaced volume")
				}
			}
			if failAt == 1 {
				if f.creates != 0 || f.removes != 0 || !reflect.DeepEqual(*f.current, original) || !os.SameFile(rootInfo, f.stat(filepath.Join(f.bundle(), "rootfs.ext4"))) {
					t.Fatal("initial journal failure changed original runtime")
				}
				f.assertContents(filepath.Join(f.bundle(), "rootfs.ext4"), []byte("old runtime root"))
				if _, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity); !os.IsNotExist(err) {
					t.Fatalf("failed initial save left journal: %v", err)
				}
				s = f.storage(true, f.store)
				if err := s.Publish(context.Background(), f.source, f.stage); err != nil {
					t.Fatal(err)
				}
				f.assertRecovered()
			} else {
				j := f.journal()
				if j.Disks["rootfs"] != (diskIdentity{}) || f.creates != 1 || f.removes != 1 || f.current.Configuration.Labels[RestoreLabel] != j.Token {
					t.Fatalf("unexpected state after root identity save failure: %#v", j)
				}
				f.assertContents(filepath.Join(f.bundle(), "rootfs.ext4"), []byte("fresh bootstrap root"))
				f.retry()
				if f.creates != 2 || f.removes != 2 || f.current.Configuration.Labels[RestoreLabel] != j.Token {
					t.Fatal("retry did not recreate unpublished root with original token")
				}
			}
		})
	}
}

func TestMaintenanceRecoveryRetriesCommittedRecord(t *testing.T) {
	for _, failure := range []string{"clear-journal", "ambiguous-save"} {
		t.Run(failure, func(t *testing.T) {
			f := newRecoveryFixture(t)
			injected := errRecoverySave
			var store workspace.Store = recoveryAmbiguousSaveStore{f.store}
			if failure == "clear-journal" {
				injected = errors.New("injected ClearRecovery failure")
				store = recoveryFailClearStore{FileStore: f.store, err: injected}
			}
			s := f.storage(true, store)
			if err := s.Publish(context.Background(), f.source, f.stage); !errors.Is(err, injected) {
				t.Fatalf("Publish = %v; want %v", err, injected)
			}
			j := f.journal()
			got, err := f.store.Load(f.source.Mode, f.source.Identity)
			if err != nil || !reflect.DeepEqual(got, f.target.Record()) {
				t.Fatalf("record was not committed: %#v, %v", got, err)
			}
			if pending, err := s.PendingMaintenance(); err != nil || pending != f.archive {
				t.Fatalf("committed record lost pending recovery: %q, %v", pending, err)
			}
			f.retry()
			if f.creates != 1 || f.removes != 1 || f.current.Configuration.Labels[RestoreLabel] != j.Token {
				t.Fatal("committed record retry recreated runtime or changed token")
			}
			for name, want := range j.Disks {
				path := filepath.Join(f.bundle(), "rootfs.ext4")
				for _, v := range f.volumes {
					if v.Payload == name {
						path = f.volume(v)
					}
				}
				if got, err := identity(path); err != nil || got != want {
					t.Errorf("%s identity = %#v, %v; want %#v", name, got, err, want)
				}
			}
		})
	}
}

func TestMaintenanceRecoveryRetriesRecordSaveFailure(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.storage(true, recoveryFailSaveStore{f.store})
	if err := s.Publish(context.Background(), f.source, f.stage); !errors.Is(err, errRecoverySave) {
		t.Fatalf("Publish = %v; want injected Save failure", err)
	}
	j := f.journal()
	if _, err := s.Snapshot(context.Background(), f.target.Record()); err == nil || !strings.Contains(err.Error(), "original archive") {
		t.Fatalf("partial state was eligible for backup: %v", err)
	}
	if j.Disks["rootfs"] == (diskIdentity{}) {
		t.Fatal("rootfs identity not journaled before Save")
	}
	prior, err := f.store.Load(f.source.Mode, f.source.Identity)
	if err != nil || !reflect.DeepEqual(prior, f.source) {
		t.Fatalf("failed Save changed prior record: %#v, %v", prior, err)
	}
	rootInfo := f.stat(filepath.Join(f.bundle(), "rootfs.ext4"))
	volumeInfo := f.stat(f.volume(f.volumes[0]))
	f.retry()
	if f.creates != 1 || f.removes != 1 || !os.SameFile(rootInfo, f.stat(filepath.Join(f.bundle(), "rootfs.ext4"))) || !os.SameFile(volumeInfo, f.stat(f.volume(f.volumes[0]))) {
		t.Fatal("retry recreated runtime or replaced owned disk inodes")
	}
}

func TestMaintenanceRecoveryRetriesPartialReplay(t *testing.T) {
	for _, failure := range []string{"between-volumes", "rootfs"} {
		t.Run(failure, func(t *testing.T) {
			f := newRecoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fired := false
			f.commandFault = func(args []string) error {
				if !fired && strings.Join(args, " ") == "volume inspect "+f.volumes[1].Name {
					data, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
					var j maintenanceJournal
					if err == nil && json.Unmarshal(data, &j) == nil && j.Disks["rootfs"] != (diskIdentity{}) {
						fired = true
						if failure == "between-volumes" {
							cancel()
						} else {
							// Invalidate only the staged root after payload validation.
							if err := os.Remove(filepath.Join(f.stage, "rootfs")); err != nil {
								t.Fatal(err)
							}
							if err := os.Mkdir(filepath.Join(f.stage, "rootfs"), 0700); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return nil
			}
			s := f.storage(true, f.store)
			if err := s.Publish(ctx, f.source, f.stage); err == nil || !fired {
				t.Fatalf("Publish = %v, fault fired = %v", err, fired)
			}
			f.assertContents(f.volume(f.volumes[0]), f.payload("volume-0"))
			f.assertContents(filepath.Join(f.bundle(), "rootfs.ext4"), []byte("fresh bootstrap root"))
			j := f.journal()
			if failure == "rootfs" {
				f.assertContents(f.volume(f.volumes[1]), f.payload("volume-1"))
				if err := os.Remove(filepath.Join(f.stage, "rootfs")); err != nil {
					t.Fatal(err)
				}
				f.write(filepath.Join(f.stage, "rootfs"), f.payload("rootfs"))
			}
			f.commandFault = nil
			f.retry()
			for name, want := range j.Disks {
				path := filepath.Join(f.bundle(), "rootfs.ext4")
				for _, v := range f.volumes {
					if v.Payload == name {
						path = f.volume(v)
					}
				}
				got, err := identity(path)
				if err != nil || got != want {
					t.Errorf("%s identity = %#v, %v; want %#v", name, got, err, want)
				}
			}
		})
	}
}

func TestMaintenanceRecoveryRejectsChangedOwnershipAndInputs(t *testing.T) {
	for _, change := range []string{"rootfs-inode", "volume-inode", "foreign-runtime", "archive-sha", "configuration"} {
		t.Run(change, func(t *testing.T) {
			f := newRecoveryFixture(t)
			s := f.storage(true, recoveryFailSaveStore{f.store})
			if err := s.Publish(context.Background(), f.source, f.stage); !errors.Is(err, errRecoverySave) {
				t.Fatal(err)
			}
			before, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
			if err != nil {
				t.Fatal(err)
			}
			s = f.storage(false, f.store)
			wantErr := ""
			var changedPath string
			switch change {
			case "rootfs-inode", "volume-inode":
				changedPath = filepath.Join(f.bundle(), "rootfs.ext4")
				wantErr = "refusing changed destination rootfs"
				if change == "volume-inode" {
					changedPath = f.volume(f.volumes[0])
					wantErr = "refusing changed destination volume"
				}
				// Retain the old inode so replacement cannot reuse it.
				if err := os.Rename(changedPath, changedPath+".original"); err != nil {
					t.Fatal(err)
				}
				f.write(changedPath, []byte("foreign disk, do not touch"))
			case "foreign-runtime":
				f.setCurrent(f.target.Record(), "")
				wantErr = "refusing foreign maintenance destination"
			case "archive-sha":
				s.ArchiveSHA256 = strings.Repeat("a", 64)
				wantErr = "archive or destination changed"
			case "configuration":
				target := f.target
				target.Workdir = "/different"
				s.Target = &target
				wantErr = "archive or destination changed"
			}
			if err := s.Publish(context.Background(), f.source, f.stage); err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("Publish = %v; want %q", err, wantErr)
			}
			if changedPath != "" {
				f.assertContents(changedPath, []byte("foreign disk, do not touch"))
			}
			after, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
			if err != nil || string(after) != string(before) || f.creates != 1 || f.removes != 1 {
				t.Fatalf("rejected retry changed journal/runtime: %v, creates=%d removes=%d", err, f.creates, f.removes)
			}
		})
	}
}

func TestMaintenanceRecoveryRetriesInterruptedBootstrap(t *testing.T) {
	for _, failure := range []string{"removal", "creation", "missing-rootfs-metadata"} {
		t.Run(failure, func(t *testing.T) {
			f := newRecoveryFixture(t)
			injected := errors.New("injected bootstrap interruption")
			if failure == "removal" {
				f.commandFault = func(args []string) error {
					if strings.Join(args, " ") == "rm "+f.source.RuntimeName {
						f.current = nil
						if err := os.RemoveAll(f.bundle()); err != nil {
							t.Fatal(err)
						}
						return injected
					}
					return nil
				}
			} else {
				f.createFault = func() error {
					if failure == "creation" {
						return injected
					}
					return os.Remove(filepath.Join(f.bundle(), "rootfs.json"))
				}
			}
			s := f.storage(true, f.store)
			if err := s.Publish(context.Background(), f.source, f.stage); err == nil {
				t.Fatal("interrupted bootstrap succeeded")
			}
			j := f.journal()
			if j.Disks["rootfs"] != (diskIdentity{}) {
				t.Fatal("incomplete bootstrap published a rootfs identity")
			}
			if f.current != nil && f.current.Configuration.Labels[RestoreLabel] != j.Token {
				t.Fatal("interrupted creation lost ownership token")
			}
			f.commandFault, f.createFault = nil, nil
			f.retry()
			wantCreates := 2
			if failure == "removal" {
				wantCreates = 1
			}
			if f.creates != wantCreates || f.current.Configuration.Labels[RestoreLabel] != j.Token {
				t.Fatalf("retry did not recreate with original token: creates=%d labels=%v", f.creates, f.current.Configuration.Labels)
			}
		})
	}
}

func TestMaintenanceRecoveryRejectsChangedOriginalRootBeforeRemoval(t *testing.T) {
	f := newRecoveryFixture(t)
	f.commandFault = func(args []string) error {
		if strings.Join(args, " ") == "rm "+f.source.RuntimeName {
			return errors.New("removal not performed")
		}
		return nil
	}
	if err := f.storage(true, f.store).Publish(context.Background(), f.source, f.stage); err == nil {
		t.Fatal("expected interruption before removal")
	}
	f.commandFault = nil
	path := filepath.Join(f.bundle(), "rootfs.ext4")
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	f.write(path, []byte("foreign disk"))
	err := f.storage(false, f.store).Publish(context.Background(), f.source, f.stage)
	if err == nil || !strings.Contains(err.Error(), "refusing changed original rootfs") {
		t.Fatalf("Publish = %v", err)
	}
	f.assertContents(path, []byte("foreign disk"))
	if f.removes != 0 || f.creates != 0 {
		t.Fatal("foreign root disk was removed")
	}
}

func TestMaintenanceRecoveryRejectsRunningVolumeUser(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.storage(true, f.store)
	original := s.Machine.Command
	s.Machine.Command = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
		if strings.Join(args, " ") == "list --all --format json" {
			var rows []any
			rows = append(rows, f.current, map[string]any{
				"configuration": map[string]any{"id": "other", "mounts": []any{map[string]any{"type": map[string]any{"volume": map[string]string{"name": f.volumes[0].Name}}}}},
				"status":        map[string]string{"state": "running"},
			})
			return json.NewEncoder(out).Encode(rows)
		}
		return original(ctx, args, in, out, stderr)
	}
	if err := s.Publish(context.Background(), f.source, f.stage); err == nil || !strings.Contains(err.Error(), "volume is in use") {
		t.Fatalf("Publish = %v", err)
	}
	if f.removes != 0 || f.creates != 0 {
		t.Fatal("busy runtime mutated")
	}
}

func TestMaintenanceRecoveryTargetOnlyVolumeOwnership(t *testing.T) {
	for _, suffix := range []string{"opencode", "project-volume-0123456789abcdef"} {
		for _, failure := range []string{"runtime-creation", "record-save", "post-bootstrap-replacement"} {
			for _, replace := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/replace=%t", suffix, failure, replace), func(t *testing.T) {
					f := newRecoveryFixture(t)
					name := f.target.RuntimeName + "--" + suffix
					f.targetVolumes = []string{name}
					path := f.volume(volumeRecord{Name: name})
					injected := errors.New("injected runtime creation failure")
					var store workspace.Store = f.store
					if failure == "record-save" {
						store = recoveryFailSaveStore{f.store}
						injected = errRecoverySave
					} else {
						f.createFault = func() error {
							if failure == "post-bootstrap-replacement" {
								if err := os.Rename(path, path+".original"); err != nil {
									t.Fatal(err)
								}
								f.write(path, []byte("foreign target-only disk"))
								return nil
							}
							return injected
						}
					}
					err := f.storage(true, store).Publish(context.Background(), f.source, f.stage)
					if failure == "post-bootstrap-replacement" {
						if err == nil || !strings.Contains(err.Error(), "refusing changed destination volume "+name) {
							t.Fatalf("post-bootstrap validation = %v", err)
						}
					} else if !errors.Is(err, injected) {
						t.Fatalf("Publish = %v; want %v", err, injected)
					}
					j := f.journal()
					wantDisks := len(f.volumes)
					if failure == "record-save" {
						wantDisks++
					}
					if j.Volumes[name] == (diskIdentity{}) || len(j.Disks) != wantDisks {
						t.Fatalf("target-only identity missing or added as archive payload: %#v", j)
					}
					if failure == "post-bootstrap-replacement" {
						// Restore the original inode to exercise a safe retry as well.
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(path+".original", path); err != nil {
							t.Fatal(err)
						}
					}
					if got, err := identity(path); err != nil || got != j.Volumes[name] {
						t.Fatalf("checkpoint = %#v; disk = %#v, %v", j.Volumes[name], got, err)
					}
					f.createFault = nil
					if replace {
						if err := os.Rename(path, path+".original"); err != nil {
							t.Fatal(err)
						}
						f.write(path, []byte("foreign target-only disk"))
						f.assertTargetVolumeRetryRejected("refusing changed destination volume " + name)
						f.assertContents(path, []byte("foreign target-only disk"))
					} else {
						f.retry()
						if got, err := identity(path); err != nil || got != j.Volumes[name] {
							t.Fatalf("retry replaced target-only disk: %#v, %v", got, err)
						}
						f.assertContents(path, []byte("fresh target-only volume"))
						wantCreates := 2
						if failure == "record-save" {
							wantCreates = 1
						}
						if f.creates != wantCreates || f.removes != wantCreates {
							t.Fatalf("retry runtime creates=%d removes=%d; want %d", f.creates, f.removes, wantCreates)
						}
					}
				})
			}
		}
	}
}

func TestMaintenanceRecoveryTargetOnlyVolumeCreationFailures(t *testing.T) {
	for _, suffix := range []string{"opencode", "project-volume-0123456789abcdef"} {
		for _, failure := range []string{"identity-checkpoint", "ambiguous-create", "absent-create", "preexisting"} {
			t.Run(suffix+"/"+failure, func(t *testing.T) {
				f := newRecoveryFixture(t)
				name := f.target.RuntimeName + "--" + suffix
				f.targetVolumes = []string{name}
				path := f.volume(volumeRecord{Name: name})
				injected := errors.New("injected target volume preparation failure")
				var store workspace.Store = f.store
				switch failure {
				case "identity-checkpoint":
					// Initial journal, zero creation intent, then volume identity.
					store = &recoveryFailJournalStore{FileStore: f.store, failAt: 3, err: injected}
				case "preexisting":
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					f.write(path, []byte("unowned target-only disk"))
				default:
					f.commandFault = func(args []string) error {
						if strings.Join(args, " ") != "volume create "+name {
							return nil
						}
						if id, ok := f.journal().Volumes[name]; !ok || id != (diskIdentity{}) {
							t.Fatal("create attempted without durable zero intent")
						}
						if failure == "ambiguous-create" {
							if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
								t.Fatal(err)
							}
							f.write(path, []byte("ambiguous target-only disk"))
						}
						return injected
					}
				}
				err := f.storage(true, store).Publish(context.Background(), f.source, f.stage)
				if failure == "preexisting" {
					if err == nil || !strings.Contains(err.Error(), "refusing uncheckpointed destination volume "+name) {
						t.Fatalf("Publish = %v", err)
					}
				} else if !errors.Is(err, injected) {
					t.Fatalf("Publish = %v; want injected failure", err)
				}
				id, ok := f.journal().Volumes[name]
				if id != (diskIdentity{}) || ok != (failure != "preexisting") || f.creates != 0 || f.current != nil {
					t.Fatalf("preparation failure state: identity=%#v present=%t creates=%d runtime=%#v", id, ok, f.creates, f.current)
				}
				f.commandFault = nil
				if failure == "absent-create" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("failed creation not absent: %v", err)
					}
					f.retry()
					f.assertContents(path, []byte("fresh target-only volume"))
				} else {
					before := f.stat(path)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					f.assertTargetVolumeRetryRejected("refusing uncheckpointed destination volume " + name)
					f.assertContents(path, data)
					if !os.SameFile(before, f.stat(path)) {
						t.Fatal("retry replaced uncheckpointed volume")
					}
				}
			})
		}
	}
}

func (f *recoveryFixture) assertTargetVolumeRetryRejected(want string) {
	f.t.Helper()
	before, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
	if err != nil {
		f.t.Fatal(err)
	}
	creates, removes := f.creates, f.removes
	if err := f.storage(false, f.store).Publish(context.Background(), f.source, f.stage); err == nil || !strings.Contains(err.Error(), want) {
		f.t.Fatalf("retry = %v; want %q", err, want)
	}
	after, err := f.store.LoadRecovery(f.source.Mode, f.source.Identity)
	if err != nil || string(after) != string(before) || f.creates != creates || f.removes != removes {
		f.t.Fatalf("rejected retry changed journal/runtime: %v, creates=%d removes=%d", err, f.creates, f.removes)
	}
	r, err := f.store.Load(f.source.Mode, f.source.Identity)
	if err != nil || !reflect.DeepEqual(r, f.source) {
		f.t.Fatalf("rejected retry changed record: %#v, %v", r, err)
	}
}

func TestMaintenanceRecoveryRejectsUncheckpointedAttachment(t *testing.T) {
	f := newRecoveryFixture(t)
	f.createFault = func() error {
		return json.Unmarshal([]byte(`{"configuration":{"mounts":[{"type":{"volume":{"name":"coop-recovery--unprepared"}}}]}}`), f.current)
	}
	err := f.storage(true, f.store).Publish(context.Background(), f.source, f.stage)
	if err == nil || !strings.Contains(err.Error(), "refusing uncheckpointed attached volume coop-recovery--unprepared") {
		t.Fatalf("Publish = %v", err)
	}
	if f.journal().Disks["rootfs"] != (diskIdentity{}) {
		t.Fatal("published disks before attachment ownership validation")
	}
}
