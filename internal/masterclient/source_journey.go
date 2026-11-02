package masterclient

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) CreateSourceDecision(ctx context.Context, in c.SourceDecisionInput) (out c.SourceDecision, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions", in, &out)
	return
}
func (cl *Client) SourceDecisions(ctx context.Context, in c.SourceDecisionQuery) (out c.SourceDecisionPage, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/list", in, &out)
	return
}
func (cl *Client) ValidateSourceDecision(ctx context.Context, in c.SourceDecisionValidation) (out c.SourceDecision, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/validate", in, &out)
	return
}
func (cl *Client) RecordSourceEffect(ctx context.Context, in c.SourceEffectReceipt) (out c.SourceEffectReceipt, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/effect", in, &out)
	return
}
func (cl *Client) SourceEffects(ctx context.Context, in c.SourceDecisionValidation) (out []c.SourceEffectReceipt, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/effects", in, &out)
	return
}
func (cl *Client) CompleteSourceTask(ctx context.Context, in c.SourceCompletionInput) (out c.ProviderTask, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/complete", in, &out)
	return
}

func (cl *Client) SourceTaskState(ctx context.Context, in c.SourceDecisionValidation) (out c.SourceTaskState, err error) {
	err = cl.call(ctx, "POST", "/api/v1/source-decisions/task-state", in, &out)
	return
}
