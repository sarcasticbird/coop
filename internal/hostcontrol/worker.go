package hostcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"io"
	"regexp"
	"time"
)

type WorkerStatus struct {
	Enabled       bool      `json:"enabled"`
	State         string    `json:"state"`
	Paused        bool      `json:"paused"`
	Heartbeat     time.Time `json:"heartbeat"`
	WakeEnabled   bool      `json:"wake_enabled"`
	ActivityKnown bool      `json:"activity_known"`
	Active        bool      `json:"active"`
	Recovery      bool      `json:"recovery"`
	CurrentTask   string    `json:"current_task,omitempty"`
	CurrentPhase  string    `json:"current_phase,omitempty"`
}
type WakeSummary struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
}

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_:-]{0,127}$`)

func safeReason(s string) string {
	if s == "" || reasonPattern.MatchString(s) {
		return s
	}
	return "worker_reported_error"
}
func workerArgs(op string) []string {
	return []string{"/usr/local/libexec/coop-worker", op, "--config", "/var/lib/coop/worker/config.json"}
}
func workerJSON(ctx context.Context, b Binding, args []string, input io.Reader, target any) error {
	if b.Agent == nil || !b.Workspace.WorkerConfigured || b.Workspace.State != "running" || !b.Workspace.Owned {
		return fail("worker_unavailable")
	}
	capture := runtime.NewBoundedCapture(64 << 10)
	if err := b.Runner.Exec(ctx, b.RuntimeName, "/", args, input, capture, io.Discard); err != nil {
		return fail("worker_transport_unavailable")
	}
	if capture.Dropped() > 0 {
		return fail("worker_output_too_large")
	}
	decoder := json.NewDecoder(bytes.NewReader(capture.Bytes()))
	if err := decoder.Decode(target); err != nil {
		return fail("invalid_worker_output")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fail("invalid_worker_output")
	}
	return nil
}
func (c *Control) WorkerStatus(ctx context.Context, ref string) (WorkerStatus, error) {
	b, err := c.Resolve(ctx, ref)
	if err != nil {
		return WorkerStatus{}, err
	}
	var status worker.Status
	if err = workerJSON(ctx, b, workerArgs("status"), nil, &status); err != nil {
		return WorkerStatus{}, err
	}
	fresh := !status.Heartbeat.IsZero() && time.Since(status.Heartbeat) <= 2*time.Minute && !status.Heartbeat.After(time.Now().Add(time.Minute)) && status.State != "stale" && status.State != "not_running"
	out := WorkerStatus{Enabled: status.Enabled, State: safeReason(status.State), Paused: status.Paused, Heartbeat: status.Heartbeat, WakeEnabled: status.WakeEnabled, ActivityKnown: fresh, Recovery: status.RecoveryIntent != nil}
	out.Active = status.ActiveTask != "" || status.SessionID != "" || status.SubmissionState != "" || out.Recovery
	if status.Current != nil {
		out.CurrentTask = status.Current.TaskURL
		out.CurrentPhase = safeReason(status.Current.Phase)
		out.Active = out.Active || status.Current.Phase == "active" || status.Current.Phase == "recovery"
	}
	if !fresh {
		out.State = "stale"
	}
	return out, nil
}
func (c *Control) WorkerRequests(ctx context.Context, ref, wakeID string) ([]WakeSummary, error) {
	if wakeID != "" && worker.ValidatePingInput(worker.PingInput{RequestID: wakeID}) != nil {
		return nil, fail("invalid_wake_id")
	}
	b, err := c.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	args := workerArgs("requests")
	if wakeID != "" {
		args = append(args, "--request-id", wakeID)
	}
	var raw []worker.WakeSummary
	if err = workerJSON(ctx, b, args, nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) > 20 {
		return nil, fail("invalid_worker_output")
	}
	out := make([]WakeSummary, 0, len(raw))
	for _, v := range raw {
		if wakeID != "" && v.ID != wakeID {
			return nil, fail("wake_identity_mismatch")
		}
		if worker.ValidatePingInput(worker.PingInput{RequestID: v.ID}) != nil || v.ID == "" {
			return nil, fail("invalid_worker_output")
		}
		out = append(out, WakeSummary{ID: v.ID, Kind: safeReason(v.Kind), Phase: safeReason(v.Phase), Reason: safeReason(v.Reason)})
	}
	return out, nil
}
func (e *Engine) registerWorker() {
	for _, action := range []string{"ping", "pause", "resume"} {
		e.handlers[action] = func(ctx context.Context, caller Caller, r OperationRecord, b Binding) (Outcome, error) {
			release, err := lock.AcquireContext(ctx, b.RuntimeName, 30*time.Second)
			if err != nil {
				return Outcome{Completed: true, Category: "workspace_busy"}, err
			}
			defer release()
			b, err = e.control.Resolve(ctx, r.Input.Workspace)
			if err != nil || b.Spec.Fingerprint != r.Fingerprint || !b.Workspace.Owned {
				return Outcome{Completed: true, Category: "workspace_changed"}, fail("workspace_changed")
			}
			if err = e.policy.Authorize(caller, b.Selector, capability(r.Input.Action)); err != nil {
				return Outcome{Completed: true, Category: "forbidden"}, err
			}
			var input io.Reader
			if r.Input.Action == "ping" {
				status, err := e.control.WorkerStatus(ctx, r.Input.Workspace)
				if err != nil || !status.ActivityKnown || !status.Enabled || !status.WakeEnabled {
					return Outcome{Completed: true, Category: "wake_unavailable"}, fail("wake_unavailable")
				}
				value := worker.PingInput{RequestID: r.WakeID, Prompt: r.Input.Prompt}
				if worker.ValidatePingInput(value) != nil {
					return Outcome{Completed: true, Category: "invalid_ping"}, fail("invalid_ping")
				}
				raw, err := json.Marshal(value)
				if err != nil {
					return Outcome{Completed: true, Category: "invalid_ping"}, err
				}
				input = bytes.NewReader(raw)
			}
			if err = e.ledger.Checkpoint(caller, r.ID, "worker_dispatch_pending"); err != nil {
				return Outcome{Completed: true, Category: "cancelled_before_dispatch"}, err
			}
			if r.Input.Action == "ping" {
				var summary worker.WakeSummary
				err = workerJSON(ctx, b, workerArgs("ping"), input, &summary)
				if err != nil {
					summaries, queryErr := e.control.WorkerRequests(ctx, r.Input.Workspace, r.WakeID)
					if queryErr != nil || len(summaries) != 1 {
						return Outcome{Category: "wake_acknowledgement_unknown"}, err
					}
					summary = worker.WakeSummary{ID: summaries[0].ID, Phase: summaries[0].Phase}
				}
				if summary.ID != r.WakeID || summary.Phase == "" {
					return Outcome{Category: "wake_acknowledgement_unknown"}, fail("wake_identity_mismatch")
				}
				if err = e.change(r.ID, func(v *OperationRecord) { v.WakePhase = safeReason(summary.Phase); v.Phase = "wake_accepted" }); err != nil {
					return Outcome{Category: "wake_checkpoint_failed"}, err
				}
				return Outcome{Completed: true}, nil
			}
			var result struct {
				Paused bool   `json:"paused"`
				State  string `json:"state"`
			}
			err = workerJSON(ctx, b, workerArgs(r.Input.Action), nil, &result)
			if err != nil {
				status, statusErr := e.control.WorkerStatus(ctx, r.Input.Workspace)
				if statusErr != nil || !status.ActivityKnown || status.Paused != (r.Input.Action == "pause") {
					return Outcome{Category: "worker_control_outcome_unknown"}, err
				}
			}
			if err = e.change(r.ID, func(v *OperationRecord) { v.Phase = r.Input.Action + "_requested" }); err != nil {
				return Outcome{Category: "worker_checkpoint_failed"}, err
			}
			return Outcome{Completed: true}, nil
		}
	}
}
func (e *Engine) reconcileWorker(ctx context.Context, r OperationRecord) {
	caller, err := recordCaller(r)
	if err != nil {
		return
	}
	selector, err := e.control.selector(r.Input.Workspace)
	if err != nil || e.policy.Authorize(caller, selector, capability(r.Input.Action)) != nil {
		return
	}
	if r.Input.Action == "ping" && r.WakeID != "" {
		summaries, err := e.control.WorkerRequests(ctx, r.Input.Workspace, r.WakeID)
		if err == nil && len(summaries) == 1 {
			if err = e.change(r.ID, func(v *OperationRecord) { v.WakePhase = summaries[0].Phase }); err == nil {
				e.finish(r.ID, Outcome{Completed: true})
			}
		}
	} else if r.Input.Action == "pause" || r.Input.Action == "resume" {
		s, err := e.control.WorkerStatus(ctx, r.Input.Workspace)
		if err == nil && s.ActivityKnown && s.Paused == (r.Input.Action == "pause") {
			e.finish(r.ID, Outcome{Completed: true})
		}
	}
}
