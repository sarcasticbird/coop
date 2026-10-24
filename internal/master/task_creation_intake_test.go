package master

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// creationWindowBackend polls after the native create effect but before its
// captured result reaches TaskCreate's durable reference/registry commits.
type creationWindowBackend struct {
	*testTasks
	poll       func()
	loseCreate bool
	failBind   bool
}

func (b *creationWindowBackend) Create(ctx context.Context, in c.TaskCreateInput, p c.Principal) (c.ProviderTask, error) {
	task, err := b.testTasks.Create(ctx, in, p)
	if err != nil {
		return task, err
	}
	if b.poll != nil {
		b.poll()
	}
	if b.loseCreate {
		return c.ProviderTask{}, c.ErrUnavailable
	}
	return task, nil
}
func (b *creationWindowBackend) Bind(ctx context.Context, in c.TaskBindInput, p c.Principal) (c.ProviderTask, error) {
	if b.failBind {
		return c.ProviderTask{}, c.ErrUnavailable
	}
	return b.testTasks.Bind(ctx, in, p)
}

// A real canonical acknowledgement advances native issue revision. Model that
// side effect so accidental intake makes the original captured CAS fail.
type creationRevisionWork struct {
	*intakeConversation
	backend *testTasks
}

func (w *creationRevisionWork) AppendComment(ctx context.Context, p c.Principal, in taskstore.CommentWrite) (taskstore.Comment, error) {
	writes := w.writes
	out, err := w.intakeConversation.AppendComment(ctx, p, in)
	if w.writes > writes && in.IssueUID == w.backend.task.IssueUID {
		w.backend.task.Revision++
	}
	return out, err
}

func TestTaskCreateIntakeWindowPreservesGoalScopeAndCAS(t *testing.T) {
	for _, interruption := range []string{"none", "unknown-create", "before-bind"} {
		t.Run(interruption, func(t *testing.T) {
			ctx := t.Context()
			store, owner, bot, team, n := mappedConversation(t)
			bridgeID := store.conversations.BridgePrincipal
			teamOnly := c.Principal{ID: "team-only", Alias: "team-only", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
			goalOnly := c.Principal{ID: "goal-only", Alias: "goal-only", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
			if err := store.Configure(ctx, []c.Principal{teamOnly, goalOnly}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SetMembers(ctx, owner, c.SetMembers{ThreadID: team.ID, ExpectedMembers: team.Members, Members: append(team.Members, teamOnly.ID), RequestID: "team-exclusive-member"}); err != nil {
				t.Fatal(err)
			}
			goalThread, err := store.CreateThread(ctx, owner, c.CreateThread{Title: "Separate engineering goal", Engagement: "coop", Members: []string{owner.ID, bot.ID, "agent-two", bridgeID, goalOnly.ID}, RequestID: "distinct-goal-thread"})
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{Store: store}
			proposal := c.GoalProposal{ThreadID: goalThread.ID, Purpose: "Preserve concurrent native creation", Scope: "bounded", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"pass"}, AuthorID: bot.ID, PeerID: "agent-two", QA: c.QAPolicy{Required: false}, RequestID: "window-goal"}
			goal, err := svc.ProposeGoal(ctx, owner, proposal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.DecideGoal(ctx, owner, c.GoalDecisionInput{GoalID: goal.Ref.GoalID, RevisionSHA256: goal.Ref.RevisionSHA256, Action: "approve", RequestID: "window-approve"}); err != nil {
				t.Fatal(err)
			}
			ref, _ := c.KataTaskReference(conversationProject, intakeIssue, "example/coop")
			backend := &creationWindowBackend{testTasks: &testTasks{task: c.ProviderTask{Reference: ref, ProjectUID: conversationProject, IssueUID: intakeIssue, Repository: "example/coop", Title: "Programmatic task", Body: "Bounded scope", Goal: goal.Ref, Revision: 1, State: "open"}}, loseCreate: interruption == "unknown-create", failBind: interruption == "before-bind"}
			work := &creationRevisionWork{intakeConversation: &intakeConversation{n}, backend: backend.testTasks}
			store.work = work
			svc.Tasks = backend
			n.events = []taskstore.WorkEvent{
				{ID: 1, UID: "window-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: owner.ID, Payload: []byte(fmt.Sprintf(`{"idempotency_key":%q}`, taskstore.CreationKey(goal.Ref, "window-create")))},
				{ID: 2, UID: "window-comment", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.commented", Actor: "native-human", Payload: []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3MF","body":"Please discuss this task"}`)},
				{ID: 3, UID: "window-notify", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.metadata_updated", Actor: "native-human", Payload: []byte(fmt.Sprintf(`{"diff":{"notify.%s":{"to":{"from":"native-human","message":"Explicit task attention"}}}}`, base64.RawURLEncoding.EncodeToString([]byte(bot.ID))))},
			}
			var duringPending, duringMappings, duringDeliveries int
			backend.poll = func() {
				if err := svc.Store.PollConversations(ctx); err != nil {
					t.Fatal(err)
				}
				pending, err := svc.Store.PendingConversationIntake(ctx, "", 100)
				if err != nil {
					t.Fatal(err)
				}
				duringPending = len(pending)
				if err = svc.Store.db.QueryRowContext(ctx, "SELECT count(*) FROM conversation_sources WHERE issue_uid=?", intakeIssue).Scan(&duringMappings); err != nil {
					t.Fatal(err)
				}
				deliveries, err := svc.Store.Pending(ctx, bot, c.PendingQuery{})
				if err != nil {
					t.Fatal(err)
				}
				duringDeliveries = len(deliveries.Deliveries)
			}
			input := c.TaskCreateInput{Goal: goal.Ref, Repository: "example/coop", BaseBranch: "main", Title: "Programmatic task", Body: "Bounded scope", ExpectedApprovalSequence: 1, RequestID: "window-create"}
			task, createErr := svc.TaskCreate(ctx, owner, input)
			if duringPending != 3 || duringMappings != 0 || duringDeliveries != 0 || n.writes != 0 {
				t.Fatalf("in-flight creation classified as intake: pending=%d mapping=%d deliveries=%d writes=%d create=%v", duringPending, duringMappings, duringDeliveries, n.writes, createErr)
			}
			if interruption != "none" {
				if !errors.Is(createErr, c.ErrUnavailable) {
					t.Fatal("creation interruption not preserved", createErr)
				}
				config := store.conversations
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenStore(ctx, StoreConfig{Path: store.path, MasterID: store.id, EpochPath: store.epochPath})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = reopened.Close() }()
				if err = reopened.ConfigureConversations(ctx, work, config); err != nil {
					t.Fatal(err)
				}
				svc.Store = reopened
				if err = reopened.PollConversations(ctx); err != nil {
					t.Fatal(err)
				}
				pending, err := reopened.PendingConversationIntake(ctx, "", 100)
				if err != nil || len(pending) != 3 || n.writes != 0 {
					t.Fatal("restart lost creation fence", pending, n.writes, err)
				}
				backend.loseCreate = false
				backend.failBind = false
				backend.poll = nil
				task, createErr = svc.TaskCreate(ctx, owner, input)
			}
			if createErr != nil || task.Binding == nil || task.Reference != ref {
				t.Fatalf("original creation CAS failed %+v %v", task, createErr)
			}
			again, err := svc.TaskCreate(ctx, owner, input)
			if err != nil || again.Reference != ref || again.Revision != task.Revision {
				t.Fatal("creation replay lost captured CAS", again, err)
			}
			if err = svc.Store.PollConversations(ctx); err != nil {
				t.Fatal(err)
			}
			if err = svc.Store.PollConversations(ctx); err != nil {
				t.Fatal(err)
			}
			pending, err := svc.Store.Pending(ctx, bot, c.PendingQuery{})
			if err != nil || len(pending.Deliveries) != 2 || n.writes != 0 {
				t.Fatal("lost or duplicated task comments/attention", pending, n.writes, err)
			}
			var ownThread string
			if err = svc.Store.db.QueryRowContext(ctx, "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", intakeIssue).Scan(&ownThread); err != nil {
				t.Fatal(err)
			}
			discussion, err := svc.Store.Thread(ctx, goalOnly, ownThread)
			if err != nil || !reflect.DeepEqual(discussion.Members, goalThread.Members) {
				t.Fatalf("task inherited wrong ACL: %+v expected %+v err=%v", discussion.Members, goalThread.Members, err)
			}
			if _, err = svc.Store.Thread(ctx, teamOnly, ownThread); !errors.Is(err, c.ErrForbidden) {
				t.Fatal("team-only principal gained goal discussion access", err)
			}
			for _, delivery := range pending.Deliveries {
				message, err := svc.Store.Message(ctx, bot, delivery.MessageID)
				if err != nil || message.ThreadID != ownThread || message.Source == nil || message.Source.IssueUID != intakeIssue {
					t.Fatal("task conversation lost canonical scope", message, err)
				}
			}
		})
	}
}

// Single-scope fixture for correlation contrasts; the interleaving test above
// separately proves distinct goal/team permissions and both restart windows.
func creationCorrelationFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.TaskCreateInput, *creationWindowBackend, *nativeConversation) {
	t.Helper()
	svc, owner, bot, in, base := taskFixture(t)
	authority, err := svc.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := svc.Store.Thread(t.Context(), owner, authority.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err = svc.Store.Configure(t.Context(), []c.Principal{bridge}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: append(thread.Members, bridge.ID), RequestID: "correlation-bridge"}); err != nil {
		t.Fatal(err)
	}
	ref, _ := c.KataTaskReference(conversationProject, intakeIssue, in.Repository)
	base.task = c.ProviderTask{Reference: ref, ProjectUID: conversationProject, IssueUID: intakeIssue, Repository: in.Repository, Goal: in.Goal, Title: in.Title, Body: in.Body, Revision: 1, State: "open"}
	backend := &creationWindowBackend{testTasks: base, loseCreate: true}
	svc.Tasks = backend
	n := &nativeConversation{comments: map[string]taskstore.Comment{}}
	work := &creationRevisionWork{intakeConversation: &intakeConversation{n}, backend: base}
	cfg := ConversationConfig{InstanceUID: conversationProject, ProjectUID: conversationProject, BridgePrincipal: bridge.ID, IntakePrincipal: bot.ID, TeamThreadID: thread.ID, TeamIssueUID: conversationIssue}
	if err = svc.Store.ConfigureConversations(t.Context(), work, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.MapConversation(t.Context(), owner, c.MapConversation{ThreadID: thread.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue}, RequestID: "correlation-team"}); err != nil {
		t.Fatal(err)
	}
	return svc, owner, bot, in, backend, n
}
func TestTaskCreationFenceKeepsUnrelatedIntakeProgressing(t *testing.T) {
	svc, owner, bot, in, _, n := creationCorrelationFixture(t)
	if _, err := svc.TaskCreate(t.Context(), owner, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	unrelated := "01M5MSA8FZCCD7DWMTP6RWM3MG"
	n.events = []taskstore.WorkEvent{
		{ID: 1, UID: "correlated-create", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: owner.ID, Payload: encode(map[string]string{"idempotency_key": taskstore.CreationKey(in.Goal, in.RequestID)})},
		{ID: 2, UID: "unrelated-create", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: unrelated, Type: "issue.created", Actor: owner.ID, Payload: encode(map[string]string{"idempotency_key": "native-key-without-server-intent"})},
	}
	if err := svc.Store.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Store.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatal("in-flight create paused unrelated intake", p, err)
	}
	message, err := svc.Store.Message(t.Context(), bot, p.Deliveries[0].MessageID)
	if err != nil || message.Source == nil || message.Source.IssueUID != unrelated {
		t.Fatal("native key supplied creation authority", message, err)
	}
	pending, err := svc.Store.PendingConversationIntake(t.Context(), "", 100)
	if err != nil || len(pending) != 1 || pending[0].IssueUID != intakeIssue {
		t.Fatal("incorrect per-issue creation fence", pending, err)
	}
}
func TestTaskCreationCorrelationMigratesJournalBeforePolling(t *testing.T) {
	svc, owner, bot, in, backend, n := creationCorrelationFixture(t)
	_, authority, err := svc.taskAuthority(t.Context(), owner, in.Goal, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := svc.taskBinding(t.Context(), owner, authority, backend.task, in.BaseBranch, in.DependsOn)
	if err != nil {
		t.Fatal(err)
	}
	binding.TaskURL = ""
	legacy := taskCreateIntent{Binding: binding, CreatedAt: svc.Store.now().UTC()}
	if err = saveRequest(t.Context(), svc.Store.db, owner.ID, in.RequestID, "task-create", in, legacy); err != nil {
		t.Fatal(err)
	}
	old := svc.Store
	cfg := old.conversations
	work := old.work
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreConfig{Path: old.path, MasterID: old.id, EpochPath: old.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err = reopened.ConfigureConversations(t.Context(), work, cfg); err != nil {
		t.Fatal(err)
	}
	svc.Store = reopened
	n.events = []taskstore.WorkEvent{{ID: 1, UID: "legacy-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: owner.ID, Payload: encode(map[string]string{"idempotency_key": taskstore.CreationKey(in.Goal, in.RequestID)})}}
	if err = reopened.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := reopened.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 0 || n.writes != 0 {
		t.Fatal("legacy server intent became native intake", p, err)
	}
	var saved taskCreateIntent
	found, err := request(t.Context(), reopened.db, owner.ID, in.RequestID, "task-create", in, &saved)
	if err != nil || !found || saved.ProjectUID != conversationProject || saved.ProviderKey != taskstore.CreationKey(in.Goal, in.RequestID) || saved.ObservedIssueUID != intakeIssue {
		t.Fatal("legacy correlation not durable", saved, err)
	}
	backend.loseCreate = false
	if _, err = svc.TaskCreate(t.Context(), owner, in); err != nil {
		t.Fatal("legacy exact create retry failed", err)
	}
	if err = reopened.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	var thread string
	if err = reopened.db.QueryRowContext(t.Context(), "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", intakeIssue).Scan(&thread); err != nil {
		t.Fatal("legacy task did not gain goal-scoped discussion", err)
	}
}
