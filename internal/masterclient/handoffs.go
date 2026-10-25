package masterclient

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) CreateHandoff(ctx context.Context, in c.HandoffInput) (out c.Handoff, err error) {
	err = cl.call(ctx, "POST", "/api/v1/handoffs", in, &out)
	return
}
func (cl *Client) Handoffs(ctx context.Context, in c.HandoffQuery) (out c.HandoffPage, err error) {
	err = cl.call(ctx, "POST", "/api/v1/handoffs/list", in, &out)
	return
}
