package masterclient

import (
	"context"
	"net/url"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) ContinueTask(ctx context.Context, in c.ContinueTaskRequest) (c.MessageReceipt, error) {
	msg, err := c.ContinuationMessage(in)
	if err != nil {
		return c.MessageReceipt{}, err
	}
	return cl.Send(ctx, msg)
}
func (cl *Client) Continuation(ctx context.Context, id string) (out c.Message, err error) {
	err = cl.call(ctx, "GET", "/api/v1/messages/"+url.PathEscape(id)+"/continuation", nil, &out)
	return
}
