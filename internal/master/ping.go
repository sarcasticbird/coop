package master

import (
	"context"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) Ping(ctx context.Context, actor c.Principal, in c.Ping) (c.MessageReceipt, error) {
	p, err := authorize(ctx, s.db, actor, "manage")
	if err != nil || p.Kind != "owner" {
		return c.MessageReceipt{}, c.ErrForbidden
	}
	target, err := s.Principal(ctx, in.AgentID)
	if err != nil || target.Kind != "agent" {
		return c.MessageReceipt{}, c.ErrForbidden
	}
	thread, err := s.CreateThread(ctx, p, c.CreateThread{Title: "Direct: " + target.ID, Engagement: in.Engagement, Members: []string{p.ID, target.ID}, RequestID: "dm_" + digest([]string{p.ID, target.ID, in.Engagement})})
	if err != nil {
		return c.MessageReceipt{}, err
	}
	if strings.TrimSpace(in.Body) == "" {
		in.Body = "Please check in with your current status."
	}
	return s.Send(ctx, p, c.SendMessage{ThreadID: thread.ID, Recipients: []string{target.ID}, Body: in.Body, RequestID: in.RequestID})
}
