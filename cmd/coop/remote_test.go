package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/spf13/cobra"
)

func captureRemoteExecute(t *testing.T, f func(context.Context, hostrouter.ActionRequest, *cobra.Command, bool) error) {
	t.Helper()
	old := remoteExecute
	remoteExecute = f
	t.Cleanup(func() { remoteExecute = old })
}

func TestRemoteExitCode(t *testing.T) {
	cmd := &cobra.Command{Use: "fixture", SilenceErrors: true, RunE: func(*cobra.Command, []string) error {
		return &hostrouter.ActionError{Code: 19}
	}}
	cmd.SetArgs(nil)
	if got := execute(cmd); got != 19 {
		t.Fatalf("remote exit = %d, want 19", got)
	}
}

func TestRemoteRouteReconstructsInvocation(t *testing.T) {
	host := hostrouter.Host{Name: "remote", Address: "deploy@trusted-alias"}
	hostile := []string{"claude", "--host", "guest-host", "--agent=guest-agent", "--credentials", "guest-grant", "--help", "", "two words", "'\";$(touch /tmp/not-executed)`id`\n*"}
	for _, tc := range []struct {
		name, directory      string
		flags, command, want []string
		kind                 hostrouter.TargetKind
		id                   string
		tty                  bool
	}{
		{name: "guest-argv", directory: "/server/project with spaces", flags: []string{"--remote-directory=/server/project with spaces"}, command: hostile, want: append([]string{"--"}, hostile...), tty: true},
		{name: "agent", flags: []string{"--agent=robin"}, command: hostile, want: append([]string{"--agent=robin", "--"}, hostile...), kind: hostrouter.Agent, id: "robin", tty: true},
		{name: "captured-agent", flags: []string{"--target-kind=agent", "--target-id=robin"}, command: []string{"status"}, want: []string{"--agent=robin", "status", "--"}, kind: hostrouter.Agent, id: "robin"},
		{name: "captured-interactive", flags: []string{"--target-kind=interactive", "--target-id=robin"}, command: []string{"status"}, want: []string{"status", "--"}, kind: hostrouter.Interactive, id: "robin"},
		{name: "credential-names", flags: []string{"--agent=missing-profile", "--credentials=github,aws-dev", "--credentials=\"comma,name\""}, command: hostile, want: append([]string{"--credentials=github,aws-dev,\"comma,name\"", "--agent=missing-profile", "--"}, hostile...), kind: hostrouter.Agent, id: "missing-profile", tty: true},
		{name: "credential-names-subcommand", flags: []string{"--agent=missing-profile", "--credentials=github,aws-dev"}, command: []string{"shell"}, want: []string{"--credentials=github,aws-dev", "--agent=missing-profile", "shell", "--"}, kind: hostrouter.Agent, id: "missing-profile", tty: true},
		{name: "slice-local-flag", flags: []string{"--agent=hawk"}, command: []string{"approve", "--repo=owner/one", "--repo=owner/two", "https://github.com/org/repo/discussions/1"}, want: []string{"--agent=hawk", "approve", "--repo=owner/one,owner/two", "--", "https://github.com/org/repo/discussions/1"}, kind: hostrouter.Agent, id: "hawk"},
		{name: "request-id", flags: []string{"--agent=robin"}, command: []string{"ping", "--prompt=private ; $(id)\nmessage", "--request-id=stable-retry"}, want: []string{"--agent=robin", "ping", "--prompt=private ; $(id)\nmessage", "--request-id=stable-retry", "--"}, kind: hostrouter.Agent, id: "robin"},
		{name: "archive-positional", directory: "/server/project", flags: []string{"--remote-directory=/server/project"}, command: []string{"restore", "--", "--host=not-a-selector"}, want: []string{"restore", "--", "--host=not-a-selector"}},
		{name: "explicit-tty", flags: []string{"--agent=robin", "--tty"}, command: []string{"status"}, want: []string{"--agent=robin", "status", "--"}, kind: hostrouter.Agent, id: "robin", tty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serverTestRegistry(t)
			if err := r.Change("add", host.Name, host.Address); err != nil {
				t.Fatal(err)
			}
			// Profiles and credential sources belong to the server, not this config.
			if err := os.WriteFile(filepath.Join(filepath.Dir(r.Path), "coop.toml"), []byte("[broken-controller-private-config"), 0o600); err != nil {
				t.Fatal(err)
			}
			forbidHostLocalDispatch(t)
			t.Chdir(t.TempDir())
			if err := os.WriteFile(".coop.toml", []byte("[broken"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			sentinel := errors.New("remote result")
			captureRemoteExecute(t, func(_ context.Context, req hostrouter.ActionRequest, _ *cobra.Command, tty bool) error {
				calls++
				want := hostrouter.ActionRequest{Target: hostrouter.Target{Host: host, Kind: tc.kind, ID: tc.id}, Operation: "command", Directory: tc.directory, Args: tc.want}
				if !reflect.DeepEqual(req, want) || tty != tc.tty {
					t.Errorf("request=%+v tty=%v; want %+v tty=%v", req, tty, want, tc.tty)
				}
				return sentinel
			})
			args := append([]string{"--host=remote"}, tc.flags...)
			args = append(args, tc.command...)
			if _, err := runHostCLI(args...); !errors.Is(err, sentinel) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestRemoteRouteNeverUsesControllerDirectory(t *testing.T) {
	r := serverTestRegistry(t)
	if err := r.Change("add", "remote", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	forbidHostLocalDispatch(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".coop.toml", []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureRemoteExecute(t, func(context.Context, hostrouter.ActionRequest, *cobra.Command, bool) error {
		t.Fatal("invalid selection reached remote executor")
		return nil
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "controller cwd is never forwarded"},
		{nil, "controller cwd is never forwarded"},
		{[]string{"--agent=", "status"}, "--agent requires"},
		{[]string{"--agent=robin", "--target-kind=agent", "--target-id=hawk", "status"}, "agent target must match"},
		{[]string{"--agent=robin", "--remote-directory=/server/project", "status"}, "cannot select a project directory"},
		{[]string{"--agent=robin", "--target-kind=interactive", "--target-id=robin", "status"}, "--agent conflicts"},
	} {
		if _, err := runHostCLI(append([]string{"--host=remote"}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error=%v; want %q", tc.args, err, tc.want)
		}
	}
}

func TestRemoteRouteLocalFlagShadows(t *testing.T) {
	r := serverTestRegistry(t)
	if err := r.Change("add", "remote", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	forbidHostLocalDispatch(t)
	for _, tc := range []struct {
		name          string
		command, want []string
	}{
		{"placement", []string{"master", "placement", "set", "--host=destination-host", "--agent=placed-agent", "--instance=worker-2", "--generation=2", "--expected-generation=1", "--epoch=3", "--dry-run"}, []string{"master", "placement", "set", "--agent=placed-agent", "--dry-run=true", "--epoch=3", "--expected-generation=1", "--generation=2", "--host=destination-host", "--instance=worker-2", "--"}},
		{"backup", []string{"master", "backup", "--directory=/server/backups/new"}, []string{"master", "backup", "--directory=/server/backups/new", "--"}},
		{"restore", []string{"master", "restore", "--backup=/server/backups/old", "--directory=/server/restored", "--epoch-path=/server/epoch"}, []string{"master", "restore", "--backup=/server/backups/old", "--directory=/server/restored", "--epoch-path=/server/epoch", "--"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			captureRemoteExecute(t, func(_ context.Context, req hostrouter.ActionRequest, _ *cobra.Command, tty bool) error {
				calls++
				if req.Target.Kind != "" || req.Target.ID != "" || req.Target.Host.Name != "remote" || req.Directory != "" || tty || !slices.Equal(req.Args, tc.want) {
					t.Errorf("shadowed flags changed routing or argv: %+v tty=%v; want argv=%q", req, tty, tc.want)
				}
				return nil
			})
			if _, err := runHostCLI(append([]string{"--host=remote"}, tc.command...)...); err != nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestRemoteRouteMCPPreRunDoesNotReadControllerPaths(t *testing.T) {
	r := serverTestRegistry(t)
	if err := r.Change("add", "remote", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	forbidHostLocalDispatch(t)
	for _, action := range []string{"serve", "stdio", "inspect", "register"} {
		for _, path := range []string{filepath.Join(t.TempDir(), "absent-private-config.json"), "server-relative-config.json"} {
			t.Run(action+"/"+filepath.Base(path), func(t *testing.T) {
				calls := 0
				captureRemoteExecute(t, func(_ context.Context, req hostrouter.ActionRequest, _ *cobra.Command, _ bool) error {
					calls++
					want := []string{"mcp", action, "--config=" + path, "--"}
					if !slices.Equal(req.Args, want) {
						t.Errorf("argv=%q, want %q", req.Args, want)
					}
					return nil
				})
				if _, err := runHostCLI("--host=remote", "mcp", action, "--config="+path); err != nil || calls != 1 {
					t.Fatalf("controller pre-run/handler ran: calls=%d error=%v", calls, err)
				}
			})
		}
	}
}

func TestRemoteRoutePrivateFileReferencesAreServerPaths(t *testing.T) {
	r := serverTestRegistry(t)
	if err := r.Change("add", "remote", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	forbidHostLocalDispatch(t)
	privatePath := filepath.Join(t.TempDir(), "not-present-on-controller.json")
	for _, tc := range []struct {
		name       string
		args, want []string
	}{
		{"master-config", []string{"master", "serve", "--config=" + privatePath}, []string{"master", "serve", "--config=" + privatePath, "--"}},
		{"message-body", []string{"--master-config=" + privatePath, "message", "send", "--body-file=" + privatePath, "--to=robin,hawk", "--request-id=stable-request", "thread-1"}, []string{"--master-config=" + privatePath, "message", "send", "--body-file=" + privatePath, "--request-id=stable-request", "--to=robin,hawk", "--", "thread-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			captureRemoteExecute(t, func(_ context.Context, req hostrouter.ActionRequest, _ *cobra.Command, _ bool) error {
				calls++
				if !slices.Equal(req.Args, tc.want) {
					t.Errorf("argv=%q, want %q", req.Args, tc.want)
				}
				return nil
			})
			if _, err := runHostCLI(append([]string{"--host=remote"}, tc.args...)...); err != nil || calls != 1 {
				t.Fatalf("server reference was resolved on controller: calls=%d error=%v", calls, err)
			}
		})
	}
}

func encodedRemoteInvocation(t *testing.T, v hostrouter.Invocation) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestReceiveHostInvocationRejectsMalformedEnvelope(t *testing.T) {
	serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	captureRemoteExecute(t, func(context.Context, hostrouter.ActionRequest, *cobra.Command, bool) error {
		t.Fatal("malformed envelope reached remote executor")
		return nil
	})
	valid := hostrouter.Invocation{Version: 1, Target: hostrouter.Target{Host: hostrouter.Local}, Args: []string{"status"}}
	for _, tc := range []struct {
		name   string
		change func(*hostrouter.Invocation)
	}{
		{"version", func(v *hostrouter.Invocation) { v.Version = 2 }},
		{"remote-host", func(v *hostrouter.Invocation) {
			v.Target.Host = hostrouter.Host{Name: "remote", Address: "trusted-alias"}
		}},
		{"empty-argv", func(v *hostrouter.Invocation) { v.Args = nil }},
		{"relative-directory", func(v *hostrouter.Invocation) { v.Directory = "controller-relative" }},
		{"nul-argv", func(v *hostrouter.Invocation) { v.Args = []string{"status\x00"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			tc.change(&v)
			if _, err := runHostCLI("host-exec", encodedRemoteInvocation(t, v)); err == nil {
				t.Fatal("malformed envelope accepted")
			}
		})
	}
	for _, envelope := range []string{"!not-base64", base64.RawURLEncoding.EncodeToString([]byte("not JSON"))} {
		if _, err := runHostCLI("host-exec", envelope); err == nil {
			t.Fatal("malformed encoding accepted")
		}
	}
	if _, err := runHostCLI("host-exec"); err == nil {
		t.Fatal("missing envelope accepted")
	}
}

func TestReceiveHostInvocationNamedAgentAndGuestArguments(t *testing.T) {
	serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	want := []string{"claude", "--host", "guest-host", "--agent=guest-agent", "--credentials=guest-grant", "", "$(id);\n'\""}
	calls := 0
	old := runNamedAgent
	runNamedAgent = func(_ context.Context, name string, args, grants []string) error {
		calls++
		if name != "robin" || !slices.Equal(args, want) || !slices.Equal(grants, []string{"github", "aws-dev"}) {
			t.Errorf("name=%q args=%q grants=%q", name, args, grants)
		}
		return nil
	}
	t.Cleanup(func() { runNamedAgent = old })
	v := hostrouter.Invocation{Version: 1, Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Agent, ID: "robin"}, Args: append([]string{"--agent=robin", "--credentials=github,aws-dev", "--"}, want...)}
	if _, err := runHostCLI("host-exec", encodedRemoteInvocation(t, v)); err != nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestReceiveHostInvocationRejectsUnsafeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind hostrouter.TargetKind
		id   string
		args []string
		want string
	}{
		{"agent-mismatch", hostrouter.Agent, "robin", []string{"--agent=hawk", "--", "claude"}, "agent identity mismatch"},
		{"agent-missing", hostrouter.Agent, "robin", []string{"status"}, "agent identity mismatch"},
		{"nested-remote", "", "", []string{"--host=remote", "status"}, "nested remote routing is forbidden"},
		{"nested-directory", "", "", []string{"--remote-directory=/server/project", "status"}, "nested host selectors are forbidden"},
		{"nested-kind", "", "", []string{"--target-kind=agent", "status"}, "nested host selectors are forbidden"},
		{"nested-id", "", "", []string{"--target-id=robin", "status"}, "nested host selectors are forbidden"},
		{"nested-tty", "", "", []string{"--tty=false", "status"}, "nested host selectors are forbidden"},
		{"missing-directory", "", "", []string{"status"}, "remote project directory is required"},
		{"controller-command", "", "", []string{"server", "list"}, "command is not remotely routable"},
		{"recursive-host-exec", "", "", []string{"host-exec", "not-an-envelope"}, "command is not remotely routable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverTestRegistry(t)
			forbidHostLocalDispatch(t)
			captureRemoteExecute(t, func(context.Context, hostrouter.ActionRequest, *cobra.Command, bool) error {
				t.Fatal("receiver attempted remote execution")
				return nil
			})
			v := hostrouter.Invocation{Version: 1, Target: hostrouter.Target{Host: hostrouter.Local, Kind: tc.kind, ID: tc.id}, Args: tc.args}
			if _, err := runHostCLI("host-exec", encodedRemoteInvocation(t, v)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestReceiveHostInvocationInteractiveRevalidation(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "wrong-kind", "wrong-host", "path-changed", "empty-project", "identity-changed", "inventory-error", "agent-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			serverTestRegistry(t)
			forbidHostLocalDispatch(t)
			m := runtime.NewMock()
			withRuntime(t, m)
			withTerminalStdin(t, false)
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, ".coop.toml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := session.New(m, project)
			if err != nil {
				t.Fatal(err)
			}
			target := hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Interactive, ID: s.Name}
			row := hostrouter.InventoryEntry{Target: target, Name: "same display name", State: "running", Project: project}
			v := hostrouter.Invocation{Version: 1, Target: target, Args: []string{"--", "claude", "--host=guest"}}
			want := ""
			switch scenario {
			case "missing":
				want = "captured target no longer exists"
			case "wrong-kind":
				row.Target.Kind = hostrouter.Agent
				want = "captured target no longer exists"
			case "wrong-host":
				row.Target.Host = hostrouter.Host{Name: "other", Address: "other-alias"}
				want = "captured target no longer exists"
			case "path-changed":
				v.Directory = filepath.Join(project, "old")
				want = "captured project path changed"
			case "empty-project":
				row.Project = ""
				want = "captured project path changed"
			case "identity-changed":
				v.Target.ID = "coop-stale"
				row.Target = v.Target
				want = "project identity changed"
			case "inventory-error":
				want = "revalidate remote target"
			case "agent-conflict":
				v.Args = []string{"--agent=robin", "--", "claude"}
				want = "target kind mismatch"
			}
			inventoryCalls := 0
			withInventoryClient(t, func(_ context.Context, host hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
				inventoryCalls++
				if host != hostrouter.Local {
					t.Errorf("revalidation host=%+v", host)
				}
				if scenario == "inventory-error" {
					return nil, errors.New("inventory sentinel")
				}
				if scenario == "missing" {
					return nil, nil
				}
				// A same-ID agent and a same-name interactive entry must not win.
				return []hostrouter.InventoryEntry{
					{Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Agent, ID: target.ID}, Name: row.Name, Project: "/wrong/agent"},
					{Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Interactive, ID: "coop-other"}, Name: row.Name, Project: "/wrong/project"}, row,
				}, nil
			})
			calls := 0
			old := runSession
			runSession = func(got *session.Session, cwd string, args, grants []string) error {
				calls++
				if got.Name != s.Name || cwd != project || !slices.Equal(args, []string{"claude", "--host=guest"}) || len(grants) != 0 {
					t.Errorf("session=%q cwd=%q args=%q grants=%q", got.Name, cwd, args, grants)
				}
				return nil
			}
			t.Cleanup(func() { runSession = old })
			before, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			_, err = runHostCLI("host-exec", encodedRemoteInvocation(t, v))
			if want == "" {
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) || calls != 0 {
				t.Fatalf("calls=%d error=%v, want %q", calls, err, want)
			}
			after, cwdErr := os.Getwd()
			if cwdErr != nil || after != before || inventoryCalls != 1 {
				t.Errorf("cwd before=%q after=%q error=%v inventory calls=%d", before, after, cwdErr, inventoryCalls)
			}
		})
	}
}

func TestRemoteCommandClassificationExhaustive(t *testing.T) {
	remote := strings.Fields("init up down status shell sync apply rebuild destroy backup restore doctor upgrade ping approve team inbox")
	remote = append(remote,
		"work-ui-exec",
		"credentials refresh", "credentials watch", "worker status", "worker pause", "worker resume", "worker requests",
		"master serve", "master backup", "master restore", "master reconcile status", "master reconcile pause", "master reconcile resume", "master reconcile evidence", "master reconcile resolve", "master placement set", "master history import", "master history list", "master cutover", "master conversation cursor", "master conversation map", "master conversation reaccept", "master conversation legacy",
		"thread list", "thread create", "thread read", "thread members", "message send", "message continue", "message status", "team inspect",
		"goal propose", "goal list", "goal authority", "goal approve", "goal reject", "goal pause", "goal resume", "goal complete", "goal reopen",
		"task capabilities", "task read", "task list", "task create", "task adopt",
		"mcp serve", "mcp stdio", "mcp inspect", "mcp register", "cache status")
	// Inventory/ls aggregate on the controller rather than executing a remote CLI.
	controller := []string{"task", "task tui", "chat", "pilot", "pilot run", "pilot status", "pilot mcp", "host-exec", "tui-exec", "inventory", "ls", "tui", "server add", "server update", "server remove", "server list", "help", "completion bash", "completion fish", "completion powershell", "completion zsh"}
	classified := map[string]bool{"": true}
	for _, name := range remote {
		classified[name] = true
	}
	for _, name := range controller {
		if _, exists := classified[name]; exists {
			t.Fatalf("duplicate classification %q", name)
		}
		classified[name] = false
	}
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--help"})
	// Execute initializes Cobra's generated help and completion command leaves.
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Run != nil || c.RunE != nil {
			name := strings.Join(commandParts(c), " ")
			seen[name] = true
			want, ok := classified[name]
			if !ok {
				t.Errorf("executable command %q has no explicit routing classification", name)
			} else if got := remoteCommandAllowed(commandParts(c)); got != want {
				t.Errorf("remoteCommandAllowed(%q)=%v, want %v", name, got, want)
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(cmd)
	for name := range classified {
		if !seen[name] {
			t.Errorf("classified command %q is not executable in the current CLI", name)
		}
	}
	for _, parts := range [][]string{{"unknown"}, {"master", "unknown"}, {"status", "unknown"}} {
		if remoteCommandAllowed(parts) {
			t.Errorf("unknown command %q is remotely allowed", parts)
		}
	}
}
