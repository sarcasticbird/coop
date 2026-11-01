package master

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestDeliveryWithdrawalWhileAuthorityDisabled(t *testing.T) {
	for _, mode := range []string{"paused", "rook-disabled", "rejected", "superseded", "stale-assignment"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, _, rook, in := deliveryFixture(t)
			grant, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal(err)
			}
			a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			backend := s.Tasks.(*testTasks)
			originalTask := backend.task
			switch mode {
			case "paused", "rejected":
				action := "pause"
				if mode == "rejected" {
					action = "reject"
				}
				a, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: action, RequestID: "disable-goal"})
			case "rook-disabled":
				disabled := rook
				disabled.Capabilities = nil
				err = s.Store.Configure(t.Context(), []c.Principal{disabled}, nil)
			case "superseded":
				proposal := a.Revision.Proposal
				proposal.Purpose, proposal.RequestID = "Revised approved purpose", "supersede-goal"
				revision, e := s.ProposeGoal(t.Context(), owner, proposal)
				if e != nil {
					t.Fatal(e)
				}
				_, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "approve-replacement"})
			case "stale-assignment":
				backend.task.Body = "Changed task after the recorded grant"
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE kind='task'", strings.Repeat("f", 64))
			}
			if err != nil {
				t.Fatal(err)
			}
			newGrant := in
			newGrant.RequestID = "must-not-grant-while-disabled"
			if _, err = s.CreateHandoff(t.Context(), owner, newGrant); err == nil {
				t.Fatal("disabled path issued a new grant")
			}
			// Withdrawal must not need a current task-provider read at all.
			s.Tasks = nil
			withdrawal := in
			withdrawal.Revoked, withdrawal.RequestID = true, "withdraw-recorded-grant"
			revocation, err := s.CreateHandoff(t.Context(), owner, withdrawal)
			if err != nil || revocation.Sequence <= grant.Sequence || !revocation.Input.Revoked {
				t.Fatalf("disabled authority vetoed withdrawal: %+v %v", revocation, err)
			}
			retry, err := s.CreateHandoff(t.Context(), owner, withdrawal)
			if err != nil || retry != revocation {
				t.Fatalf("withdrawal replay changed history: %+v %v", retry, err)
			}
			var raw []byte
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT body FROM handoffs WHERE id=?", grant.ID).Scan(&raw); err != nil || string(raw) != string(encode(grant)) {
				t.Fatal("withdrawal changed original grant", err)
			}
			var count int
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM handoffs WHERE goal_id=? AND revision=?", in.Goal.GoalID, in.Goal.RevisionSHA256).Scan(&count); err != nil || count != 2 {
				t.Fatalf("withdrawal retry appended history: count=%d err=%v", count, err)
			}
			s.Tasks = backend
			switch mode {
			case "paused":
				_, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "resume", RequestID: "resume-after-withdrawal"})
			case "rook-disabled":
				err = s.Store.Configure(t.Context(), []c.Principal{rook}, nil)
			case "stale-assignment":
				backend.task = originalTask
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE kind='task'", in.AssignmentSHA)
			}
			if err != nil {
				t.Fatal(err)
			}
			complete := c.TaskCompleteInput{Reference: in.TaskURL, Goal: in.Goal, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: grant.ID, RequestID: "complete-withdrawn-grant"}
			_, err = s.TaskComplete(t.Context(), rook, complete)
			if err == nil || backend.closes != 0 {
				t.Fatalf("withdrawn grant revived: closes=%d err=%v", backend.closes, err)
			}
			if mode == "paused" || mode == "rook-disabled" || mode == "stale-assignment" {
				if !errors.Is(err, c.ErrConflict) {
					t.Fatal("restored authority was not blocked by withdrawal", err)
				}
			}
		})
	}
}

func TestDeliveryWithdrawalRequiresExactRecordedInput(t *testing.T) {
	s, owner, _, _, in := deliveryFixture(t)
	withdrawal := in
	withdrawal.Revoked, withdrawal.RequestID = true, "withdraw-exact-input"
	if _, err := s.CreateHandoff(t.Context(), owner, withdrawal); !errors.Is(err, c.ErrConflict) {
		t.Fatal("fabricated withdrawal without a grant", err)
	}
	if _, err := s.CreateHandoff(t.Context(), owner, in); err != nil {
		t.Fatal(err)
	}
	// Mutate every binding field independently, retaining valid hash syntax.
	for i := 0; i < reflect.TypeFor[c.HandoffInput]().NumField(); i++ {
		field := reflect.TypeFor[c.HandoffInput]().Field(i)
		if field.Name == "RequestID" || field.Name == "Revoked" || field.Name == "Kind" {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			changed := withdrawal
			value := reflect.ValueOf(&changed).Elem().Field(i)
			switch value.Kind() {
			case reflect.String:
				text := value.String()
				if strings.Contains(field.Name, "SHA") || field.Name == "PRScopeRevision" {
					text = strings.Repeat("f", len(text))
				} else {
					text += "-changed"
				}
				value.SetString(text)
			case reflect.Int64:
				value.SetInt(value.Int() + 1)
			case reflect.Struct:
				changed.Goal.RevisionSHA256 = strings.Repeat("f", 64)
			default:
				t.Fatalf("unhandled input field %s", field.Name)
			}
			if _, err := s.CreateHandoff(t.Context(), owner, changed); err == nil {
				t.Fatal("mismatched recorded input withdrawn")
			}
		})
	}
	revocation, err := s.CreateHandoff(t.Context(), owner, withdrawal)
	if err != nil {
		t.Fatal("failed withdrawals poisoned request journal", err)
	}
	// Even a payload matching another recorded grant cannot replace a saved
	// withdrawal's request identity.
	other := in
	other.HeadSHA, other.RequestID = strings.Repeat("f", 40), "other-recorded-grant"
	if _, err = s.CreateHandoff(t.Context(), owner, other); err != nil {
		t.Fatal(err)
	}
	other.Revoked, other.RequestID = true, withdrawal.RequestID
	if _, err = s.CreateHandoff(t.Context(), owner, other); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed withdrawal journal payload accepted", err)
	}
	if retry, e := s.CreateHandoff(t.Context(), owner, withdrawal); e != nil || retry != revocation {
		t.Fatalf("original withdrawal retry changed: %+v %v", retry, e)
	}
}

func TestDeliveryWithdrawalRequiresLiveOwnerAuthority(t *testing.T) {
	for _, mode := range []string{"agent", "no-read", "no-decide", "source-membership", "qa-membership", "admission"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, planner, _, in := deliveryFixture(t)
			if _, err := s.CreateHandoff(t.Context(), owner, in); err != nil {
				t.Fatal(err)
			}
			a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			actor := owner
			switch mode {
			case "agent":
				actor = planner
				actor.Capabilities = append(actor.Capabilities, "decide")
				err = s.Store.Configure(t.Context(), []c.Principal{actor}, nil)
			case "no-read":
				actor.Capabilities = []string{"decide"}
				err = s.Store.Configure(t.Context(), []c.Principal{actor}, nil)
			case "no-decide":
				actor.Capabilities = []string{"read"}
				err = s.Store.Configure(t.Context(), []c.Principal{actor}, nil)
			case "source-membership", "qa-membership":
				thread := a.Revision.Proposal.ThreadID
				if mode == "qa-membership" {
					thread = a.Revision.Proposal.QA.EvidenceDestination.ThreadID
				}
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM members WHERE thread_id=? AND principal_id=?", thread, owner.ID)
			case "admission":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE meta SET value='1' WHERE key='quiescent'")
			}
			if err != nil {
				t.Fatal(err)
			}
			in.Revoked, in.RequestID = true, "unauthorized-withdrawal"
			want := c.ErrForbidden
			if mode == "admission" {
				want = c.ErrEpoch
			}
			if _, err = s.CreateHandoff(t.Context(), actor, in); !errors.Is(err, want) {
				t.Fatalf("withdrawal authorization: got %v, want %v", err, want)
			}
			var count int
			if err = s.Store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM requests WHERE request_id=?", in.RequestID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied withdrawal reserved journal: count=%d err=%v", count, err)
			}
		})
	}
}
