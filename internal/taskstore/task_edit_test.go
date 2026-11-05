package taskstore

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestKataTaskEditorPinsIdentityRevisionAndNativeOperations(t *testing.T) {
	issue := Issue{UID: testIssue, ProjectUID: testProject, Title: "Original", Body: "Scope", Status: "open", Revision: 1}
	var calls []string
	var lastKey string
	var lastInput map[string]json.RawMessage
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-fixture-token" {
			t.Error("lost native credential pin")
		}
		write := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case "/api/v1/instance":
			write(map[string]any{"instance_uid": testProject})
			return
		case "/api/v1/projects":
			write(map[string]any{"projects": []any{map[string]any{"id": 1, "uid": testProject, "name": "coop-test"}}})
			return
		case "/api/v1/projects/1/issues":
			if r.Method == "GET" {
				if r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("sort") != "created" || r.URL.Query().Get("cursor") != "opaque&cursor" {
					t.Error("unbounded or unescaped list", r.URL)
				}
				write(map[string]any{"issues": []Issue{issue}, "next_cursor": "page-2", "complete": false})
				return
			}
		case "/api/v1/projects/1/issues/" + testIssue:
			if r.Method == "GET" {
				write(map[string]any{"issue": issue})
				return
			}
		}
		if r.Method != "POST" && r.Method != "PATCH" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		var input map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if string(input["actor"]) != `"robin"` {
			t.Error("actor came from somewhere other than authenticated principal", string(input["actor"]))
		}
		lastInput = input
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing native deduplication key")
		}
		lastKey = r.Header.Get("Idempotency-Key")
		if r.URL.Path != "/api/v1/projects/1/issues" && !strings.HasSuffix(r.URL.Path, "/comments") && r.Header.Get("If-Match") != `"rev-`+strconv.FormatInt(issue.Revision, 10)+`"` {
			w.WriteHeader(http.StatusPreconditionFailed)
			write(map[string]string{"error": "conflict"})
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/v1/projects/1/issues":
			_ = json.Unmarshal(input["title"], &issue.Title)
			_ = json.Unmarshal(input["body"], &issue.Body)
		case r.Method == "PATCH":
			_ = json.Unmarshal(input["title"], &issue.Title)
		case strings.HasSuffix(r.URL.Path, "/actions/claim"):
			if string(input["if_unowned"]) != "true" || input["force"] != nil {
				t.Error("claim can overwrite another bird")
			}
			issue.Owner = "robin"
		case strings.HasSuffix(r.URL.Path, "/actions/assign"):
			_ = json.Unmarshal(input["owner"], &issue.Owner)
		case strings.HasSuffix(r.URL.Path, "/actions/close"):
			if string(input["evidence"]) == "null" {
				t.Error("native Kata rejects null evidence; omit absent optional fields")
			}
			if string(input["retry_protocol"]) != `"close-v1"` {
				t.Error("native close retry protocol missing")
			}
			issue.Status = "closed"
			_ = json.Unmarshal(input["reason"], &issue.ClosedReason)
		case strings.HasSuffix(r.URL.Path, "/actions/reopen"):
			issue.Status = "open"
			issue.ClosedReason = ""
		case strings.HasSuffix(r.URL.Path, "/comments"):
			var body string
			_ = json.Unmarshal(input["body"], &body)
			write(map[string]any{"issue": issue, "comment": Comment{UID: testProject, Body: body}})
			return
		default:
			t.Error("unexpected mutation", r.URL)
		}
		issue.Revision++
		write(map[string]any{"issue": issue})
	}).(TaskEditor)
	p := c.Principal{ID: "robin"}
	created, err := b.CreateIssue(t.Context(), p, c.TaskCreateInput{Title: "Useful task", Body: "Task context", RequestID: "one-task"})
	if err != nil || created.Binding != nil || created.Goal != (c.GoalRef{}) || created.Reference != testIssue {
		t.Fatal(created, err)
	}
	if len(lastInput) != 4 {
		t.Fatal("creation injected reserved metadata or code authority", lastInput)
	}
	if lastKey != taskEditKey(p, "one-task") || lastKey == taskEditKey(c.Principal{ID: "hawk"}, "one-task") {
		t.Fatal("native request namespace shared between birds")
	}
	for _, op := range []string{"edit", "claim", "assign", "comment", "close", "reopen"} {
		in := c.TaskUpdateInput{Reference: testIssue, Operation: op, ExpectedRevision: issue.Revision, RequestID: op}
		switch op {
		case "edit":
			title := "Edited"
			in.Title = &title
		case "assign":
			owner := "hawk"
			in.Owner = &owner
		case "comment":
			in.ExpectedRevision, in.Message = 0, "A response"
		case "close":
			in.Reason, in.Message = "wontfix", "Disposable task complete"
		}
		if _, err = b.UpdateIssue(t.Context(), p, in); err != nil {
			t.Fatal(op, err)
		}
	}
	before := len(calls)
	if _, err = b.UpdateIssue(t.Context(), p, c.TaskUpdateInput{Reference: testIssue, Operation: "reopen", ExpectedRevision: 1, RequestID: "stale"}); !errors.Is(err, c.ErrConflict) || len(calls) != before {
		t.Fatal("stale revision overwrote work", err)
	}
	page, err := b.ListIssues(t.Context(), c.TaskListInput{After: "opaque&cursor"})
	if err != nil || page.Next != "page-2" || len(page.Tasks) != 1 {
		t.Fatal(page, err)
	}
	want := []string{"POST /api/v1/projects/1/issues", "PATCH /api/v1/projects/1/issues/" + testIssue, "POST /api/v1/projects/1/issues/" + testIssue + "/actions/claim", "POST /api/v1/projects/1/issues/" + testIssue + "/actions/assign", "POST /api/v1/projects/1/issues/" + testIssue + "/comments", "POST /api/v1/projects/1/issues/" + testIssue + "/actions/close", "POST /api/v1/projects/1/issues/" + testIssue + "/actions/reopen"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatal(calls)
	}
}
func TestKataTaskEditRejectsScope(t *testing.T) {
	calls := 0
	b := conversationProvider(t, func(w http.ResponseWriter, r *http.Request) { calls++; http.NotFound(w, r) }).(TaskEditor)
	wrong, _ := c.KataTaskReference(testIssue, testIssue, "code/repo")
	for _, in := range []c.TaskUpdateInput{
		{Reference: wrong, Operation: "comment", Message: "No", RequestID: "cross-project"},
		{Reference: testIssue, Operation: "delete", ExpectedRevision: 1, RequestID: "delete"},
		{Reference: testIssue, Operation: "edit", ExpectedRevision: 1, RequestID: "empty"},
		{Reference: testIssue, Operation: "comment", Message: "Body", ExpectedRevision: 1, RequestID: "mixed"},
	} {
		if _, err := b.UpdateIssue(t.Context(), c.Principal{ID: "hawk"}, in); err == nil {
			t.Fatal("invalid mutation accepted", in)
		}
	}
	if calls != 0 {
		t.Fatal("entered provider before validating scope/input")
	}
}
