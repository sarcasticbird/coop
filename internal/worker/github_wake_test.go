package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWakeReplyEvidenceIsRequestActorAndThreadBound(t *testing.T) {
	c, _ := wakeFixture(t)
	r := testWakeRequest(c)
	valid := Comment{ID: "answer", NodeID: "answer", URL: r.Source.URL, RootID: r.Source.RootID, ParentID: r.Source.RootID, Author: c.Self, Body: "Here is the answer.\n" + WakeResultMarker(r), CreatedAt: time.Now()}
	for _, mode := range []string{"valid", "intent-only", "wrong-actor", "wrong-thread", "wrong-target", "wrong-request", "marker-only", "reformatted-marker-only", "marked-intent-only"} {
		v := valid
		switch mode {
		case "intent-only":
			v.Body = "I will reply later."
		case "marked-intent-only":
			v.Body = "I will reply later.\n" + WakeResultMarker(r)
		case "wrong-actor":
			v.Author = c.Reviewer
		case "wrong-thread":
			v.RootID = "other"
		case "wrong-target":
			v.ParentID = "other"
		case "wrong-request":
			v.Body = strings.Replace(v.Body, r.ID, BodyHash("other"), 1)
		case "marker-only":
			v.Body = WakeResultMarker(r)
		case "reformatted-marker-only":
			v.Body = strings.ReplaceAll(WakeResultMarker(r), ",", ", ")
		}
		_, found, err := matchingWakeReply(c, r, []Comment{v})
		if err != nil || found != (mode == "valid") {
			t.Errorf("%s: found %v error %v", mode, found, err)
		}
	}
	other := valid
	other.ID, other.NodeID = "second", "second"
	other.CreatedAt = valid.CreatedAt.Add(time.Second)
	got, found, err := matchingWakeReply(c, r, []Comment{other, valid})
	if !found || got.ID != valid.ID || errorCategory(err) != "duplicate_reply" {
		t.Fatal("ambiguous duplicate not reconciled deterministically")
	}
}
func TestWakeSourceRevalidationReadsActualParentAndRejectsChanges(t *testing.T) {
	c, _ := wakeFixture(t)
	r := testWakeRequest(c)
	parent := "root"
	body := r.Source.Body
	r.Source.ParentID = parent
	r.Source.ParentAuthor = c.Reviewer
	r.Source.Revision = ConversationRevision(r.Source)
	runner := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
		var req struct{ Query string }
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		actor := func(a Actor) any {
			return map[string]any{"databaseId": a.ID, "login": strings.TrimSuffix(a.Login, "[bot]"), "__typename": a.Kind}
		}
		page := func(nodes []any) any {
			return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
		}
		var data any
		switch {
		case strings.Contains(req.Query, "WorkerWakeDiscussion"):
			data = map[string]any{"node": map[string]any{"id": "discussion", "url": r.Source.DiscussionURL, "body": "planning", "author": actor(c.Owner)}}
		case strings.Contains(req.Query, "WorkerDiscussionComments"):
			data = map[string]any{"node": map[string]any{"comments": page([]any{map[string]any{"id": "root", "url": r.Source.URL, "body": "root", "author": actor(c.Reviewer)}})}}
		case strings.Contains(req.Query, "WorkerDiscussionReplies"):
			if !strings.Contains(req.Query, "replyTo{id author{") || !strings.Contains(req.Query, "id url body") {
				t.Fatal("missing verified parent/permalink query")
			}
			data = map[string]any{"node": map[string]any{"replies": page([]any{map[string]any{"id": r.Source.NodeID, "url": r.Source.URL, "body": body, "createdAt": r.Source.CreatedAt, "author": actor(c.Owner), "replyTo": map[string]any{"id": parent, "author": actor(c.Reviewer)}}})}}
		default:
			t.Fatalf("unexpected query %s", req.Query)
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	g := NewGitHubClient(runner)
	g.config = c
	if _, err := g.RevalidateWake(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	parent = "different"
	if _, err := g.RevalidateWake(context.Background(), r); errorCategory(err) != "source_superseded" {
		t.Fatal("changed parent accepted")
	}
	parent = "root"
	body = "meaningfully edited"
	if _, err := g.RevalidateWake(context.Background(), r); errorCategory(err) != "source_superseded" {
		t.Fatal("changed source accepted")
	}
}
