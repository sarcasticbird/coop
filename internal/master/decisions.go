package master

import (
	"context"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (svc *Service) DecideGoal(ctx context.Context, actor c.Principal, in c.GoalDecisionInput) (c.GoalAuthority, error) {
	s := svc.Store
	var out c.GoalAuthority
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "decide")
	if err != nil || p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "decision", in, &out); yes || e != nil {
		return out, e
	}
	ref := c.GoalRef{Source: "master", MasterID: s.id, GoalID: in.GoalID, RevisionSHA256: in.RevisionSHA256}
	out, err = s.resolveGoal(ctx, tx, p, ref)
	if err != nil {
		return out, err
	}
	if in.ExpectedDecisionSequence != out.DecisionSequence {
		return out, c.ErrConflict
	}
	state, active, approval := out.State, out.ActiveRevisionSHA256, out.ApprovalSequence
	switch in.Action {
	case "approve":
		if state == "completed" {
			return out, c.ErrConflict
		}
		if err = s.goalMembers(ctx, tx, out.Revision.Proposal); err != nil {
			return out, err
		}
		active = in.RevisionSHA256
		state = "approved"
		approval = out.DecisionSequence + 1
	case "reject":
		if active == in.RevisionSHA256 {
			active = ""
			approval = 0
			if state != "completed" {
				state = "unapproved"
			}
		}
	case "pause":
		if active == "" || state != "approved" || active != in.RevisionSHA256 {
			return out, c.ErrConflict
		}
		state = "paused"
	case "resume":
		if state != "paused" || active != in.RevisionSHA256 {
			return out, c.ErrConflict
		}
		state = "approved"
	case "complete":
		if active == "" || active != in.RevisionSHA256 || state == "completed" {
			return out, c.ErrConflict
		}
		state = "completed"
	case "reopen":
		if state != "completed" {
			return out, c.ErrConflict
		}
		state = "unapproved"
		active = ""
		approval = 0
	default:
		return out, c.ErrInvalid
	}
	seq := out.DecisionSequence + 1
	if _, err = tx.ExecContext(ctx, "UPDATE goals SET state=?,active_sha=?,decision_seq=?,approval_seq=? WHERE id=?", state, active, seq, approval, in.GoalID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO goal_decisions(goal_id,seq,actor,body) VALUES(?,?,?,?)", in.GoalID, seq, p.ID, encode(in)); err != nil {
		return out, err
	}
	out, err = s.resolveGoal(ctx, tx, p, ref)
	if err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "decision", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "goal_decision", out.Revision.Proposal.ThreadID, ""); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
