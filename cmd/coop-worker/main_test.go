package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
)

type readOnlyGitHub struct {
	t     *testing.T
	reads int
}

func (g *readOnlyGitHub) Snapshot(context.Context, worker.Config, worker.Snapshot) (worker.Snapshot, error) {
	g.reads++
	return worker.Snapshot{Complete: true}, nil
}
func (g *readOnlyGitHub) Claim(context.Context, worker.Job, string) (worker.Claim, error) {
	g.t.Fatal("dry-run wrote a claim")
	return worker.Claim{}, nil
}
func (g *readOnlyGitHub) Reconcile(context.Context, worker.Job) (worker.Artifacts, error) {
	g.t.Fatal("dry-run reconciled writes")
	return worker.Artifacts{}, nil
}
func (g *readOnlyGitHub) SyncTask(context.Context, worker.Job, string, string) error {
	g.t.Fatal("dry-run wrote a Project")
	return nil
}
func (g *readOnlyGitHub) ReportBlock(context.Context, worker.Job, string, string) error {
	g.t.Fatal("dry-run posted a comment")
	return nil
}
func TestWorkerDryRunOnlyReadsAndDoesNotCreateState(t *testing.T) {
	path := cliConfig(t)
	c, err := worker.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	old := clients
	t.Cleanup(func() { clients = old })
	g := &readOnlyGitHub{t: t}
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) { return g, nil, nil }
	var out bytes.Buffer
	if err := run([]string{"dry-run", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if g.reads != 1 || !bytes.Contains(out.Bytes(), []byte(`"jobs":[]`)) {
		t.Fatal("dry-run did not inspect GitHub")
	}
	if _, err := os.Stat(c.StateDir); !os.IsNotExist(err) {
		t.Fatal("dry-run created worker state")
	}
}

func cliConfig(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := worker.Config{Version: 1, Name: "builder", Role: "implementer", Model: "example/model", StateDir: filepath.Join(dir, "state"), WorkRoot: filepath.Join(dir, "work"), Owner: worker.Actor{ID: 1, Login: "owner", Kind: "User"}, Self: worker.Actor{ID: 2, Login: "builder[bot]", Kind: "Bot"}, Implementer: worker.Actor{ID: 2, Login: "builder[bot]", Kind: "Bot"}, Reviewer: worker.Actor{ID: 3, Login: "reviewer[bot]", Kind: "Bot"}, CodeOwners: []string{"code"}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "worker.json")
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("config write")
	}
	return path
}
func TestWorkerCLIValidateStatusAndLocalPause(t *testing.T) {
	path := cliConfig(t)
	var out bytes.Buffer
	if err := run([]string{"validate", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"valid":true`)) {
		t.Fatal("validation result missing")
	}
	out.Reset()
	if err := run([]string{"status", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"state":"not_running"`)) {
		t.Fatal("absent worker appeared running")
	}
	for _, command := range []string{"pause", "resume"} {
		out.Reset()
		if err := run([]string{command, "--config", path}, &out); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"ping", "--config", path}, &out); err == nil {
		t.Fatal("v2 ping accidentally implemented")
	}
}
func TestWorkerCLIRejectsCredentialFlagsAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{{"status", "--config", "/absent", "--token", "sentinel-secret"}, {"unknown"}, {"run"}} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil || bytes.Contains(out.Bytes(), []byte("sentinel-secret")) {
			t.Fatal("invalid CLI request accepted or echoed")
		}
	}
}

func TestGuestPingStrictInputLocalOnlyAndRedactedRequests(t *testing.T) {
	path := cliConfig(t)
	c, err := worker.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c.DiscussionRepositories = []string{"planning/board"}
	c.Wake = &worker.WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) {
		t.Fatal("local ping constructed clients")
		return nil, nil, nil
	}
	store := worker.NewFileStore(c.StateDir)
	release, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.WriteStatus(worker.Status{Enabled: true, WakeEnabled: true, State: "paused", Heartbeat: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"unknown":"sentinel"}`, `{"prompt":"sentinel"} {}`, `{"request_id":"sentinel"}`, `{"prompt":"\u0000sentinel"}`} {
		var out bytes.Buffer
		err := runInput([]string{"ping", "--config", path}, strings.NewReader(input), &out)
		if err == nil || strings.Contains(err.Error(), "sentinel") || strings.Contains(out.String(), "sentinel") {
			t.Fatal("bad ping accepted or leaked")
		}
	}
	prompt := "`literal` $(sentinel)\n café"
	payload, _ := json.Marshal(worker.PingInput{Prompt: prompt})
	var out bytes.Buffer
	if err := runInput([]string{"ping", "--config", path}, bytes.NewReader(payload), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sentinel") {
		t.Fatal("summary leaked prompt")
	}
	var summary worker.WakeSummary
	if json.Unmarshal(out.Bytes(), &summary) != nil || summary.Phase != "queued" {
		t.Fatal("ping not queued while paused")
	}
	out.Reset()
	if err := run([]string{"requests", "--config", path, "--request-id", summary.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sentinel") || !strings.Contains(out.String(), summary.ID) {
		t.Fatal("request output not redacted")
	}
	release()
	out.Reset()
	if err := runInput([]string{"ping", "--config", path}, bytes.NewReader(payload), &out); err == nil {
		t.Fatal("fresh but stopped worker accepted")
	}
}
