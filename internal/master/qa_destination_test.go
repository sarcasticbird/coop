package master

import (
	"encoding/json"
	"errors"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestGoalQAUsesCurrentMembershipNotPrivateDestination(t *testing.T) {
	svc, owner, planner, qa, handoff := qaHandoffFixture(t)
	a, err := svc.ResolveGoal(t.Context(), owner, handoff.Goal)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(a.Revision)
	dest := a.Revision.Proposal.QA.EvidenceDestination
	private, err := svc.Store.Thread(t.Context(), owner, dest.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: private.ID, ExpectedMembers: private.Members, Members: []string{owner.ID}, RequestID: "retire-private-report-access"}); err != nil {
		t.Fatal(err)
	}
	a, err = svc.ResolveGoal(t.Context(), planner, handoff.Goal)
	if err != nil || !a.Valid {
		t.Fatal("retired report ACL revoked current QA authority", a, err)
	}
	unchanged, _ := json.Marshal(a.Revision)
	if string(original) != string(unchanged) {
		t.Fatal("historical proposal or revision was rewritten")
	}
	// Explicit handoffs still require access to their actual evidence message.
	if _, err := svc.CreateHandoff(t.Context(), planner, handoff); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("unreadable explicit evidence accepted", err)
	}
	if _, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: handoff.Goal.GoalID, RevisionSHA256: handoff.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "renew-without-report-acl"}); err != nil {
		t.Fatal("renewal required obsolete ACL", err)
	}
	goal, err := svc.Store.Thread(t.Context(), owner, a.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	var members []string
	for _, id := range goal.Members {
		if id != qa.ID {
			members = append(members, id)
		}
	}
	if _, err := svc.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: goal.ID, ExpectedMembers: goal.Members, Members: members, RequestID: "revoke-current-qa"}); err != nil {
		t.Fatal(err)
	}
	a, err = svc.ResolveGoal(t.Context(), owner, handoff.Goal)
	if err != nil || a.Valid {
		t.Fatal("current QA membership revocation ignored", a, err)
	}
}

func TestGoalProposalNeedsNoPrivateQADestination(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "none", true: "missing-historical-thread"}[historical], func(t *testing.T) {
			svc, owner, _, _, handoff := qaHandoffFixture(t)
			a, err := svc.ResolveGoal(t.Context(), owner, handoff.Goal)
			if err != nil {
				t.Fatal(err)
			}
			proposal := a.Revision.Proposal
			proposal.GoalID, proposal.RequestID = "", "new-pr-qa-goal"
			proposal.QA.EvidenceDestination = c.EvidenceDestination{}
			if historical {
				proposal.QA.EvidenceDestination = c.EvidenceDestination{MasterID: svc.Store.id, ThreadID: "retired-private-thread", Engagement: "coop"}
			}
			revision, err := svc.ProposeGoal(t.Context(), owner, proposal)
			if err != nil {
				t.Fatal("private report destination required for proposal", err)
			}
			if revision.Proposal.QA.EvidenceDestination != proposal.QA.EvidenceDestination {
				t.Fatal("proposal synthesized or rewrote a report destination")
			}
			approved, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "approve-pr-qa-goal"})
			if err != nil || !approved.Valid {
				t.Fatal("private destination required for approval", err)
			}
		})
	}
}

func TestExplicitHandoffStillScopesPrivateEvidence(t *testing.T) {
	for _, fault := range []string{"master", "destination-engagement", "thread-engagement"} {
		t.Run(fault, func(t *testing.T) {
			svc, owner, planner, _, in := qaHandoffFixture(t)
			a, err := svc.ResolveGoal(t.Context(), owner, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			proposal := a.Revision.Proposal
			proposal.GoalID, proposal.RequestID = "", "handoff-scope-goal"
			switch fault {
			case "master":
				proposal.QA.EvidenceDestination.MasterID = "foreign-master"
			case "destination-engagement":
				proposal.QA.EvidenceDestination.Engagement = "other"
			case "thread-engagement":
				// This represents stored thread drift; there is no public thread
				// retarget operation. Only the explicit evidence use must refuse.
				if _, err := svc.Store.db.ExecContext(t.Context(), "UPDATE threads SET engagement='other' WHERE id=?", proposal.QA.EvidenceDestination.ThreadID); err != nil {
					t.Fatal(err)
				}
				// The owner/planner can read both engagements, so this test reaches
				// the handoff's exact engagement check, not a general read refusal.
				for _, actor := range []*c.Principal{&owner, &planner} {
					actor.Engagements = append(actor.Engagements, "other")
					if err := svc.Store.Configure(t.Context(), []c.Principal{*actor}, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			revision, err := svc.ProposeGoal(t.Context(), owner, proposal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "approve-handoff-scope"}); err != nil {
				t.Fatal(err)
			}
			in.Goal, in.TaskURL = revision.Ref, "https://github.com/example/coop/issues/2"
			// GitHub's exact task/PR registry check follows the evidence check.
			// No missing registry entry may conceal an out-of-scope acceptance.
			if _, err := svc.CreateHandoff(t.Context(), planner, in); !errors.Is(err, c.ErrForbidden) {
				t.Fatal("explicit private evidence escaped its scope", err)
			}
		})
	}
}
