package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

const intakeIssue = "01M5MSA8FZCCD7DWMTP6RWM3ME"

type intakeConversation struct{ *nativeConversation }

func (n *intakeConversation) InspectIssue(_ context.Context, uid string) (taskstore.Issue, error) {
	return taskstore.Issue{UID: uid, ProjectUID: conversationProject, Title: "Native ticket", Body: "Investigate this request", Revision: 1, Status: "open"}, nil
}
func TestKataNativeIntakeNotifiesPlanner(t *testing.T) {
	s, _, bot, team, n := mappedConversation(t)
	s.work = &intakeConversation{n}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "native-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "owner"}}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("intake ignored or duplicated %+v %v", pending, err)
	}
	m, err := s.Message(t.Context(), bot, pending.Deliveries[0].MessageID)
	if err != nil || m.ThreadID == team.ID || m.Source == nil || m.Source.IssueUID != intakeIssue || !strings.Contains(m.Body, "planning") || n.writes != 1 {
		t.Fatalf("intake acknowledgement %+v writes=%d %v", m, n.writes, err)
	}
	th, err := s.Thread(t.Context(), bot, m.ThreadID)
	if err != nil || strings.Join(th.Members, ",") != strings.Join(team.Members, ",") {
		t.Fatalf("scope %+v %v", th, err)
	}
	observed, err := s.PendingConversationIntake(t.Context(), "", 100)
	if err != nil || len(observed) != 0 {
		t.Fatal("intake not durably handled", observed, err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM artifacts").Scan(&count); err != nil || count != 0 {
		t.Fatal("native ticket admitted execution", count, err)
	}
}
func TestKataNativeIntakePreservesPremappingComments(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	s.work = &intakeConversation{n}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "intake-comment", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.commented", Actor: "external", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MF","body":"Please investigate"}`)}}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 2 {
		t.Fatalf("pending observations %+v %v", p, err)
	}
}

type unavailableIntake struct {
	*intakeConversation
	fail bool
}

func (n *unavailableIntake) InspectIssue(ctx context.Context, uid string) (taskstore.Issue, error) {
	if n.fail && uid != intakeIssue {
		return taskstore.Issue{}, c.ErrUnavailable
	}
	return n.intakeConversation.InspectIssue(ctx, uid)
}
func TestKataIntakeRetryPageDoesNotStarveLaterIssue(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	backend := &unavailableIntake{intakeConversation: &intakeConversation{n}, fail: true}
	s.work = backend
	for i := 0; i < 101; i++ {
		uid := fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", i)
		if i == 100 {
			uid = intakeIssue
		}
		e := taskstore.WorkEvent{ID: int64(i + 1), UID: fmt.Sprintf("intake-%04d", i), OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: uid, Type: "issue.created", Actor: "external"}
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO source_observations(event_uid,issue_uid,state,body) VALUES(?,?,'pending_intake',?)", e.UID, e.IssueUID, encode(e)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PollConversations(t.Context()); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("failed first page starved later native intake", p, err)
	}
}
func TestKataIntakeMalformedEventDoesNotBlockNextIssue(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	s.work = &intakeConversation{n}
	n.events = []taskstore.WorkEvent{
		{ID: 1, UID: "invalid-intake", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: "01M5MSA8FZCCD7DWMTP6RWM3MF", Type: "issue.commented", Actor: "external", Payload: []byte(`{"body":"missing comment identity"}`)},
		{ID: 2, UID: "valid-intake", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "external"},
	}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("invalid native intake blocked later issue", p, err)
	}
	var state, reason string
	if err = s.db.QueryRowContext(t.Context(), "SELECT state,reason FROM source_observations WHERE event_uid='invalid-intake'").Scan(&state, &reason); err != nil || state != "rejected" || reason != "invalid_request" {
		t.Fatal(state, reason, err)
	}
}

func TestKataIntakeLostAcknowledgementRecoversAfterRestart(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	backend := &intakeConversation{n}
	s.work = backend
	n.lose = true
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "restart-intake", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "external"}}
	if err := s.PollConversations(t.Context()); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("expected uncertain acknowledgement", err)
	}
	pending, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 0 {
		t.Fatal("uncertain ack delivery escaped", pending, err)
	}
	cfg := s.conversations
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreConfig{Path: s.path, MasterID: s.id, EpochPath: s.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err = reopened.ConfigureConversations(t.Context(), backend, cfg); err != nil {
		t.Fatal(err)
	}
	if err = reopened.RecoverConversationPublications(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = reopened.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err = reopened.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 || n.writes != 1 {
		t.Fatal("restart lost or duplicated intake", pending, n.writes, err)
	}
}

type largeIntake struct{ *intakeConversation }

func (n *largeIntake) InspectIssue(ctx context.Context, uid string) (taskstore.Issue, error) {
	issue, err := n.intakeConversation.InspectIssue(ctx, uid)
	issue.Title = "Native\x1b title " + strings.Repeat("界", 200)
	issue.Body = strings.Repeat("界", 20000)
	return issue, err
}
func TestKataIntakeBoundsContextAndSanitizesThreadTitle(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	s.work = &largeIntake{&intakeConversation{n}}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "large-intake", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "external"}}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("large native issue lost intake", p, err)
	}
	m, err := s.Message(t.Context(), bot, p.Deliveries[0].MessageID)
	if err != nil || len(m.Body) > c.MaxText || !strings.Contains(m.Body, "Context shortened") {
		t.Fatal("unbounded intake", len(m.Body), err)
	}
	th, err := s.Thread(t.Context(), bot, m.ThreadID)
	if err != nil || strings.Contains(th.Title, "\x1b") || len([]rune(th.Title)) > 140 {
		t.Fatal("unsanitized native title", th.Title, err)
	}
}

func TestKataIntakePreservesRegisteredTaskLifecycle(t *testing.T) {
	s, owner, bot, th, n := mappedConversation(t)
	s.work = &intakeConversation{n}
	svc := &Service{Store: s, Tasks: &testTasks{}}
	proposal := c.GoalProposal{ThreadID: th.ID, Purpose: "Task creation compatibility", Scope: "bounded", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"pass"}, AuthorID: bot.ID, PeerID: "agent-two", QA: c.QAPolicy{Required: false}, RequestID: "compat-proposal"}
	th, err := s.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: append(th.Members, "agent-two"), RequestID: "compat-members"})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := svc.ProposeGoal(t.Context(), owner, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: goal.Ref.GoalID, RevisionSHA256: goal.Ref.RevisionSHA256, Action: "approve", RequestID: "compat-approve"}); err != nil {
		t.Fatal(err)
	}
	// The fake provider uses a different UID to avoid the standing team identity.
	taskBackend := svc.Tasks.(*testTasks)
	ref, _ := c.KataTaskReference(conversationProject, intakeIssue, "example/coop")
	taskBackend.task = c.ProviderTask{Reference: ref, ProjectUID: conversationProject, IssueUID: intakeIssue, Repository: "example/coop", Title: "Programmatic task", Body: "bounded", Goal: goal.Ref, Revision: 1, State: "open"}
	if _, err = svc.TaskCreate(t.Context(), owner, c.TaskCreateInput{Goal: goal.Ref, Repository: "example/coop", BaseBranch: "main", Title: "Programmatic task", Body: "bounded", ExpectedApprovalSequence: 1, RequestID: "compat-create"}); err != nil {
		t.Fatal(err)
	}
	baselineWrites := n.writes
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "registered-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: owner.ID}}
	if err = s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 0 || n.writes != baselineWrites {
		t.Fatal("registered task got unadmitted adoption prompt", p, n.writes, err)
	}
	var state, reason string
	if err = s.db.QueryRowContext(t.Context(), "SELECT state,reason FROM source_observations WHERE event_uid='registered-created'").Scan(&state, &reason); err != nil || state != "observed" || reason != "registered_task_lifecycle" {
		t.Fatal(state, reason, err)
	}
	n.events = append(n.events, taskstore.WorkEvent{ID: 2, UID: "registered-comment", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.commented", Actor: "external-human", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MF","body":"Please discuss the registered task"}`)})
	if err = s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err = s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("registered task lost native conversation", p, err)
	}
	m, err := s.Message(t.Context(), bot, p.Deliveries[0].MessageID)
	if err != nil || m.Source == nil || m.Source.IssueUID != intakeIssue || m.ThreadID == th.ID {
		t.Fatal("registered task lacked scoped own-thread", m, err)
	}
}

type forgedIntake struct{ *intakeConversation }

func (n *forgedIntake) InspectIssue(ctx context.Context, uid string) (taskstore.Issue, error) {
	issue, err := n.intakeConversation.InspectIssue(ctx, uid)
	issue.Metadata = map[string]json.RawMessage{"coop.native": encode(map[string]any{"actor": "owner", "approved": true})}
	return issue, err
}
func TestKataIntakeNativeMetadataCannotClaimRegisteredLifecycle(t *testing.T) {
	s, _, bot, _, n := mappedConversation(t)
	s.work = &forgedIntake{&intakeConversation{n}}
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "forged-native-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "owner"}}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("native metadata bypassed intake", p, err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM artifacts").Scan(&count); err != nil || count != 0 {
		t.Fatal("native metadata registered execution", count, err)
	}
}
