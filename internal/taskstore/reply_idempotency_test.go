package taskstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Kata's one-reply-per-author policy differs from transport idempotency. A
// distinct Coop message may reply to the same parent, while retrying its exact
// immutable key must still return the original comment.
func TestKataDistinctRepliesKeepIdempotency(t *testing.T) {
	type write struct {
		Actor string `json:"actor"`
		Body  string `json:"body"`
		Reply string `json:"reply_to"`
		Kind  string `json:"kind"`
		Force bool   `json:"force"`
	}
	type saved struct {
		write write
		uid   string
	}
	receipts := map[string]saved{}
	replies := 0
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		encode := func(v any) {
			t.Helper()
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case "/api/v1/instance":
			encode(map[string]any{"instance_uid": testProject})
		case "/api/v1/projects":
			encode(map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}})
		case "/api/v1/projects/1/issues/" + testIssue:
			encode(map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject, "revision": 1}})
		case "/api/v1/projects/1/issues/" + testIssue + "/comments":
			var in write
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				t.Error("missing immutable key")
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			prior, ok := receipts[key]
			if ok {
				// Kata fingerprints actor/body/ancestry/kind, independently of force.
				a, z := in, prior.write
				a.Force = false
				z.Force = false
				if a != z {
					http.Error(w, "idempotency_conflict", http.StatusConflict)
					return
				}
			} else {
				if in.Reply != "" {
					if in.Kind != "reply" || in.Reply != testProject {
						t.Error("reply ancestry changed")
						http.Error(w, "invalid", http.StatusBadRequest)
						return
					}
					if replies > 0 && !in.Force {
						http.Error(w, "duplicate_reply", http.StatusConflict)
						return
					}
					replies++
				} else if in.Force {
					t.Error("ordinary comment unnecessarily forced")
				}
				prior = saved{in, fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", len(receipts)+1)}
				receipts[key] = prior
			}
			encode(map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject}, "comment": map[string]any{"uid": prior.uid, "body": prior.write.Body, "reply_to_uid": prior.write.Reply}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	actor := c.Principal{ID: "rook"}
	first := CommentWrite{IssueUID: testIssue, Body: "coordination update", ReplyToUID: testProject, RequestID: "coordination"}
	if _, err := b.AppendComment(ctx, actor, first); err != nil {
		t.Fatal(err)
	}
	second := CommentWrite{IssueUID: testIssue, Body: "durable acknowledgement", ReplyToUID: testProject, RequestID: "acknowledgement"}
	got, err := b.AppendComment(ctx, actor, second)
	if err != nil {
		t.Fatalf("distinct reply to same parent rejected: %v", err)
	}
	again, err := b.AppendComment(ctx, actor, second)
	if err != nil || again.UID != got.UID {
		t.Fatalf("retry duplicated reply: %+v %v", again, err)
	}
	second.Body = "changed immutable body"
	if _, err = b.AppendComment(ctx, actor, second); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("changed key payload accepted: %v", err)
	}
	if replies != 2 {
		t.Fatalf("created %d replies, want 2", replies)
	}
	if _, err = b.AppendComment(ctx, actor, CommentWrite{IssueUID: testIssue, Body: "ordinary comment", RequestID: "ordinary"}); err != nil {
		t.Fatal(err)
	}
}
