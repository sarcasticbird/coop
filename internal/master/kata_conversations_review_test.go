package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"testing"
)

type flexibleConversation struct{ *nativeConversation }

func (n flexibleConversation) InspectIssue(_ context.Context, uid string) (taskstore.Issue, error) {
	return taskstore.Issue{UID: uid, ProjectUID: conversationProject, Revision: 1, Status: "open"}, nil
}
func TestKataRevokedBridgeDoesNotBlockOtherThread(t *testing.T) {
	s, owner, bot, first, n := mappedConversation(t)
	ctx := context.Background()
	s.work = flexibleConversation{n}
	second, err := s.CreateThread(ctx, owner, c.CreateThread{Title: "Second independent thread", Engagement: "coop", Members: []string{owner.ID, bot.ID, s.conversations.BridgePrincipal}, RequestID: "second-thread"})
	if err != nil {
		t.Fatal(err)
	}
	secondIssue := "01M5MSA8FZCCD7DWMTP6RWM3ME"
	_, err = s.MapConversation(ctx, owner, c.MapConversation{ThreadID: second.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: secondIssue}, RequestID: "second-map"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Thread(ctx, owner, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetMembers(ctx, owner, c.SetMembers{ThreadID: first.ID, ExpectedMembers: current.Members, Members: []string{owner.ID, bot.ID}, RequestID: "remove-first-bridge"})
	if err != nil {
		t.Fatal(err)
	}
	n.events = []taskstore.WorkEvent{
		{ID: 1, UID: "revoked-thread-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: "human", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MD","body":"first"}`)},
		{ID: 2, UID: "valid-thread-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: secondIssue, Type: "issue.commented", Actor: "human", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MF","body":"second"}`)},
	}
	err = s.PollConversations(ctx)
	page, readErr := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || readErr != nil || len(page.Deliveries) != 1 {
		t.Fatalf("unrelated thread stalled: poll=%v read=%v deliveries=%d", err, readErr, len(page.Deliveries))
	}
	diagnostics, err := s.ConversationObservations(ctx, owner, "")
	if err != nil || len(diagnostics.Observations) != 1 || diagnostics.Observations[0].Reason != "forbidden" || diagnostics.Observations[0].ThreadID != first.ID {
		t.Fatalf("revoked thread rejection not recorded %+v %v", diagnostics, err)
	}
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 1 {
		t.Fatalf("cursor repeated valid delivery %+v %v", page, err)
	}
}
func TestKataUncertainPublicationBeyondFirstPageDoesNotWake(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	var last c.MessageReceipt
	for i := 0; i < c.MaxPending+1; i++ {
		n.lose = true
		var err error
		last, err = s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "archive-only gateway comment", RequestID: fmt.Sprintf("lost-%d", i)})
		if !errors.Is(err, c.ErrUnavailable) {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	publicationKey := s.id + ":" + last.MessageID
	comment := n.comments[publicationKey]
	if comment.UID == "" {
		t.Fatal("fixture lacks canonical comment")
	}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "last-publication-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"comment_uid":%q,"body":%q,"idempotency_key":%q}`, comment.UID, comment.Body, taskstore.CommentKey(publicationKey)))}}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 0 {
		t.Fatalf("gateway echo became native intake: error=%v deliveries=%d", err, len(page.Deliveries))
	}
}

type publicationRecoveryConversation struct {
	*nativeConversation
	blocked  map[string]bool
	attempts int
}

func (n *publicationRecoveryConversation) AppendComment(ctx context.Context, p c.Principal, in taskstore.CommentWrite) (taskstore.Comment, error) {
	n.attempts++
	if n.blocked[in.RequestID] {
		return taskstore.Comment{}, c.ErrUnavailable
	}
	return n.nativeConversation.AppendComment(ctx, p, in)
}
func TestKataPublicationRecoveryProgressesBeyondFailedPage(t *testing.T) {
	s, owner, _, th, n := mappedConversation(t)
	ctx := context.Background()
	for i := 0; i < c.MaxPending+1; i++ {
		n.lose = true
		if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "pending canonical comment", RequestID: fmt.Sprintf("recovery-page-%d", i)}); !errors.Is(err, c.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM source_publications WHERE comment_uid='' ORDER BY created_at,message_id LIMIT ?", c.MaxPending)
	if err != nil {
		t.Fatal(err)
	}
	blocked := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var in taskstore.CommentWrite
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		blocked[in.RequestID] = true
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	provider := &publicationRecoveryConversation{nativeConversation: n, blocked: blocked}
	s.work = provider
	if err = s.RecoverConversationPublications(ctx); !errors.Is(err, c.ErrUnavailable) {
		t.Fatalf("first recovery page %v", err)
	}
	if provider.attempts != c.MaxPending {
		t.Fatalf("unbounded first recovery: %d", provider.attempts)
	}
	if err = s.RecoverConversationPublications(ctx); err != nil {
		t.Fatalf("failed first page starved later publication: %v", err)
	}
	var published int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM source_publications WHERE comment_uid!=''").Scan(&published); err != nil || published != 1 {
		t.Fatalf("later publication not recovered count=%d %v", published, err)
	}
	if provider.attempts != c.MaxPending+1 {
		t.Fatalf("recovery lost its page bound: %d", provider.attempts)
	}
}

func TestKataPublicationKeysMigrateBeforePolling(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	n.lose = true
	pending, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "pre-index uncertain intent", RequestID: "pre-key-table"})
	if !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	// Reopen the previous schema: immutable publication intents already exist,
	// while the new lookup table does not. Initialization must index them before
	// the source cursor or a recipient reservation can advance.
	if _, err = s.db.ExecContext(ctx, "DROP TABLE source_publication_keys"); err != nil {
		t.Fatal(err)
	}
	cfg := s.conversations
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, StoreConfig{Path: s.path, MasterID: s.id, EpochPath: s.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err = reopened.ConfigureConversations(ctx, n, cfg); err != nil {
		t.Fatal(err)
	}
	key := taskstore.CommentKey(s.id + ":" + pending.MessageID)
	comment := n.comments[s.id+":"+pending.MessageID]
	var indexed string
	if err = reopened.db.QueryRowContext(ctx, "SELECT message_id FROM source_publication_keys WHERE issue_uid=? AND native_key=?", conversationIssue, key).Scan(&indexed); err != nil || indexed != pending.MessageID {
		t.Fatalf("old intent not indexed %q %v", indexed, err)
	}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "migrated-key-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"comment_uid":%q,"body":%q,"idempotency_key":%q}`, comment.UID, comment.Body, key))}}
	if err = reopened.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 0 {
		t.Fatalf("old gateway echo became intake %+v %v", page, err)
	}
}

type retiredReplyConversation struct {
	*nativeConversation
	before                  bool
	after                   func()
	attempts, notifications int
}

func (n *retiredReplyConversation) AppendComment(ctx context.Context, p c.Principal, in taskstore.CommentWrite) (taskstore.Comment, error) {
	n.attempts++
	if n.before {
		return taskstore.Comment{}, c.ErrUnavailable
	}
	comment, err := n.nativeConversation.AppendComment(ctx, p, in)
	if n.after != nil {
		n.after()
	}
	return comment, err
}
func (n *retiredReplyConversation) Notify(context.Context, c.Principal, taskstore.NotificationWrite) (taskstore.Notification, error) {
	n.notifications++
	return taskstore.Notification{}, nil
}
func TestKataRetiredReplyDoesNotRetryNativeWrite(t *testing.T) {
	for _, timing := range []string{"before-write", "during-write", "uncertain-write"} {
		t.Run(timing, func(t *testing.T) {
			s, owner, bot, th, native := mappedConversation(t)
			ctx := t.Context()
			other, err := s.Principal(ctx, "agent-two")
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.Thread(ctx, owner, th.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.SetMembers(ctx, owner, c.SetMembers{ThreadID: th.ID, ExpectedMembers: current.Members, Members: append(current.Members, other.ID), RequestID: "reply-recipient"}); err != nil {
				t.Fatal(err)
			}
			initial, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "question", Recipients: []string{bot.ID}, RequestID: "question"})
			if err != nil {
				t.Fatal(err)
			}
			u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: initial.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
			if err = s.SetDelivery(ctx, bot, u); err != nil {
				t.Fatal(err)
			}
			u.State = "running"
			u.Intent = &c.NativeIntent{SessionID: "session", MessageID: "turn"}
			if err = s.SetDelivery(ctx, bot, u); err != nil {
				t.Fatal(err)
			}
			cancel := func() {
				u.State = "cancelled"
				u.Reason = "retired turn"
				if err = s.SetDelivery(ctx, bot, u); err != nil {
					t.Fatal(err)
				}
			}
			provider := &retiredReplyConversation{nativeConversation: native, before: timing == "before-write"}
			if timing == "during-write" {
				provider.after = cancel
			}
			if timing == "uncertain-write" {
				native.lose = true
			}
			s.work = provider
			in := c.PublishReply{RestoreEpoch: 1, DeliveryID: u.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *u.Intent, Body: "retired answer", Recipients: []string{other.ID}, RequestID: "retired-answer"}
			_, err = s.PublishReply(ctx, bot, in)
			if err == nil {
				t.Fatal("retired/uncertain write unexpectedly completed")
			}
			if timing != "during-write" {
				cancel()
			}
			provider.before, provider.after = false, nil
			_ = s.RecoverConversationPublications(ctx)
			if err = s.RecoverConversationPublications(ctx); err != nil {
				t.Fatal("retired publication keeps failing recovery", err)
			}
			if provider.attempts != 1 || provider.notifications != 0 {
				t.Fatalf("retired reply retried or notified: attempts=%d notifications=%d", provider.attempts, provider.notifications)
			}
			delivery, err := s.OwnDelivery(ctx, bot, u.DeliveryID)
			if err != nil || delivery.State != "cancelled" || delivery.Intent == nil || *delivery.Intent != *u.Intent {
				t.Fatalf("retired turn evidence changed: %+v %v", delivery, err)
			}
			reply, err := s.Message(ctx, bot, delivery.ReplyMessageID)
			if err != nil || reply.Body != "retired answer" {
				t.Fatal("immutable reply lost", reply, err)
			}
			if timing == "during-write" {
				var raw []byte
				if err = s.db.QueryRowContext(ctx, "SELECT body FROM source_publication_evidence WHERE message_id=?", reply.ID).Scan(&raw); err != nil {
					t.Fatal("successful native write evidence lost", err)
				}
			}
			if timing == "uncertain-write" {
				var write taskstore.CommentWrite
				var raw []byte
				if err = s.db.QueryRowContext(ctx, "SELECT body FROM source_publications WHERE message_id=?", reply.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &write); err != nil {
					t.Fatal(err)
				}
				comment := native.comments[write.RequestID]
				native.events = []taskstore.WorkEvent{{ID: 1, UID: "retired-write-evidence", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: bot.ID, Payload: encode(map[string]any{"comment_uid": comment.UID, "body": comment.Body, "idempotency_key": taskstore.CommentKey(write.RequestID)})}}
				if err = s.PollConversations(ctx); err != nil {
					t.Fatal(err)
				}
				if err = s.db.QueryRowContext(ctx, "SELECT body FROM source_publication_evidence WHERE message_id=?", reply.ID).Scan(&raw); err != nil {
					t.Fatal("uncertain native evidence lost", err)
				}
				if err = s.RecoverConversationPublications(ctx); err != nil {
					t.Fatal(err)
				}
				if provider.attempts != 1 || provider.notifications != 0 {
					t.Fatal("quarantined evidence triggered publication")
				}
			}
			status, err := s.MessageStatus(ctx, bot, reply.ID)
			if err != nil || len(status.Deliveries) != 1 || status.Deliveries[0].State != "cancelled" {
				t.Fatal("retired reply left active delivery", status, err)
			}
		})
	}
}
