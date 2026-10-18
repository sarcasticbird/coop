package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

type inventoryClientFunc func(context.Context, hostrouter.Host) ([]hostrouter.InventoryEntry, error)

func (f inventoryClientFunc) Inventory(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
	return f(ctx, h)
}

func serverTestRegistry(t *testing.T) hostrouter.Registry {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	r, err := hostrouter.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	return r
}

func forbidHostLocalDispatch(t *testing.T) {
	t.Helper()
	oldRuntime, oldAgent, oldRouter := newRuntime, runNamedAgent, newInventoryRouter
	newRuntime = func() runtime.Runtime {
		t.Fatal("unexpected local runtime construction")
		return nil
	}
	runNamedAgent = func(context.Context, string, []string, []string) error {
		t.Fatal("unexpected local named-agent entry")
		return nil
	}
	newInventoryRouter = func() hostrouter.Router {
		t.Fatal("unexpected inventory dispatch")
		return hostrouter.Router{}
	}
	t.Cleanup(func() {
		newRuntime, runNamedAgent, newInventoryRouter = oldRuntime, oldAgent, oldRouter
	})
	// An accidental subprocess must not find SSH or a container runtime either.
	t.Setenv("PATH", t.TempDir())
}

func runHostCLI(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := root()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func withInventoryClient(t *testing.T, client inventoryClientFunc) {
	t.Helper()
	old := newInventoryRouter
	newInventoryRouter = func() hostrouter.Router {
		return hostrouter.Router{Local: client, Remote: client}
	}
	t.Cleanup(func() { newInventoryRouter = old })
}

func TestServerCRUDAndSortedListWithoutRuntime(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	t.Chdir(t.TempDir())
	// Registry commands must not load project configuration.
	if err := os.WriteFile(".coop.toml", []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"server", "list"}, "NAME ADDRESS\n"},
		{[]string{"server", "add", "zulu", "deploy@zulu-alias"}, ""},
		{[]string{"server", "add", "alpha", "alpha.example"}, ""},
		{[]string{"server", "add", "alpha", "alpha.example"}, ""},
		{[]string{"server", "list"}, "NAME ADDRESS\nalpha alpha.example\nzulu deploy@zulu-alias\n"},
		{[]string{"--host", "local", "server", "update", "alpha", "new-alias"}, ""},
		{[]string{"server", "list"}, "NAME ADDRESS\nalpha new-alias\nzulu deploy@zulu-alias\n"},
		{[]string{"server", "remove", "alpha"}, ""},
		{[]string{"server", "list"}, "NAME ADDRESS\nzulu deploy@zulu-alias\n"},
		{[]string{"server", "remove", "zulu"}, ""},
		{[]string{"server", "list"}, "NAME ADDRESS\n"},
	} {
		if got, err := runHostCLI(step.args...); err != nil || got != step.want {
			t.Fatalf("%q: output=%q error=%v; want %q", step.args, got, err, step.want)
		}
	}
	for _, path := range []string{r.Path, r.Path + ".lock"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	for _, path := range []string{os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CACHE_HOME")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("registry CRUD created runtime state at %s: %v", path, err)
		}
	}
}

func TestInventoryInvalidRegistryRootDoesNotBlockLocal(t *testing.T) {
	serverTestRegistry(t)
	t.Setenv("XDG_CONFIG_HOME", "relative")
	withInventoryClient(t, func(_ context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		if h != hostrouter.Local {
			t.Errorf("unexpected host %+v", h)
		}
		return []hostrouter.InventoryEntry{}, nil
	})
	for _, selected := range []bool{false, true} {
		args := []string{"inventory", "--json"}
		if selected {
			args = append([]string{"--host", "local"}, args...)
		}
		out, err := runHostCLI(args...)
		if (err == nil) != selected {
			t.Fatalf("selected=%v error=%v", selected, err)
		}
		var snapshot hostrouter.Snapshot
		if json.Unmarshal([]byte(out), &snapshot) != nil || len(snapshot.Hosts) != 1 || snapshot.Hosts[0].Host != hostrouter.Local || snapshot.Hosts[0].Error != "" {
			t.Fatalf("local result lost: %s", out)
		}
		if (snapshot.RegistryError == "") != selected {
			t.Fatalf("registry error=%q selected=%v", snapshot.RegistryError, selected)
		}
	}
}

func TestServerInvalidChangesPreserveRegistry(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	if err := r.Change("add", "existing", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "local", "alias"}, {"update", "local", "alias"}, {"remove", "local"},
		{"add", "invalid/name", "alias"}, {"add", "valid", "host;command"},
		{"add", "existing", "different-alias"}, {"update", "missing", "alias"}, {"remove", "missing"},
		{"add", "missing-address"}, {"update", "missing-address"}, {"remove"}, {"list", "extra"},
	} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			if out, err := runHostCLI(append([]string{"server"}, args...)...); err == nil || out != "" {
				t.Fatalf("invalid command: output=%q error=%v", out, err)
			}
			after, err := os.ReadFile(r.Path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected command changed registry: %q, %v", after, err)
			}
		})
	}
}

func TestServerAndInventoryRejectAgentAndCredentials(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	for _, command := range [][]string{
		{"inventory"}, {"server", "list"}, {"server", "add", "alpha", "alias"},
		{"server", "update", "alpha", "alias"}, {"server", "remove", "alpha"},
	} {
		for _, selector := range []string{"--agent=robin", "--agent=", "--credentials=token", "--credentials="} {
			t.Run(strings.Join(command, "/")+"/"+selector, func(t *testing.T) {
				args := append([]string{"--host=local", selector}, command...)
				out, err := runHostCLI(args...)
				if err == nil || !strings.Contains(err.Error(), "do not accept --agent or --credentials") || out != "" {
					t.Fatalf("selector rejection: output=%q error=%v", out, err)
				}
				if _, err := os.Stat(r.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("incompatible selectors wrote registry: %v", err)
				}
			})
		}
	}
}

func TestHostRemoteUnsupportedCommandsNeverFallBackToLocal(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	if err := r.Change("add", "remote", "trusted-alias"); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	if err := os.WriteFile(".coop.toml", []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateConfig := filepath.Join(project, "must-not-be-read.json")
	before, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{}, {"claude", "--help"}, {"up"}, {"down"}, {"status"},
		{"--agent", "missing-profile"}, {"--agent", "missing-profile", "up"},
		{"--agent", "missing-profile", "down"}, {"--agent", "missing-profile", "status"},
		{"master", "serve", "--config", privateConfig},
		{"mcp", "serve", "--config", privateConfig},
		{"mcp", "stdio", "--config", privateConfig},
		{"mcp", "register", "--config", privateConfig, "--project", project},
		{"server", "add", "accidental", "alias"},
	} {
		t.Run(strings.Join(command, "/"), func(t *testing.T) {
			args := append([]string{"--host", "remote"}, command...)
			out, err := runHostCLI(args...)
			if err == nil || !strings.Contains(err.Error(), "does not yet support remote --host; no local action was executed") || out != "" {
				t.Fatalf("remote dispatch: output=%q error=%v", out, err)
			}
		})
	}
	after, err := os.ReadFile(r.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("unsupported remote command changed registry: %q, %v", after, err)
	}
	files, err := os.ReadDir(project)
	if err != nil || len(files) != 1 || files[0].Name() != ".coop.toml" {
		t.Fatalf("unsupported remote command wrote project files: %v, %v", files, err)
	}
	for _, path := range []string{os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CACHE_HOME")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unsupported remote command created %s: %v", path, err)
		}
	}
}

func TestHostUnknownAndEmptySelectionRejectBeforeDispatch(t *testing.T) {
	serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	for _, name := range []string{"", "unknown"} {
		for _, command := range [][]string{{}, {"up"}, {"inventory"}, {"ls"}, {"server", "list"}} {
			t.Run(name+"/"+strings.Join(command, "/"), func(t *testing.T) {
				out, err := runHostCLI(append([]string{"--host=" + name}, command...)...)
				want := "unknown server"
				if name == "" {
					want = "--host requires local or a registered server name"
				}
				if err == nil || !strings.Contains(err.Error(), want) || out != "" {
					t.Fatalf("host selection: output=%q error=%v; want %q", out, err, want)
				}
			})
		}
	}
}

func TestHostDefaultAndExplicitLocalPreserveHandlers(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit-local"}[explicit], func(t *testing.T) {
			r := serverTestRegistry(t)
			if err := os.WriteFile(r.Path, []byte("not JSON"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			if err := os.WriteFile(".coop.toml", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			withTerminalStdin(t, false)
			m := runtime.NewMock()
			m.ListErr = errors.New("local list sentinel")
			withRuntime(t, m)
			s, err := session.New(m, ".")
			if err != nil {
				t.Fatal(err)
			}
			m.Existing[s.Name] = true
			m.ContainerImages = map[string]string{s.Name: "coop:local-test"}
			oldSession, oldAgent := runSession, runNamedAgent
			sessionCalls, agentCalls := 0, 0
			runSession = func(*session.Session, string, []string, []string) error {
				sessionCalls++
				return nil
			}
			runNamedAgent = func(_ context.Context, name string, args, grants []string) error {
				agentCalls++
				if name != "robin" || !slices.Equal(args, []string{"claude", "--help"}) {
					t.Errorf("local named-agent dispatch: name=%q args=%q", name, args)
				}
				return nil
			}
			t.Cleanup(func() { runSession, runNamedAgent = oldSession, oldAgent })
			prefix := []string{}
			if explicit {
				prefix = []string{"--host", "local"}
			}
			for _, command := range [][]string{{}, {"status"}, {"down"}, {"--agent", "robin", "claude", "--help"}} {
				if _, err := runHostCLI(append(slices.Clone(prefix), command...)...); err != nil {
					t.Fatalf("local %q: %v", command, err)
				}
			}
			if _, err := runHostCLI(append(slices.Clone(prefix), "ls")...); !errors.Is(err, m.ListErr) {
				t.Fatalf("local ls did not reach original runtime: %v", err)
			}
			if sessionCalls != 1 || agentCalls != 1 {
				t.Fatalf("session calls=%d agent calls=%d", sessionCalls, agentCalls)
			}
			if !slices.Equal(m.Stopped, []string{s.Name}) {
				t.Fatalf("local down did not stop original Coop: %q", m.Stopped)
			}
			m.StateErr = errors.New("local up sentinel")
			if _, err := runHostCLI(append(slices.Clone(prefix), "up")...); !errors.Is(err, m.StateErr) {
				t.Fatalf("local up did not reach original runtime: %v", err)
			}
		})
	}
}

func TestHostGuestFlagsAfterPositionalTokenAreUnchanged(t *testing.T) {
	serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	old := runNamedAgent
	var got []string
	runNamedAgent = func(_ context.Context, name string, args, grants []string) error {
		if name != "robin" || len(grants) != 0 {
			t.Errorf("name=%q grants=%q", name, grants)
		}
		got = slices.Clone(args)
		return nil
	}
	t.Cleanup(func() { runNamedAgent = old })
	want := []string{"claude", "--host", "guest-host", "--agent", "guest-agent", "--credentials", "guest-token", "--timeout", "0s", "--json", "--help"}
	for _, prefix := range [][]string{{"--agent", "robin"}, {"--host", "local", "--agent", "robin"}} {
		got = nil
		if _, err := runHostCLI(append(prefix, want...)...); err != nil || !slices.Equal(got, want) {
			t.Fatalf("guest forwarding: args=%q error=%v; want %q", got, err, want)
		}
	}
	withRuntime(t, runtime.NewMock())
	withTerminalStdin(t, false)
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".coop.toml", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldSession := runSession
	runSession = func(_ *session.Session, _ string, args, grants []string) error {
		got = slices.Clone(args)
		if len(grants) != 0 {
			t.Errorf("guest flag was interpreted as a credential grant: %q", grants)
		}
		return nil
	}
	t.Cleanup(func() { runSession = oldSession })
	for _, prefix := range [][]string{nil, {"--host=local"}} {
		got = nil
		if _, err := runHostCLI(append(slices.Clone(prefix), want...)...); err != nil || !slices.Equal(got, want) {
			t.Fatalf("root guest forwarding: args=%q error=%v; want %q", got, err, want)
		}
	}
}

func TestInventoryTimeoutBoundsAndForwarding(t *testing.T) {
	serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	deadlines := make(chan time.Duration, 1)
	withInventoryClient(t, func(ctx context.Context, _ hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		deadline, _ := ctx.Deadline()
		deadlines <- time.Until(deadline)
		return nil, nil
	})
	for _, value := range []string{"0s", "-1ns", "1m1ns", "2m", "not-a-duration"} {
		if out, err := runHostCLI("inventory", "--timeout="+value); err == nil || out != "" {
			t.Fatalf("invalid timeout %q: output=%q error=%v", value, out, err)
		}
		select {
		case <-deadlines:
			t.Fatalf("invalid timeout %q reached an inventory client", value)
		default:
		}
	}
	for _, tc := range []struct {
		flag string
		want time.Duration
	}{{"", 5 * time.Second}, {"1ns", time.Nanosecond}, {"1m", time.Minute}} {
		args := []string{"--host=local", "inventory", "--json"}
		if tc.flag != "" {
			args = append(args, "--timeout="+tc.flag)
		}
		if _, err := runHostCLI(args...); err != nil {
			t.Fatalf("valid timeout %q: %v", tc.flag, err)
		}
		remaining := <-deadlines
		if remaining > tc.want || remaining < tc.want-time.Second {
			t.Errorf("timeout %q client deadline remaining=%v, want near %v", tc.flag, remaining, tc.want)
		}
	}
}

func TestInventoryAggregatedJSONSortedExplicitIdentities(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	alpha := hostrouter.Host{Name: "alpha", Address: "deploy@alpha-alias"}
	zulu := hostrouter.Host{Name: "zulu", Address: "zulu.example"}
	for _, h := range []hostrouter.Host{zulu, alpha} {
		if err := r.Change("add", h.Name, h.Address); err != nil {
			t.Fatal(err)
		}
	}
	rows := []hostrouter.InventoryEntry{
		{Target: hostrouter.Target{Host: zulu, Kind: hostrouter.Interactive, ID: "coop-project"}, Name: "same-name", State: "stopped", Project: "/remote/project", Owned: true},
		{Target: hostrouter.Target{Host: zulu, Kind: hostrouter.Agent, ID: "zebra"}, Name: "same-name", State: "absent"},
		{Target: hostrouter.Target{Host: zulu, Kind: hostrouter.Agent, ID: "robin"}, Name: "same-name", State: "running", Owned: true},
	}
	withInventoryClient(t, func(context.Context, hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		return rows, nil
	})
	out, err := runHostCLI("inventory", "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := hostrouter.Snapshot{Version: 1, Hosts: []hostrouter.HostInventory{}}
	for _, h := range []hostrouter.Host{alpha, hostrouter.Local, zulu} {
		entries := []hostrouter.InventoryEntry{rows[2], rows[1], rows[0]}
		for i := range entries {
			entries[i].Target.Host = h
		}
		want.Hosts = append(want.Hosts, hostrouter.HostInventory{Host: h, Entries: entries})
	}
	var got hostrouter.Snapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory JSON=%s error=%v; want %+v", out, err, want)
	}
	// Compare JSON objects too: zero-valued fields such as owned=false must be explicit.
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var gotObject, wantObject any
	if err := json.Unmarshal([]byte(out), &gotObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wantJSON, &wantObject); err != nil || !reflect.DeepEqual(gotObject, wantObject) {
		t.Fatalf("inventory JSON schema=%s; want %s (error=%v)", out, wantJSON, err)
	}
}

type inventoryStreamWriter struct {
	bytes.Buffer
	localRow chan struct{}
}

func (w *inventoryStreamWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(w.Buffer.String(), "local agent robin Robin running\n") {
		select {
		case w.localRow <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestInventoryTextStreamsLocalBeforeRemoteCompletes(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	if err := r.Change("add", "remote", "remote-alias"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	withInventoryClient(t, func(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		if h != hostrouter.Local {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []hostrouter.InventoryEntry{{Target: hostrouter.Target{Kind: hostrouter.Agent, ID: "robin"}, Name: "Robin", State: "running"}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w := &inventoryStreamWriter{localRow: make(chan struct{}, 1)}
	cmd := root()
	cmd.SetOut(w)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"inventory", "--timeout=30s"})
	done, finished := make(chan error, 1), make(chan struct{})
	t.Cleanup(func() { cancel(); <-finished })
	go func() {
		defer close(finished)
		done <- cmd.ExecuteContext(ctx)
	}()
	select {
	case <-w.localRow:
	case err := <-done:
		t.Fatalf("inventory finished before streaming local row: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("local output waited for the blocked remote")
	}
	select {
	case err := <-done:
		t.Fatalf("inventory finished before remote was released: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inventory did not finish after remote release")
	}
	want := "HOST KIND ID NAME STATE\nlocal agent robin Robin running\nremote agent robin Robin running\n"
	if w.String() != want {
		t.Fatalf("streaming text=%q, want %q", w.String(), want)
	}
}

func TestInventoryPartialOfflineResultsHaveNonzeroExit(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			r := serverTestRegistry(t)
			forbidHostLocalDispatch(t)
			for _, name := range []string{"offline", "invalid", "timeout"} {
				if err := r.Change("add", name, name+"-alias"); err != nil {
					t.Fatal(err)
				}
			}
			local := hostrouter.InventoryEntry{Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Agent, ID: "robin"}, Name: "Robin", State: "running"}
			withInventoryClient(t, func(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
				switch h.Name {
				case "offline":
					return nil, errors.New("private-ssh-error-sentinel")
				case "invalid":
					return []hostrouter.InventoryEntry{{Name: "bad-identity", State: "running"}}, nil
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				default:
					return []hostrouter.InventoryEntry{local}, nil
				}
			})
			var out bytes.Buffer
			cmd := root()
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"inventory", "--timeout=100ms", "--json=" + map[bool]string{false: "false", true: "true"}[jsonOutput]})
			if code := execute(cmd); code != 1 {
				t.Fatalf("partial inventory exit=%d, want 1", code)
			}
			if strings.Contains(out.String(), "private-ssh-error-sentinel") {
				t.Fatal("inventory exposed remote error details")
			}
			if jsonOutput {
				want := hostrouter.Snapshot{Version: 1, Hosts: []hostrouter.HostInventory{
					{Host: hostrouter.Host{Name: "invalid", Address: "invalid-alias"}, Entries: []hostrouter.InventoryEntry{}, Error: "invalid_inventory"},
					{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{local}},
					{Host: hostrouter.Host{Name: "offline", Address: "offline-alias"}, Entries: []hostrouter.InventoryEntry{}, Error: "unreachable"},
					{Host: hostrouter.Host{Name: "timeout", Address: "timeout-alias"}, Entries: []hostrouter.InventoryEntry{}, Error: "timeout"},
				}}
				var got hostrouter.Snapshot
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("partial JSON=%s error=%v; want %+v", out.String(), err, want)
				}
			} else {
				lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
				if len(lines) != 5 || lines[0] != "HOST KIND ID NAME STATE" {
					t.Fatalf("partial text=%q", out.String())
				}
				slices.Sort(lines[1:])
				want := []string{"invalid - - - invalid_inventory", "local agent robin Robin running", "offline - - - unreachable", "timeout - - - timeout"}
				if !slices.Equal(lines[1:], want) {
					t.Fatalf("partial rows=%q, want %q", lines[1:], want)
				}
			}
		})
	}
}

func TestInventoryMalformedRegistryRetainsLocalAndExplicitLocalSkipsIt(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput]+"/"+map[bool]string{false: "all", true: "local"}[explicit], func(t *testing.T) {
				r := serverTestRegistry(t)
				forbidHostLocalDispatch(t)
				const malformed = `{"version":1,"servers":[private-registry-sentinel`
				if err := os.WriteFile(r.Path, []byte(malformed), 0o600); err != nil {
					t.Fatal(err)
				}
				calls := make(chan hostrouter.Host, 2)
				local := hostrouter.InventoryEntry{Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Interactive, ID: "coop-project"}, Name: "project", State: "running"}
				withInventoryClient(t, func(_ context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
					calls <- h
					return []hostrouter.InventoryEntry{local}, nil
				})
				var out bytes.Buffer
				cmd := root()
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				args := []string{"inventory", "--json=" + map[bool]string{false: "false", true: "true"}[jsonOutput]}
				if explicit {
					args = append([]string{"--host=local"}, args...)
				}
				cmd.SetArgs(args)
				wantCode := 1
				registryError := "registry_unavailable"
				if explicit {
					wantCode, registryError = 0, ""
				}
				if code := execute(cmd); code != wantCode {
					t.Fatalf("exit=%d, want %d", code, wantCode)
				}
				if len(calls) != 1 || <-calls != hostrouter.Local {
					t.Fatal("malformed registry did not query local inventory exactly once")
				}
				if jsonOutput {
					want := hostrouter.Snapshot{Version: 1, RegistryError: registryError, Hosts: []hostrouter.HostInventory{{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{local}}}}
					var got hostrouter.Snapshot
					if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("malformed registry JSON=%s error=%v; want %+v", out.String(), err, want)
					}
				} else if want := "HOST KIND ID NAME STATE\nlocal interactive coop-project project running\n"; out.String() != want {
					t.Fatalf("local text=%q, want %q", out.String(), want)
				}
				after, err := os.ReadFile(r.Path)
				if err != nil || string(after) != malformed {
					t.Fatalf("inventory changed malformed registry: %q, %v", after, err)
				}
			})
		}
	}
}

func TestInventoryRemoteHostAndLSSelectOnlyRegisteredHost(t *testing.T) {
	r := serverTestRegistry(t)
	forbidHostLocalDispatch(t)
	remote := hostrouter.Host{Name: "remote", Address: "deploy@remote-alias"}
	if err := r.Change("add", remote.Name, remote.Address); err != nil {
		t.Fatal(err)
	}
	calls := make(chan hostrouter.Host, 2)
	withInventoryClient(t, func(_ context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		calls <- h
		return []hostrouter.InventoryEntry{{Target: hostrouter.Target{Kind: hostrouter.Agent, ID: "robin"}, Name: "Robin", State: "running"}}, nil
	})
	for _, command := range []string{"inventory", "ls"} {
		out, err := runHostCLI("--host=remote", command)
		if want := "HOST KIND ID NAME STATE\nremote agent robin Robin running\n"; err != nil || out != want {
			t.Fatalf("remote %s: output=%q error=%v; want %q", command, out, err, want)
		}
		if len(calls) != 1 || <-calls != remote {
			t.Fatalf("remote %s did not query only the registered identity", command)
		}
	}
}
