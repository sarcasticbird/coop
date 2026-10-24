package taskstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	_ "modernc.org/sqlite"
)

// Schema is the unmodified official v0.19.0 SQLite schema, not an invented API.
func commentDatabase(t *testing.T, home string) *sql.DB {
	t.Helper()
	path := filepath.Join(home, "kata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	schema, err := os.ReadFile("testdata/kata-v0.19.0-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO meta(key,value) VALUES('schema_version','34'),('instance_uid','` + testProject + `')`,
		`INSERT INTO projects(id,uid,name) VALUES(1,'` + testProject + `','coop-test')`,
		`INSERT INTO issues(id,uid,project_id,short_id,title,author) VALUES(1,'` + testIssue + `',1,'m3md','fixture','human')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestCurrentCommentReadOnlyPinsAndBounds(t *testing.T) {
	for _, change := range []string{"unchanged", "instance", "schema", "provider-version", "provider-schema", "project", "issue", "missing", "oversized", "symlink", "permissions", "postgres", "deleted", "denied", "configured-path", "readonly-file"} {
		t.Run(change, func(t *testing.T) {
			version, schema := "0.19.0", 34
			b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var out any
				switch r.URL.Path {
				case "/api/v1/instance":
					out = map[string]any{"instance_uid": testProject, "version": version, "schema_version": schema}
				case "/api/v1/projects":
					out = map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}}
				case "/api/v1/projects/1/issues/" + testIssue:
					if change == "denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if !strings.HasPrefix(r.URL.Query().Get("inbound"), "coop-inspect-") {
						t.Errorf("downloaded history: %s", r.URL)
					}
					out = map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject, "revision": 1}}
				default:
					t.Errorf("unexpected native request %s", r.URL)
					http.NotFound(w, r)
					return
				}
				if err := json.NewEncoder(w).Encode(out); err != nil {
					t.Error(err)
				}
			}).(*kata)
			db := commentDatabase(t, b.cfg.Home)
			commentUID := "01M5MSA8FZCCD7DWMTP6RWM3ME"
			if _, err := db.Exec(`INSERT INTO comments(uid,issue_id,author,body,created_at) VALUES(?,1,'human','current text','2026-10-23T01:00:00Z')`, commentUID); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(b.cfg.Home, "kata.db")
			query := ""
			want := c.ErrConflict
			switch change {
			case "instance":
				query = `UPDATE meta SET value='other-instance' WHERE key='instance_uid'`
			case "schema":
				query = `UPDATE meta SET value='35' WHERE key='schema_version'`
				want = c.ErrUnsupported
			case "provider-version":
				version = "0.20.0"
				want = c.ErrUnsupported
			case "provider-schema":
				schema = 35
				want = c.ErrUnsupported
			case "project":
				query = `INSERT INTO projects(id,uid,name) VALUES(2,'01M5MSA8FZCCD7DWMTP6RWM3MA','elsewhere'); UPDATE issues SET project_id=2`
				want = c.ErrNotFound
			case "issue":
				query = `UPDATE comments SET issue_id=2`
				want = c.ErrNotFound
			case "missing":
				commentUID = "01M5MSA8FZCCD7DWMTP6RWM3MF"
				want = c.ErrNotFound
			case "oversized":
				if _, err := db.Exec(`UPDATE comments SET body=?`, strings.Repeat("x", c.MaxText+1)); err != nil {
					t.Fatal(err)
				}
				want = c.ErrSize
			case "deleted":
				query = `UPDATE issues SET deleted_at='2026-10-23T01:00:00Z'`
				want = c.ErrNotFound
			case "denied":
				want = c.ErrForbidden
			case "postgres":
				if err := os.WriteFile(filepath.Join(b.cfg.Home, "config.toml"), []byte("[auth]\ntoken='private-fixture-token'\n[storage]\ndsn='postgres://localhost/kata'\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = c.ErrUnsupported
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				want = c.ErrForbidden
			}
			if query != "" {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if change == "symlink" || change == "configured-path" {
				other := filepath.Join(b.cfg.Home, "current native.db")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if change == "symlink" {
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
					want = c.ErrForbidden
				} else {
					if err := os.WriteFile(filepath.Join(b.cfg.Home, "config.toml"), []byte(fmt.Sprintf("[auth]\ntoken='private-fixture-token'\n[storage]\ndsn='sqlite://%s'\n", other)), 0600); err != nil {
						t.Fatal(err)
					}
					path = other
				}
			}
			if change == "readonly-file" {
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := b.Comments(t.Context(), CommentQuery{IssueUID: testIssue, CommentUID: commentUID, Limit: 1})
			if change == "unchanged" || change == "configured-path" || change == "readonly-file" {
				if err != nil || len(page.Comments) != 1 || page.Comments[0].Body != "current text" {
					t.Fatal(page, err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("%s: got %v, want %v", change, err, want)
			}
			after, e := os.ReadFile(path)
			if e != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("native reader changed database", e)
			}
		})
	}
}

func TestCurrentCommentAvoidsLargeReplyContext(t *testing.T) {
	root := "01M5MSA8FZCCD7DWMTP6RWM3ME"
	var comments []Comment
	for i := 0; i < 9; i++ {
		comments = append(comments, Comment{UID: fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", i), Author: "human", Body: strings.Repeat("x", c.MaxText), ReplyToUID: root})
	}
	comments[0].UID, comments[0].ReplyToUID = root, ""
	events := []WorkEvent{{ID: 1, UID: "created", ProjectUID: testProject, OriginInstanceUID: testProject, IssueUID: testIssue, Type: "issue.commented", Payload: json.RawMessage(fmt.Sprintf(`{"comment_uid":%q,"body":"old creation text"}`, root))}}
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/api/v1/instance":
			out = map[string]any{"instance_uid": testProject, "version": "0.19.0", "schema_version": 34}
		case "/api/v1/projects":
			out = map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}}
		case "/api/v1/projects/1/events":
			out = map[string]any{"events": events, "next_after_id": 1}
		case "/api/v1/projects/1/issues/" + testIssue:
			if strings.HasPrefix(r.URL.Query().Get("inbound"), "coop-inspect-") {
				out = map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject, "revision": 1}}
				break
			}
			// The actual v0.19 thread selector returns the root and reply bodies.
			t.Error("single current-comment lookup downloaded thread history")
			out = map[string]any{"issue": map[string]any{"uid": testIssue, "project_uid": testProject}, "comments": comments}
		default:
			t.Errorf("unexpected read %s", r.URL)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	})
	db := commentDatabase(t, b.(*kata).cfg.Home)
	for _, x := range comments {
		if _, err := db.Exec(`INSERT INTO comments(uid,issue_id,author,body,reply_to_uid,created_at) VALUES(?,1,?,?,?,'2026-10-23T01:00:00Z')`, x.UID, x.Author, x.Body, x.ReplyToUID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE comments SET body='current edited text',edited_at='2026-10-23T02:00:00Z' WHERE uid=?`, root); err != nil {
		t.Fatal(err)
	}
	for _, q := range []CommentQuery{{IssueUID: testIssue, CommentUID: root, Limit: 1}, {IssueUID: testIssue, Cursor: EventCursor{InstanceUID: testProject, ProjectUID: testProject}, Limit: 1}} {
		page, err := b.Comments(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Comments) != 1 || page.Comments[0].UID != root || page.Comments[0].Body != "current edited text" || page.Comments[0].EditedAt == nil || !page.Comments[0].EditedAt.Equal(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)) {
			t.Fatalf("current text lost: %+v", page)
		}
	}
}

func TestCurrentCommentNativeReadOnlyIntegration(t *testing.T) {
	home := os.Getenv("COOP_KATA_TEST_HOME")
	if home == "" {
		t.Skip("requires explicitly selected disposable native profile")
	}
	cfg := Config{Provider: "kata", Binary: "/opt/homebrew/bin/kata", Home: home, ProjectName: "coop-consolidation-acceptance", ProjectUID: "01M5N8JN3PA04EJRZSHHKHY4TJ", InstanceUID: "01M5N8JCKTYDTF0CE5FXR9AKX8", PolicySHA: strings.Repeat("a", 64)}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	work := b.(WorkBackend)
	cursor := EventCursor{InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID}
	page, err := work.Events(t.Context(), cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		comment, ok := EventComment(event)
		if !ok {
			continue
		}
		exact, err := work.Comments(t.Context(), CommentQuery{IssueUID: event.IssueUID, CommentUID: comment.UID, Limit: 1})
		if err != nil || len(exact.Comments) != 1 || exact.Comments[0].UID != comment.UID {
			t.Fatal(exact, err)
		}
		cursor.AfterID = event.ID - 1
		refreshed, err := work.Comments(t.Context(), CommentQuery{IssueUID: event.IssueUID, Cursor: cursor, Limit: 1})
		if err != nil || len(refreshed.Comments) != 1 || refreshed.Comments[0].UID != comment.UID || refreshed.Comments[0].Body != exact.Comments[0].Body {
			t.Fatal(refreshed, err)
		}
		t.Log("verified exact current row and page refresh against selected disposable native SQLite profile without writes")
		return
	}
	t.Fatal("disposable native fixture has no comment in its first bounded event page")
}
