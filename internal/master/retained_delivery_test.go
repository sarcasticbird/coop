package master

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type retainedTasks struct {
	*testTasks
	policy string
}

func (b *retainedTasks) Capabilities() c.TaskCapabilities {
	out := b.testTasks.Capabilities()
	out.PolicySHA = b.policy
	return out
}

func retainedFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.HandoffInput, *retainedTasks) {
	t.Helper()
	s, owner, _, rook, in := deliveryFixture(t)
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"hawk", "wren"} {
		p := c.Principal{ID: id, Alias: id, Kind: "agent", GitHubBotID: int64(100 + i), Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
		if err = s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
			t.Fatal(err)
		}
		for _, threadID := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
			thread, e := s.Store.Thread(t.Context(), owner, threadID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: threadID, ExpectedMembers: thread.Members, Members: append(thread.Members, id), RequestID: id + threadID}); e != nil {
				t.Fatal(e)
			}
		}
	}
	b := &retainedTasks{testTasks: s.Tasks.(*testTasks), policy: strings.Repeat("c", 64)}
	s.Tasks = b
	in.Kind, in.RequestID = "retained-gates", "retained-gates"
	in.Retained = c.RetainedDeliveryAuthority{SourcePolicySHA: b.task.Binding.PolicySHA, ExecutionPolicySHA: b.policy, SourceTreeSHA: strings.Repeat("d", 40), PayloadSHA: strings.Repeat("e", 64), SourcePeerReviewID: in.PeerReviewID, SourcePeerBodySHA: strings.Repeat("f", 64), SourceQAReviewID: in.QAReviewID, SourceQABodySHA: strings.Repeat("f", 64), SourceReportID: in.QAEvidenceMessageID, SourceReportBodySHA: in.QAEvidenceBodySHA, SourceEnvironment: in.Environment, SourceEnvironmentRevision: in.EnvironmentRevision, DeliveryAcceptanceSHA: strings.Repeat("a", 64), Override: c.RetainedDeliveryOverride}
	return s, owner, rook, in, b
}

func retainedReports(t *testing.T, s *Service, owner c.Principal, gates c.Handoff) c.HandoffInput {
	t.Helper()
	in := gates.Input
	in.Kind, in.RequestID = "retained-deliver", "retained-deliver"
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hawk", "wren"} {
		body := string(encode(c.RetainedGateReport{GateHandoffID: gates.ID, AssignmentSHA: in.AssignmentSHA, Binding: gates.Input, Result: "ready"}))
		m, e := s.Store.Send(t.Context(), c.Principal{ID: id}, c.SendMessage{ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: body, RequestID: "report-" + id})
		if e != nil {
			t.Fatal(e)
		}
		if id == "hawk" {
			in.Retained.EngineeringReportID, in.Retained.EngineeringReportBodySHA = m.MessageID, retainedBodySHA(body)
		} else {
			in.Retained.QAReportID, in.Retained.QAReportBodySHA = m.MessageID, retainedBodySHA(body)
		}
	}
	return in
}

func TestRetainedDeliveryPreservesSourceBinding(t *testing.T) {
	s, owner, rook, in, b := retainedFixture(t)
	before := digest(b.task.Binding)
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	// Readiness is assigned to today's gate actors, not the disabled source roster.
	for _, id := range []string{a.Revision.Proposal.AuthorID, a.Revision.Proposal.PeerID, a.Revision.Proposal.QA.ActorID} {
		p, e := principal(t.Context(), s.Store.db, id)
		if e != nil {
			t.Fatal(e)
		}
		p.Capabilities = nil
		if e = s.Store.Configure(t.Context(), []c.Principal{p}, nil); e != nil {
			t.Fatal(e)
		}
	}
	if _, err := s.TaskRead(t.Context(), rook, c.TaskReadInput{Reference: in.TaskURL}); err != nil {
		t.Fatal(err)
	}
	gates, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	in = retainedReports(t, s, owner, gates)
	grant, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if retry, e := s.CreateHandoff(t.Context(), owner, in); e != nil || retry != grant {
		t.Fatalf("retry: %+v %v", retry, e)
	}
	if got, e := s.ValidateRetainedDeliveryHandoff(t.Context(), rook, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); e != nil || got != grant {
		t.Fatalf("disabled source roster prevented validation: %+v %v", got, e)
	}
	complete := c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "retained-complete"}
	if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil {
		t.Fatal(err)
	}
	if digest(b.task.Binding) != before || b.binds != 1 || b.closes != 1 {
		t.Fatal("retained delivery changed source binding or failed completion")
	}
	// Normal completion still cannot adopt the current policy implicitly.
	normal := in
	normal.Kind, normal.RequestID, normal.Retained = "deliver", "normal-after-retained", c.RetainedDeliveryAuthority{}
	h, err := s.CreateHandoff(t.Context(), owner, normal)
	if err != nil {
		t.Fatal(err)
	}
	complete.DeliveryHandoffID, complete.RequestID = h.ID, "normal-complete"
	if _, err = s.TaskComplete(t.Context(), rook, complete); err == nil {
		t.Fatal("normal completion migrated policy")
	}
}

func TestRetainedAuthorityValidation(t *testing.T) {
	s, owner, rook, in, _ := retainedFixture(t)
	if _, err := s.CreateHandoff(t.Context(), rook, in); err == nil {
		t.Fatal("executor assigned its own gates")
	}
	for name, mutate := range map[string]func(*c.HandoffInput){
		"override":                    func(h *c.HandoffInput) { h.Retained.Override = "allow" },
		"source-policy":               func(h *c.HandoffInput) { h.Retained.SourcePolicySHA = "bad" },
		"execution-policy":            func(h *c.HandoffInput) { h.Retained.ExecutionPolicySHA = strings.Repeat("f", 64) },
		"source-report":               func(h *c.HandoffInput) { h.Retained.SourceReportID = "different" },
		"source-environment":          func(h *c.HandoffInput) { h.Retained.SourceEnvironmentRevision = "different" },
		"source-peer":                 func(h *c.HandoffInput) { h.Retained.SourcePeerReviewID = "different" },
		"source-qa":                   func(h *c.HandoffInput) { h.Retained.SourceQAReviewID = "different" },
		"missing-current-environment": func(h *c.HandoffInput) { h.Environment = "" },
		"premature-report":            func(h *c.HandoffInput) { h.Retained.EngineeringReportID = "report" },
		"missing-gate-reports":        func(h *c.HandoffInput) { h.Kind = "retained-deliver" },
		"normal-evidence-migration":   func(h *c.HandoffInput) { h.Kind = "deliver" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in
			mutate(&bad)
			if _, err := s.CreateHandoff(t.Context(), owner, bad); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var decoded c.HandoffInput
	if err = c.DecodeJSON(raw, &decoded); err != nil || decoded != in {
		t.Fatalf("value schema round trip: %v", err)
	}
	if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
		t.Fatal("rejected requests poisoned retry journal", err)
	}
}

func TestRetainedDeliveryDriftAndRevocation(t *testing.T) {
	for _, change := range []string{"policy", "source-policy", "source-scope", "report-body", "report-author", "report-imported", "gate-revoked", "gate-replaced", "delivery-revoked", "source-author", "gate-owner", "owner-evidence", "task-registration", "pr-registration", "paused", "superseded", "gate-capability", "gate-membership"} {
		t.Run(change, func(t *testing.T) {
			s, owner, rook, gatesInput, b := retainedFixture(t)
			gates, err := s.CreateHandoff(t.Context(), owner, gatesInput)
			if err != nil {
				t.Fatal(err)
			}
			in := retainedReports(t, s, owner, gates)
			grant, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "policy":
				b.policy = strings.Repeat("f", 64)
			case "source-policy":
				b.task.Binding.PolicySHA = b.policy
			case "source-scope":
				b.task.Body += "drift"
			case "report-body":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.body','drift') WHERE id=?", in.Retained.QAReportID)
			case "report-author":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.author_id','hawk') WHERE id=?", in.Retained.QAReportID)
			case "report-imported":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", in.Retained.EngineeringReportID)
			case "gate-revoked", "gate-replaced":
				gatesInput.Revoked, gatesInput.RequestID = change == "gate-revoked", "latest-gates"
				_, err = s.CreateHandoff(t.Context(), owner, gatesInput)
			case "delivery-revoked":
				in.Revoked, in.RequestID = true, "withdraw-delivery"
				_, err = s.CreateHandoff(t.Context(), owner, in)
			case "source-author":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.github_bot_id',?) WHERE id='hawk'", b.task.Binding.AuthorID)
			case "gate-owner":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE handoffs SET body=json_set(body,'$.actor_id','hawk') WHERE id=?", gates.ID)
			case "owner-evidence":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.author_id','hawk') WHERE id=?", in.EvidenceMessageID)
			case "task-registration":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM artifacts WHERE kind='task'")
			case "pr-registration":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET body=json_set(body,'$.task_url','other') WHERE kind='pull_request'")
			case "paused":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE goals SET state='paused' WHERE id=?", in.Goal.GoalID)
			case "superseded":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE goals SET active_sha='superseded' WHERE id=?", in.Goal.GoalID)
			case "gate-capability":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE principals SET body=json_set(body,'$.capabilities',json('[]')) WHERE id='hawk'")
			case "gate-membership":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE principal_id='wren'")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), rook, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); err == nil {
				t.Fatal("drift passed pre-effect validation")
			}
			complete := c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "retained-complete"}
			if _, err = s.TaskComplete(t.Context(), rook, complete); err == nil || b.closes != 0 {
				t.Fatalf("drift reached completion: closes=%d err=%v", b.closes, err)
			}
		})
	}
}

func TestRetainedValidationOwnerDecideRevocation(t *testing.T) {
	for _, revoked := range []string{"gate-owner", "delivery-owner", "gate-evidence", "delivery-evidence"} {
		t.Run(revoked, func(t *testing.T) {
			s, owner, rook, in, b := retainedFixture(t)
			a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{"gate-owner", "delivery-owner", "gate-evidence", "delivery-evidence"}
			for _, id := range ids {
				p := owner
				p.ID, p.Alias = id, id
				if err = s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
					t.Fatal(err)
				}
			}
			for _, threadID := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
				thread, e := s.Store.Thread(t.Context(), owner, threadID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.Store.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: threadID, ExpectedMembers: thread.Members, Members: append(thread.Members, ids...), RequestID: "owners-" + threadID}); e != nil {
					t.Fatal(e)
				}
			}
			gateEvidence, err := s.Store.Send(t.Context(), c.Principal{ID: "gate-evidence"}, c.SendMessage{ThreadID: a.Revision.Proposal.ThreadID, Body: "Authorize retained gates", RequestID: "gate-evidence"})
			if err != nil {
				t.Fatal(err)
			}
			in.EvidenceMessageID = gateEvidence.MessageID
			gates, err := s.CreateHandoff(t.Context(), c.Principal{ID: "gate-owner"}, in)
			if err != nil {
				t.Fatal(err)
			}
			in = retainedReports(t, s, owner, gates)
			deliveryEvidence, err := s.Store.Send(t.Context(), c.Principal{ID: "delivery-evidence"}, c.SendMessage{ThreadID: a.Revision.Proposal.ThreadID, Body: "Authorize retained delivery", RequestID: "delivery-evidence"})
			if err != nil {
				t.Fatal(err)
			}
			in.EvidenceMessageID = deliveryEvidence.MessageID
			grant, err := s.CreateHandoff(t.Context(), c.Principal{ID: "delivery-owner"}, in)
			if err != nil {
				t.Fatal(err)
			}
			validation := c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}
			if got, e := s.ValidateRetainedDeliveryHandoff(t.Context(), rook, validation); e != nil || got != grant {
				t.Fatalf("initial validation: %+v %v", got, e)
			}
			p, err := principal(t.Context(), s.Store.db, revoked)
			if err != nil {
				t.Fatal(err)
			}
			p.Capabilities = []string{"read", "send"}
			if err = s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), rook, validation); !errors.Is(err, c.ErrForbidden) || b.closes != 0 {
				t.Fatalf("decide revocation passed pre-effect validation: %v closes=%d", err, b.closes)
			}
			if _, err = s.Store.Message(t.Context(), rook, in.Retained.SourceReportID); err != nil {
				t.Fatal("historical source QA became unreadable", err)
			}
			if _, err = s.TaskComplete(t.Context(), rook, c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "revoked-complete"}); !errors.Is(err, c.ErrForbidden) || b.closes != 0 {
				t.Fatalf("decide revocation passed completion: %v closes=%d", err, b.closes)
			}
		})
	}
}

func TestRetainedCurrentEnvironmentApproval(t *testing.T) {
	s, owner, rook, in, b := retainedFixture(t)
	historical := in.Retained
	in.Environment, in.EnvironmentRevision = "current staging", "v2"
	if _, err := s.CreateHandoff(t.Context(), owner, in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("unapproved current-only environment drift accepted", err)
	}
	reassessment := c.HandoffInput{Kind: "reassess-qa", Goal: in.Goal, TaskURL: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PRURL: in.PRURL, HeadSHA: in.HeadSHA, ArtifactSHA: in.ArtifactSHA, Environment: in.Environment, EnvironmentRevision: in.EnvironmentRevision, ReviewID: "previous-qa-review", EvidenceMessageID: in.EvidenceMessageID, RequestID: "approve-current-environment"}
	if _, err := s.CreateHandoff(t.Context(), owner, reassessment); err != nil {
		t.Fatal(err)
	}
	gates, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal("approved current environment rejected", err)
	}
	if gates.Input.Retained != historical {
		t.Fatal("current environment relabeled historical assertions")
	}
	for _, id := range []string{"hawk", "wren"} {
		if got, e := s.ValidateRetainedDeliveryHandoff(t.Context(), c.Principal{ID: id}, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: gates.ID}); e != nil || got != gates {
			t.Fatalf("gate validation for %s: %+v %v", id, got, e)
		}
	}
	in = retainedReports(t, s, owner, gates)
	grant, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), rook, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); err != nil {
		t.Fatal(err)
	}
	complete := c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "approved-environment-complete"}
	if _, err = s.TaskComplete(t.Context(), rook, complete); err != nil || b.closes != 1 {
		t.Fatalf("approved current environment failed completion: %v closes=%d", err, b.closes)
	}
	// The current environment decision is revalidated, never cached in a grant.
	if _, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM handoffs WHERE json_extract(body,'$.input.kind')='reassess-qa'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), rook, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("withdrawn current environment approval accepted", err)
	}
	if _, err = s.TaskComplete(t.Context(), rook, complete); !errors.Is(err, c.ErrForbidden) || b.closes != 1 {
		t.Fatalf("completion recovery bypassed current environment validation: %v closes=%d", err, b.closes)
	}
}

func TestRetainedValidationExecutorAndLatestDecision(t *testing.T) {
	s, owner, rook, in, _ := retainedFixture(t)
	gates, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []c.Principal{owner, rook} {
		if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), p, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: gates.ID}); !errors.Is(err, c.ErrForbidden) {
			t.Fatal("non-gate executor accepted", err)
		}
	}
	in = retainedReports(t, s, owner, gates)
	grant, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{owner.ID, "hawk", "wren"} {
		if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), c.Principal{ID: id}, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); !errors.Is(err, c.ErrForbidden) {
			t.Fatal("non-delivery executor accepted", err)
		}
	}
	// A newer ordinary decision supersedes retained delivery before any scope
	// checks, including when the newer ordinary decision is then withdrawn.
	in.Kind, in.RequestID, in.Retained = "deliver", "ordinary-latest", c.RetainedDeliveryAuthority{}
	if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
		t.Fatal(err)
	}
	for _, revoke := range []bool{false, true} {
		if revoke {
			in.Revoked, in.RequestID = true, "ordinary-withdrawn"
			if _, err = s.CreateHandoff(t.Context(), owner, in); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.ValidateRetainedDeliveryHandoff(t.Context(), rook, c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID}); !errors.Is(err, c.ErrConflict) {
			t.Fatal("fell back to old retained grant", err)
		}
	}
}

func TestRetainedValidationAuthenticatedReadOnlyEndpoint(t *testing.T) {
	s, owner, rook, in, b := retainedFixture(t)
	gates, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	in = retainedReports(t, s, owner, gates)
	grant, err := s.CreateHandoff(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Configure(t.Context(), nil, []c.Placement{{AgentID: rook.ID, HostID: "host", InstanceID: "instance", Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if e := s.Store.db.QueryRowContext(t.Context(), "SELECT (SELECT count(*) FROM requests)+(SELECT count(*) FROM handoffs)").Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	before, binds := count(), b.binds
	h := NewHandler(s, policyFixture(owner, rook))
	for _, tc := range []struct {
		token string
		want  int
	}{{"wrong-token", 401}, {"owner-test-token", 403}, {"agent-test-token", 200}, {"agent-test-token", 200}} {
		req := httptest.NewRequest("POST", "http://example.com/api/v1/handoffs/retained-delivery/validate", bytes.NewReader(encode(c.RetainedDeliveryValidation{Goal: in.Goal, HandoffID: grant.ID})))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Coop-Expected-Master-ID", s.Store.id)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("validation status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
		}
	}
	if count() != before || b.binds != binds || b.closes != 0 {
		t.Fatal("validation created a grant, receipt, binding or completion effect")
	}
}

func TestRetainedRevocationIndependentOfEligibility(t *testing.T) {
	for _, kind := range []string{"retained-gates", "retained-deliver"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, rook, in, b := retainedFixture(t)
			gates, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			grant := gates
			if kind == "retained-deliver" {
				in = retainedReports(t, s, owner, gates)
				grant, err = s.CreateHandoff(t.Context(), owner, in)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.Store.db.ExecContext(context.Background(), "UPDATE goals SET state='paused', active_sha='superseded' WHERE id=?", in.Goal.GoalID)
			if err != nil {
				t.Fatal(err)
			}
			rook.Capabilities = nil
			if err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil); err != nil {
				t.Fatal(err)
			}
			b.policy = strings.Repeat("f", 64)
			s.Store.now = func() time.Time { return grant.CreatedAt.Add(-time.Hour) }
			in.Revoked, in.RequestID = true, "withdraw-retained"
			revoked, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil || revoked.Sequence <= grant.Sequence {
				t.Fatalf("withdrawal: %+v %v", revoked, err)
			}
			if retry, e := s.CreateHandoff(t.Context(), owner, in); e != nil || retry != revoked {
				t.Fatalf("withdrawal retry: %+v %v", retry, e)
			}
		})
	}
}
