package master

import (
	"context"
	"database/sql"
	"fmt"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// stageCanonicalReceipt is called only after the service has authenticated and
// validated the decision/adoption. It shares the authority transaction and uses
// the existing source outbox, so a crash cannot lose a committed receipt.
func (s *Store) stageCanonicalReceipt(ctx context.Context, tx *sql.Tx, thread, body, id string) (c.MessageReceipt, error) {
	var out c.MessageReceipt
	if s.work == nil {
		return out, nil
	}
	source, err := s.conversationSource(ctx, tx, thread)
	if err != nil || source == nil {
		return out, err
	}
	bridge, err := authorize(ctx, tx, c.Principal{ID: s.conversations.BridgePrincipal}, "send")
	if err != nil || bridge.Kind != "service" {
		return out, c.ErrForbidden
	}
	if err = member(ctx, tx, bridge, thread); err != nil {
		return out, err
	}
	input := c.SendMessage{ThreadID: thread, Body: body, Recipients: []string{}, RequestID: id}
	if found, e := request(ctx, tx, bridge.ID, id, "canonical-receipt", input, &out); found || e != nil {
		return out, e
	}
	out, err = s.insertMessage(ctx, tx, bridge, input)
	if err != nil {
		return out, err
	}
	if err = s.stageSource(ctx, tx, bridge, &out, ""); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, bridge.ID, id, "canonical-receipt", input, out); err != nil {
		return out, err
	}
	return out, nil
}
func (s *Store) publishCanonicalReceipt(ctx context.Context, receipt c.MessageReceipt) error {
	if receipt.MessageID == "" {
		return nil
	}
	_, err := s.publishSource(ctx, c.Principal{ID: s.conversations.BridgePrincipal}, receipt)
	return err
}
func (s *Store) stageGoalDecisionReceipt(ctx context.Context, tx *sql.Tx, p c.Principal, in c.GoalDecisionInput, out c.GoalAuthority) (c.MessageReceipt, error) {
	return s.stageCanonicalReceipt(ctx, tx, out.Revision.Proposal.ThreadID, fmt.Sprintf("Authenticated Coop owner %s recorded %s for goal %s at exact revision %s, decision sequence %d, approval sequence %d. This receipt records the Coop decision; native actor labels and approval text do not grant authority.", p.ID, in.Action, in.GoalID, in.RevisionSHA256, out.DecisionSequence, out.ApprovalSequence), "goal-receipt-"+digest(struct{ Actor, Request string }{p.ID, in.RequestID}))
}
