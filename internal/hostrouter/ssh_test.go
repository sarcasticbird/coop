package hostrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sshSnapshotBytes(t *testing.T, snapshot Snapshot) []byte {
	t.Helper()
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSSHUsesFixedCommandAndSafeArgv(t *testing.T) {
	rows := []InventoryEntry{testEntry(Agent, "profile"), testEntry(Interactive, "runtime")}
	rows[1].Project = "/remote/path with spaces/$(not-a-command)"
	response := sshSnapshotBytes(t, Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: rows}}})
	for _, address := range []string{"trusted-alias", "deploy@host.example", "192.0.2.1", "deploy@2001:db8::1"} {
		t.Run(address, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "marker")
			calls := 0
			s := SSH{Command: func(gotCtx context.Context, args []string, stdout io.Writer) error {
				calls++
				if gotCtx.Value(contextKey{}) != "marker" {
					t.Error("caller context not passed to command")
				}
				want := []string{"-T", "-x", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=3", "--", address, "coop --host local inventory --json"}
				if !reflect.DeepEqual(args, want) {
					t.Errorf("SSH argv = %#v; want %#v", args, want)
				}
				_, err := stdout.Write(response)
				return err
			}}
			got, err := s.Inventory(ctx, Host{Name: "selected-host", Address: address})
			if err != nil || !reflect.DeepEqual(got, rows) || calls != 1 {
				t.Fatalf("Inventory() = %#v, %v; command calls=%d", got, err, calls)
			}
		})
	}
}

func TestSSHRejectsUserInputBeforeCommand(t *testing.T) {
	for _, h := range []Host{
		Local,
		{Name: "local", Address: "alias"},
		{Name: "private-sentinel;cmd", Address: "alias"},
		{Name: "remote", Address: "-oProxyCommand=private-sentinel"},
		{Name: "remote", Address: "alias private-sentinel"},
		{Name: "remote", Address: "alias;private-sentinel"},
		{Name: "remote", Address: "$(private-sentinel)"},
		{Name: "remote", Address: "alias\nprivate-sentinel"},
		{Name: "remote", Address: "user@-oProxyCommand=private-sentinel"},
		{Name: "remote", Address: "user:private-sentinel@alias"},
	} {
		t.Run(h.Name+"/"+h.Address, func(t *testing.T) {
			called := false
			s := SSH{Command: func(context.Context, []string, io.Writer) error {
				called = true
				return nil
			}}
			if _, err := s.Inventory(context.Background(), h); err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("unsafe host accepted or echoed: %v", err)
			}
			if called {
				t.Fatal("unsafe host reached SSH command")
			}
		})
	}
}

func TestSSHRejectsMalformedRemoteOutput(t *testing.T) {
	valid := Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: []InventoryEntry{testEntry(Agent, "profile")}}}}
	validBytes := sshSnapshotBytes(t, valid)
	fixtures := map[string][]byte{
		"empty":                nil,
		"garbage":              []byte("private-sentinel not JSON"),
		"truncated":            []byte(`{"version":1,"hosts":[`),
		"trailing document":    append(append([]byte{}, validBytes...), []byte(` {"private-sentinel":true}`)...),
		"unknown field":        []byte(`{"version":1,"hosts":[],"token":"private-sentinel"}`),
		"null":                 []byte(`null`),
		"null hosts":           []byte(`{"version":1,"hosts":null}`),
		"null entries":         []byte(`{"version":1,"hosts":[{"host":{"name":"local"},"entries":null}]}`),
		"missing entries":      []byte(`{"version":1,"hosts":[{"host":{"name":"local"}}]}`),
		"nested unknown field": []byte(`{"version":1,"hosts":[{"host":{"name":"local","token":"private-sentinel"},"entries":[]}]}`),
		"unknown entry field":  []byte(`{"version":1,"hosts":[{"host":{"name":"local"},"entries":[{"target":{"host":{"name":"local"},"kind":"agent","id":"profile"},"name":"name","state":"running","token":"private-sentinel"}]}]}`),
	}
	for name, change := range map[string]func(*Snapshot){
		"wrong version":     func(s *Snapshot) { s.Version = 2 },
		"missing hosts":     func(s *Snapshot) { s.Hosts = []HostInventory{} },
		"multiple hosts":    func(s *Snapshot) { s.Hosts = append(s.Hosts, s.Hosts[0]) },
		"remote host claim": func(s *Snapshot) { s.Hosts[0].Host = Host{Name: "forged", Address: "private-sentinel"} },
		"remote error":      func(s *Snapshot) { s.Hosts[0].Error = "private-sentinel" },
		"remote target claim": func(s *Snapshot) {
			s.Hosts[0].Entries[0].Target.Host = Host{Name: "forged", Address: "private-sentinel"}
		},
		"invalid target kind": func(s *Snapshot) { s.Hosts[0].Entries[0].Target.Kind = "private-sentinel" },
		"invalid target id":   func(s *Snapshot) { s.Hosts[0].Entries[0].Target.ID = "../private-sentinel" },
		"control name":        func(s *Snapshot) { s.Hosts[0].Entries[0].Name = "private-sentinel\x1b[2J" },
		"invalid state":       func(s *Snapshot) { s.Hosts[0].Entries[0].State = "private-sentinel" },
		"duplicate target":    func(s *Snapshot) { s.Hosts[0].Entries = append(s.Hosts[0].Entries, s.Hosts[0].Entries[0]) },
	} {
		var snapshot Snapshot
		if err := json.Unmarshal(validBytes, &snapshot); err != nil {
			t.Fatal(err)
		}
		change(&snapshot)
		fixtures[name] = sshSnapshotBytes(t, snapshot)
	}
	for name, response := range fixtures {
		t.Run(name, func(t *testing.T) {
			s := SSH{Command: func(_ context.Context, _ []string, stdout io.Writer) error {
				_, err := stdout.Write(response)
				return err
			}}
			rows, err := s.Inventory(context.Background(), Host{Name: "remote", Address: "alias"})
			if err == nil || strings.Contains(err.Error(), "private-sentinel") || len(rows) != 0 {
				t.Fatalf("malformed remote inventory accepted or echoed: %#v, %v", rows, err)
			}
		})
	}
}

func TestSSHResponseSizeBoundary(t *testing.T) {
	response := sshSnapshotBytes(t, Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: []InventoryEntry{}}}})
	const limit = 4 << 20
	for _, tc := range []struct {
		name string
		size int
	}{{"exact limit", limit}, {"one byte over", limit + 1}, {"double limit", 2 * limit}} {
		t.Run(tc.name, func(t *testing.T) {
			payload := append(append([]byte{}, response...), bytes.Repeat([]byte(" "), tc.size-len(response))...)
			s := SSH{Command: func(_ context.Context, _ []string, stdout io.Writer) error {
				// A valid prefix must not disguise truncated excess output.
				for len(payload) > 0 {
					n := min(len(payload), 32<<10)
					if _, err := stdout.Write(payload[:n]); err != nil {
						return err
					}
					payload = payload[n:]
				}
				return nil
			}}
			rows, err := s.Inventory(context.Background(), Host{Name: "remote", Address: "alias"})
			if tc.size == limit {
				if err != nil || rows == nil || len(rows) != 0 {
					t.Fatalf("exact limit rejected: %#v, %v", rows, err)
				}
			} else if err == nil || err.Error() != "inventory response too large" || len(rows) != 0 {
				t.Fatalf("oversized response accepted: %#v, %v", rows, err)
			}
		})
	}
}

func TestSSHTransportFailureIsSafe(t *testing.T) {
	response := sshSnapshotBytes(t, Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: []InventoryEntry{testEntry(Agent, "partial")}}}})
	s := SSH{Command: func(_ context.Context, _ []string, stdout io.Writer) error {
		if _, err := stdout.Write(response); err != nil {
			return err
		}
		return errors.New("private-sentinel SSH configuration and key path")
	}}
	rows, err := s.Inventory(context.Background(), Host{Name: "remote", Address: "alias"})
	if err == nil || err.Error() != "SSH inventory unavailable" || len(rows) != 0 {
		t.Fatalf("transport failure leaked output or diagnostics: %#v, %v", rows, err)
	}
}

func TestSSHContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s := SSH{Command: func(ctx context.Context, _ []string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	rows, err := s.Inventory(ctx, Host{Name: "remote", Address: "alias"})
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || err == nil || err.Error() != "SSH inventory unavailable" || len(rows) != 0 {
		t.Fatalf("timeout = %#v, %v; context error=%v", rows, err, ctx.Err())
	}
}

func TestSSHSubprocessFixture(t *testing.T) {
	dir := t.TempDir()
	response := sshSnapshotBytes(t, Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: []InventoryEntry{}}}})
	// A fake executable exercises the real subprocess path without connecting.
	script := "#!/bin/sh\nprintf '%s' '" + string(response) + "'\nprintf '%s' 'private-sentinel' >&2\n"
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	rows, err := (SSH{}).Inventory(context.Background(), Host{Name: "fixture", Address: "fixture-alias"})
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("subprocess inventory = %#v, %v", rows, err)
	}
	if err := os.WriteFile(path, []byte(script+"exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	rows, err = (SSH{}).Inventory(context.Background(), Host{Name: "fixture", Address: "fixture-alias"})
	if err == nil || strings.Contains(err.Error(), "private-sentinel") || len(rows) != 0 {
		t.Fatalf("subprocess failure = %#v, %v", rows, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := (SSH{}).Inventory(ctx, Host{Name: "fixture", Address: "fixture-alias"}); err == nil || time.Since(started) > time.Second {
		t.Fatalf("subprocess timeout = %v after %s", err, time.Since(started))
	}
}
