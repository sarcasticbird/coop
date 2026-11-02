package master

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func sourceAuthorityFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.SourceDecisionInput, *testTasks) {
	t.Helper()
	s, owner, planner, create, backend := taskFixture(t)
	a, err := s.ResolveGoal(t.Context(), owner, create.Goal)
	if err != nil {
		t.Fatal(err)
	}
	rook := c.Principal{ID: c.DeliveryPrincipalID, Alias: "rook", Kind: "agent", GitHubBotID: c.DeliveryGitHubBotID, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	qa := c.Principal{ID: "source-qa", Alias: "source-qa", Kind: "agent", GitHubBotID: 333, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err = s.Store.Configure(t.Context(), []c.Principal{rook, qa}, nil); err != nil {
		t.Fatal(err)
	}
	thread, err := s.Store.Thread(t.Context(), owner, a.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: append(thread.Members, rook.ID, qa.ID), RequestID: "source-members"}); err != nil {
		t.Fatal(err)
	}
	proposal := a.Revision.Proposal
	proposal.RequestID = "source-with-qa"
	proposal.QA = c.QAPolicy{Required: true, ActorID: qa.ID, Environment: "hermetic", EnvironmentRevision: "v1", EvidenceDestination: c.EvidenceDestination{MasterID: create.Goal.MasterID, ThreadID: thread.ID, Engagement: "coop"}}
	revision, err := s.ProposeGoal(t.Context(), owner, proposal)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "source-approve"})
	if err != nil {
		t.Fatal(err)
	}
	create.Goal, create.ExpectedApprovalSequence, create.Purpose = revision.Ref, approved.ApprovalSequence, "source-gates"
	task, err := s.TaskCreate(t.Context(), planner, create)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Body: "Explicit exact human source intake; no source authorship transfer", RequestID: "source-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.SourceDecisionInput{Kind: "intake", Goal: revision.Ref, ApprovalSequence: approved.ApprovalSequence, GateTaskURL: task.Reference, GateAssignmentSHA: digest(task.Binding), PolicySHA: backend.Capabilities().PolicySHA, Source: c.SourceIdentity{Repository: "example/coop", RepositoryID: 1, PRURL: "https://github.com/example/coop/pull/158", PRID: "PR158", AuthorID: 111, AuthorKind: "User", OwnerID: 111, SourceRepo: "example/coop", SourceRepoID: 1, Branch: "human", BaseBranch: "main", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), TreeSHA: strings.Repeat("c", 40), BundleKind: "source-bundle-v1", BundleRevision: 1, BundleSHA: strings.Repeat("d", 64), PayloadSHA: strings.Repeat("d", 64), Contributors: []int64{111}, ContributorsComplete: true}, Target: c.SourceTarget{Repository: "example/coop", RepositoryID: 1, Branch: "main", SHA: strings.Repeat("a", 40), ResultTreeSHA: strings.Repeat("c", 40), ResultBundleSHA: strings.Repeat("d", 64)}, AcceptanceSHA: digest(proposal.Acceptance), Environment: "hermetic", EnvironmentRevision: "v1", ReviewerID: proposal.PeerID, QAID: qa.ID, EvidenceMessageID: evidence.MessageID, RequestID: "source-intake"}
	return s, owner, rook, in, backend
}

func TestSourceIntakeRegistrationAndDurableWithdrawal(t *testing.T) {
	s, owner, rook, in, b := sourceAuthorityFixture(t)
	before := digest(b.task.Binding)
	d, err := s.CreateSourceDecision(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateSourceDecision(t.Context(), owner, in)
	if err != nil || digest(retry) != digest(d) {
		t.Fatal("lost-response retry changed intake", err)
	}
	for _, change := range []string{"request", "bundle", "owner"} {
		bad := in
		if change == "request" {
			bad.RequestID = "duplicate-intake"
		}
		if change == "bundle" {
			bad.Source.BundleSHA = strings.Repeat("e", 64)
		}
		actor := owner
		if change == "owner" {
			actor = rook
		}
		if _, err = s.CreateSourceDecision(t.Context(), actor, bad); err == nil {
			t.Fatal("ambiguous intake accepted", change)
		}
	}
	if _, err = s.ValidateSourceDecision(t.Context(), c.Principal{ID: in.ReviewerID}, c.SourceDecisionValidation{Goal: in.Goal, ID: d.ID}); err != nil {
		t.Fatal(err)
	}
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "pause", RequestID: "pause-source"}); err != nil {
		t.Fatal(err)
	}
	revoke := c.SourceDecisionInput{Kind: "revoke", Goal: in.Goal, RevokeID: d.ID, RequestID: "withdraw-source"}
	withdrawn, err := s.CreateSourceDecision(t.Context(), owner, revoke)
	if err != nil || withdrawn.Sequence <= d.Sequence {
		t.Fatal("paused goal prevented owner withdrawal", err)
	}
	if _, err = s.ValidateSourceDecision(t.Context(), c.Principal{ID: in.ReviewerID}, c.SourceDecisionValidation{Goal: in.Goal, ID: d.ID}); err == nil {
		t.Fatal("withdrawn intake executable")
	}
	if digest(b.task.Binding) != before || b.creates != 1 || b.binds != 1 || b.closes != 0 {
		t.Fatal("intake rewrote or completed product/source task")
	}
	var nativePRs int
	if err = s.Store.db.QueryRow("SELECT count(*) FROM artifacts WHERE kind='pull_request'").Scan(&nativePRs); err != nil || nativePRs != 0 {
		t.Fatal("human source gained fabricated native registration", err)
	}
}

func TestSourceIntakeIndependentIdentityAndPolicy(t *testing.T) {
	for _, mode := range []string{"unknown", "reviewer-alias", "qa-alias", "delivery-alias", "policy", "approval", "acceptance", "environment", "task", "assessor", "actor"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, rook, in, _ := sourceAuthorityFixture(t)
			switch mode {
			case "unknown":
				in.Source.ContributorsComplete = false
			case "reviewer-alias":
				p, _ := principal(t.Context(), s.Store.db, in.ReviewerID)
				in.Source.Contributors = append(in.Source.Contributors, p.GitHubBotID)
			case "qa-alias":
				in.Source.Contributors = append(in.Source.Contributors, 333)
			case "delivery-alias":
				in.Source.Contributors = append(in.Source.Contributors, c.DeliveryGitHubBotID)
			case "policy":
				in.PolicySHA = strings.Repeat("f", 64)
			case "approval":
				in.ApprovalSequence++
			case "acceptance":
				in.AcceptanceSHA = strings.Repeat("e", 64)
			case "environment":
				in.EnvironmentRevision = "v2"
			case "task":
				in.GateAssignmentSHA = strings.Repeat("e", 64)
			case "assessor":
				in.ReviewerID = in.QAID
			case "actor":
				owner = rook
			}
			if _, err := s.CreateSourceDecision(t.Context(), owner, in); err == nil {
				t.Fatal("invalid supplemental authority admitted", mode)
			}
		})
	}
}

func TestSourceConcurrentIntakeExactlyOnce(t *testing.T) {
	s, owner, _, in, _ := sourceAuthorityFixture(t)
	var wg sync.WaitGroup
	results := make(chan c.SourceDecision, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { d, err := s.CreateSourceDecision(t.Context(), owner, in); results <- d; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for d := range results {
		if id != "" && id != d.ID {
			t.Fatal("same request created multiple assignments")
		}
		id = d.ID
	}
	page, err := s.SourceDecisions(t.Context(), owner, c.SourceDecisionQuery{Goal: in.Goal})
	if err != nil || len(page.Decisions) != 1 {
		t.Fatal("duplicated intake", err)
	}
}

func TestSourceExecutionRequiresRenewedIndependentReports(t *testing.T) {
	s, owner, rook, in, b := sourceAuthorityFixture(t)
	gates, err := s.CreateSourceDecision(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	exec := in
	exec.Kind, exec.ParentID, exec.ActorID, exec.RequestID = "deliver", gates.ID, "rook", "source-deliver"
	for _, stage := range []string{"source-review", "source-qa"} {
		actor := in.ReviewerID
		if stage == "source-qa" {
			actor = in.QAID
		}
		report := c.SourceGateReport{DecisionID: gates.ID, ScopeSHA: digest(in), Stage: stage, Verdict: "approved", ReviewID: fmt.Sprint("new-", stage), ReportBody: "Fresh exact source+target assessment, not a historical report"}
		body, _ := json.Marshal(report)
		msg, err := s.Store.Send(t.Context(), c.Principal{ID: actor}, c.SendMessage{ThreadID: s.mustSourceThread(t, owner, in.Goal), Body: string(body), RequestID: stage})
		if err != nil {
			t.Fatal(err)
		}
		if stage == "source-review" {
			exec.EngineeringReportID, exec.EngineeringBodySHA = msg.MessageID, retainedBodySHA(string(body))
		} else {
			exec.QAReportID, exec.QABodySHA = msg.MessageID, retainedBodySHA(string(body))
		}
	}
	d, err := s.CreateSourceDecision(t.Context(), owner, exec)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []c.Principal{owner, {ID: in.ReviewerID}, {ID: in.QAID}, {ID: "alternate"}} {
		if _, err = s.ValidateSourceDecision(t.Context(), actor, c.SourceDecisionValidation{Goal: in.Goal, ID: d.ID}); err == nil {
			t.Fatal("non-Rook executable delivery", actor.ID)
		}
	}
	if _, err = s.ValidateSourceDecision(t.Context(), rook, c.SourceDecisionValidation{Goal: in.Goal, ID: d.ID}); err != nil {
		t.Fatal(err)
	}
	completion := c.SourceCompletionInput{Goal: in.Goal, DecisionID: d.ID, RequestID: "source-complete"}
	if _, err = s.CompleteSourceTask(t.Context(), rook, completion); err == nil || b.closes != 0 {
		t.Fatal("completion without acknowledged integration receipt")
	}
	if _, err = s.RecordSourceEffect(t.Context(), rook, c.SourceEffectReceipt{Goal: in.Goal, DecisionID: d.ID, Kind: "deliver", CommitSHA: strings.Repeat("e", 40), TreeSHA: in.Target.ResultTreeSHA, RequestID: "delivery-receipt"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteSourceTask(t.Context(), rook, completion); err != nil || b.closes != 1 {
		t.Fatal("source completion", err)
	}
	revoke := c.SourceDecisionInput{Kind: "revoke", Goal: in.Goal, RevokeID: gates.ID, RequestID: "revoke-parent"}
	if _, err = s.CreateSourceDecision(t.Context(), owner, revoke); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateSourceDecision(t.Context(), rook, c.SourceDecisionValidation{Goal: in.Goal, ID: d.ID}); err == nil {
		t.Fatal("revoked parent resurrected execution")
	}
}

func (s *Service) mustSourceThread(t *testing.T, owner c.Principal, goal c.GoalRef) string {
	t.Helper()
	a, err := s.ResolveGoal(t.Context(), owner, goal)
	if err != nil {
		t.Fatal(err)
	}
	return a.Revision.Proposal.QA.EvidenceDestination.ThreadID
}
