package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func deliveryFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.Principal, c.HandoffInput) {
	t.Helper()
	s, owner, planner, qa, in := qaHandoffFixture(t)
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	rook := c.Principal{ID: c.DeliveryPrincipalID, Alias: "delivery-rook", Kind: "agent", GitHubBotID: c.DeliveryGitHubBotID, Engagements: []string{"coop"}, Capabilities: []string{"read"}}
	if err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
		thread, err := s.Store.Thread(t.Context(), owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: id, ExpectedMembers: thread.Members, Members: append(thread.Members, rook.ID), RequestID: "rook-member-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.Store.Send(t.Context(), qa, c.SendMessage{ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: "Exact artifact passed acceptance", RequestID: "delivery-qa"})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("Exact artifact passed acceptance"))
	binding := s.Tasks.(*testTasks).task.Binding
	in.Kind, in.ReviewID = "deliver", ""
	in.ApprovalSequence, in.TaskScopeSHA, in.BaseBranch = a.ApprovalSequence, binding.TaskScopeSHA, binding.BaseBranch
	in.BaseSHA, in.PRScopeRevision, in.AcceptanceSHA = strings.Repeat("d", 40), strings.Repeat("e", 64), digest(a.Revision.Proposal.Acceptance)
	in.PeerReviewID, in.QAReviewID = "peer-1", "qa-1"
	in.Environment, in.EnvironmentRevision = "test", "v1"
	in.QAEvidenceMessageID, in.QAEvidenceBodySHA = report.MessageID, hex.EncodeToString(hash[:])
	return s, owner, planner, rook, in
}

func TestDeliverySequenceSurvivesClockChanges(t *testing.T) {
	for _, offset := range []time.Duration{0, -time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			s, owner, _, rook, in := deliveryFixture(t)
			now := s.Store.now().UTC()
			s.Store.now = func() time.Time { return now }
			grant, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(offset)
			revoke := in
			revoke.Revoked, revoke.RequestID = true, "revoke-with-clock-change"
			revocation, err := s.CreateHandoff(t.Context(), owner, revoke)
			if err != nil {
				t.Fatal(err)
			}
			if grant.Sequence < 1 || revocation.Sequence <= grant.Sequence || revocation.CreatedAt.After(grant.CreatedAt) {
				t.Fatalf("clock affected decision order: grant=%+v revocation=%+v", grant, revocation)
			}
			for _, original := range []c.Handoff{grant, revocation} {
				var rowID int64
				if err = s.Store.db.QueryRowContext(t.Context(), "SELECT rowid FROM handoffs WHERE id=?", original.ID).Scan(&rowID); err != nil || original.Sequence != rowID {
					t.Fatalf("published sequence disagrees with persisted order: sequence=%d rowid=%d err=%v", original.Sequence, rowID, err)
				}
				retry, err := s.CreateHandoff(t.Context(), owner, original.Input)
				if err != nil || retry != original {
					t.Fatalf("retry changed sequence or decision: %+v %v", retry, err)
				}
			}
			page, err := s.Handoffs(t.Context(), rook, c.HandoffQuery{Goal: in.Goal})
			if err != nil || len(page.Handoffs) != 2 {
				t.Fatalf("retry appended a decision: %+v %v", page, err)
			}
			// Workers select the highest Sequence before checking revocation,
			// independently of list/ID order or the wall clock.
			for _, decisions := range [][]c.Handoff{page.Handoffs, {page.Handoffs[1], page.Handoffs[0]}} {
				var latest c.Handoff
				for _, h := range decisions {
					if h.Sequence > latest.Sequence {
						latest = h
					}
				}
				if latest.ID != revocation.ID || !latest.Input.Revoked {
					t.Fatalf("worker sequence rule selected an older decision: %+v", latest)
				}
			}
			complete := c.TaskCompleteInput{Reference: in.TaskURL, Goal: in.Goal, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, RequestID: "complete-clock-change"}
			for _, h := range []c.Handoff{grant, revocation} {
				complete.DeliveryHandoffID = h.ID
				if _, err = s.TaskComplete(t.Context(), rook, complete); !errors.Is(err, c.ErrConflict) {
					t.Fatalf("master disagreed with sequence revocation: %v", err)
				}
			}
			if s.Tasks.(*testTasks).closes != 0 {
				t.Fatal("revoked decision reached provider")
			}
			in.RequestID = "grant-after-revocation"
			renewed, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil || renewed.Sequence <= revocation.Sequence {
				t.Fatalf("new grant did not advance sequence: %+v %v", renewed, err)
			}
			complete.DeliveryHandoffID = renewed.ID
			if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil {
				t.Fatal("denied completion poisoned request journal", err)
			}
		})
	}
}

func TestDeliveryOwnerReassessmentEnvironment(t *testing.T) {
	for _, change := range []string{"approved", "missing", "agent", "goal", "task", "assignment", "pr", "head", "artifact", "environment", "revision", "imported", "evidence-agent", "evidence-thread", "evidence-membership", "owner-source-membership", "source-identity", "qa-identity"} {
		t.Run(change, func(t *testing.T) {
			s, owner, _, rook, in := deliveryFixture(t)
			in.Environment, in.EnvironmentRevision = "repaired staging", "repair-v2"
			evidence, err := s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, in.Goal), Body: "Owner approves this exact artifact's repaired environment", RequestID: "environment-decision-evidence"})
			if err != nil {
				t.Fatal(err)
			}
			reassessment := c.HandoffInput{Kind: "reassess-qa", Goal: in.Goal, TaskURL: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PRURL: in.PRURL, HeadSHA: in.HeadSHA, ArtifactSHA: in.ArtifactSHA, Environment: in.Environment, EnvironmentRevision: in.EnvironmentRevision, ReviewID: "previous-qa-review", EvidenceMessageID: evidence.MessageID, RequestID: "approve-repaired-environment"}
			h, err := s.CreateHandoff(t.Context(), owner, reassessment)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM handoffs WHERE id=?", h.ID)
			case "agent":
				h.ActorKind, h.ActorID = "agent", "agent-three"
			case "goal":
				h.Input.Goal.GoalID = "other-goal"
			case "task":
				h.Input.TaskURL += "other"
			case "assignment":
				h.Input.AssignmentSHA = strings.Repeat("f", 64)
			case "pr":
				h.Input.PRURL += "9"
			case "head":
				h.Input.HeadSHA = strings.Repeat("f", 40)
			case "artifact":
				h.Input.ArtifactSHA = strings.Repeat("f", 64)
			case "environment":
				h.Input.Environment = "other staging"
			case "revision":
				h.Input.EnvironmentRevision = "repair-v3"
			case "imported":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", h.Input.EvidenceMessageID)
			case "evidence-agent":
				h.Input.EvidenceMessageID = in.QAEvidenceMessageID
			case "evidence-thread":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.thread_id','unrelated') WHERE id=?", h.Input.EvidenceMessageID)
			case "evidence-membership":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE principal_id=? AND thread_id=?", owner.ID, s.mustHandoffEvidenceThread(t, owner, h.Input.EvidenceMessageID))
			case "owner-source-membership":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE principal_id=? AND thread_id=?", owner.ID, mustHandoffThread(t, s, owner, in.Goal))
			case "source-identity":
				h.ActorID = "agent-three"
			case "qa-identity":
				in.QAEvidenceMessageID = h.Input.EvidenceMessageID
				m, e := s.Store.Message(t.Context(), owner, in.QAEvidenceMessageID)
				if e != nil {
					t.Fatal(e)
				}
				hash := sha256.Sum256([]byte(m.Body))
				in.QAEvidenceBodySHA = hex.EncodeToString(hash[:])
			}
			if err != nil {
				t.Fatal(err)
			}
			if change != "missing" {
				if _, err = s.Store.db.ExecContext(t.Context(), "UPDATE handoffs SET body=? WHERE id=?", encode(h), h.ID); err != nil {
					t.Fatal(err)
				}
			}
			grant, err := s.CreateHandoff(t.Context(), owner, in)
			if change != "approved" {
				if !errors.Is(err, c.ErrForbidden) {
					t.Fatalf("invalid environment authority: got %v, want forbidden", err)
				}
				return
			}
			if err != nil {
				t.Fatal("exact owner environment rejected", err)
			}
			if retry, e := s.CreateHandoff(t.Context(), owner, in); e != nil || retry != grant {
				t.Fatalf("environment grant retry changed: %+v %v", retry, e)
			}
			complete := c.TaskCompleteInput{Reference: in.TaskURL, Goal: in.Goal, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "complete-repaired-environment"}
			if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil {
				t.Fatal("approved environment could not complete", err)
			}
			// Fresh evidence authorization precedes journal replay, even after
			// an accepted completion. Cached success is not a new grant.
			if _, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", h.Input.EvidenceMessageID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.TaskComplete(t.Context(), rook, complete); !errors.Is(err, c.ErrForbidden) {
				t.Fatal("completion retry bypassed fresh authority", err)
			}
			if _, err = s.CreateHandoff(t.Context(), owner, in); !errors.Is(err, c.ErrForbidden) {
				t.Fatal("grant retry bypassed fresh environment authority", err)
			}
		})
	}
}

type deliveryReadHook struct {
	*testTasks
	afterRead func()
}

func (b *deliveryReadHook) Read(ctx context.Context, ref string) (c.ProviderTask, error) {
	task, err := b.testTasks.Read(ctx, ref)
	if b.afterRead != nil {
		b.afterRead()
		b.afterRead = nil
	}
	return task, err
}

func TestDeliveryCompletionRechecksTransactionalIdentity(t *testing.T) {
	for _, field := range []string{"id", "kind", "github_bot_id"} {
		t.Run(field, func(t *testing.T) {
			s, owner, _, rook, in := deliveryFixture(t)
			h, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			backend := s.Tasks.(*testTasks)
			s.Tasks = &deliveryReadHook{testTasks: backend, afterRead: func() {
				changed := rook
				switch field {
				case "id":
					changed.ID = owner.ID
				case "kind":
					changed.Kind = "owner"
				case "github_bot_id":
					changed.GitHubBotID++
				}
				if _, err := s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=? WHERE id=?", encode(changed), rook.ID); err != nil {
					t.Fatal(err)
				}
			}}
			complete := c.TaskCompleteInput{Reference: in.TaskURL, Goal: in.Goal, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: h.ID, RequestID: "complete-raced-identity"}
			if _, err = s.TaskComplete(t.Context(), rook, complete); !errors.Is(err, c.ErrForbidden) || backend.closes != 0 {
				t.Fatalf("changed identity reached completion: closes=%d, err=%v", backend.closes, err)
			}
			var count int
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM requests WHERE request_id=?", complete.RequestID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied identity reserved journal: count=%d, err=%v", count, err)
			}
		})
	}
}

func TestDeliveryOwnerGrantAndImmutableRetry(t *testing.T) {
	s, owner, planner, rook, in := deliveryFixture(t)
	for _, actor := range []c.Principal{planner, rook} {
		if _, err := s.CreateHandoff(t.Context(), actor, in); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("nonowner granted delivery: %v", err)
		}
	}
	before := digest(s.Tasks.(*testTasks).task.Binding)
	h, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil || h.ActorKind != "owner" || h.Input != in {
		t.Fatalf("grant: %+v %v", h, err)
	}
	again, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil || again != h {
		t.Fatalf("retry changed grant: %+v %v", again, err)
	}
	in.HeadSHA = strings.Repeat("f", 40)
	if _, err = s.CreateHandoff(t.Context(), owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	if digest(s.Tasks.(*testTasks).task.Binding) != before || s.Tasks.(*testTasks).binds != 1 {
		t.Fatal("grant changed assignment")
	}
}

func TestDeliveryGrantGuards(t *testing.T) {
	for _, change := range []string{"approval", "scope", "base", "acceptance", "environment", "qa-review", "qa-hash", "qa-author", "qa-thread", "qa-imported", "owner-imported", "owner-evidence", "owner-decide", "rook-kind", "rook-bot", "rook-read", "rook-source", "rook-qa", "paused"} {
		t.Run(change, func(t *testing.T) {
			s, owner, _, rook, in := deliveryFixture(t)
			var err error
			switch change {
			case "approval":
				in.ApprovalSequence++
			case "scope":
				in.TaskScopeSHA = strings.Repeat("f", 64)
			case "base":
				in.BaseBranch = "other"
			case "acceptance":
				in.AcceptanceSHA = strings.Repeat("f", 64)
			case "environment":
				in.EnvironmentRevision = "stale"
			case "qa-review":
				in.QAReviewID, in.QAEvidenceMessageID, in.QAEvidenceBodySHA = "", "", ""
			case "qa-hash":
				in.QAEvidenceBodySHA = strings.Repeat("f", 64)
			case "qa-author":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.author_id',?) WHERE id=?", owner.ID, in.QAEvidenceMessageID)
			case "qa-thread":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.thread_id',?) WHERE id=?", mustHandoffThread(t, s, owner, in.Goal), in.QAEvidenceMessageID)
			case "qa-imported", "owner-imported":
				id := in.QAEvidenceMessageID
				if change == "owner-imported" {
					id = in.EvidenceMessageID
				}
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", id)
			case "owner-evidence":
				in.EvidenceMessageID = in.QAEvidenceMessageID
			case "owner-decide":
				owner.Capabilities = []string{"read"}
				err = s.Store.Configure(t.Context(), []c.Principal{owner}, nil)
			case "rook-kind":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.kind','owner') WHERE id=?", rook.ID)
			case "rook-bot":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.github_bot_id',1) WHERE id=?", rook.ID)
			case "rook-read":
				rook.Capabilities = nil
				err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil)
			case "rook-source", "rook-qa":
				a, e := s.ResolveGoal(t.Context(), owner, in.Goal)
				if e != nil {
					t.Fatal(e)
				}
				thread := a.Revision.Proposal.ThreadID
				if change == "rook-qa" {
					thread = a.Revision.Proposal.QA.EvidenceDestination.ThreadID
				}
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE thread_id=? AND principal_id=?", thread, rook.ID)
			case "paused":
				a, e := s.ResolveGoal(t.Context(), owner, in.Goal)
				if e != nil {
					t.Fatal(e)
				}
				_, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "pause", RequestID: "pause-delivery"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateHandoff(t.Context(), owner, in); err == nil {
				t.Fatal("unsafe grant accepted")
			}
		})
	}
}

func TestDeliveryCompletionRequiresLatestGrantAndRook(t *testing.T) {
	for _, change := range []string{"valid", "owner", "author", "missing", "revoked", "revoked-latest", "superseded", "stale-scope", "stale-approval", "stale-acceptance", "identity", "disabled", "changed-retry"} {
		t.Run(change, func(t *testing.T) {
			s, owner, planner, rook, in := deliveryFixture(t)
			h, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			complete := c.TaskCompleteInput{Reference: in.TaskURL, Goal: in.Goal, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: h.ID, RequestID: "complete-delivery"}
			actor := rook
			switch change {
			case "owner":
				actor = owner
			case "author":
				actor = planner
			case "missing":
				complete.DeliveryHandoffID = ""
			case "revoked", "revoked-latest", "superseded":
				in.Revoked, in.RequestID = change != "superseded", "latest-delivery"
				latest, e := s.CreateHandoff(t.Context(), owner, in)
				if e != nil {
					t.Fatal(e)
				}
				if change == "revoked-latest" {
					complete.DeliveryHandoffID = latest.ID
				}
				if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
					t.Fatal(err)
				}
				// Replaying the original grant must not move it ahead of a
				// later decision, including a revocation.
				if _, err = s.CreateHandoff(t.Context(), owner, h.Input); err != nil {
					t.Fatal(err)
				}
			case "stale-scope":
				s.Tasks.(*testTasks).task.Body = "Changed scope"
			case "stale-approval":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE handoffs SET body=json_set(body,'$.input.approval_sequence',999) WHERE id=?", h.ID)
			case "stale-acceptance":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE handoffs SET body=json_set(body,'$.input.acceptance_sha256',?) WHERE id=?", strings.Repeat("f", 64), h.ID)
			case "identity":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.github_bot_id',1) WHERE id=?", rook.ID)
			case "disabled":
				rook.Capabilities = nil
				err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil)
			case "changed-retry":
				if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil {
					t.Fatal(err)
				}
				in.RequestID = "replacement-grant"
				h, err = s.CreateHandoff(t.Context(), owner, in)
				complete.DeliveryHandoffID = h.ID
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.TaskComplete(t.Context(), actor, complete)
			if change == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil {
					t.Fatal("exact completion retry rejected", err)
				}
			} else if err == nil {
				t.Fatal("unsafe completion accepted")
			}
			if change != "valid" && change != "changed-retry" && s.Tasks.(*testTasks).closes != 0 {
				t.Fatal("unauthorized completion reached provider")
			}
		})
	}
}

func TestDeliveryOptionalQAGitHubGrant(t *testing.T) {
	s, owner, _, in := githubHandoffFixture(t)
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	rook := c.Principal{ID: c.DeliveryPrincipalID, Alias: "delivery-rook", Kind: "agent", GitHubBotID: c.DeliveryGitHubBotID, Engagements: []string{"coop"}, Capabilities: []string{"read"}}
	if err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range normalized([]string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID}) {
		thread, err := s.Store.Thread(t.Context(), owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: id, ExpectedMembers: thread.Members, Members: append(thread.Members, rook.ID), RequestID: "rook-member-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	in.Kind, in.Tag, in.Workflow = "deliver", "", ""
	in.ApprovalSequence, in.TaskScopeSHA, in.BaseBranch = a.ApprovalSequence, strings.Repeat("d", 64), "main"
	in.BaseSHA, in.PRScopeRevision, in.AcceptanceSHA = strings.Repeat("e", 40), strings.Repeat("f", 64), digest(a.Revision.Proposal.Acceptance)
	in.PeerReviewID = "peer-1"
	s.Tasks = nil // Retained GitHub grants do not require a Kata provider.
	if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
		t.Fatal("QA-optional delivery rejected", err)
	}
	in.Revoked, in.RequestID = true, "revoke-delivery"
	if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
		t.Fatal("owner revocation rejected", err)
	}
}

func TestDeliveryInputValidation(t *testing.T) {
	_, _, _, _, in := deliveryFixture(t)
	for _, mutate := range []func(*c.HandoffInput){
		func(h *c.HandoffInput) { h.BaseSHA = "bad" },
		func(h *c.HandoffInput) { h.HeadSHA = "bad" },
		func(h *c.HandoffInput) { h.PRScopeRevision = "bad" },
		func(h *c.HandoffInput) { h.PeerReviewID = "bad/id" },
		func(h *c.HandoffInput) { h.EnvironmentRevision = "" },
		func(h *c.HandoffInput) { h.QAReviewID = "bad/id" },
		func(h *c.HandoffInput) { h.Kind = "release"; h.Tag = "v1.2.3"; h.Workflow = "release.yml" },
	} {
		bad := in
		mutate(&bad)
		if err := c.ValidateHandoff(bad); !errors.Is(err, c.ErrInvalid) {
			t.Fatal("malformed delivery accepted", err)
		}
	}
	in.Environment, in.EnvironmentRevision, in.QAReviewID, in.QAEvidenceMessageID, in.QAEvidenceBodySHA = "", "", "", "", ""
	if err := c.ValidateHandoff(in); err != nil {
		t.Fatal("QA-optional shape rejected", err)
	}
}
