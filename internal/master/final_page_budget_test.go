package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"strings"
	"testing"
	"time"
)

type editedCanonicalPage struct{ *nativeConversation }

func (n editedCanonicalPage) Comments(_ context.Context, q taskstore.CommentQuery) (taskstore.CommentPage, error) {
	out := taskstore.CommentPage{Next: q.Cursor}
	for i := int(q.Cursor.AfterID) + 1; i <= c.MaxPageRecords && len(out.Comments) < q.Limit; i++ {
		out.Comments = append(out.Comments, taskstore.Comment{UID: fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", i), IssueUID: conversationIssue, Author: "external-human", Body: strings.Repeat("x", c.MaxText), EventID: int64(i), Revision: fmt.Sprint(i), CreatedAt: time.Now().UTC()})
		out.Next.AfterID = int64(i)
	}
	return out, nil
}
func TestRootCanonicalReadRespectsPageByteBudget(t *testing.T) {
	s, owner, _, th, n := mappedConversation(t)
	s.work = editedCanonicalPage{n}
	page, err := s.Messages(t.Context(), owner, th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > c.MaxPageBytes {
		t.Fatalf("canonical page exceeds client budget: bytes=%d limit=%d records=%d next=%d", len(raw), c.MaxPageBytes, len(page.Messages), page.Next)
	}
}

type deduplicatingCanonicalPage struct {
	*nativeConversation
	calls int
}

func (n *deduplicatingCanonicalPage) Comments(_ context.Context, q taskstore.CommentQuery) (taskstore.CommentPage, error) {
	n.calls++
	out := taskstore.CommentPage{Next: q.Cursor}
	seen := map[string]int{}
	// The first 40 events create comments. Later events edit two of those
	// comments, followed by 38 unrelated events. Match the provider's dedupe.
	for id := q.Cursor.AfterID + 1; id <= 80 && id <= q.Cursor.AfterID+int64(q.Limit); id++ {
		out.Next.AfterID = id
		if id > 42 {
			continue
		}
		commentID := id
		if id > 40 {
			commentID = id - 40
		}
		x := taskstore.Comment{UID: fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", commentID), IssueUID: conversationIssue, Author: "external-human", Body: strings.Repeat("x", c.MaxText), EventID: id, Revision: fmt.Sprint(id)}
		if i, ok := seen[x.UID]; ok {
			out.Comments[i] = x
		} else {
			seen[x.UID] = len(out.Comments)
			out.Comments = append(out.Comments, x)
		}
	}
	return out, nil
}

func TestCanonicalPageBudgetPreservesUnsentEditsAndTrailingEvents(t *testing.T) {
	s, o, _, th, n := mappedConversation(t)
	backend := &deduplicatingCanonicalPage{nativeConversation: n}
	s.work = backend
	seen := map[string]int{}
	var after int64
	pages := 0
	for after < 80 {
		beforeCalls := backend.calls
		page, err := s.Messages(t.Context(), o, th.ID, after)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(Envelope{MasterID: s.id, RestoreEpoch: s.epoch, Data: page})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw)+1 > c.MaxPageBytes || len(page.Messages) > c.MaxPageRecords {
			t.Fatalf("oversized envelope: %d", len(raw)+1)
		}
		if page.Next <= after || backend.calls-beforeCalls > 7 {
			t.Fatalf("unbounded/stalled page: after %d next %d attempts %d", after, page.Next, backend.calls-beforeCalls)
		}
		for _, m := range page.Messages {
			if m.Sequence <= after || m.Sequence > page.Next {
				t.Fatal("event outside consumed window", m.Sequence, after, page.Next)
			}
			seen[m.Source.Revision]++
		}
		after = page.Next
		pages++
	}
	if pages < 2 {
		t.Fatal("fixture did not exercise multiple pages")
	}
	for id := 1; id <= 42; id++ {
		if seen[fmt.Sprint(id)] != 1 {
			t.Fatalf("event %d delivered %d times", id, seen[fmt.Sprint(id)])
		}
	}
	last, err := s.Messages(t.Context(), o, th.ID, after)
	if err != nil || len(last.Messages) != 0 || last.Next != 0 {
		t.Fatal(last, err)
	}
}

type invalidCanonicalPage struct {
	*nativeConversation
	kind  string
	calls int
}

func (n *invalidCanonicalPage) Comments(_ context.Context, q taskstore.CommentQuery) (taskstore.CommentPage, error) {
	n.calls++
	out := taskstore.CommentPage{Next: q.Cursor, Comments: []taskstore.Comment{{UID: conversationIssue, IssueUID: conversationIssue, Body: strings.Repeat("x", c.MaxPageBytes), EventID: 1}}}
	if n.kind != "stalled" {
		out.Next.AfterID = 1
	}
	if n.kind == "over-records" {
		out.Comments = make([]taskstore.Comment, q.Limit+1)
	}
	return out, nil
}
func TestCanonicalPageBudgetFailsClosedWithoutProgress(t *testing.T) {
	for _, kind := range []string{"stalled", "over-records", "single-oversized"} {
		t.Run(kind, func(t *testing.T) {
			s, o, _, th, n := mappedConversation(t)
			backend := &invalidCanonicalPage{nativeConversation: n, kind: kind}
			s.work = backend
			_, err := s.Messages(t.Context(), o, th.ID, 0)
			if err == nil || (!errors.Is(err, c.ErrSize) && !errors.Is(err, c.ErrConflict)) || backend.calls > 7 {
				t.Fatal("unbounded invalid page", err, backend.calls)
			}
		})
	}
}
