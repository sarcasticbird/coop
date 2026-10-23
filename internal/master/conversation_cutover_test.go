package master

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

func cutoverFixture(t *testing.T) (*Store, c.Principal, c.Principal, c.Thread, *nativeConversation, c.MapConversation) {
	t.Helper()
	s, o, b, th := storeFixture(t)
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err := s.Configure(t.Context(), []c.Principal{bridge}, nil); err != nil {
		t.Fatal(err)
	}
	var err error
	th, err = s.SetMembers(t.Context(), o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: append(th.Members, bridge.ID), RequestID: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeConversation{comments: map[string]taskstore.Comment{}}
	if err = s.ConfigureConversations(t.Context(), n, ConversationConfig{InstanceUID: conversationProject, ProjectUID: conversationProject, BridgePrincipal: bridge.ID, IntakePrincipal: b.ID}); err != nil {
		t.Fatal(err)
	}
	return s, o, b, th, n, c.MapConversation{ThreadID: th.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue}, RequestID: "cutover"}
}
func cutoverEvent(id int64) taskstore.WorkEvent {
	return taskstore.WorkEvent{ID: id, UID: fmt.Sprintf("event-%d", id), OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue, Type: "issue.commented", Actor: "human", Payload: []byte(fmt.Sprintf(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RW%04d","body":"native conversation %d"}`, id, id))}
}
func resumeCutover(t *testing.T, s *Store, o c.Principal) {
	t.Helper()
	for _, id := range []string{"agent-one", "agent-two"} {
		p, err := s.Principal(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		placement := c.Placement{AgentID: id, HostID: "host", InstanceID: "instance", Generation: 1}
		if id == "agent-two" {
			placement.InstanceID = "instance-two"
		}
		_, err = s.ReconcileGuest(t.Context(), p, c.GuestRecovery{RestoreEpoch: s.epoch, Placement: placement, Complete: true, ManifestSHA256: c.RecoveryDigest(nil)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Resume(t.Context(), o, s.epoch); err != nil {
		t.Fatal(err)
	}
}
func TestConversationCutoverPreservesAcceptedReply(t *testing.T) {
	s, o, b, th, n, mapping := cutoverFixture(t)
	ctx := t.Context()
	sent, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "accepted legacy question", RequestID: "question"})
	if err != nil {
		t.Fatal(err)
	}
	update := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: sent.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, b, update); err != nil {
		t.Fatal(err)
	}
	update.State = "running"
	update.Intent = &c.NativeIntent{SessionID: "existing-session", MessageID: "existing-model-turn"}
	if err = s.SetDelivery(ctx, b, update); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MapConversation(ctx, o, mapping); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("running accepted reply not fenced: %v", err)
	}
	replyIn := c.PublishReply{RestoreEpoch: 1, DeliveryID: update.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *update.Intent, Body: "accepted legacy answer", RequestID: "accepted-reply"}
	reply, err := s.PublishReply(ctx, b, replyIn)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Quiesce(ctx, o, 1); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.MapConversation(cancelled, o, mapping); err == nil {
		t.Fatal("disconnected operator unexpectedly mapped")
	}
	mapped, err := s.MapConversation(ctx, o, mapping)
	if err != nil || mapped.ID != th.ID {
		t.Fatal(mapped, err)
	}
	// A lost mapping response can be retried while the master remains paused.
	again, err := s.MapConversation(ctx, o, mapping)
	if err != nil || again.ID != mapped.ID {
		t.Fatal(again, err)
	}
	if _, err = s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Body: "new root while paused", RequestID: "paused"}); err == nil {
		t.Fatal("pause bypassed")
	}
	resumeCutover(t, s, o)
	retried, err := s.PublishReply(ctx, b, replyIn)
	if err != nil || retried.MessageID != reply.MessageID || n.writes != 0 {
		t.Fatalf("legacy reply replay published twice: %+v %v", retried, err)
	}
	legacy, err := s.LegacyMessages(ctx, o, th.ID, 0)
	if err != nil || len(legacy.Messages) != 2 || legacy.Messages[1].ID != reply.MessageID {
		t.Fatal(legacy, err)
	}
	fresh, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, ParentMessageID: reply.MessageID, Body: "canonical follow-up", Recipients: []string{}, RequestID: "new"})
	if err != nil || fresh.Source == nil || n.writes != 1 {
		t.Fatal(fresh, err)
	}
	n.lose = true
	in := c.SendMessage{ThreadID: th.ID, Body: "uncertain canonical write", RequestID: "uncertain"}
	if _, err = s.Send(ctx, o, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	if err = s.RecoverConversationPublications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(ctx, o, in); err != nil || n.writes != 2 {
		t.Fatal("uncertain recovery duplicate", err, n.writes)
	}
	legacy, err = s.LegacyMessages(ctx, o, th.ID, 0)
	if err != nil || len(legacy.Messages) != 2 {
		t.Fatal("source writes leaked into legacy history", legacy, err)
	}
	s.work = nil
	legacy, err = s.LegacyMessages(ctx, o, th.ID, 0)
	if err != nil || len(legacy.Messages) != 2 {
		t.Fatal("offline legacy history lost", err)
	}
	if _, err = s.Messages(ctx, o, th.ID, 0); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("canonical read silently fell back", err)
	}
}
func TestConversationCutoverKeepsGoalAndQADestination(t *testing.T) {
	s, o, b, th, n, mapping := cutoverFixture(t)
	svc := &Service{Store: s}
	ctx := t.Context()
	qa, err := s.CreateThread(ctx, o, c.CreateThread{Title: "QA evidence", Engagement: th.Engagement, Members: th.Members, RequestID: "qa-thread"})
	if err != nil {
		t.Fatal(err)
	}
	s.work = flexibleConversation{n}
	proposal := c.GoalProposal{ThreadID: th.ID, Purpose: "bounded goal", Scope: "fixture", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"guarded evidence"}, AuthorID: b.ID, PeerID: "agent-two", QA: c.QAPolicy{EvidenceDestination: c.EvidenceDestination{MasterID: s.id, ThreadID: qa.ID, Engagement: th.Engagement}}, RequestID: "goal"}
	revision, err := svc.ProposeGoal(ctx, b, proposal)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MapConversation(ctx, o, mapping); err != nil {
		t.Fatal(err)
	}
	qaMapping := mapping
	qaMapping.ThreadID = qa.ID
	qaMapping.Source.IssueUID = "01M5MSA8FZCCD7DWMTP6RWM3ME"
	qaMapping.RequestID = "qa-map"
	if _, err = s.MapConversation(ctx, o, qaMapping); err != nil {
		t.Fatal(err)
	}
	after, err := svc.ResolveGoal(ctx, o, revision.Ref)
	if err != nil || !after.Valid || digest(after) != digest(before) {
		t.Fatalf("goal or QA anchor changed: %+v %v", after, err)
	}
	evidence, err := s.Send(ctx, o, c.SendMessage{ThreadID: after.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: "QA evidence", Recipients: []string{}, RequestID: "qa"})
	if err != nil || evidence.Source == nil || evidence.Source.IssueUID != qaMapping.Source.IssueUID {
		t.Fatal(evidence, err)
	}
}
func TestCutoverDoesNotWakeHistoricalMessages(t *testing.T) {
	s, o, b, th, n, mapping := cutoverFixture(t)
	ctx := t.Context()
	if _, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Body: "legacy context", RequestID: "history"}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 300; i++ {
		n.events = append(n.events, cutoverEvent(i))
	}
	mapping.AfterEventID = 300
	if err := s.Quiesce(ctx, o, 1); err != nil {
		t.Fatal(err)
	}
	// Native comments can arrive after the recorded baseline and before mapping.
	n.events = append(n.events, cutoverEvent(301))
	if _, err := s.MapConversation(ctx, o, mapping); err != nil {
		t.Fatal(err)
	}
	resumeCutover(t, s, o)
	reads := n.reads
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, b, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatal(pending, err)
	}
	if n.reads != reads+2 {
		t.Fatal("history affected bounded page reads")
	}
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending(ctx, b, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatal("replayed transition", pending, err)
	}
	for _, bad := range []int64{999, 299} {
		copy := mapping
		copy.RequestID = fmt.Sprintf("bad-%d", bad)
		copy.AfterEventID = bad
		other, err := s.CreateThread(ctx, o, c.CreateThread{Title: "Other", Engagement: th.Engagement, Members: th.Members, RequestID: copy.RequestID})
		if err != nil {
			t.Fatal(err)
		}
		copy.ThreadID = other.ID
		if _, err = s.MapConversation(ctx, o, copy); err == nil {
			t.Fatal("unsafe future/stale cursor accepted", bad)
		}
	}
}
func TestCutoverReplaysSourceCursorSafely(t *testing.T) {
	s, o, _, _, n := mappedConversation(t)
	ctx := t.Context()
	n.events = []taskstore.WorkEvent{cutoverEvent(1)}
	if err := s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "before")
	if _, err := s.Backup(ctx, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	cfg := s.conversations
	lineage := s.epochPath
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(ctx, snapshot, target, lineage, false); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: s.id, EpochPath: lineage})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err = restored.ConfigureConversations(ctx, n, cfg); err != nil {
		t.Fatal(err)
	}
	if err = restored.PollConversations(ctx); !errors.Is(err, c.ErrEpoch) {
		t.Fatal("restored poll was not fenced", err)
	}
	observed, err := restored.ConversationCursor(ctx, o)
	if err != nil || observed.AcceptedEpoch != 1 || observed.RestoreEpoch != 2 || observed.AfterEventID != 1 {
		t.Fatal(observed, err)
	}
	in := c.ReacceptConversationCursor{Observed: observed, Reason: "consistent fixture backup reconciled", RequestID: "reaccept"}
	for _, field := range []string{"epoch", "cursor", "instance", "project", "actor"} {
		bad := in
		actor := o
		switch field {
		case "epoch":
			bad.Observed.RestoreEpoch = 1
		case "cursor":
			bad.Observed.AfterEventID = 0
		case "instance":
			bad.Observed.InstanceUID = "wrong"
		case "project":
			bad.Observed.ProjectUID = "wrong"
		case "actor":
			actor.ID = "agent-one"
		}
		if _, err = restored.ReacceptConversationCursor(ctx, actor, bad); err == nil {
			t.Fatal("bad observation accepted", field)
		}
	}
	n.events = nil
	if _, err = restored.ReacceptConversationCursor(ctx, o, in); !errors.Is(err, c.ErrEpoch) {
		t.Fatal("source rollback behind cursor accepted", err)
	}
	n.events = []taskstore.WorkEvent{cutoverEvent(1)}
	accepted, err := restored.ReacceptConversationCursor(ctx, o, in)
	if err != nil || accepted.AfterEventID != 1 || accepted.AcceptedEpoch != 2 {
		t.Fatal(accepted, err)
	}
	if _, err = restored.ReacceptConversationCursor(ctx, o, in); err != nil {
		t.Fatal("lost response retry failed", err)
	}
	// Old publication/delivery observations remain durable through restore. A
	// cursor reacceptance does not resume workers or cancel unresolved work.
	if restored.ConversationActive(ctx) {
		t.Fatal("reaccept resumed execution")
	}
	var count int
	if err = restored.db.QueryRowContext(ctx, "SELECT count(*) FROM deliveries").Scan(&count); err != nil || count != 1 {
		t.Fatal("restore duplicated delivery", count, err)
	}
}
