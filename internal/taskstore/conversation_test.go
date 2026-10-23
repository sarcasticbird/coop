package taskstore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A real authenticated Unix HTTP boundary verifies native request bounds and
// pinning without a production daemon or its credentials.
func conversationProvider(t *testing.T, handler http.HandlerFunc) WorkBackend {
	t.Helper()
	cfg := testConfig(t)
	cfg.InstanceUID = testProject
	socket := filepath.Join(t.TempDir(), "kata.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg.Binary = filepath.Join(cfg.Home, "kata")
	script := "#!/bin/sh\necho '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix://" + socket + "\"}'\n"
	if err = os.WriteFile(cfg.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cfg.Home, "config.toml"), []byte("[auth]\ntoken = 'private-fixture-token'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b.(WorkBackend)
}
func TestKataEventsRemainBounded(t *testing.T) {
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-fixture-token" {
			t.Error("private auth absent")
		}
		switch r.URL.Path {
		case "/api/v1/instance":
			if err := json.NewEncoder(w).Encode(map[string]any{"instance_uid": testProject}); err != nil {
				t.Error(err)
			}
		case "/api/v1/projects":
			if err := json.NewEncoder(w).Encode(map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}}); err != nil {
				t.Error(err)
			}
		case "/api/v1/projects/1/events":
			if r.URL.Query().Get("limit") != "7" || r.URL.Query().Get("after_id") != "42" {
				t.Errorf("unbounded request %s", r.URL)
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "next_after_id": 42}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected history read %s", r.URL)
			http.NotFound(w, r)
		}
	})
	cursor := EventCursor{InstanceUID: testProject, ProjectUID: testProject, AfterID: 42}
	if _, err := b.Events(context.Background(), cursor, 7); err != nil {
		t.Fatal(err)
	}
	cursor.InstanceUID = testIssue
	if _, err := b.Events(context.Background(), cursor, 7); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("wrong instance accepted: %v", err)
	}
}

func TestKataNativeConversationIntegration(t *testing.T) {
	home := os.Getenv("COOP_KATA_TEST_HOME")
	if home == "" {
		t.Skip("requires explicitly selected disposable native profile")
	}
	cfg := Config{Provider: "kata", Binary: "/opt/homebrew/bin/kata", Home: home, ProjectName: "coop-consolidation-acceptance", ProjectUID: "01M5N8JN3PA04EJRZSHHKHY4TJ", InstanceUID: "01M5N8JCKTYDTF0CE5FXR9AKX8", PolicySHA: strings.Repeat("a", 64)}
	backend, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := backend.(WorkBackend)
	ctx := context.Background()
	uid := "01M5N9PEZ0EV8FX9FHW605SP4Q"
	issue, err := b.InspectIssue(ctx, uid)
	if err != nil || issue.UID != uid {
		t.Fatalf("native issue %+v %v", issue, err)
	}
	in := CommentWrite{IssueUID: uid, Body: "Native provider integration idempotent comment", RequestID: "native-provider-integration-v1"}
	actor := c.Principal{ID: "coop-fixture-gateway"}
	first, err := b.AppendComment(ctx, actor, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.AppendComment(ctx, actor, in)
	if err != nil || again.UID != first.UID {
		t.Fatalf("native replay %+v %v", again, err)
	}
	reply, err := b.AppendComment(ctx, actor, CommentWrite{IssueUID: uid, Body: "Native provider reply ancestry verification", ReplyToUID: first.UID, RequestID: "native-provider-reply-v1"})
	if err != nil || reply.ReplyToUID != first.UID {
		t.Fatalf("native reply %+v %v", reply, err)
	}
	notice, err := b.Notify(ctx, actor, NotificationWrite{IssueUID: uid, Recipient: "rook", Message: "Native provider correlation verification", Correlation: "native-fixture-correlation"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := EventCursor{InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID}
	page, err := b.Events(ctx, cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Events {
		if x, ok := EventComment(e); ok && x.UID == first.UID {
			found = true
			if x.Body != in.Body || x.Author != actor.ID {
				t.Fatalf("native comment payload %+v", x)
			}
		}
	}
	if !found {
		t.Fatal("native event lacked canonical comment")
	}
	if notice.EventID > 0 {
		found = false
		for _, e := range page.Events {
			if e.ID == notice.EventID && e.Type == "issue.metadata_updated" {
				found = true
			}
		}
		if !found {
			t.Fatal("native notification event missing")
		}
	}
}

func TestKataTaskReadDoesNotLoadDiscussion(t *testing.T) {
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/instance":
			if err := json.NewEncoder(w).Encode(map[string]any{"instance_uid": testProject}); err != nil {
				t.Error(err)
			}
		case "/api/v1/projects":
			if err := json.NewEncoder(w).Encode(map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}}); err != nil {
				t.Error(err)
			}
		case "/api/v1/projects/1/issues/" + testIssue:
			if !strings.HasPrefix(r.URL.Query().Get("inbound"), "coop-inspect-") {
				t.Fatalf("unbounded task read %s", r.URL)
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject, "title": "task", "body": "scope", "status": "open", "revision": 1, "metadata": map[string]any{"coop.creation": map[string]any{"repository": "example/coop"}}}}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected read %s", r.URL)
			http.NotFound(w, r)
		}
	})
	ref, _ := c.KataTaskReference(testProject, testIssue, "example/coop")
	task, err := b.(Backend).Read(context.Background(), ref)
	if err != nil || task.Title != "task" || task.Body != "scope" {
		t.Fatalf("bounded task %+v %v", task, err)
	}
}
