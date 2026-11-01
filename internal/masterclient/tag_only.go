package masterclient

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) ValidateTagOnlyHandoff(ctx context.Context, in c.TagOnlyValidation) (out c.Handoff, err error) {
	err = cl.call(ctx, "POST", "/api/v1/handoffs/tag-only/validate", in, &out)
	return
}
