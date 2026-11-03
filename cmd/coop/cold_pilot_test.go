package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sarcasticbird/coop/internal/backup"
	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	rt "github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"golang.org/x/sys/unix"
)

// Restore an existing, stopped TestPortableRuntimePilot result without booting
// or modifying the source. All writes go to fresh private state and a fresh
// pilot restore target. No provider credentials, model prompts or GitHub calls.
func TestPortableColdRestorePilot(t *testing.T) {
	if os.Getenv("COOP_COLD_PILOT") != "1" {
		t.Skip("opt-in real cold-disk acceptance")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("requires Apple Container on macOS")
	}
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("missing %s", key)
		}
		return v
	}
	root, sourceRoot := required("COOP_COLD_ROOT"), required("COOP_COLD_SOURCE_ROOT")
	name, cli, gateway := required("COOP_COLD_NAME"), required("COOP_COLD_CLI"), required("COOP_COLD_GATEWAY")
	if !filepath.IsAbs(root) || !filepath.IsAbs(sourceRoot) || !filepath.IsAbs(cli) || !strings.HasPrefix(name, "pilot-") || config.ValidateProfileName(name) != nil {
		t.Fatal("requires absolute private paths and a fresh pilot-* target")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	var prior struct {
		Pilot      string `json:"pilot"`
		Passed     bool   `json:"passed"`
		Session    string `json:"native_session_id"`
		Checkpoint string `json:"checkpoint"`
	}
	raw, err := os.ReadFile(filepath.Join(sourceRoot, "evidence", "runtime-pilot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &prior) != nil || !prior.Passed || !strings.HasPrefix(prior.Pilot, "pilot-") || prior.Pilot == name || prior.Session == "" || prior.Checkpoint == "" {
		t.Fatal("requires successful isolated source-pilot evidence")
	}
	source, err := (workspace.FileStore{Root: filepath.Join(sourceRoot, "ownership")}).Load(workspace.Agent, prior.Pilot)
	if err != nil {
		t.Fatal(err)
	}
	if source.Applied.Runtime != "container" || source.Applied.Execution == nil {
		t.Fatal("requires a placement-bound portable pilot")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("use fresh private state; previous evidence is never overwritten", err)
	}
	evidence := map[string]any{"source": source.RuntimeName, "target": "coop-agent-" + name, "started_at": time.Now().UTC()}
	t.Cleanup(func() {
		evidence["passed"] = !t.Failed()
		evidence["finished_at"] = time.Now().UTC()
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "cold-pilot.json"), raw, 0600)
	})
	step := func(s string) { t.Log(s); evidence[s] = true }
	write := func(path string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	machine := rt.AppleMachine{}
	rows, err := machine.NativeContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Configuration.ID == "coop-agent-"+name {
			t.Fatal("target already exists")
		}
		if row.Configuration.ID == source.RuntimeName {
			found = true
			if row.Status.State != "stopped" {
				t.Fatal("source must already be stopped")
			}
		}
	}
	if !found {
		t.Fatal("source is absent")
	}
	volumes, err := machine.NativeVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range volumes {
		if strings.HasPrefix(v, "coop-agent-"+name+"--") {
			t.Fatal("target volumes already exist")
		}
	}
	t.Cleanup(func() {
		stopCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		_, _ = machine.Output(stopCtx, "stop", "coop-agent-"+name)
	})

	// Copy only the closed synthetic master's database. Refuse an uncheckpointed
	// WAL instead of copying a potentially inconsistent SQLite state.
	dbPath := filepath.Join(sourceRoot, "pilot-master.sqlite")
	if info, err := os.Stat(dbPath + "-wal"); err == nil && info.Size() != 0 {
		t.Fatal("source master has an uncheckpointed WAL")
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "master.sqlite"), raw)
	raw, err = os.ReadFile(dbPath + ".epoch")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "master.sqlite.epoch"), raw)
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(root, "master.sqlite"), MasterID: source.Applied.Execution.MasterID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	old := *source.Applied.Execution
	next := old
	next.Generation++
	next.InstanceID = strings.Repeat("c", 64)
	oldRaw, err := os.ReadFile(filepath.Join(sourceRoot, "new-token"))
	if err != nil {
		t.Fatal(err)
	}
	oldToken, newToken := string(oldRaw), rand.Text()
	token := func(value string, p c.Placement) master.Token {
		sum := sha256.Sum256([]byte(value))
		return master.Token{PrincipalID: old.AgentID, SHA256: hex.EncodeToString(sum[:]), InstanceID: p.InstanceID, Generation: p.Generation}
	}
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1", gateway}, Tokens: []master.Token{token(oldToken, old.Placement), token(newToken, next.Placement)}}
	server := httptest.NewUnstartedServer(master.NewHandler(&master.Service{Store: store}, policy))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	// httptest's URL can be 0.0.0.0; clients must use explicit loopback.
	clientURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	newClient, err := masterclient.New(masterclient.ClientConfig{URL: clientURL, MasterID: old.MasterID, Token: newToken, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	oldClient, err := masterclient.New(masterclient.ClientConfig{URL: clientURL, MasterID: old.MasterID, Token: oldToken, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := func(which, value string) string {
		path := filepath.Join(root, which+"-client.toml")
		tokenPath := filepath.Join(root, which+"-token")
		write(tokenPath, []byte(value))
		write(path, []byte(fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", clientURL, old.MasterID, tokenPath)))
		return path
	}
	oldConfig, newConfig := clientConfig("old", oldToken), clientConfig("new", newToken)
	profile := config.Profile{Runtime: "container", Command: source.Applied.Command, Orgs: source.Applied.Orgs, Restart: source.Applied.Restart, Resources: source.Applied.Resources, Services: source.Applied.Services, Execution: &config.AgentExecution{ExecutionBinding: old, ClientConfig: oldConfig}}
	configHome, stateHome := filepath.Join(root, "config"), filepath.Join(root, "state")
	ownership := workspace.FileStore{Root: filepath.Join(stateHome, "coop", "workspaces")}
	if err := ownership.Save(source); err != nil {
		t.Fatal(err)
	}
	setProfiles := func(binding c.ExecutionBinding, client string) {
		target := profile
		target.Execution = &config.AgentExecution{ExecutionBinding: binding, ClientConfig: client}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(struct {
			Profiles map[string]config.Profile `toml:"profiles"`
		}{map[string]config.Profile{source.Identity: profile, name: target}}); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(configHome, "coop", "coop.toml"), buf.Bytes())
	}
	setProfiles(old, oldConfig)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome, "XDG_STATE_HOME="+stateHome)
		return cmd.CombinedOutput()
	}
	mustRun := func(args ...string) []byte {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	archive := filepath.Join(root, "pilot.zip")
	snapshot, err := (backup.AppleStorage{Machine: machine}).Snapshot(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]pilotDiskProof{}
	sources := map[string]backup.Payload{}
	for _, p := range snapshot.Payloads {
		sources[p.Name] = p
		f, err := p.Open()
		if err != nil {
			t.Fatal(err)
		}
		proof, err := provePilotDisk(f.(*os.File))
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		before[p.Name] = proof
	}
	mustRun("--agent", source.Identity, "backup", archive)
	if out, err := run("--agent", source.Identity, "backup", archive); err == nil || !bytes.Contains(out, []byte("already exists")) {
		t.Fatal("backup overwrote evidence", err, string(out))
	}
	manifest, err := backup.ReadManifest(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 5 || manifest.RuntimeVersion != "1.6.1" {
		t.Fatal("incomplete/unexpected runtime archive", manifest.RuntimeVersion, len(manifest.Files))
	}
	for _, file := range manifest.Files {
		if file.SHA256 != before[file.Name].SHA256 {
			t.Fatal("archive differs from source disk", file.Name)
		}
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive is not private")
	}
	evidence["archive_bytes"] = info.Size()
	step("cli-cold-backup-all-five-disks")
	if out, err := run("--agent", name, "restore", archive); err == nil || !bytes.Contains(out, []byte("next fenced worker placement")) {
		t.Fatal("restore reused a live worker placement", err, string(out))
	}
	step("live-placement-clone-refused")
	owner := c.Principal{ID: "pilot-owner"}
	bird := c.Principal{ID: old.AgentID}
	if err := store.Quiesce(ctx, owner, 1); err != nil {
		t.Fatal(err)
	}
	// This source fixture submitted no model turn or consequential work. Its
	// accepted recovery inventory is empty; the copied master verifies that.
	if _, err := store.ReconcileGuest(ctx, bird, c.GuestRecovery{Placement: old.Placement, RestoreEpoch: 1, Complete: true, ManifestSHA256: c.RecoveryDigest(nil)}); err != nil {
		t.Fatal("source inventory is not empty/accepted", err)
	}
	if _, err := store.SetPlacement(ctx, owner, c.PlacementChange{Placement: next.Placement, ExpectedGeneration: old.Generation, RestoreEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.Self(ctx); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("old placement token remains live", err)
	}
	setProfiles(next, newConfig)
	mustRun("--agent", name, "restore", archive)
	target, err := ownership.Load(workspace.Agent, name)
	if err != nil {
		t.Fatal(err)
	}
	if target.Applied.Execution == nil || *target.Applied.Execution != next {
		t.Fatal("target ownership has the wrong placement")
	}
	rows, err = machine.NativeContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Configuration.ID == target.RuntimeName && row.Status.State != "stopped" {
			t.Fatal("restore target is not cold")
		}
	}
	after, err := (backup.AppleStorage{Machine: machine}).Snapshot(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	restored := map[string]pilotDiskProof{}
	for _, p := range after.Payloads {
		f, err := p.Open()
		if err != nil {
			t.Fatal(err)
		}
		proof, err := provePilotDisk(f.(*os.File))
		if err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		original, err := sources[p.Name].Open()
		if err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		compareErr := comparePilotDisks(original.(*os.File), f.(*os.File), proof.Size)
		_ = original.Close()
		_ = f.Close()
		if compareErr != nil {
			t.Fatal("restored logical bytes differ", p.Name, compareErr)
		}
		if proof.Size != before[p.Name].Size || proof.Allocated >= proof.Size/10 {
			t.Fatal("restored disk changed or lost sparseness", p.Name, proof)
		}
		restored[p.Name] = proof
	}
	evidence["source_disks"] = before
	evidence["restored_disks"] = restored
	step("cli-restore-stopped-checksummed-sparse-disks")
	if out, err := run("--agent", name, "restore", archive); err == nil || !bytes.Contains(out, []byte("already exists")) {
		t.Fatal("restore overwrote existing target", err, string(out))
	}
	if out, err := run("--agent", name, "up"); err == nil || !bytes.Contains(out, []byte("installed worker does not match")) {
		t.Fatal("stale restored worker admitted", err, string(out))
	}
	guest := func(input io.Reader, args ...string) []byte {
		var out, stderr bytes.Buffer
		if err := machine.NativeExec(ctx, target.RuntimeName, "/", args, input, &out, &stderr); err != nil {
			t.Fatalf("target guest %s: %v\n%s", args[0], err, stderr.String())
		}
		return out.Bytes()
	}
	guest(nil, "/bin/bash", "-euc", "! systemctl is-active --quiet coop-helper@server.service; ! systemctl is-active --quiet coop-helper@worker.service")
	step("stale-restored-worker-blocks-service-start")
	guestWrite := func(path string, body []byte) {
		guest(bytes.NewReader(body), "/bin/bash", "-euc", `umask 077; cat > "$1"`, "pilot-write", path)
	}
	previous := guest(nil, "cat", "/var/lib/coop/worker/config.json")
	guestWrite("/var/lib/coop/pilot/restore-previous.json", previous)
	var cfg map[string]any
	if json.Unmarshal(previous, &cfg) != nil {
		t.Fatal("invalid restored worker config")
	}
	binding := cfg["master"].(map[string]any)
	binding["generation"] = next.Generation
	binding["worker_instance_id"] = next.InstanceID
	binding["client_config"] = "/var/lib/coop/pilot/restore-client.toml"
	raw, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	guestWrite("/var/lib/coop/worker/config.json", raw)
	guestWrite("/var/lib/coop/pilot/restore-token", []byte(newToken))
	guestWrite("/var/lib/coop/pilot/restore-client.toml", []byte(fmt.Sprintf("url = \"http://127.0.0.1:17744\"\nmaster_id = %q\ntoken_file = \"/var/lib/coop/pilot/restore-token\"\nallow_loopback_http = true\n", old.MasterID)))
	guest(nil, "systemd-run", "--unit=coop-pilot-master-proxy", "/usr/local/libexec/pilot-master-proxy", "--upstream", fmt.Sprintf("http://%s:%d", gateway, port))
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "adopt-placement", "--config", "/var/lib/coop/worker/config.json", "--previous-config", "/var/lib/coop/pilot/restore-previous.json")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json", "--apply")
	if err := store.Resume(ctx, owner, 1); err != nil {
		t.Fatal(err)
	}
	mustRun("--agent", name, "up")
	step("restored-worker-adopts-next-placement")
	for path, want := range map[string]string{"/root/pilot-home.marker": "home survives", "/workspace/pilot-project/source.txt": "uncommitted work survives too", "/usr/local/libexec/pilot-managed.marker": "managed helper survives", "/var/lib/coop/pilot/action-receipt.json": `{"request_id":"synthetic-action","state":"completed","effects":1}`} {
		if string(guest(nil, "cat", path)) != want {
			t.Fatal("restored marker changed", path)
		}
	}
	if strings.TrimSpace(string(guest(nil, "git", "-C", "/workspace/pilot-project", "rev-parse", "HEAD"))) != prior.Checkpoint {
		t.Fatal("restored checkpoint changed")
	}
	var native struct {
		ID string `json:"id"`
	}
	response := guest(nil, "curl", "--silent", "--show-error", "--fail", "--retry", "10", "--retry-connrefused", "--retry-delay", "1", "--user", "opencode:synthetic-pilot-password", "http://127.0.0.1:4096/session/"+prior.Session+"?directory=/workspace/pilot-project")
	if json.Unmarshal(response, &native) != nil || native.ID != prior.Session {
		t.Fatal("native session was not restored")
	}
	evidence["native_session_id"], evidence["checkpoint"] = native.ID, prior.Checkpoint
	var memory c.Memory
	response = guest(nil, "/usr/local/libexec/coop-worker", "memory", "read", "--config", "/var/lib/coop/worker/config.json", "--input", "/var/lib/coop/pilot/memory-read.json")
	if json.Unmarshal(response, &memory) != nil || memory.Revision != 1 || memory.Body != "Synthetic knowledge survives container replacement." {
		t.Fatal("restored guest cannot read preserved SQLite memory")
	}
	if _, err := newClient.Self(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.Self(ctx); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("old token regained access", err)
	}
	step("native-session-work-state-helpers-and-memory-preserved")
	mustRun("--agent", name, "down")
	for _, p := range snapshot.Payloads {
		f, err := p.Open()
		if err != nil {
			t.Fatal(err)
		}
		proof, err := provePilotDisk(f.(*os.File))
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(proof, before[p.Name]) {
			t.Fatal("source disk was modified", p.Name)
		}
	}
	step("source-disks-unchanged-target-stopped")
}

type pilotDiskProof struct {
	SHA256                   string
	Size, Allocated, ModTime int64
	Device, Inode            uint64
}

// Hash the exact sparse stream used in Coop archives, independently of ZIP.
// Never scan hundreds of GiB of holes or mutate a runtime disk.
func provePilotDisk(f *os.File) (pilotDiskProof, error) {
	info, err := f.Stat()
	if err != nil {
		return pilotDiskProof{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return pilotDiskProof{}, err
	}
	proof := pilotDiskProof{Size: info.Size(), Allocated: stat.Blocks * 512, ModTime: info.ModTime().UnixNano(), Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}
	h := sha256.New()
	if err := binary.Write(h, binary.LittleEndian, uint64(info.Size())); err != nil {
		return proof, err
	}
	for offset := int64(0); offset < info.Size(); {
		data, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
		if err == unix.ENXIO {
			break
		}
		if err != nil {
			return proof, err
		}
		hole, err := unix.Seek(int(f.Fd()), data, unix.SEEK_HOLE)
		if err != nil {
			return proof, err
		}
		if hole > info.Size() {
			hole = info.Size()
		}
		if data < offset || hole <= data {
			return proof, fmt.Errorf("invalid sparse extent")
		}
		if err := binary.Write(h, binary.LittleEndian, uint64(data)); err != nil {
			return proof, err
		}
		if err := binary.Write(h, binary.LittleEndian, uint64(hole-data)); err != nil {
			return proof, err
		}
		if _, err := f.Seek(data, io.SeekStart); err != nil {
			return proof, err
		}
		if _, err := io.CopyN(h, f, hole-data); err != nil {
			return proof, err
		}
		offset = hole
	}
	if err := binary.Write(h, binary.LittleEndian, ^uint64(0)); err != nil {
		return proof, err
	}
	proof.SHA256 = hex.EncodeToString(h.Sum(nil))
	return proof, nil
}

// APFS may coalesce restored zero-filled extents differently. Compare the union
// of both disks' allocated ranges, not their allocation-dependent sparse stream
// hashes. Bytes outside that union are holes on both disks and therefore zero.
func comparePilotDisks(a, b *os.File, size int64) error {
	type extent struct{ start, end int64 }
	var ranges []extent
	for _, f := range []*os.File{a, b} {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() != size {
			return fmt.Errorf("disk size mismatch")
		}
		for offset := int64(0); offset < size; {
			start, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
			if err == unix.ENXIO {
				break
			}
			if err != nil {
				return err
			}
			end, err := unix.Seek(int(f.Fd()), start, unix.SEEK_HOLE)
			if err != nil {
				return err
			}
			if end > size {
				end = size
			}
			if start < offset || end <= start {
				return fmt.Errorf("invalid sparse extent")
			}
			ranges = append(ranges, extent{start, end})
			offset = end
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	left, right := make([]byte, 1<<20), make([]byte, 1<<20)
	var checked int64
	for _, r := range ranges {
		for offset := max(checked, r.start); offset < r.end; {
			n := min(int64(len(left)), r.end-offset)
			if _, err := a.ReadAt(left[:n], offset); err != nil {
				return err
			}
			if _, err := b.ReadAt(right[:n], offset); err != nil {
				return err
			}
			if !bytes.Equal(left[:n], right[:n]) {
				return fmt.Errorf("content differs at offset %d", offset)
			}
			offset += n
		}
		checked = max(checked, r.end)
	}
	return nil
}

func TestColdPilotComparesLogicalBytesAcrossDifferentSparseAllocation(t *testing.T) {
	files := make([]*os.File, 2)
	for i := range files {
		f, err := os.CreateTemp(t.TempDir(), "disk")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		files[i] = f
		if err := f.Truncate(1 << 30); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte("kept"), 4096); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := files[1].WriteAt(make([]byte, 16384), 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := comparePilotDisks(files[0], files[1], 1<<30); err != nil {
		t.Fatal("zero allocation is not a content change", err)
	}
	if _, err := files[1].WriteAt([]byte("unexpected"), 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := comparePilotDisks(files[0], files[1], 1<<30); err == nil {
		t.Fatal("extra nonzero extent was ignored")
	}
}
