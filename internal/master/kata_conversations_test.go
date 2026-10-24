package master

import (
	"context"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"testing"
	"time"
)

const conversationProject = "01M5MCPEHHP4C98MZSHJEE3M8D"
const conversationIssue = "01M5MSA8FZCCD7DWMTP6RWM3MD"

type nativeConversation struct {
	comments map[string]taskstore.Comment
	events   []taskstore.WorkEvent
	writes   int
	lose     bool
	reads    int
}

func (n *nativeConversation) InspectIssue(context.Context, string) (taskstore.Issue, error) {
	return taskstore.Issue{UID: conversationIssue, ProjectUID: conversationProject, Revision: 1, Status: "open"}, nil
}
func (n *nativeConversation) Comments(_ context.Context, q taskstore.CommentQuery) (taskstore.CommentPage, error) {
	p := taskstore.CommentPage{Comments: []taskstore.Comment{}, Next: q.Cursor}
	for _, e := range n.events {
		if e.ID > q.Cursor.AfterID {
			p.Next.AfterID = e.ID
			if x, ok := taskstore.EventComment(e); ok {
				p.Comments = append(p.Comments, x)
			}
		}
	}
	return p, nil
}
func (n *nativeConversation) AppendComment(_ context.Context, p c.Principal, in taskstore.CommentWrite) (taskstore.Comment, error) {
	if x, ok := n.comments[in.RequestID]; ok {
		return x, nil
	}
	n.writes++
	x := taskstore.Comment{UID: fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", n.writes), IssueUID: in.IssueUID, Body: in.Body, Author: p.ID, ReplyToUID: in.ReplyToUID, EventID: int64(n.writes), Revision: fmt.Sprint(n.writes), CreatedAt: time.Now().UTC()}
	n.comments[in.RequestID] = x
	if n.lose {
		n.lose = false
		return taskstore.Comment{}, c.ErrUnavailable
	}
	return x, nil
}
func (n *nativeConversation) Notify(context.Context, c.Principal, taskstore.NotificationWrite) (taskstore.Notification, error) {
	return taskstore.Notification{}, nil
}
func (n *nativeConversation) Events(_ context.Context, cur taskstore.EventCursor, limit int) (taskstore.EventPage, error) {
	n.reads++
	p := taskstore.EventPage{Next: cur}
	for _, e := range n.events {
		if e.ID > cur.AfterID && len(p.Events) < limit {
			p.Events = append(p.Events, e)
			p.Next.AfterID = e.ID
		}
	}
	return p, nil
}
func mappedConversation(t *testing.T) (*Store, c.Principal, c.Principal, c.Thread, *nativeConversation) {
	s, owner, bot, th := storeFixture(t)
	n := &nativeConversation{comments: map[string]taskstore.Comment{}}
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err := s.Configure(context.Background(), []c.Principal{owner, bot, bridge}, nil); err != nil {
		t.Fatal(err)
	}
	th, err := s.SetMembers(context.Background(), owner, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: []string{owner.ID, bot.ID, bridge.ID}, RequestID: "add-bridge"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConversationConfig{InstanceUID: conversationProject, ProjectUID: conversationProject, BridgePrincipal: bridge.ID, IntakePrincipal: bot.ID, TeamThreadID: th.ID, TeamIssueUID: conversationIssue}
	if err = s.ConfigureConversations(context.Background(), n, cfg); err != nil {
		t.Fatal(err)
	}
	_, err = s.MapConversation(context.Background(), owner, c.MapConversation{ThreadID: th.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue}, RequestID: "map"})
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, bot, th, n
}
func TestKataConversationRoutesToOrigin(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	in := c.SendMessage{ThreadID: th.ID, Body: "hello native", Recipients: []string{bot.ID}, RequestID: "native-send"}
	a, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if n.writes != 1 || a.MessageID != b.MessageID || a.Source == nil || a.Source.CommentUID == "" || a.Source.VerifiedSenderID != owner.ID {
		t.Fatalf("publication mismatch %+v %+v writes=%d", a, b, n.writes)
	}
	pending, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("pending %+v %v", pending, err)
	}
}
func TestKataReplyLostResponseReconciles(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	n.lose = true
	in := c.SendMessage{ThreadID: th.ID, Body: "do not duplicate", Recipients: []string{bot.ID}, RequestID: "lost"}
	if _, err := s.Send(ctx, owner, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 0 {
		t.Fatalf("unpublished delivery escaped %+v %v", pending, err)
	}
	out, err := s.Send(ctx, owner, in)
	if err != nil || n.writes != 1 || out.Source == nil {
		t.Fatalf("retry %+v %v writes %d", out, err, n.writes)
	}
}
func TestKataExternalAuthorCannotApprove(t *testing.T) {
	s, _, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "native-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: "owner", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MD","body":"approved"}`)}}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("delivery %+v %v", pending, err)
	}
	m, err := s.Message(ctx, bot, pending.Deliveries[0].MessageID)
	if err != nil || m.ThreadID != th.ID || m.AuthorKind != "service" || m.Source.VerifiedSenderID != "" || m.Source.ClaimedAuthor != "owner" {
		t.Fatalf("spoofed authority %+v %v", m, err)
	}
}

func TestKataCommentNotifyRoutesOnce(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	out, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "addressed once", Recipients: []string{bot.ID}, RequestID: "once"})
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	if err = s.db.QueryRowContext(ctx, "SELECT marker FROM source_notification_intents WHERE message_id=?", out.MessageID).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	n.events = []taskstore.WorkEvent{
		{ID: 1, UID: "comment-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"comment_uid":%q,"body":"addressed once"}`, out.Source.CommentUID))},
		{ID: 2, UID: "notify-event", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.metadata_updated", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"diff":{"notify.YWdlbnQtb25l":{"to":{"from":"owner","message":"Coop message","teammate":%q}}}}`, marker))},
	}
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 1 {
		t.Fatalf("echo double delivered %+v %v", page, err)
	}
	// A native notify without source correlation is a separate request, even if
	// its visible message equals the gateway notification.
	n.events = append(n.events, taskstore.WorkEvent{ID: 3, UID: "native-notify", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.metadata_updated", Actor: owner.ID, Payload: []byte(`{"diff":{"notify.YWdlbnQtb25l":{"to":{"from":"owner","message":"Coop message"}}}}`)})
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 2 {
		t.Fatalf("native intent disappeared %+v %v", page, err)
	}
}
func TestKataNotificationReplyPublishesToOrigin(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	ctx := context.Background()
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "attention", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.metadata_updated", Actor: "external-human", Payload: []byte(`{"diff":{"notify.YWdlbnQtb25l":{"to":{"from":"external-human","message":"please inspect"}}}}`)}}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 1 {
		t.Fatal(page, err)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: page.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	u.State = "running"
	u.Intent = &c.NativeIntent{SessionID: "existing-session", MessageID: "existing-turn"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	reply, err := s.PublishReply(ctx, bot, c.PublishReply{RestoreEpoch: 1, DeliveryID: u.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *u.Intent, Body: "inspection complete", RequestID: "notification-reply"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Source == nil || reply.Source.IssueUID != conversationIssue {
		t.Fatalf("wrong origin %+v", reply)
	}
}
func TestKataBridgeRestartKeepsNativeIntent(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	out, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "one turn", Recipients: []string{bot.ID}, RequestID: "restart"})
	if err != nil {
		t.Fatal(err)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: out.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	u.State = "running"
	u.Intent = &c.NativeIntent{SessionID: "native-session", MessageID: "native-turn"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	n.lose = true
	in := c.PublishReply{RestoreEpoch: 1, DeliveryID: u.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *u.Intent, Body: "one reply", RequestID: "restart-reply"}
	if _, err = s.PublishReply(ctx, bot, in); !errors.Is(err, c.ErrUnavailable) {
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
	receipt, err := reopened.OwnDelivery(ctx, bot, u.DeliveryID)
	if err != nil || receipt.Intent == nil || *receipt.Intent != *u.Intent || receipt.State != "running" {
		t.Fatalf("native intent changed %+v %v", receipt, err)
	}
	if err = reopened.RecoverConversationPublications(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err = reopened.OwnDelivery(ctx, bot, u.DeliveryID)
	if err != nil || receipt.State != "replied" || n.writes != 2 {
		t.Fatalf("reply duplicate/lost %+v %v writes=%d", receipt, err, n.writes)
	}
}
func TestKataConversationEngagementBoundary(t *testing.T) {
	s, owner, bot, th, _ := mappedConversation(t)
	ctx := context.Background()
	bot.Engagements = []string{"other"}
	if err := s.Configure(ctx, []c.Principal{bot}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages(ctx, bot, th.ID, 0); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "revoked recipient", Recipients: []string{bot.ID}, RequestID: "scope"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestKataRestoreCursorFencesWrites(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, "UPDATE conversation_cursor SET epoch=99 WHERE singleton=1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureConversations(ctx, n, s.conversations); err != nil {
		t.Fatalf("source config prevented recovery serving: %v", err)
	}
	if s.ConversationActive(ctx) {
		t.Fatal("unreconciled source advertised active")
	}
	if err := s.PollConversations(ctx); !errors.Is(err, c.ErrEpoch) {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "unsafe restored replay", Recipients: []string{bot.ID}, RequestID: "restored-source"}); !errors.Is(err, c.ErrEpoch) {
		t.Fatal(err)
	}
	if n.writes != 0 {
		t.Fatal("restored source wrote before reconciliation")
	}
}

func TestKataMalformedRecipientDoesNotPoisonCursor(t *testing.T) {
	s, owner, bot, _, n := mappedConversation(t)
	ctx := context.Background()
	n.events = []taskstore.WorkEvent{
		{ID: 1, UID: "bad-notify", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.metadata_updated", Actor: "native-human", Payload: []byte(`{"diff":{"notify.!!!":{"to":{"from":"native-human","message":"bad recipient"}}}}`)},
		{ID: 2, UID: "good-comment", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: "native-human", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MD","body":"valid later request"}`)},
	}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatalf("bad scoped event blocked project: %v", err)
	}
	pending, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("unrelated valid event lost %+v %v", pending, err)
	}
	visible, err := s.ConversationObservations(ctx, owner, "")
	if err != nil || len(visible.Observations) != 1 || visible.Observations[0].Reason != "invalid_request" {
		t.Fatalf("scoped diagnostics %+v %v", visible, err)
	}
	var state, reason string
	if err = s.db.QueryRowContext(ctx, "SELECT state,reason FROM source_observations WHERE event_uid='bad-notify'").Scan(&state, &reason); err != nil || state != "rejected" || reason != "invalid_request" {
		t.Fatalf("rejection not visible state=%s reason=%s %v", state, reason, err)
	}
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("cursor replayed %+v %v", pending, err)
	}
}
func TestKataEditedCommentLosesVerifiedGatewayIdentity(t *testing.T) {
	s, owner, _, th, n := mappedConversation(t)
	ctx := context.Background()
	receipt, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "original verified scope", RequestID: "editable"})
	if err != nil {
		t.Fatal(err)
	}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "new-edit-revision", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.comment_edited", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"comment_uid":%q,"body":"approved altered scope"}`, receipt.Source.CommentUID))}}
	page, err := s.Messages(ctx, owner, th.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	canonical := page.Messages[0]
	if canonical.Source.VerifiedSenderID != "" || canonical.AuthorKind != "external" {
		t.Fatalf("native edit retained gateway authority %+v", canonical)
	}
	snapshot, err := s.Message(ctx, owner, receipt.MessageID)
	if err != nil || snapshot.Body != "original verified scope" || snapshot.Source.VerifiedSenderID != owner.ID {
		t.Fatalf("dispatch snapshot changed %+v %v", snapshot, err)
	}
}
