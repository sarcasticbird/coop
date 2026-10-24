package masterclient

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) TaskCapabilities(ctx context.Context) (out c.TaskCapabilities, err error) {
	err = cl.call(ctx, "GET", "/api/v1/tasks/capabilities", nil, &out)
	return
}
func (cl *Client) TaskRead(ctx context.Context, ref string) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/read", c.TaskReadInput{Reference: ref}, &out)
	return
}
func (cl *Client) TaskList(ctx context.Context, in c.TaskListInput) (out c.ProviderTaskPage, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/list", in, &out)
	return
}
func (cl *Client) TaskCreate(ctx context.Context, in c.TaskCreateInput) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/create", in, &out)
	return
}
func (cl *Client) TaskBind(ctx context.Context, in c.TaskBindInput) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/bind", in, &out)
	return
}
func (cl *Client) TaskComplete(ctx context.Context, in c.TaskCompleteInput) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/complete", in, &out)
	return
}

func (cl *Client) TaskAdopt(ctx context.Context, in c.TaskAdoptInput) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/tasks/adopt", in, &out)
	return
}
