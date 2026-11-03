package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	rt "github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// This test is deliberately opt-in. It creates only the explicitly named pilot,
// leaves its stopped volumes/evidence intact, and never installs a production
// profile, touches an existing bird, submits a model prompt or calls GitHub/Kata.
func TestPortableRuntimePilot(t *testing.T) {
	if os.Getenv("COOP_PORTABLE_PILOT") != "1" {
		t.Skip("opt-in real Apple Container acceptance")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("pilot requires a macOS Apple Container host")
	}
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("missing %s", key)
		}
		return v
	}
	root := required("COOP_PILOT_ROOT")
	name := required("COOP_PILOT_NAME")
	imageA, imageB := required("COOP_PILOT_IMAGE_A"), required("COOP_PILOT_IMAGE_B")
	workerBinary, proxyBinary := required("COOP_PILOT_WORKER"), required("COOP_PILOT_PROXY")
	gateway := required("COOP_PILOT_GATEWAY")
	if !filepath.IsAbs(root) || !strings.HasPrefix(name, "pilot-") || config.ValidateProfileName(name) != nil || imageA == imageB {
		t.Fatal("pilot requires private absolute state, a pilot-* identity and two distinct images")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{"pilot": name, "image_a": imageA, "image_b": imageB, "started_at": time.Now().UTC()}
	t.Cleanup(func() {
		evidence["passed"] = !t.Failed()
		evidence["finished_at"] = time.Now().UTC()
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "evidence", "runtime-pilot.json"), raw, 0600)
	})
	step := func(name string) { t.Log(name); evidence[name] = true }
	write := func(path string, body []byte) {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(root, "pilot-master.sqlite"), MasterID: "portable-pilot-master"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner := c.Principal{ID: "pilot-owner", Alias: "pilot-owner", Kind: "owner", Engagements: []string{"pilot"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	bird := c.Principal{ID: name, Alias: name, Kind: "agent", GitHubBotID: 2, Engagements: []string{"pilot"}, Capabilities: []string{"read", "send"}}
	old := c.ExecutionBinding{MasterID: "portable-pilot-master", Placement: c.Placement{AgentID: name, HostID: "pilot-host", InstanceID: strings.Repeat("a", 64), Generation: 1}}
	next := old
	next.InstanceID = strings.Repeat("b", 64)
	next.Generation = 2
	if err := store.Configure(ctx, []c.Principal{owner, bird}, []c.Placement{old.Placement}); err != nil {
		t.Fatal(err)
	}
	oldToken, newToken := rand.Text(), rand.Text()
	token := func(value string, p c.Placement) master.Token {
		sum := sha256.Sum256([]byte(value))
		return master.Token{PrincipalID: name, SHA256: hex.EncodeToString(sum[:]), InstanceID: p.InstanceID, Generation: p.Generation}
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
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	oldClient, err := masterclient.New(masterclient.ClientConfig{URL: url, MasterID: old.MasterID, Token: oldToken, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	newClient, err := masterclient.New(masterclient.ClientConfig{URL: url, MasterID: old.MasterID, Token: newToken, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "old-token"), []byte(oldToken))
	write(filepath.Join(root, "new-token"), []byte(newToken))
	clientConfig := func(which string) string {
		path := filepath.Join(root, which+"-client.toml")
		write(path, []byte(fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", url, old.MasterID, filepath.Join(root, which+"-token"))))
		return path
	}
	oldConfig, newConfig := clientConfig("old"), clientConfig("new")
	cfg := config.Default()
	cfg.Seeds = nil
	cfg.IncludeCredentials = nil
	cfg.Resources = config.Resources{CPUs: 2, Memory: "4G"}
	profile := config.Profile{Runtime: "container", Command: []string{"sleep", "infinity"}, Execution: &config.AgentExecution{ExecutionBinding: old, ClientConfig: oldConfig}, Services: map[string]config.Service{"server": {Command: []string{"/usr/local/libexec/pilot-opencode"}}, "worker": {Command: []string{"/bin/true"}}}}
	spec := workspace.Spec{Mode: workspace.Agent, Identity: name, RuntimeName: "coop-agent-" + name, Home: "/root", Workdir: "/workspace", Image: imageA, Config: cfg, Profile: profile}
	spec.Fingerprint = workspace.Fingerprint(spec)
	ownership := workspace.FileStore{Root: filepath.Join(root, "ownership")}
	backend := workspace.ContainerBackend{Runtime: rt.AppleMachine{}, Store: ownership, Guard: executionGuard}
	session := Session{Spec: spec, Controller: workspace.Controller{Backend: backend, Store: ownership}, Runner: backend, CredentialManager: credential.NewManager(filepath.Join(root, "credentials")), Output: io.Discard}
	if err := session.Controller.Ensure(ctx, spec); err != nil {
		t.Fatal("create isolated pilot", err)
	}
	t.Cleanup(func() {
		stopCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		_, _ = (rt.AppleMachine{}).Output(stopCtx, "stop", spec.RuntimeName)
	})
	guest := func(input io.Reader, args ...string) []byte {
		var stdout, stderr bytes.Buffer
		if err := backend.Exec(ctx, spec.RuntimeName, "/", args, input, &stdout, &stderr); err != nil {
			var journal bytes.Buffer
			_ = backend.Exec(ctx, spec.RuntimeName, "/", []string{"journalctl", "--no-pager", "-u", "coop-helper@server.service", "-n", "20"}, nil, &journal, io.Discard)
			t.Fatalf("pilot command %s failed: %v\nstdout: %s\nstderr: %s\nserver: %s", args[0], err, stdout.String(), stderr.String(), journal.String())
		}
		return stdout.Bytes()
	}
	guestWrite := func(path string, body []byte, mode string) {
		guest(bytes.NewReader(body), "/bin/bash", "-euc", `umask 077; mkdir -p -- "$(dirname "$1")"; cat > "$1"; chmod "$2" "$1"`, "pilot-write", path, mode)
	}
	for host, guestPath := range map[string]string{workerBinary: "/usr/local/libexec/coop-worker", proxyBinary: "/usr/local/libexec/pilot-master-proxy"} {
		body, err := os.ReadFile(host)
		if err != nil {
			t.Fatal(err)
		}
		guestWrite(guestPath, body, "0700")
	}
	guestWrite("/var/lib/coop/pilot/opencode-password", []byte("synthetic-pilot-password"), "0600")
	guestWrite("/usr/local/libexec/pilot-opencode", []byte("#!/bin/bash\nset -euo pipefail\nexport HOME=/root\nexport OPENCODE_SERVER_PASSWORD=$(cat /var/lib/coop/pilot/opencode-password)\nexec /usr/local/bin/coop-user-env -- opencode serve --hostname 127.0.0.1 --port 4096\n"), "0700")
	proxyStart := func() {
		guest(nil, "systemd-run", "--unit=coop-pilot-master-proxy", "/usr/local/libexec/pilot-master-proxy", "--upstream", fmt.Sprintf("http://%s:%d", gateway, port))
	}
	proxyStart()
	base, err := os.ReadFile(filepath.Join("..", "..", "examples", "worker-master.json"))
	if err != nil {
		t.Fatal(err)
	}
	var workerConfig map[string]any
	if err := json.Unmarshal(base, &workerConfig); err != nil {
		t.Fatal(err)
	}
	workerConfig["master"] = map[string]any{"backend": "master", "client_config": "/var/lib/coop/pilot/old-client.toml", "master_id": old.MasterID, "engagement": "pilot", "agent_id": name, "host_id": old.HostID, "worker_instance_id": old.InstanceID, "generation": 1}
	raw, err := json.Marshal(workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	guestWrite("/var/lib/coop/worker/config.json", raw, "0600")
	guestWrite("/var/lib/coop/pilot/old-config.json", raw, "0600")
	for which, value := range map[string]string{"old": oldToken, "new": newToken} {
		guestWrite("/var/lib/coop/pilot/"+which+"-token", []byte(value), "0600")
		guestWrite("/var/lib/coop/pilot/"+which+"-client.toml", []byte(fmt.Sprintf("url = \"http://127.0.0.1:17744\"\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", old.MasterID, "/var/lib/coop/pilot/"+which+"-token")), "0600")
	}
	guest(nil, "/usr/local/libexec/coop-worker", "init-state", "--config", "/var/lib/coop/worker/config.json")
	if err := session.Up(ctx, nil); err != nil {
		t.Fatal("start pilot services", err)
	}
	guest(nil, "systemctl", "is-active", "coop-helper@server.service")
	step("boot-and-installed-config")
	curl := func(method, path string, body []byte) []byte {
		args := []string{"curl", "--silent", "--show-error", "--fail", "--retry", "10", "--retry-connrefused", "--retry-delay", "1", "--max-time", "10", "--user", "opencode:synthetic-pilot-password", "--request", method, "--header", "Content-Type: application/json"}
		var input io.Reader
		if body != nil {
			args = append(args, "--data-binary", "@-")
			input = bytes.NewReader(body)
		}
		return guest(input, append(args, "http://127.0.0.1:4096"+path)...)
	}
	health := curl("GET", "/global/health", nil)
	evidence["health_before"] = json.RawMessage(health)
	guest(nil, "mkdir", "-p", "/workspace/pilot-project")
	created := curl("POST", "/session?directory=/workspace/pilot-project", []byte(`{"title":"portable pilot session: no model prompt"}`))
	var native struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &native) != nil || native.ID == "" {
		t.Fatal("native session was not created")
	}
	evidence["native_session_id"] = native.ID
	markers := map[string]string{"/root/pilot-home.marker": "home survives", "/workspace/pilot-project/source.txt": "checkpointed source survives", "/var/lib/coop/pilot/action-receipt.json": `{"request_id":"synthetic-action","state":"completed","effects":1}`, "/usr/local/libexec/pilot-managed.marker": "managed helper survives"}
	for path, body := range markers {
		guestWrite(path, []byte(body), "0600")
	}
	guest(nil, "git", "-C", "/workspace/pilot-project", "init", "-b", "main")
	guest(nil, "git", "-C", "/workspace/pilot-project", "add", "source.txt")
	guest(nil, "git", "-C", "/workspace/pilot-project", "-c", "user.name=Pilot", "-c", "user.email=pilot@example.invalid", "commit", "-m", "pilot local checkpoint")
	checkpoint := strings.TrimSpace(string(guest(nil, "git", "-C", "/workspace/pilot-project", "rev-parse", "HEAD")))
	evidence["checkpoint"] = checkpoint
	markers["/workspace/pilot-project/source.txt"] = "uncommitted work survives too"
	guestWrite("/workspace/pilot-project/source.txt", []byte(markers["/workspace/pilot-project/source.txt"]), "0600")
	memoryInput := []byte(`{"key":"pilot/knowledge","body":"Synthetic knowledge survives container replacement.","expected_revision":0}`)
	guestWrite("/var/lib/coop/pilot/memory.json", memoryInput, "0600")
	guest(nil, "/usr/local/libexec/coop-worker", "memory", "write", "--config", "/var/lib/coop/worker/config.json", "--input", "/var/lib/coop/pilot/memory.json")
	step("native-session-checkpoint-and-memory")
	if err := store.Quiesce(ctx, owner, 1); err != nil {
		t.Fatal(err)
	}
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json", "--apply")
	if _, err := store.SetPlacement(ctx, owner, c.PlacementChange{Placement: next.Placement, ExpectedGeneration: 1, RestoreEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.ReadMemory(ctx, c.MemoryQuery{Key: "pilot/knowledge"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("old token was not fenced", err)
	}
	if _, err := newClient.ReadMemory(ctx, c.MemoryQuery{Key: "pilot/knowledge"}); err != nil {
		t.Fatal("new token lost memory", err)
	}
	workerMaster := workerConfig["master"].(map[string]any)
	workerMaster["generation"] = 2
	workerMaster["worker_instance_id"] = next.InstanceID
	workerMaster["client_config"] = "/var/lib/coop/pilot/new-client.toml"
	raw, err = json.Marshal(workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	guestWrite("/var/lib/coop/worker/config.json", raw, "0600")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "adopt-placement", "--config", "/var/lib/coop/worker/config.json", "--previous-config", "/var/lib/coop/pilot/old-config.json")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json")
	guest(nil, "/usr/local/libexec/coop-worker", "comms", "reconcile", "--config", "/var/lib/coop/worker/config.json", "--apply")
	if err := store.Resume(ctx, owner, 1); err != nil {
		t.Fatal(err)
	}
	step("inventory-adoption-and-token-fencing")
	if err := session.Down(ctx); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := backend.Inspect(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	newSpec := spec
	newProfile := profile
	newProfile.Execution = &config.AgentExecution{ExecutionBinding: next, ClientConfig: newConfig}
	newSpec.Profile = newProfile
	newSpec.Image = imageB
	newSpec.Fingerprint = workspace.Fingerprint(newSpec)
	interrupted := false
	broken := backend
	broken.Runtime.Command = func(ctx context.Context, args []string, input io.Reader, out, stderr io.Writer) error {
		if args[0] == "create" && !interrupted {
			interrupted = true
			return errors.New("pilot injected interruption after old root removal")
		}
		cmd := exec.CommandContext(ctx, "container", args...)
		cmd.Stdin = input
		cmd.Stdout = out
		cmd.Stderr = stderr
		return cmd.Run()
	}
	controller := workspace.Controller{Backend: broken, Store: ownership}
	if err := controller.Apply(ctx, newSpec); err == nil || !interrupted {
		t.Fatal("replacement interruption was not exercised", err)
	}
	if err := controller.Ensure(ctx, newSpec); err == nil {
		t.Fatal("ordinary startup bypassed interrupted replacement")
	}
	controller.Backend = backend
	if err := controller.Apply(ctx, newSpec); err != nil {
		t.Fatal("resume replacement", err)
	}
	newInfo, err := backend.Inspect(ctx, newSpec)
	if err != nil {
		t.Fatal(err)
	}
	if newInfo.State != rt.StateStopped || newInfo.RuntimeInstance == oldInfo.RuntimeInstance {
		t.Fatal("replacement did not leave a new stopped incarnation")
	}
	evidence["incarnation_before"], evidence["incarnation_after"] = oldInfo.RuntimeInstance, newInfo.RuntimeInstance
	step("interrupted-image-replacement")
	session.Spec = newSpec
	session.Controller = controller
	if err := controller.Ensure(ctx, newSpec); err != nil {
		t.Fatal(err)
	}
	proxyStart()
	if err := session.Up(ctx, nil); err != nil {
		t.Fatal("start replaced pilot", err)
	}
	for path, want := range markers {
		if got := string(guest(nil, "cat", path)); got != want {
			t.Fatalf("durable marker changed: %s", path)
		}
	}
	if got := strings.TrimSpace(string(guest(nil, "git", "-C", "/workspace/pilot-project", "rev-parse", "HEAD"))); got != checkpoint {
		t.Fatal("checkpoint changed")
	}
	retained := curl("GET", "/session/"+native.ID+"?directory=/workspace/pilot-project", nil)
	var after struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(retained, &after) != nil || after.ID != native.ID {
		t.Fatal("native session did not survive")
	}
	health = curl("GET", "/global/health", nil)
	evidence["health_after"] = json.RawMessage(health)
	var ready struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if json.Unmarshal(health, &ready) != nil || !ready.Healthy || ready.Version != worker.SupportedOpenCodeVersion {
		t.Fatal("replacement image is not worker-compatible", string(health))
	}
	var sessions []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(curl("GET", "/session?directory=/workspace/pilot-project", nil), &sessions) != nil || len(sessions) != 1 || sessions[0].ID != native.ID {
		t.Fatal("native session was duplicated or lost", sessions)
	}
	guestWrite("/var/lib/coop/pilot/memory-read.json", []byte(`{"key":"pilot/knowledge"}`), "0600")
	read := guest(nil, "/usr/local/libexec/coop-worker", "memory", "read", "--config", "/var/lib/coop/worker/config.json", "--input", "/var/lib/coop/pilot/memory-read.json")
	var memory c.Memory
	if json.Unmarshal(read, &memory) != nil || memory.Revision != 1 || memory.Body != "Synthetic knowledge survives container replacement." {
		t.Fatal("memory changed", string(read))
	}
	retry := guest(nil, "/usr/local/libexec/coop-worker", "memory", "write", "--config", "/var/lib/coop/worker/config.json", "--input", "/var/lib/coop/pilot/memory.json")
	if json.Unmarshal(retry, &memory) != nil || memory.Revision != 1 {
		t.Fatal("retry duplicated durable memory write", string(retry))
	}
	if _, err := oldClient.ReadMemory(ctx, c.MemoryQuery{Key: "pilot/knowledge"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("old token regained access after replacement", err)
	}
	step("state-native-session-and-memory-survive")
	if err := session.Down(ctx); err != nil {
		t.Fatal(err)
	}
	step("pilot-stopped-production-untouched")
}
