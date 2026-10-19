package hostrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestInvocationStrictSchema(t *testing.T) {
	valid := `{"version":1,"target":{"host":{"name":"local"},"kind":"agent","id":"profile"},"args":["status"]}`
	for name, document := range map[string]string{
		"unknown field":        strings.TrimSuffix(valid, "}") + `,"private-sentinel":true}`,
		"unknown target field": strings.Replace(valid, `"kind":"agent"`, `"private-sentinel":true,"kind":"agent"`, 1),
		"unknown host field":   strings.Replace(valid, `"name":"local"`, `"name":"local","private-sentinel":true`, 1),
		"trailing document":    valid + ` {"private-sentinel":true}`,
		"trailing garbage":     valid + "private-sentinel",
		"truncated":            strings.TrimSuffix(valid, "}"),
		"null":                 `null`,
		"array":                `[]`,
		"missing version":      strings.Replace(valid, `"version":1,`, "", 1),
		"zero version":         strings.Replace(valid, `"version":1`, `"version":0`, 1),
		"future version":       strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"negative version":     strings.Replace(valid, `"version":1`, `"version":-1`, 1),
		"string version":       strings.Replace(valid, `"version":1`, `"version":"1"`, 1),
		"fractional version":   strings.Replace(valid, `"version":1`, `"version":1.5`, 1),
		"missing target":       `{"version":1,"args":["status"]}`,
		"null target":          `{"version":1,"target":null,"args":["status"]}`,
		"missing args":         strings.Replace(valid, `,"args":["status"]`, "", 1),
		"null args":            strings.Replace(valid, `["status"]`, `null`, 1),
		"empty args":           strings.Replace(valid, `["status"]`, `[]`, 1),
		"number arg":           strings.Replace(valid, `["status"]`, `[42]`, 1),
		"null arg":             strings.Replace(valid, `["status"]`, `[null]`, 1),
		"null directory":       strings.TrimSuffix(valid, "}") + `,"directory":null}`,
		"conflicting versions": strings.Replace(valid, `"version":1`, `"version":2,"version":1`, 1),
		"conflicting hosts":    strings.Replace(valid, `"name":"local"`, `"name":"private-sentinel","name":"local"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeInvocation(base64.RawURLEncoding.EncodeToString([]byte(document)))
			if err == nil {
				t.Fatal("strict invocation schema accepted malformed or ambiguous wire data")
			}
			if strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("schema error exposed private input: %v", err)
			}
		})
	}
	for _, encoded := range []string{"", "%%%private-sentinel", base64.RawURLEncoding.EncodeToString([]byte(valid)) + "=", "ab+c/d"} {
		if _, err := DecodeInvocation(encoded); err == nil {
			t.Errorf("accepted malformed base64url envelope %q", encoded)
		}
	}
}

func TestInvocationValidationAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Invocation)
		valid  bool
	}{
		{"host-wide", func(v *Invocation) { v.Target.Kind, v.Target.ID = "", "" }, true},
		{"interactive", func(v *Invocation) { v.Target.Kind = Interactive }, true},
		{"empty argument", func(v *Invocation) { v.Args = []string{""} }, true},
		{"argument limit", func(v *Invocation) { v.Args = []string{strings.Repeat("a", 64<<10)} }, true},
		{"argument over limit", func(v *Invocation) { v.Args = []string{strings.Repeat("a", (64<<10)+1)} }, false},
		{"argument count limit", func(v *Invocation) { v.Args = make([]string, 1024) }, true},
		{"argument count over limit", func(v *Invocation) { v.Args = make([]string, 1025) }, false},
		{"nul argument", func(v *Invocation) { v.Args = []string{"private-sentinel\x00"} }, false},
		{"directory limit", func(v *Invocation) { v.Directory = "/" + strings.Repeat("a", 4095) }, true},
		{"directory over limit", func(v *Invocation) { v.Directory = "/" + strings.Repeat("a", 4096) }, false},
		{"relative directory", func(v *Invocation) { v.Directory = "private-sentinel/path" }, false},
		{"control directory", func(v *Invocation) { v.Directory = "/private-sentinel\n" }, false},
		{"nul directory", func(v *Invocation) { v.Directory = "/private-sentinel\x00" }, false},
		{"target id limit", func(v *Invocation) { v.Target.ID = strings.Repeat("a", 255) }, true},
		{"target id over limit", func(v *Invocation) { v.Target.ID = strings.Repeat("a", 256) }, false},
		{"missing id", func(v *Invocation) { v.Target.ID = "" }, false},
		{"missing kind", func(v *Invocation) { v.Target.Kind = "" }, false},
		{"invalid kind", func(v *Invocation) { v.Target.Kind = "private-sentinel" }, false},
		{"invalid id", func(v *Invocation) { v.Target.ID = "../private-sentinel" }, false},
		{"foreign host", func(v *Invocation) { v.Target.Host = Host{Name: "remote", Address: "private-sentinel"} }, false},
		{"forged local host", func(v *Invocation) { v.Target.Host.Address = "private-sentinel" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Invocation{Version: 1, Target: Target{Host: Local, Kind: Agent, ID: "profile"}, Args: []string{"status"}}
			tc.change(&v)
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeInvocation(base64.RawURLEncoding.EncodeToString(b))
			if (err == nil) != tc.valid || (v.Validate() == nil) != tc.valid {
				t.Fatalf("validation safeguard: valid=%t, DecodeInvocation=%v, Validate=%v", tc.valid, err, v.Validate())
			}
			if tc.valid && !reflect.DeepEqual(got, v) {
				t.Fatalf("round trip changed invocation: %#v", got)
			}
			if err != nil && strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("private input leaked: %v", err)
			}
		})
	}
}

func TestInvocationEnvelopeSizeBounds(t *testing.T) {
	v := Invocation{Version: 1, Target: Target{Host: Local}, Args: []string{"status"}}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	// Whitespace keeps the JSON valid while exercising the encoded-size boundary.
	b = append(b, bytes.Repeat([]byte(" "), (96<<10)-len(b))...)
	encoded := base64.RawURLEncoding.EncodeToString(b)
	if len(encoded) != 128<<10 {
		t.Fatalf("fixture size = %d", len(encoded))
	}
	if _, err := DecodeInvocation(encoded); err != nil {
		t.Fatalf("exact wire limit rejected: %v", err)
	}
	if _, err := DecodeInvocation(encoded + "A"); err == nil {
		t.Fatal("accepted envelope above 128 KiB")
	}
	for _, over := range []int{0, 1} {
		req := ActionRequest{Target: Target{Host: Host{Name: "remote", Address: "trusted-alias"}}, Operation: "command", Args: []string{strings.Repeat("a", 64<<10), ""}}
		v := Invocation{Version: 1, Target: Target{Host: Local}, Args: req.Args}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		req.Args[1] = strings.Repeat("b", (95<<10)-len(b)+over)
		command, err := actionCommand(req)
		if over == 0 {
			if err != nil {
				t.Fatalf("exact command limit rejected: %v", err)
			}
			if _, err := DecodeInvocation(strings.TrimPrefix(command, "coop host-exec ")); err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("accepted command JSON above 95 KiB")
		}
	}
}

func TestActionCommandShellSafeEnvelope(t *testing.T) {
	for _, kind := range []TargetKind{"", Agent, Interactive} {
		t.Run(string(kind), func(t *testing.T) {
			target := Target{Host: Host{Name: "remote", Address: "trusted-alias"}, Kind: kind}
			if kind != "" {
				target.ID = "profile"
			}
			req := ActionRequest{Target: target, Operation: "command", Directory: "/remote/path with spaces/$(private-sentinel)", Args: []string{"run", "", "\n", "two\nlines", "'\"`$(){};&|<>*?[]!\\", "--host", "local", "--request-id", "unchanged-id"}}
			command, err := actionCommand(req)
			if err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^coop host-exec [A-Za-z0-9_-]+$`).MatchString(command) {
				t.Fatalf("user input escaped the fixed shell-safe envelope: %q", command)
			}
			got, err := DecodeInvocation(strings.TrimPrefix(command, "coop host-exec "))
			want := Invocation{Version: 1, Target: target, Directory: req.Directory, Args: req.Args}
			want.Target.Host = Local
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("envelope changed argv or identity: %#v, %v", got, err)
			}
		})
	}
}

// TestActionSSHProcess is a real subprocess, not an injected Command function.
// It records every launch before checking argv so retries/fallbacks cannot hide.
func TestActionSSHProcess(t *testing.T) {
	if os.Getenv("COOP_ACTION_FIXTURE") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(90)
	}
	args := os.Args[separator+1:]
	f, err := os.OpenFile(os.Getenv("COOP_ACTION_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(91)
	}
	err = json.NewEncoder(f).Encode(args)
	_ = f.Close()
	if err != nil {
		os.Exit(92)
	}
	want := []string{"-T", "-x", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=3", "--", "fixture-alias"}
	if len(args) != len(want)+1 || !reflect.DeepEqual(args[:len(args)-1], want) {
		os.Exit(93)
	}
	command := args[len(args)-1]
	if !regexp.MustCompile(`^coop host-exec [A-Za-z0-9_-]+$`).MatchString(command) {
		os.Exit(94)
	}
	v, err := DecodeInvocation(strings.TrimPrefix(command, "coop host-exec "))
	var expected Invocation
	if json.Unmarshal([]byte(os.Getenv("COOP_ACTION_EXPECTED")), &expected) != nil || err != nil || !reflect.DeepEqual(v, expected) {
		os.Exit(95)
	}
	fmt.Fprintln(os.Stderr, "private-sentinel /private/controller/key credential-value")
	if _, err := fmt.Fprintln(os.Stdout, "ACTION_READY"); err != nil {
		os.Exit(97)
	}
	if os.Getenv("COOP_ACTION_MODE") == "block" {
		// No child process or busy loop: CommandContext must kill and reap us.
		time.Sleep(time.Hour)
		os.Exit(96)
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		os.Exit(97)
	}
	switch os.Getenv("COOP_ACTION_MODE") {
	case "exit":
		os.Exit(7)
	case "disconnect":
		os.Exit(255)
	default:
		os.Exit(0)
	}
}

func actionFixture(t *testing.T) (ActionRequest, context.Context, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "fixture", Address: "fixture-alias"}
	if err := r.Change("add", h.Name, h.Address); err != nil {
		t.Fatal(err)
	}
	// A directory would fail a controller-side read; the private file's contents
	// must not replace its path. Nonexistent server paths must stay literal too.
	controllerPath := t.TempDir()
	privateFile := filepath.Join(controllerPath, "controller-private-file")
	if err := os.WriteFile(privateFile, []byte("controller-only-secret-not-for-transport"), 0600); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{Target: Target{Host: h, Kind: Agent, ID: "profile"}, Operation: "command", Directory: "/remote/private-sentinel/project", Args: []string{"run", "--file", controllerPath, "--file", privateFile, "--file", "/remote/nonexistent/private-sentinel", "", "a\nb", "$(private-sentinel); ' \" & | >", "--request-id", "unchanged-id"}}
	v := Invocation{Version: 1, Target: req.Target, Directory: req.Directory, Args: req.Args}
	v.Target.Host = Local
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_ACTION_EXPECTED", string(b))
	t.Setenv("COOP_ACTION_FIXTURE", "1")
	t.Setenv("COOP_ACTION_MODE", "success")
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.jsonl")
	t.Setenv("COOP_ACTION_CALLS", calls)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := "exec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestActionSSHProcess$ -- "
	for _, name := range []string{"ssh", "coop"} {
		prefix := ""
		if name == "coop" {
			prefix = "FALLBACK "
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+command+prefix+"\"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return req, ctx, calls
}

func actionCallCount(t *testing.T, path string, want int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && want == 0 {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	count := 0
	for {
		var args []string
		if err := decoder.Decode(&args); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		count++
		if len(args) > 0 && args[0] == "FALLBACK" {
			t.Error("remote action fell back to controller coop")
		}
	}
	if count != want {
		t.Errorf("SSH/fallback subprocess calls = %d; want %d (no retry or fallback)", count, want)
	}
}

func TestActionExecuteStreamingAndRemotePaths(t *testing.T) {
	req, ctx, calls := actionFixture(t)
	input, feed := io.Pipe()
	output, receive := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = feed.Close(); _ = output.Close(); _ = receive.Close() })
	type outcome struct {
		result ActionResult
		err    error
	}
	done := make(chan outcome, 1)
	var diagnostics bytes.Buffer
	go func() {
		result, err := (SSH{}).Execute(ctx, req, input, receive, &diagnostics)
		_ = receive.Close()
		done <- outcome{result, err}
	}()
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "ACTION_READY\n" {
		t.Fatalf("stdout did not stream before stdin EOF: %q, %v", line, err)
	}
	const payload = "streamed stdin\n\x00empty lines\n\n"
	if _, err := io.WriteString(feed, payload); err != nil {
		t.Fatal(err)
	}
	_ = feed.Close()
	b, err := io.ReadAll(reader)
	if err != nil || string(b) != payload {
		t.Fatalf("stdin/stdout changed: %q, %v", b, err)
	}
	got := <-done
	if got.err != nil || got.result.Target != req.Target || got.result.ExitCode != 0 {
		t.Fatalf("Execute = %#v, %v", got.result, got.err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("private SSH diagnostics reached caller: %q", diagnostics.String())
	}
	actionCallCount(t, calls, 1)
}

func TestActionExecuteExitAndDisconnect(t *testing.T) {
	for _, mode := range []string{"exit", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			req, ctx, calls := actionFixture(t)
			t.Setenv("COOP_ACTION_MODE", mode)
			var stdout, stderr bytes.Buffer
			result, err := (SSH{}).Execute(ctx, req, strings.NewReader("request"), &stdout, &stderr)
			if err == nil {
				t.Fatal("remote failure reported success")
			}
			wantCode := 7
			if mode == "disconnect" {
				wantCode = 255
			}
			if result.Target != req.Target || result.ExitCode != wantCode {
				t.Fatalf("lost selected target or remote exit status: %#v", result)
			}
			var actionErr *ActionError
			if mode == "exit" {
				if !errors.As(err, &actionErr) || actionErr.Code != 7 || actionErr.ExitCode() != 7 || actionErr.RemoteExitCode() != 7 {
					t.Fatalf("ordinary exit lost typed remote code: %v", err)
				}
			} else {
				if errors.As(err, &actionErr) {
					t.Fatalf("disconnect misrepresented as known remote command failure: %v", err)
				}
				for _, phrase := range []string{"outcome may be unknown", "no local fallback or retry"} {
					if !strings.Contains(err.Error(), phrase) {
						t.Errorf("disconnect omitted %q: %v", phrase, err)
					}
				}
			}
			if stdout.String() != "ACTION_READY\nrequest" {
				t.Fatalf("lost remote stdout on failure: %q", stdout.String())
			}
			if stderr.Len() != 0 || strings.Contains(err.Error(), "private-sentinel") || strings.Contains(err.Error(), "credential-value") {
				t.Fatalf("private diagnostics leaked: %q, %v", stderr.String(), err)
			}
			actionCallCount(t, calls, 1)
		})
	}
}

func TestActionExecuteRejectsInvalidAndStaleIdentity(t *testing.T) {
	for _, mode := range []string{"operation", "local", "invalid host", "forged address", "invalid kind", "missing id", "id without kind", "changed registry", "removed registry"} {
		t.Run(mode, func(t *testing.T) {
			req, ctx, calls := actionFixture(t)
			r, err := DefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "operation":
				req.Operation = "private-sentinel"
			case "local":
				req.Target.Host = Local
			case "invalid host":
				req.Target.Host.Address = "-oProxyCommand=private-sentinel"
			case "forged address":
				req.Target.Host.Address = "private-sentinel"
			case "invalid kind":
				req.Target.Kind = "private-sentinel"
			case "missing id":
				req.Target.ID = ""
			case "id without kind":
				req.Target.Kind = ""
			case "changed registry":
				err = r.Change("update", req.Target.Host.Name, "changed-alias")
			case "removed registry":
				err = r.Change("remove", req.Target.Host.Name, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			result, err := (SSH{}).Execute(ctx, req, nil, &stdout, &stderr)
			if err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("invalid/stale identity accepted or echoed: %v", err)
			}
			if result.Target != req.Target || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("rejected action lost identity or produced output: %#v, %q, %q", result, stdout.String(), stderr.String())
			}
			actionCallCount(t, calls, 0)
		})
	}
}

func TestActionExecuteCancellation(t *testing.T) {
	for _, mode := range []string{"already canceled", "running"} {
		t.Run(mode, func(t *testing.T) {
			req, parent, calls := actionFixture(t)
			t.Setenv("COOP_ACTION_MODE", "block")
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			if mode == "already canceled" {
				cancel()
				result, err := (SSH{}).Execute(ctx, req, nil, io.Discard, io.Discard)
				if !errors.Is(err, context.Canceled) || result.Target != req.Target {
					t.Fatalf("pre-canceled Execute = %#v, %v", result, err)
				}
				actionCallCount(t, calls, 0)
				return
			}
			output, receive := io.Pipe()
			defer func() { _ = output.Close() }()
			defer func() { _ = receive.Close() }()
			done := make(chan error, 1)
			go func() {
				result, err := (SSH{}).Execute(ctx, req, nil, receive, io.Discard)
				if result.Target != req.Target {
					t.Errorf("cancellation lost selected target: %#v", result)
				}
				_ = receive.Close()
				done <- err
			}()
			if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ACTION_READY\n" {
				t.Fatalf("SSH helper never started: %q, %v", line, err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost context error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Execute did not reap SSH promptly after cancellation")
			}
			actionCallCount(t, calls, 1)
		})
	}
}

func TestActionExecuteMissingSSHNoFallback(t *testing.T) {
	req, ctx, calls := actionFixture(t)
	// Retain the local coop tripwire but remove only the SSH executable.
	if err := os.Remove(filepath.Join(os.Getenv("PATH"), "ssh")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := (SSH{}).Execute(ctx, req, nil, &stdout, &stderr)
	if err == nil || result.Target != req.Target || result.ExitCode != 255 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("missing SSH did not fail closed: %#v, %v", result, err)
	}
	if strings.Contains(err.Error(), os.Getenv("PATH")) {
		t.Fatalf("startup failure exposed controller path: %v", err)
	}
	actionCallCount(t, calls, 0)
}
