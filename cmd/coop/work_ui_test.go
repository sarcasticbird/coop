package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"github.com/spf13/cobra"
)

func TestWorkUIEntryUsesPinnedMasterAndProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "kata")
	project := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	issue := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	// Kata's optional --inbound value must be attached with '='. A separate
	// actor becomes an extra positional argument, which native show rejects.
	script := "#!/bin/sh\nfor arg do [ \"$arg\" != --inbound ] || exit 2; done\ncase \"$*\" in\n*'daemon locate'*) echo '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix:///tmp/kata.sock\"}';;\n*'projects show'*) echo '{\"project\":{\"uid\":\"" + project + "\"}}';;\n*'show '*) echo '{\"issue\":{\"uid\":\"" + issue + "\",\"project_uid\":\"" + project + "\"}}';;\n*) printf '%s\\n' \"$PWD\" \"$KATA_HOME\" \"${KATA_REMOTE-unset}\" \"$@\";;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATA_REMOTE", "must-not-forward")
	cfg := taskstore.Config{Provider: "kata", Binary: binary, Home: dir, ProjectName: "Duplicate display name", ProjectUID: project, PolicySHA: strings.Repeat("a", 64)}
	req := hostrouter.WorkUIRequest{MasterID: "master-a", ProjectUID: project, IssueUID: issue}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	child, err := nativeWorkUICommand(context.Background(), req, "master-a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{binary, "--workspace", dir, "--project", "Duplicate display name", "tui", issue}
	if !reflect.DeepEqual(child.Args, want) {
		t.Fatalf("literal argv %#v want %#v", child.Args, want)
	}
	var out bytes.Buffer
	child.Stdout = &out
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), dir+"\n"+dir+"\nunset\n") {
		t.Fatalf("cwd/environment lost: %s", out.String())
	}
	if after, _ := os.Getwd(); after != cwd {
		t.Fatal("launcher changed controller cwd")
	}
	for _, test := range []struct{ name, master, project, binary string }{
		{"master", "master-b", project, binary},
		{"project", "master-a", "01ARZ3NDEKTSV4RRFFQ69G5FAX", binary},
		{"missing tool", "master-a", project, filepath.Join(dir, "missing")},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := cfg
			c.ProjectUID = test.project
			c.Binary = test.binary
			if _, err := nativeWorkUICommand(context.Background(), req, test.master, c); err == nil {
				t.Fatal("unsafe destination accepted")
			}
		})
	}
	// Even a matching display name cannot substitute for the pinned native UID.
	cfg.ProjectUID = project
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in *'daemon locate'*) echo '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix:///tmp/kata.sock\"}';; *) echo '{\"project\":{\"uid\":\"01ARZ3NDEKTSV4RRFFQ69G5FAX\"}}';; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeWorkUICommand(context.Background(), req, "master-a", cfg); err == nil {
		t.Fatal("duplicate display name selected wrong native project")
	}
}

func TestWorkUIClientDispatchUsesAuthenticatedPins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	project, issue := "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	servedProject, servedMaster := project, "master-a"
	var conversation *c.ConversationSource
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks/capabilities" || r.Header.Get("X-Coop-Expected-Master-ID") != "master-a" || r.Header.Get("Authorization") != "Bearer native-entry-test-token" {
			t.Error("wrong authenticated request")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("X-Coop-Master-ID", servedMaster)
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": servedMaster, "restore_epoch": 1, "data": c.TaskCapabilities{Provider: "kata", ProjectUID: servedProject, ProjectName: "Display name", Conversation: conversation}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	profile := filepath.Join(dir, "coop")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, raw string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(profile, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("master-client.toml", fmt.Sprintf("url=%q\nmaster_id=%q\ntoken_env=%q\nallow_loopback_http=true\n", server.URL, "master-a", "COOP_NATIVE_TEST_TOKEN"))
	t.Setenv("COOP_NATIVE_TEST_TOKEN", "native-entry-test-token")
	write("work-tools.toml", fmt.Sprintf("master_id=%q\nproject_uid=%q\nhost_name=%q\nteam_issue_uid=%q\n", "master-a", project, "rack", issue))
	registry, err := hostrouter.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Change("add", "rack", "owner@rack-alias"); err != nil {
		t.Fatal(err)
	}
	original := remoteExecute
	defer func() { remoteExecute = original }()
	dispatched := 0
	remoteExecute = func(ctx context.Context, action hostrouter.ActionRequest, cmd *cobra.Command, tty bool) error {
		dispatched++
		if !tty || action.Target.Host != (hostrouter.Host{Name: "rack", Address: "owner@rack-alias"}) || action.Directory != "" || len(action.Args) != 2 || action.Args[0] != "work-ui-exec" {
			t.Fatalf("incorrect native transport %#v", action)
		}
		req, err := hostrouter.DecodeWorkUIRequest(action.Args[1])
		if err != nil {
			t.Fatal(err)
		}
		if req.MasterID != "master-a" || req.ProjectUID != project || req.IssueUID != issue {
			t.Fatalf("wrong receiver identity %#v", req)
		}
		if strings.Contains(strings.Join(action.Args, " "), "native-entry-test-token") {
			t.Fatal("client credential in receiver argv")
		}
		return nil
	}
	execute := func(args ...string) error {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	ref, _ := c.KataTaskReference(project, issue, "example/repository")
	if err := execute("task", "tui", ref); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 {
		t.Fatal("native entry did not use selected SSH terminal")
	}
	for _, args := range [][]string{{"task", "tui", "; echo injected"}, {"chat"}} {
		if err := execute(args...); err == nil {
			t.Fatalf("unsafe/unconfigured entry succeeded: %v", args)
		}
	}
	conversation = &c.ConversationSource{Provider: "kata", InstanceUID: "native-instance", ProjectUID: project, IssueUID: issue}
	if err := execute("chat"); err != nil {
		t.Fatalf("active canonical team source rejected: %v", err)
	}
	conversation.IssueUID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if err := execute("chat"); err == nil {
		t.Fatal("different standing team ticket accepted")
	}
	conversation.ProjectUID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if err := execute("chat"); err == nil {
		t.Fatal("cross-project conversation accepted")
	}
	servedProject = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if err := execute("task", "tui", ref); err == nil {
		t.Fatal("wrong provider project accepted")
	}
	servedProject = project
	servedMaster = "master-b"
	if err := execute("task", "tui", ref); err == nil {
		t.Fatal("wrong authenticated master accepted")
	}
	if dispatched != 2 {
		t.Fatal("failed verification dispatched receiver")
	}
}

func TestWorkUIMissingReceiverFailsClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	req := hostrouter.WorkUIRequest{MasterID: "master-a", ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	encoded, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"work-ui-exec", encoded})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "master.toml") {
		t.Fatalf("missing receiver did not fail safely: %v", err)
	}
}

func TestWorkUICommandsAndForeground(t *testing.T) {
	r := root()
	for _, args := range [][]string{{"task", "tui"}, {"chat"}} {
		cmd, _, err := r.Find(args)
		if err != nil || cmd.Name() != args[len(args)-1] {
			t.Fatalf("missing normal command %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"task"}, {"chat"}} {
		child, err := tuiCommand(context.Background(), hostrouter.ActionRequest{Operation: "work-ui", Args: args})
		if err != nil || child == nil {
			t.Fatalf("foreground %v: %v", args, err)
		}
	}
	if _, err := tuiCommand(context.Background(), hostrouter.ActionRequest{Operation: "work-ui", Args: []string{"chat", "--config=/tmp/untrusted"}}); err == nil {
		t.Fatal("arbitrary foreground args accepted")
	}
}

func TestWorkUIReceiverVerifiesConfiguredNativeInstance(t *testing.T) {
	// Keep the Unix socket path short on both Darwin and Linux; the default
	// macOS temporary directory can exceed sockaddr_un.sun_path.
	dir, err := os.MkdirTemp("/tmp", "coop-native-pin-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	project, instance := "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	socket := filepath.Join(dir, "kata.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	actual := instance
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-native-owner-token" {
			t.Error("native auth missing")
		}
		switch r.URL.Path {
		case "/api/v1/instance":
			_ = json.NewEncoder(w).Encode(map[string]any{"instance_uid": actual})
		case "/api/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []any{map[string]any{"id": 1, "uid": project, "name": "fixture"}}})
		case "/api/v1/projects/1/events":
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "next_after_id": 0})
		default:
			t.Error("unexpected native path", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	defer server.Close()
	if err = os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[auth]\ntoken = \"fixture-native-owner-token\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "kata")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *'daemon locate'*) echo '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix://%s\"}';; *'projects show'*) echo '{\"project\":{\"uid\":\"%s\"}}';; esac\n", socket, project)
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := taskstore.Config{Provider: "kata", Binary: binary, Home: dir, ProjectName: "fixture", ProjectUID: project, InstanceUID: instance, PolicySHA: strings.Repeat("a", 64)}
	req := hostrouter.WorkUIRequest{MasterID: "master", ProjectUID: project}
	if _, err = nativeWorkUICommand(t.Context(), req, "master", cfg); err != nil {
		t.Fatal("matching actual native instance rejected", err)
	}
	actual = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if _, err = nativeWorkUICommand(t.Context(), req, "master", cfg); err == nil {
		t.Fatal("copied project in wrong native instance accepted")
	}
}
