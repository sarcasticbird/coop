package hostrouter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inventoryTestFunc func(context.Context, Host) ([]InventoryEntry, error)

func (f inventoryTestFunc) Inventory(ctx context.Context, h Host) ([]InventoryEntry, error) {
	return f(ctx, h)
}

func testEntry(kind TargetKind, id string) InventoryEntry {
	return InventoryEntry{Target: Target{Host: Local, Kind: kind, ID: id}, Name: "same-display-name", State: "running", Project: "/remote/project", Owned: true}
}

func TestRouterCombinesAndSortsWithoutIdentityCollisions(t *testing.T) {
	alpha := Host{Name: "alpha", Address: "alpha-alias"}
	zulu := Host{Name: "zulu", Address: "zulu-alias"}
	rows := []InventoryEntry{testEntry(Interactive, "shared"), testEntry(Agent, "zulu"), testEntry(Agent, "shared")}
	rows[0].Target.Host = Host{Name: "forged", Address: "forged-alias"}
	client := inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) { return rows, nil })
	r := Router{Local: client, Remote: client}
	want := Snapshot{Version: 1, Hosts: []HostInventory{}}
	for _, h := range []Host{alpha, Local, zulu} {
		entries := []InventoryEntry{rows[2], rows[1], rows[0]}
		for i := range entries {
			entries[i].Target.Host = h
		}
		want.Hosts = append(want.Hosts, HostInventory{Host: h, Entries: entries})
	}
	for i := range 10 {
		got := r.Inventory(context.Background(), []Host{zulu, Local, alpha})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: combined inventory = %#v; want %#v", i, got, want)
		}
		seen := map[Target]bool{}
		for _, h := range got.Hosts {
			for _, row := range h.Entries {
				if seen[row.Target] {
					t.Fatalf("host/kind identity collided: %#v", row.Target)
				}
				seen[row.Target] = true
			}
		}
		if len(seen) != 9 {
			t.Fatalf("lost identities: %d", len(seen))
		}
	}
	if rows[0].Target.Host.Name != "forged" {
		t.Fatal("router mutated client-owned inventory")
	}
}

func TestResolveTargetRejectsStaleAndInvalidIdentity(t *testing.T) {
	r := testRegistry(t)
	if err := r.Change("add", "fleet", "old-alias"); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "fleet", Address: "old-alias"}
	for _, kind := range []TargetKind{Agent, Interactive} {
		target := Target{Host: h, Kind: kind, ID: "same-id"}
		if got, err := r.ResolveTarget(target); err != nil || got != h {
			t.Fatalf("valid target = %#v, %v", got, err)
		}
	}
	for _, target := range []Target{
		{Host: h, Kind: "shell", ID: "same-id"},
		{Host: h, Kind: Agent, ID: ""},
		{Host: h, Kind: Agent, ID: "../../private-sentinel"},
		{Host: Host{Name: "local", Address: "old-alias"}, Kind: Agent, ID: "same-id"},
		{Host: Host{Name: "unknown", Address: "old-alias"}, Kind: Agent, ID: "same-id"},
	} {
		if _, err := r.ResolveTarget(target); err == nil || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatalf("invalid target accepted or echoed: %#v, %v", target, err)
		}
	}
	stale := Target{Host: h, Kind: Agent, ID: "same-id"}
	if err := r.Change("update", "fleet", "new-alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveTarget(stale); err == nil {
		t.Fatal("stale registration address accepted")
	}
	fresh := stale
	fresh.Host.Address = "new-alias"
	if got, err := r.ResolveTarget(fresh); err != nil || got != fresh.Host {
		t.Fatalf("refreshed target = %#v, %v", got, err)
	}
	if err := r.Change("remove", "fleet", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveTarget(fresh); err == nil {
		t.Fatal("removed registration target accepted")
	}
	writeRegistryFixture(t, r, []byte("private-sentinel malformed registry"))
	localTarget := Target{Host: Local, Kind: Agent, ID: "not-a-local-profile"}
	if got, err := r.ResolveTarget(localTarget); err != nil || got != Local {
		t.Fatalf("local identity depended on remote registry or profile: %#v, %v", got, err)
	}
}

func TestRouterWatchDeliversLocalBeforeBlockedRemotes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 10)
	local := []InventoryEntry{testEntry(Agent, "local-agent")}
	r := Router{
		Local: inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) { return local, nil }),
		Remote: inventoryTestFunc(func(ctx context.Context, h Host) ([]InventoryEntry, error) {
			started <- struct{}{}
			select {
			case <-release:
				return nil, errors.New("private-sentinel remote offline")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
	}
	hosts := []Host{}
	for i := range 10 {
		hosts = append(hosts, Host{Name: fmt.Sprintf("remote-%02d", i), Address: "alias"})
	}
	hosts = append(hosts, Local)
	out := r.Watch(ctx, hosts)
	// Fill every remote slot while their clients are explicitly blocked.
	for range 8 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("remote fixtures did not start")
		}
	}
	select {
	case got, ok := <-out:
		if !ok || got.Host != Local || got.Error != "" || !reflect.DeepEqual(got.Entries, local) {
			t.Fatalf("first watch result = %#v, open=%v; want immediate local result", got, ok)
		}
	case <-ctx.Done():
		t.Fatal("local result waited behind blocked remote inventory")
	}
	cancel()
	count := 1
	for got := range out {
		count++
		if got.Host == Local || got.Error != "timeout" || len(got.Entries) != 0 {
			t.Fatalf("canceled remote result = %#v", got)
		}
	}
	if count != len(hosts) {
		t.Fatalf("Watch delivered %d results; want %d", count, len(hosts))
	}
}

func TestRouterOfflineRemotePreservesLocal(t *testing.T) {
	remote := Host{Name: "offline", Address: "offline-alias"}
	local := []InventoryEntry{testEntry(Interactive, "local-coop")}
	r := Router{
		Local: inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) { return local, nil }),
		Remote: inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) {
			return []InventoryEntry{testEntry(Agent, "partial")}, errors.New("private-sentinel ssh diagnostic")
		}),
	}
	want := Snapshot{Version: 1, Hosts: []HostInventory{
		{Host: Local, Entries: local},
		{Host: remote, Entries: []InventoryEntry{}, Error: "unreachable"},
	}}
	if got := r.Inventory(context.Background(), []Host{remote, Local}); !reflect.DeepEqual(got, want) {
		t.Fatalf("offline remote lost local data or leaked diagnostics: %#v", got)
	}
}

func TestRouterTimeoutAndParentCancellation(t *testing.T) {
	for _, parentCanceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("parentCanceled=%v", parentCanceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if parentCanceled {
				cancel()
			}
			client := inventoryTestFunc(func(ctx context.Context, _ Host) ([]InventoryEntry, error) {
				if _, ok := ctx.Deadline(); !ok {
					return nil, errors.New("missing deadline")
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			r := Router{Local: client, Remote: client, Timeout: 20 * time.Millisecond}
			done := make(chan Snapshot, 1)
			go func() { done <- r.Inventory(ctx, []Host{Local, {Name: "remote", Address: "alias"}}) }()
			select {
			case got := <-done:
				if len(got.Hosts) != 2 {
					t.Fatalf("timeout lost results: %#v", got)
				}
				for _, h := range got.Hosts {
					if h.Error != "timeout" || len(h.Entries) != 0 {
						t.Fatalf("timeout result = %#v", h)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("inventory failed to honor cancellation/deadline")
			}
		})
	}
}

func TestRouterRejectsInvalidInventoryAndUnavailableClients(t *testing.T) {
	remote := Host{Name: "remote", Address: "alias"}
	for _, tc := range []struct {
		name   string
		host   Host
		client InventoryClient
		want   string
	}{
		{"nil local", Local, nil, "invalid_host"},
		{"nil remote", remote, nil, "invalid_host"},
		{"invalid host", Host{Name: "local", Address: "forged"}, nil, "invalid_host"},
		{"local failure", Local, inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) { return nil, errors.New("private-sentinel") }), "inventory_unavailable"},
		{"duplicate identity", remote, inventoryTestFunc(func(context.Context, Host) ([]InventoryEntry, error) {
			return []InventoryEntry{testEntry(Agent, "duplicate"), testEntry(Agent, "duplicate")}, nil
		}), "invalid_inventory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Router{Local: tc.client, Remote: tc.client}
			got := r.Inventory(context.Background(), []Host{tc.host})
			if len(got.Hosts) != 1 || got.Hosts[0].Error != tc.want || len(got.Hosts[0].Entries) != 0 {
				t.Fatalf("inventory = %#v; want safe error %q", got, tc.want)
			}
		})
	}
}

func TestValidateEntries(t *testing.T) {
	valid := testEntry(Agent, "valid-id")
	for _, tc := range []struct {
		name   string
		change func(*InventoryEntry)
	}{
		{"kind", func(e *InventoryEntry) { e.Target.Kind = "shell" }},
		{"empty id", func(e *InventoryEntry) { e.Target.ID = "" }},
		{"option id", func(e *InventoryEntry) { e.Target.ID = "-option" }},
		{"path id", func(e *InventoryEntry) { e.Target.ID = "../private-sentinel" }},
		{"long id", func(e *InventoryEntry) { e.Target.ID = strings.Repeat("a", 256) }},
		{"control name", func(e *InventoryEntry) { e.Name = "private-sentinel\x1b[2J" }},
		{"long name", func(e *InventoryEntry) { e.Name = strings.Repeat("a", 256) }},
		{"control project", func(e *InventoryEntry) { e.Project = "private-sentinel\npath" }},
		{"long project", func(e *InventoryEntry) { e.Project = strings.Repeat("a", 4097) }},
		{"state", func(e *InventoryEntry) { e.State = "private-sentinel" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := valid
			tc.change(&entry)
			if err := ValidateEntries([]InventoryEntry{entry}); err == nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("invalid entry accepted or echoed: %v", err)
			}
		})
	}
	for _, state := range []string{"running", "stopped", "absent"} {
		entry := valid
		entry.State = state
		entry.Target.ID = strings.Repeat("a", 255)
		entry.Name = strings.Repeat("n", 255)
		entry.Project = strings.Repeat("p", 4096)
		if err := ValidateEntries([]InventoryEntry{entry}); err != nil {
			t.Fatalf("valid boundary rejected: %v", err)
		}
	}
	otherKind := valid
	otherKind.Target.Kind = Interactive
	if err := ValidateEntries([]InventoryEntry{valid, otherKind}); err != nil {
		t.Fatalf("same id across kinds rejected: %v", err)
	}
	forged := valid
	forged.Target.Host = Host{Name: "forged", Address: "alias"}
	if err := ValidateEntries([]InventoryEntry{valid, forged}); err == nil {
		t.Fatal("duplicate identity bypassed validation through a remote host claim")
	}
}

func TestRouterEmptyInventory(t *testing.T) {
	if got := (Router{}).Inventory(context.Background(), nil); !reflect.DeepEqual(got, Snapshot{Version: 1, Hosts: []HostInventory{}}) {
		t.Fatalf("empty inventory = %#v", got)
	}
}
