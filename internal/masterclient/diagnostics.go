package masterclient

import (
	"context"
	"errors"
	"net/url"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) ReportDiagnostics(ctx context.Context, report c.WorkerDiagnostics) error {
	if err := c.ValidateDiagnostics(report); err != nil {
		return err
	}
	return cl.diagnosticCall(ctx, "POST", "/api/v1/diagnostics", report, nil)
}

func (cl *Client) AgentDiagnostics(ctx context.Context, agentID string) (out c.AgentDiagnostics, err error) {
	err = cl.diagnosticCall(ctx, "GET", "/api/v1/team/"+url.PathEscape(agentID)+"/diagnostics", nil, &out)
	if err == nil {
		err = c.ValidateDiagnostics(out.Report)
	}
	if err == nil && (out.Report.AgentID != agentID || out.ReceivedAt.IsZero()) {
		err = c.ErrConflict
	}
	return
}

func (cl *Client) diagnosticCall(ctx context.Context, method, path string, in, out any) error {
	err := cl.call(ctx, method, path, in, out)
	if !errors.Is(err, errDiagnosticRoute) {
		return err
	}
	// Older masters do not wrap unknown routes, so their 404 has no pin.
	// Verify the existing identity endpoint before classifying that response.
	if _, err = cl.Self(ctx); err != nil {
		return err
	}
	return c.ErrUnsupported
}
