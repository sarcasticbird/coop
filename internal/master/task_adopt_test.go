package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type adoptTasks struct {
	*testTasks
	issue     taskstore.Issue
	adopts    int
	lose      bool
	after     func()
	afterBind func()
}

func (b *adoptTasks) InspectIssue(context.Context, string) (taskstore.Issue, error) {
	return b.issue, nil
}
func (b *adoptTasks) Adopt(_ context.Context, in c.TaskAdoptInput, binding c.NativeTaskBinding, p c.Principal) (c.ProviderTask, error) {
	b.adopts++
	if b.task.Binding == nil {
		if b.issue.Revision != in.ExpectedRevision {
			return c.ProviderTask{}, c.ErrConflict
		}
		b.task = c.ProviderTask{Reference: in.Reference, ProjectUID: b.issue.ProjectUID, IssueUID: b.issue.UID, Repository: in.Repository, Goal: in.Goal, Title: b.issue.Title, Body: b.issue.Body, State: "open", Revision: b.issue.Revision + 1, Binding: &binding, BindingActor: p}
		b.issue.Revision++
		b.issue.Metadata = map[string]json.RawMessage{"coop.native": encode(struct {
			Binding c.NativeTaskBinding `json:"binding"`
			Actor   c.Principal         `json:"actor"`
		}{binding, p})}
	}
	if b.lose {
		b.lose = false
		return c.ProviderTask{}, c.ErrUnavailable
	}
	if b.after != nil {
		b.after()
	}
	return b.task, nil
}
func (b *adoptTasks) Bind(ctx context.Context, in c.TaskBindInput, actor c.Principal) (c.ProviderTask, error) {
	task, err := b.testTasks.Bind(ctx, in, actor)
	if err == nil && b.afterBind != nil {
		b.afterBind()
	}
	return task, err
}
func adoptionFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.TaskAdoptInput, *adoptTasks) {
	s, o, p, create, b := taskFixture(t)
	ref, _ := c.KataTaskReference(b.Capabilities().ProjectUID, conversationIssue, create.Repository)
	a := &adoptTasks{testTasks: b, issue: taskstore.Issue{UID: conversationIssue, ProjectUID: b.Capabilities().ProjectUID, Title: "Native request", Body: "Please fix this", Status: "open", Revision: 7}}
	s.Tasks = a
	return s, o, p, c.TaskAdoptInput{Reference: ref, Goal: create.Goal, Repository: create.Repository, BaseBranch: create.BaseBranch, ExpectedRevision: 7, ExpectedApprovalSequence: 1, RequestID: "adopt"}, a
}
func TestTaskAdoptRetainsUIDAndCurrentScope(t *testing.T) {
	s, _, p, in, b := adoptionFixture(t)
	task, err := s.TaskAdopt(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if task.IssueUID != b.issue.UID || task.Binding == nil || task.Binding.Goal != in.Goal || task.Binding.ApprovalSequence != 1 || task.Binding.PolicySHA != b.Capabilities().PolicySHA || task.Binding.AuthorID == 0 || task.Binding.PeerID == 0 || b.creates != 0 {
		t.Fatalf("same UID scope: %+v creates=%d", task, b.creates)
	}
	artifacts, err := s.Artifacts(t.Context(), p, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(artifacts.Links) != 1 || artifacts.Links[0].AssignmentSHA != digest(task.Binding) {
		t.Fatalf("registry %+v %v", artifacts, err)
	}
}
func TestTaskAdoptChangedBodyCAS(t *testing.T) {
	s, o, _, in, b := adoptionFixture(t)
	b.issue.Body = "changed after observation"
	b.issue.Revision++
	if _, err := s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrConflict) || b.adopts != 0 {
		t.Fatal("stale native scope accepted", err)
	}
}
func TestTaskAdoptReplayIsStable(t *testing.T) {
	s, o, _, in, b := adoptionFixture(t)
	b.lose = true
	if _, err := s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	a, err := s.TaskAdopt(t.Context(), o, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.TaskAdopt(t.Context(), o, in)
	if err != nil || a.Reference != again.Reference || a.Revision != again.Revision || b.creates != 0 {
		t.Fatalf("replay %+v %+v %v", a, again, err)
	}
	in.BaseBranch = "other"
	if _, err = s.TaskAdopt(t.Context(), o, in); err == nil {
		t.Fatal("replayed changed scope")
	}
}
func TestTaskAdoptRejectsUntrustedOrChangedAuthority(t *testing.T) {
	for _, name := range []string{"project", "repository", "base", "approval", "forged", "dependency", "actors", "claim-owner"} {
		t.Run(name, func(t *testing.T) {
			s, o, p, in, b := adoptionFixture(t)
			switch name {
			case "project":
				b.issue.ProjectUID = conversationIssue
			case "repository":
				in.Repository = "other/repository"
			case "base":
				in.BaseBranch = "other"
			case "approval":
				in.ExpectedApprovalSequence++
			case "forged":
				b.issue.Metadata = map[string]json.RawMessage{"coop.native": encode(c.NativeTaskBinding{Goal: in.Goal})}
			case "dependency":
				in.DependsOn = []string{"https://github.com/other/repo/issues/1"}
			case "actors":
				p.Engagements = []string{"other"}
				if err := s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
					t.Fatal(err)
				}
			case "claim-owner":
				p.Kind = "owner"
				p.Capabilities = []string{"read"}
				if err := s.Store.Configure(t.Context(), []c.Principal{{ID: p.ID, Alias: p.Alias, Kind: "agent", GitHubBotID: p.GitHubBotID, Engagements: p.Engagements, Capabilities: []string{"read"}}}, nil); err != nil {
					t.Fatal(err)
				}
				o = p
			}
			if _, err := s.TaskAdopt(t.Context(), o, in); err == nil || b.adopts != 0 || b.creates != 0 {
				t.Fatalf("unauthorized adoption %v adopts=%d", err, b.adopts)
			}
		})
	}
}

func TestTaskAdoptReplayRevalidatesApprovalAndBody(t *testing.T) {
	for _, change := range []string{"body", "approval", "policy"} {
		t.Run(change, func(t *testing.T) {
			s, o, _, in, b := adoptionFixture(t)
			if _, err := s.TaskAdopt(t.Context(), o, in); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				b.issue.Body = "changed"
				b.issue.Revision++
			case "approval":
				if _, err := s.DecideGoal(t.Context(), o, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "approve", RequestID: "reapprove"}); err != nil {
					t.Fatal(err)
				}
			case "policy":
				b.task.Binding.PolicySHA = strings.Repeat("c", 64)
			}
			if _, err := s.TaskAdopt(t.Context(), o, in); err == nil {
				t.Fatal("changed admitted scope accepted")
			}
		})
	}
}
func TestKataOwnerDecisionPublishesExactRevisionReceipt(t *testing.T) {
	s, o, p, in, _ := taskFixture(t)
	authority, err := s.ResolveGoal(t.Context(), o, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := s.Store.Thread(t.Context(), o, authority.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err = s.Store.Configure(t.Context(), []c.Principal{bridge}, nil); err != nil {
		t.Fatal(err)
	}
	thread, err = s.Store.SetMembers(t.Context(), o, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: append(thread.Members, bridge.ID), RequestID: "add-source-bridge"})
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeConversation{comments: map[string]taskstore.Comment{}}
	if err = s.Store.ConfigureConversations(t.Context(), n, ConversationConfig{InstanceUID: conversationProject, ProjectUID: conversationProject, BridgePrincipal: bridge.ID, IntakePrincipal: p.ID, TeamThreadID: thread.ID, TeamIssueUID: conversationIssue}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.MapConversation(t.Context(), o, c.MapConversation{ThreadID: thread.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue}, RequestID: "map-goal"}); err != nil {
		t.Fatal(err)
	}
	decision := c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "approve", RequestID: "owner-approval"}
	forged := p
	forged.Kind = "owner"
	forged.Capabilities = []string{"decide"}
	if _, err = s.DecideGoal(t.Context(), forged, decision); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("forged owner claim accepted", err)
	}
	if _, err = s.DecideGoal(t.Context(), o, decision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideGoal(t.Context(), o, decision); err != nil {
		t.Fatal(err)
	}
	if n.writes != 1 {
		t.Fatal("missing or duplicate canonical approval receipt", n.writes)
	}
	for _, receipt := range n.comments {
		if !strings.Contains(receipt.Body, in.Goal.RevisionSHA256) || !strings.Contains(receipt.Body, "sequence 2") || !strings.Contains(receipt.Body, o.ID) {
			t.Fatalf("inexact receipt %+v", receipt)
		}
	}
}

func TestTaskAdoptHTTPRejectsSpoofedActorAndMaster(t *testing.T) {
	for _, tc := range []struct {
		token, pin string
		forged     bool
		want       int
	}{{"wrong", "test-master", false, 401}, {"owner-test-token", "wrong", false, 409}, {"owner-test-token", "test-master", true, 400}, {"owner-test-token", "test-master", false, 200}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			s, o, p, in, b := adoptionFixture(t)
			raw, _ := json.Marshal(in)
			body := string(raw)
			if tc.forged {
				body = strings.TrimSuffix(body, "}") + `,"actor":{"id":"owner"}}`
			}
			req := httptest.NewRequest("POST", "/api/v1/tasks/adopt", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			req.Header.Set("X-Coop-Expected-Master-ID", tc.pin)
			w := httptest.NewRecorder()
			NewHandler(s, policyFixture(o, p)).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("HTTP %d %s want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.want != 200 && b.adopts != 0 {
				t.Fatal("unauthorized adoption reached provider")
			}
		})
	}
}

func TestTaskReadUnboundPlannerCASInspectionIsScoped(t *testing.T) {
	for _, scenario := range []string{"allowed", "unmapped", "nonplanner", "wrongthread", "admittedcorrupt"} {
		t.Run(scenario, func(t *testing.T) {
			s, o, p, in, b := adoptionFixture(t)
			a, err := s.ResolveGoal(t.Context(), o, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			source := c.ConversationSource{Provider: "kata", InstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: conversationIssue}
			if scenario != "unmapped" {
				if _, err = s.Store.db.ExecContext(t.Context(), "INSERT INTO conversation_sources(thread_id,issue_uid,after_id,body) VALUES(?,?,0,?)", a.Revision.Proposal.ThreadID, conversationIssue, encode(source)); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "nonplanner":
				p.Capabilities = []string{"read"}
				if err = s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
					t.Fatal(err)
				}
			case "wrongthread":
				if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE thread_id=? AND principal_id=?", a.Revision.Proposal.ThreadID, p.ID); err != nil {
					t.Fatal(err)
				}
			case "admittedcorrupt":
				b.issue.Metadata = map[string]json.RawMessage{"coop.native": []byte(`"corrupt"`)}
			}
			task, err := s.TaskRead(t.Context(), p, c.TaskReadInput{Reference: in.Reference})
			if scenario != "allowed" {
				if err == nil {
					t.Fatal("raw inspection bypassed scope")
				}
				return
			}
			if err != nil || task.Revision != 7 || task.Body != b.issue.Body || task.Goal != (c.GoalRef{}) || task.Binding != nil || task.BindingActor.ID != "" {
				t.Fatalf("CAS inspection %+v %v", task, err)
			}
		})
	}
}

func TestTaskAdoptUnknownProviderWriteRecoversAfterRestart(t *testing.T) {
	s, o, _, in, b := adoptionFixture(t)
	b.lose = true
	if _, err := s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	old := s.Store
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreConfig{Path: old.path, MasterID: old.id, EpochPath: old.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	s.Store = reopened
	task, err := s.TaskAdopt(t.Context(), o, in)
	if err != nil || task.IssueUID != b.issue.UID || b.creates != 0 || task.Revision != 8 {
		t.Fatalf("restarted adoption %+v creates=%d err=%v", task, b.creates, err)
	}
}

func TestTaskAdoptChangedApprovalDuringProviderWriteDoesNotRegister(t *testing.T) {
	s, o, _, in, b := adoptionFixture(t)
	b.after = func() {
		if _, err := s.DecideGoal(t.Context(), o, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "approve", RequestID: "concurrent-reapproval"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed approval admitted during provider write", err)
	}
	page, err := s.Artifacts(t.Context(), o, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(page.Links) != 0 {
		t.Fatal("stale provider assignment registered", page, err)
	}
	b.after = nil
	oldStore := s.Store
	if err = oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreConfig{Path: oldStore.path, MasterID: oldStore.id, EpochPath: oldStore.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	s.Store = reopened
	// The original adoption remains immutable; recovery uses a fresh binding CAS.
	if _, err = s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale adoption replay accepted", err)
	}
	old := *b.task.Binding
	current := old
	current.ApprovalSequence = 2
	rebind := c.TaskBindInput{Binding: current, ExpectedRevision: b.task.Revision, ExpectedAssignmentSHA: digest(old), RequestID: "recover-adoption"}
	task, err := s.TaskBind(t.Context(), o, rebind)
	if err != nil {
		t.Fatal("applied adoption cannot recover under current approval", err)
	}
	if task.IssueUID != b.issue.UID || task.Binding.ApprovalSequence != 2 || b.creates != 0 {
		t.Fatal("recovery replaced native task", task)
	}
	page, err = s.Artifacts(t.Context(), o, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(page.Links) != 1 || page.Links[0].AssignmentSHA != digest(current) {
		t.Fatal("recovery registry missing", page, err)
	}
	if _, exists, err := taskMutation(t.Context(), s.Store.db, in.Reference); err != nil || exists {
		t.Fatal("recovery left reservation", exists, err)
	}
	if _, err = s.TaskAdopt(t.Context(), o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("recovery mutated original adoption request", err)
	}
}

func TestTaskAdoptRecoveryRetainsReservationWhenGuardsFail(t *testing.T) {
	for _, scenario := range []string{"journal", "mismatched-journal", "actor", "unauthorized", "revision", "assignment", "registry", "approval", "provider-actor", "provider-binding", "mapped-membership"} {
		t.Run(scenario, func(t *testing.T) {
			s, owner, plannerActor, in, backend := adoptionFixture(t)
			backend.lose = true
			if _, err := s.TaskAdopt(t.Context(), owner, in); !errors.Is(err, c.ErrUnavailable) {
				t.Fatal(err)
			}
			old := *backend.task.Binding
			if _, err := s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "approve", RequestID: "fresh-approval"}); err != nil {
				t.Fatal(err)
			}
			current := old
			current.ApprovalSequence = 2
			bind := c.TaskBindInput{Binding: current, ExpectedRevision: backend.task.Revision, ExpectedAssignmentSHA: digest(old), RequestID: "recover-adoption"}
			actor := owner
			switch scenario {
			case "journal":
				if _, err := s.Store.db.ExecContext(t.Context(), "DELETE FROM requests WHERE kind='task-adopt'"); err != nil {
					t.Fatal(err)
				}
			case "mismatched-journal":
				if _, err := s.Store.db.ExecContext(t.Context(), "UPDATE requests SET result=json_set(result,'$.binding.approval_sequence',99) WHERE kind='task-adopt'"); err != nil {
					t.Fatal(err)
				}
			case "unauthorized":
				owner.Engagements = []string{"other"}
				if err := s.Store.Configure(t.Context(), []c.Principal{owner}, nil); err != nil {
					t.Fatal(err)
				}
				actor = owner
			case "mapped-membership":
				thread, err := s.Store.CreateThread(t.Context(), owner, c.CreateThread{Title: "Restricted source", Engagement: "coop", Members: []string{owner.ID, plannerActor.ID}, RequestID: "restricted-source"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Store.db.ExecContext(t.Context(), "INSERT INTO conversation_sources(thread_id,issue_uid,after_id,body) VALUES(?,?,0,?)", thread.ID, conversationIssue, encode(c.ConversationSource{Provider: "kata", ProjectUID: conversationProject, IssueUID: conversationIssue})); err != nil {
					t.Fatal(err)
				}
				if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE thread_id=? AND principal_id=?", thread.ID, owner.ID); err != nil {
					t.Fatal(err)
				}
			case "actor":
				actor = plannerActor
			case "revision":
				bind.ExpectedRevision--
			case "assignment":
				bind.ExpectedAssignmentSHA = strings.Repeat("f", 64)
			case "registry":
				if _, err := s.Store.db.ExecContext(t.Context(), "INSERT INTO artifacts(goal_id,revision,url,kind,assignment_sha,body) VALUES(?,?,?,'task',?,?)", in.Goal.GoalID, in.Goal.RevisionSHA256, in.Reference, digest(old), encode(c.ArtifactLink{})); err != nil {
					t.Fatal(err)
				}
			case "approval":
				bind.Binding.ApprovalSequence = 1
			case "provider-actor":
				backend.task.BindingActor = plannerActor
			case "provider-binding":
				changed := old
				changed.PeerID++
				backend.task.Binding = &changed
			}
			if _, err := s.TaskBind(t.Context(), actor, bind); err == nil {
				t.Fatal("recovery bypassed", scenario)
			}
			pending, exists, err := taskMutation(t.Context(), s.Store.db, in.Reference)
			if err != nil || !exists || pending.Actor != owner.ID || digest(pending.Input.Binding) != digest(old) || backend.binds != 0 || backend.creates != 0 {
				t.Fatal("failed recovery changed intent", pending, err)
			}
		})
	}
}

func TestTaskAdoptRecoveryRechecksApprovalAfterNativeWrite(t *testing.T) {
	s, owner, _, in, backend := adoptionFixture(t)
	backend.lose = true
	if _, err := s.TaskAdopt(t.Context(), owner, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	old := *backend.task.Binding
	approve := func(seq int64) {
		if _, err := s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: seq, Action: "approve", RequestID: fmt.Sprintf("approval-%d", seq)}); err != nil {
			t.Fatal(err)
		}
	}
	approve(1)
	current := old
	current.ApprovalSequence = 2
	backend.afterBind = func() { approve(2) }
	if _, err := s.TaskBind(t.Context(), owner, c.TaskBindInput{Binding: current, ExpectedRevision: backend.task.Revision, ExpectedAssignmentSHA: digest(old), RequestID: "recover-adoption"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("approval changed during recovery write admitted", err)
	}
	page, err := s.Artifacts(t.Context(), owner, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(page.Links) != 0 || backend.creates != 0 {
		t.Fatal("stale recovery registered or created issue", page, err)
	}
	backend.afterBind = nil
	store := s.Store
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), StoreConfig{Path: store.path, MasterID: store.id, EpochPath: store.epochPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	s.Store = reopened
	latest := current
	latest.ApprovalSequence = 3
	retry := c.TaskBindInput{Binding: latest, ExpectedRevision: backend.task.Revision, ExpectedAssignmentSHA: digest(current), RequestID: "recover-adoption-again"}
	task, err := s.TaskBind(t.Context(), owner, retry)
	if err != nil {
		t.Fatal("second fresh recovery after approval change and restart stranded", err)
	}
	if task.IssueUID != backend.issue.UID || task.Binding == nil || task.Binding.ApprovalSequence != 3 || backend.creates != 0 {
		t.Fatal("second recovery changed native identity", task)
	}
	revision := task.Revision
	task, err = s.TaskBind(t.Context(), owner, retry)
	if err != nil || task.Revision != revision {
		t.Fatal("second recovery replay changed native assignment", task, err)
	}
	if _, err = s.TaskAdopt(t.Context(), owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("original adoption changed after recovery", err)
	}
	var original taskAdoptIntent
	found, err := request(t.Context(), s.Store.db, owner.ID, in.RequestID, "task-adopt", in, &original)
	if err != nil || !found || digest(original.Binding) != digest(old) {
		t.Fatal("original authenticated journal changed", original, err)
	}
	page, err = s.Artifacts(t.Context(), owner, c.ArtifactQuery{Goal: in.Goal})
	if err != nil || len(page.Links) != 1 || page.Links[0].AssignmentSHA != digest(latest) {
		t.Fatal("second recovery registry missing", page, err)
	}
	if _, exists, err := taskMutation(t.Context(), s.Store.db, in.Reference); err != nil || exists {
		t.Fatal("second recovery left reservation", exists, err)
	}
}

func TestTaskAdoptRepeatedRecoveryRejectsForgedOrigin(t *testing.T) {
	for _, scenario := range []string{"missing-origin", "wrong-origin-request", "wrong-origin-task", "missing-adoption-journal", "missing-origin-bind-journal", "mismatched-origin-bind-journal", "missing-pending-bind-journal", "mismatched-pending-bind-journal"} {
		t.Run(scenario, func(t *testing.T) {
			s, owner, _, in, backend := adoptionFixture(t)
			backend.lose = true
			if _, err := s.TaskAdopt(t.Context(), owner, in); !errors.Is(err, c.ErrUnavailable) {
				t.Fatal(err)
			}
			origin, exists, err := taskMutation(t.Context(), s.Store.db, in.Reference)
			if err != nil || !exists {
				t.Fatal(origin, err)
			}
			approve := func(seq int64) {
				if _, err := s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: seq, Action: "approve", RequestID: fmt.Sprintf("approval-%d", seq)}); err != nil {
					t.Fatal(err)
				}
			}
			approve(1)
			second := origin.Input.Binding
			second.ApprovalSequence = 2
			backend.afterBind = func() { approve(2) }
			if _, err = s.TaskBind(t.Context(), owner, c.TaskBindInput{Binding: second, ExpectedRevision: backend.task.Revision, ExpectedAssignmentSHA: digest(origin.Input.Binding), RequestID: "recover-adoption"}); !errors.Is(err, c.ErrConflict) {
				t.Fatal(err)
			}
			backend.afterBind = nil
			var raw string
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key=?", taskMutationKey(in.Reference)).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var reserved map[string]any
			if err = json.Unmarshal([]byte(raw), &reserved); err != nil {
				t.Fatal(err)
			}
			fake := origin.Input
			switch scenario {
			case "missing-origin":
				delete(reserved, "adoption_origin")
			case "wrong-origin-request":
				fake.RequestID = "unrelated-bind"
				reserved["adoption_origin"] = fake
			case "wrong-origin-task":
				fake.Binding.TaskURL += "-other"
				reserved["adoption_origin"] = fake
			case "missing-adoption-journal":
				if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM requests WHERE actor=? AND request_id=?", owner.ID, in.RequestID); err != nil {
					t.Fatal(err)
				}
			case "missing-origin-bind-journal":
				if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM requests WHERE actor=? AND request_id=?", owner.ID, origin.Input.RequestID); err != nil {
					t.Fatal(err)
				}
			case "mismatched-origin-bind-journal":
				if _, err = s.Store.db.ExecContext(t.Context(), "UPDATE requests SET digest=? WHERE actor=? AND request_id=?", strings.Repeat("f", 64), owner.ID, origin.Input.RequestID); err != nil {
					t.Fatal(err)
				}
			case "missing-pending-bind-journal":
				if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM requests WHERE actor=? AND request_id='recover-adoption'", owner.ID); err != nil {
					t.Fatal(err)
				}
			case "mismatched-pending-bind-journal":
				if _, err = s.Store.db.ExecContext(t.Context(), "UPDATE requests SET digest=? WHERE actor=? AND request_id='recover-adoption'", strings.Repeat("f", 64), owner.ID); err != nil {
					t.Fatal(err)
				}
			}
			raw = string(encode(reserved))
			if _, err = s.Store.db.ExecContext(t.Context(), "UPDATE meta SET value=? WHERE key=?", raw, taskMutationKey(in.Reference)); err != nil {
				t.Fatal(err)
			}
			latest := second
			latest.ApprovalSequence = 3
			if _, err = s.TaskBind(t.Context(), owner, c.TaskBindInput{Binding: latest, ExpectedRevision: backend.task.Revision, ExpectedAssignmentSHA: digest(second), RequestID: "recover-again"}); err == nil {
				t.Fatal("forged adoption origin accepted", scenario)
			}
			var after string
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key=?", taskMutationKey(in.Reference)).Scan(&after); err != nil || after != raw || backend.binds != 1 || backend.creates != 0 {
				t.Fatal("forged recovery changed reservation or native task", err)
			}
		})
	}
}
