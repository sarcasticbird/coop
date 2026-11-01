package master

import (
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func masterTagOnlyFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.HandoffInput, *retainedTasks) {
	t.Helper()
	s, owner, rook, in, tasks := retainedFixture(t)
	gates, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.CreateHandoff(t.Context(), owner, retainedReports(t, s, owner, gates))
	if err != nil {
		t.Fatal(err)
	}
	in = source.Input
	if _, err = s.TaskComplete(t.Context(), rook, c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: source.ID, RequestID: "source-complete"}); err != nil {
		t.Fatal(err)
	}
	tasks.task.State, tasks.task.ClosedReason = "closed", "done"
	in.Kind, in.Tag, in.RequestID, in.Retained = "tag-only", "v1.2.3", "tag-only", c.RetainedDeliveryAuthority{}
	m, err := loadMessage(t.Context(), s.Store.db, in.EvidenceMessageID)
	if err != nil {
		t.Fatal(err)
	}
	in.TagOnly = c.TagOnlyAuthority{ExecutionPolicySHA: tasks.policy, SourcePolicySHA: tasks.task.Binding.PolicySHA, SourceCommitSHA: in.HeadSHA, SourceTreeSHA: strings.Repeat("a", 40), IntegratedCommitSHA: strings.Repeat("b", 40), IntegratedTreeSHA: strings.Repeat("a", 40), OwnerBodySHA: retainedBodySHA(m.Body), PeerBodySHA: strings.Repeat("c", 64), QABodySHA: strings.Repeat("d", 64)}
	in.TagOnly.SourceDeliveryHandoffID, in.TagOnly.SourceDeliveryInputSHA = source.ID, digest(source.Input)
	in.TagOnly.SourceCompletionRequestID = "source-complete"
	in.TagOnly.SourceTreeSHA, in.TagOnly.IntegratedTreeSHA = source.Input.Retained.SourceTreeSHA, source.Input.Retained.SourceTreeSHA
	in.TagOnly.PeerBodySHA, in.TagOnly.QABodySHA = source.Input.Retained.SourcePeerBodySHA, source.Input.Retained.SourceQABodySHA
	in.TagOnly.TagAcceptanceSHA, in.TagOnly.TagEnvironment, in.TagOnly.TagEnvironmentRevision = digest("exact tag verification"), "staging", "staging-v1"
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"hawk", "wren"} {
		body := string(encode(c.TagOnlyReport{Binding: c.TagOnlyReportBinding(in), Result: "ready"}))
		out, e := s.Store.Send(t.Context(), c.Principal{ID: actor}, c.SendMessage{ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: body, RequestID: "tag-report-" + actor})
		if e != nil {
			t.Fatal(e)
		}
		if actor == "hawk" {
			in.TagOnly.EngineeringReportID, in.TagOnly.EngineeringReportBodySHA = out.MessageID, retainedBodySHA(body)
		} else {
			in.TagOnly.QAReportID, in.TagOnly.QAReportBodySHA = out.MessageID, retainedBodySHA(body)
		}
	}
	return s, owner, rook, in, tasks
}

func TestMasterTagOnlyAuthorityAndWithdrawal(t *testing.T) {
	s, owner, rook, in, tasks := masterTagOnlyFixture(t)
	before := digest(tasks.task.Binding)
	if _, err := s.CreateHandoff(t.Context(), rook, in); err == nil {
		t.Fatal("executor self-authorized")
	}
	h, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	validation := c.TagOnlyValidation{Goal: in.Goal, HandoffID: h.ID}
	if got, e := s.ValidateTagOnlyHandoff(t.Context(), rook, validation); e != nil || got != h {
		t.Fatalf("current authority: %+v %v", got, e)
	}
	if _, e := s.ValidateTagOnlyHandoff(t.Context(), owner, validation); e == nil {
		t.Fatal("non-Rook validator")
	}
	if again, e := s.CreateHandoff(t.Context(), owner, in); e != nil || again != h {
		t.Fatalf("idempotency: %+v %v", again, e)
	}
	other := in
	other.RequestID = "duplicate-tag"
	if _, e := s.CreateHandoff(t.Context(), owner, other); e == nil {
		t.Fatal("duplicate tag reserved")
	}
	// Withdrawal remains possible after source policy and task status drift.
	tasks.policy, tasks.task.State = strings.Repeat("f", 64), "open"
	in.Revoked, in.RequestID = true, "withdraw-tag"
	withdrawn, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil || withdrawn.Sequence <= h.Sequence || !withdrawn.Input.Revoked {
		t.Fatalf("withdrawal: %+v %v", withdrawn, err)
	}
	if _, e := s.ValidateTagOnlyHandoff(t.Context(), rook, validation); e == nil {
		t.Fatal("revoked authority validated")
	}
	if digest(tasks.task.Binding) != before || tasks.closes != 1 {
		t.Fatal("tag handoff mutated source")
	}
}

func TestMasterTagOnlyValidationRechecksEvidence(t *testing.T) {
	for _, change := range []string{"policy", "owner", "report", "scope", "rook"} {
		t.Run(change, func(t *testing.T) {
			s, owner, rook, in, tasks := masterTagOnlyFixture(t)
			h, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "policy":
				tasks.policy = strings.Repeat("e", 64)
			case "scope":
				tasks.task.Body += "drift"
			case "owner":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.capabilities',json('[]')) WHERE id=?", owner.ID)
			case "rook":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.github_bot_id',1) WHERE id='rook'")
			case "report":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.body','drift') WHERE id=?", in.TagOnly.QAReportID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ValidateTagOnlyHandoff(t.Context(), rook, c.TagOnlyValidation{Goal: in.Goal, HandoffID: h.ID}); err == nil {
				t.Fatal("stale authority validated")
			}
		})
	}
}

func TestMasterTagOnlyFailsClosed(t *testing.T) {
	for _, change := range []string{"workflow", "policy", "source-policy", "open", "owner-body", "source-tree", "source-commit", "version", "report-body", "report-author", "report-imported", "rook-id", "rook-disabled", "owner-evidence-imported"} {
		t.Run(change, func(t *testing.T) {
			s, owner, _, in, tasks := masterTagOnlyFixture(t)
			var err error
			switch change {
			case "workflow":
				in.Workflow = "release.yml"
			case "policy":
				tasks.policy = strings.Repeat("e", 64)
			case "source-policy":
				tasks.task.Binding.PolicySHA = strings.Repeat("e", 64)
			case "open":
				tasks.task.State = "open"
			case "owner-body":
				in.TagOnly.OwnerBodySHA = strings.Repeat("e", 64)
			case "source-tree":
				in.TagOnly.IntegratedTreeSHA = strings.Repeat("e", 40)
			case "source-commit":
				in.TagOnly.SourceCommitSHA = strings.Repeat("e", 40)
			case "version":
				in.TagOnly.VersionPRURL, in.TagOnly.VersionHeadSHA, in.TagOnly.VersionCommitSHA = "https://github.com/code/repo/pull/2", in.HeadSHA, in.HeadSHA
			case "report-body":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.body','drift') WHERE id=?", in.TagOnly.EngineeringReportID)
			case "report-author":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.author_id','hawk') WHERE id=?", in.TagOnly.QAReportID)
			case "report-imported":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", in.TagOnly.QAReportID)
			case "rook-id":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.github_bot_id',1) WHERE id='rook'")
			case "rook-disabled":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.capabilities',json('[]')) WHERE id='rook'")
			case "owner-evidence-imported":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", in.EvidenceMessageID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateHandoff(t.Context(), owner, in); err == nil {
				t.Fatal("invalid authority admitted")
			}
		})
	}
}
