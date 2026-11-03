package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func TestMemoryRevisionAndIsolation(t *testing.T) {
	s, owner, bird, _ := storeFixture(t)
	ctx := t.Context()
	in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "projects/coop.md"}, Body: "Use Kata for tasks.", Source: "markdown:projects/coop.md"}
	first, err := s.WriteMemory(ctx, bird, in)
	if err != nil || first.AgentID != bird.ID || first.Revision != 1 || first.UpdatedBy != bird.ID {
		t.Fatal(first, err)
	}
	retry, err := s.WriteMemory(ctx, bird, in)
	if err != nil || retry != first {
		t.Fatal("lost-response retry changed memory", retry, err)
	}
	other, _ := s.Principal(ctx, "agent-two")
	for _, actor := range []c.Principal{other, {ID: "missing"}} {
		q := c.MemoryQuery{AgentID: bird.ID, Key: in.Key}
		if _, err = s.ReadMemory(ctx, actor, q); !errors.Is(err, c.ErrForbidden) {
			t.Fatal("read another bird's memory", err)
		}
		if _, err = s.ListMemory(ctx, actor, c.MemoryList{AgentID: bird.ID}); !errors.Is(err, c.ErrForbidden) {
			t.Fatal("listed another bird's memory", err)
		}
		if _, err = s.WriteMemory(ctx, actor, c.MemoryWrite{MemoryQuery: q, Body: "overwrite", ExpectedRevision: 1}); !errors.Is(err, c.ErrForbidden) {
			t.Fatal("wrote another bird's memory", err)
		}
	}
	// The owner can inspect same-engagement memory by stable identity or alias.
	got, err := s.ReadMemory(ctx, owner, c.MemoryQuery{AgentID: bird.Alias, Key: in.Key})
	if err != nil || got != first {
		t.Fatal(got, err)
	}
	in.ExpectedRevision, in.Body = 1, "Rook owns merging."
	updated, err := s.WriteMemory(ctx, bird, in)
	if err != nil || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	in.ExpectedRevision, in.Body = 0, first.Body
	if _, err = s.WriteMemory(ctx, bird, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale retry rolled back newer knowledge", err)
	}
	// Re-fetch current permissions, not the stale actor struct passed by callers.
	current := bird
	current.Capabilities = []string{"read"}
	if err = s.Configure(ctx, []c.Principal{current}, nil); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = 2
	if _, err = s.WriteMemory(ctx, bird, in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("revoked writer retained access", err)
	}
	owner.Engagements = []string{"outside"}
	if err = s.Configure(ctx, []c.Principal{owner}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadMemory(ctx, owner, c.MemoryQuery{AgentID: bird.ID, Key: in.Key}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("owner crossed engagement boundary", err)
	}
}

func TestMemoryConcurrentEdits(t *testing.T) {
	s, _, bird, _ := storeFixture(t)
	in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "notes"}, Body: "initial"}
	if _, err := s.WriteMemory(t.Context(), bird, in); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsOut := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			edit := in
			edit.ExpectedRevision, edit.Body = 1, fmt.Sprintf("edit %d", i)
			_, err := s.WriteMemory(t.Context(), bird, edit)
			errorsOut <- err
		})
	}
	wg.Wait()
	close(errorsOut)
	wins := 0
	for err := range errorsOut {
		if err == nil {
			wins++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d writers overwrote the same revision", wins)
	}
}

func TestMemoryPaginationAndLimits(t *testing.T) {
	s, _, bird, _ := storeFixture(t)
	for i := range 52 {
		if _, err := s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: fmt.Sprintf("notes/%03d", i)}, Body: "knowledge"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListMemory(t.Context(), bird, c.MemoryList{Prefix: "notes/"})
	if err != nil || len(page.Entries) != 50 || page.Next != "notes/049" {
		t.Fatal(page, err)
	}
	next, err := s.ListMemory(t.Context(), bird, c.MemoryList{After: page.Next, Prefix: "notes/"})
	if err != nil || len(next.Entries) != 2 || next.Next != "" {
		t.Fatal(next, err)
	}
	// Prefixes are literal, not SQL LIKE patterns.
	page, err = s.ListMemory(t.Context(), bird, c.MemoryList{Prefix: "notes/%"})
	if err != nil || len(page.Entries) != 0 {
		t.Fatal(page, err)
	}
	for _, key := range []string{"", ".", "../secret", "/absolute", "a/../b", "a\\b", "a\n"} {
		if _, err = s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: key}, Body: "body"}); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("invalid key %q: %v", key, err)
		}
	}
	if _, err = s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "large"}, Body: strings.Repeat("x", c.MaxText+1)}); !errors.Is(err, c.ErrSize) {
		t.Fatal(err)
	}
	if _, err = s.ReadMemory(t.Context(), bird, c.MemoryQuery{Key: "missing"}); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMemoryCapacityStillAllowsEdits(t *testing.T) {
	s, _, bird, _ := storeFixture(t)
	for i := range c.MaxMemoryEntries {
		if _, err := s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: fmt.Sprintf("note-%03d", i)}, Body: "knowledge"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "overflow"}, Body: "knowledge"}); !errors.Is(err, c.ErrCapacity) {
		t.Fatal("memory quota not enforced", err)
	}
	if got, err := s.WriteMemory(t.Context(), bird, c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "note-000"}, Body: "updated", ExpectedRevision: 1}); err != nil || got.Revision != 2 {
		t.Fatal("quota prevented editing existing knowledge", got, err)
	}
}

func TestMemorySurvivesPlacementRestartAndBackup(t *testing.T) {
	s, owner, bird, _ := storeFixture(t)
	ctx := t.Context()
	in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "identity"}, Body: "I am a bird, not a container."}
	memory, err := s.WriteMemory(ctx, bird, in)
	if err != nil {
		t.Fatal(err)
	}
	moveMemoryBird(t, s, owner, bird)
	path, lineage, id := s.path, s.epochPath, s.id
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, StoreConfig{Path: path, MasterID: id})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.ReadMemory(ctx, bird, in.MemoryQuery)
	if err != nil || got != memory {
		t.Fatal("placement/restart lost memory", got, err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = reopened.Backup(ctx, backup, nil); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = Restore(ctx, backup, target, lineage, false); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), EpochPath: lineage, MasterID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	got, err = restored.ReadMemory(ctx, bird, in.MemoryQuery)
	if err != nil || got != memory {
		t.Fatal("backup/restore lost memory", got, err)
	}
	if _, err = restored.WriteMemory(ctx, bird, in); !errors.Is(err, c.ErrEpoch) {
		t.Fatal("restore hold bypassed", err)
	}
}

func TestMemoryHTTPPlacementAndLostResponse(t *testing.T) {
	s, owner, bird, _ := storeFixture(t)
	policy := policyFixture(owner, bird)
	policy.AllowedHosts = []string{"127.0.0.1"}
	handler := NewHandler(&Service{Store: s}, policy)
	var drop atomic.Bool
	drop.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/memory/write" && drop.CompareAndSwap(true, false) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Error(recorder.Code, recorder.Body.String())
			}
			w.Header().Set("X-Coop-Master-ID", "test-master")
			writeError(w, c.ErrUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: "test-master", Token: "agent-test-token", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{Key: "notes"}, Body: "remember"}
	if _, err = client.WriteMemory(t.Context(), in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("expected lost response", err)
	}
	if got, e := client.WriteMemory(t.Context(), in); e != nil || got.Revision != 1 {
		t.Fatal("duplicate write after lost response", got, e)
	}
	moveMemoryBird(t, s, owner, bird)
	if _, err = client.ReadMemory(t.Context(), in.MemoryQuery); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("retired container still reads memory", err)
	}
	hash := sha256.Sum256([]byte("replacement-token"))
	// Use a fresh handler to avoid mutating a live policy concurrently.
	replacementPolicy := *policy
	replacementPolicy.Tokens = append([]Token{{PrincipalID: bird.ID, SHA256: hex.EncodeToString(hash[:]), InstanceID: "new", Generation: 2}}, policy.Tokens...)
	replacement := httptest.NewServer(NewHandler(&Service{Store: s}, &replacementPolicy))
	defer replacement.Close()
	client, err = masterclient.New(masterclient.ClientConfig{URL: replacement.URL, MasterID: "test-master", Token: "replacement-token", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadMemory(context.Background(), in.MemoryQuery)
	if err != nil || got.Body != in.Body || got.Revision != 1 {
		t.Fatal("new container could not recover knowledge", got, err)
	}
	page, err := client.ListMemory(t.Context(), c.MemoryList{})
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "remember") {
		t.Fatal("list leaked bodies")
	}
}

func moveMemoryBird(t *testing.T, s *Store, owner, bird c.Principal) {
	t.Helper()
	if err := s.Quiesce(t.Context(), owner, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileGuest(t.Context(), bird, c.GuestRecovery{Placement: c.Placement{AgentID: bird.ID, HostID: "host", InstanceID: "instance", Generation: 1}, RestoreEpoch: 1, Complete: true, ManifestSHA256: c.RecoveryDigest(nil)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlacement(t.Context(), owner, c.PlacementChange{Placement: c.Placement{AgentID: bird.ID, HostID: "replacement", InstanceID: "new", Generation: 2}, ExpectedGeneration: 1, RestoreEpoch: 1}); err != nil {
		t.Fatal(err)
	}
}
