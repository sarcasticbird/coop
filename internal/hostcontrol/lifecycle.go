package hostcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"time"
)

func (c *Control) activity(ctx context.Context, b Binding) (bool, bool) {
	if !b.Workspace.WorkerConfigured || b.Workspace.State != "running" {
		return true, false
	}
	capture := runtime.NewBoundedCapture(64 << 10)
	args := []string{"/usr/local/libexec/coop-worker", "status", "--config", "/var/lib/coop/worker/config.json"}
	if err := b.Runner.Exec(ctx, b.RuntimeName, "/", args, nil, capture, io.Discard); err != nil || capture.Dropped() > 0 {
		return false, false
	}
	var status worker.Status
	if json.Unmarshal(capture.Bytes(), &status) != nil || status.Heartbeat.IsZero() || time.Since(status.Heartbeat) > 2*time.Minute || status.Heartbeat.After(time.Now().Add(time.Minute)) || status.State == "stale" || status.State == "not_running" {
		return false, false
	}
	active := status.ActiveTask != "" || status.SessionID != "" || status.RecoveryIntent != nil || status.SubmissionState != "" || status.Current != nil && (status.Current.Phase == "active" || status.Current.Phase == "recovery")
	return true, active
}

// runtimeIdentity survives an ordinary stop/start. The fallback supports runtime
// backends whose authoritative child identity is stable across that transition.
func runtimeIdentity(b Binding) string {
	if b.Instance != "" {
		return b.Instance
	}
	return b.Incarnation
}
func recordIdentity(r OperationRecord) string {
	if r.RuntimeInstance != "" {
		return r.RuntimeInstance
	}
	return r.Incarnation
}
func lifecycleFailure(err error, attempted bool, category string) (Outcome, error) {
	if errors.Is(err, workspace.ErrMaintenanceRequired) {
		return Outcome{Completed: true, Category: "maintenance_required"}, err
	}
	if !attempted {
		category = "lifecycle_pre_dispatch_failed"
		var coded *Error
		if errors.As(err, &coded) {
			category = coded.Code
		}
		if errors.Is(err, context.Canceled) {
			category = "cancelled_before_dispatch"
		}
		return Outcome{Completed: true, Category: category}, err
	}
	return Outcome{Category: category}, err
}
func (c *Control) Lifecycle(ctx context.Context, b Binding, action string, interrupt bool, checkpoint func(string) error) (Outcome, error) {
	if action != "start" && action != "stop" && action != "restart" {
		return Outcome{Completed: true, Category: "invalid_action"}, fail("invalid_action")
	}
	// A preliminary read provides prompt feedback. The dispatch check repeats this
	// after acquiring the shared lifecycle lock, using a fresh runtime binding.
	if action != "start" && b.Workspace.State == "running" && !interrupt {
		if err := c.checkActivity(ctx, b); err != nil {
			return Outcome{Completed: true, Category: err.(*Error).Code}, err
		}
	}
	if action == "stop" || action == "restart" {
		attempted := false
		check := func() error {
			fresh, err := c.Resolve(ctx, b.Workspace.Ref)
			if err != nil {
				return err
			}
			if !interrupt {
				if err = c.checkActivity(ctx, fresh); err != nil {
					return err
				}
			}
			if err = checkpoint("stop_pending"); err != nil {
				return err
			}
			attempted = true
			return nil
		}
		done := func() error { return checkpoint("stopped") }
		var err error
		if b.Agent != nil {
			err = b.Agent.DownChecked(ctx, check, done)
		} else {
			err = b.Interactive.DownChecked(ctx, check, done)
		}
		if err != nil {
			return lifecycleFailure(err, attempted, "stop_outcome_unknown")
		}
		if action == "stop" {
			return Outcome{Completed: true, TerminationProven: true}, nil
		}
	}
	attempted := false
	check := func() error {
		if err := checkpoint("start_pending"); err != nil {
			return err
		}
		attempted = true
		return nil
	}
	done := func() error { return checkpoint("started") }
	var err error
	if b.Agent != nil {
		err = b.Agent.UpChecked(ctx, nil, check, done)
	} else {
		err = b.Interactive.UpChecked(ctx, check, done)
	}
	if err != nil {
		return lifecycleFailure(err, attempted, "start_outcome_unknown")
	}
	return Outcome{Completed: true}, nil
}
func (c *Control) checkActivity(ctx context.Context, b Binding) error {
	known, active := c.activity(ctx, b)
	if active {
		return fail("active_work_requires_interruption")
	}
	if !known {
		return fail("activity_unknown")
	}
	return nil
}
func (e *Engine) lifecycleCheckpoint(ctx context.Context, caller Caller, original OperationRecord) func(string) error {
	return func(phase string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		selector, err := e.control.selector(original.Input.Workspace)
		if err != nil {
			return err
		}
		if err = e.policy.Authorize(caller, selector, "lifecycle"); err != nil {
			return err
		}
		r, err := e.ledger.Lookup(caller, original.ID)
		if err != nil {
			return err
		}
		b, err := e.control.Resolve(ctx, r.Input.Workspace)
		if err != nil || b.Spec.Fingerprint != r.Fingerprint || b.Workspace.State != "absent" && !b.Workspace.Owned {
			return fail("workspace_changed")
		}
		want, have := recordIdentity(r), runtimeIdentity(b)
		// Only the synchronous successful first creation may establish a new identity.
		firstCreation := phase == "started" && want == "" && r.Phase == "start_pending" && r.Incarnation == ""
		if !firstCreation && (want != have || b.Workspace.State != "absent" && have == "") {
			return fail("workspace_incarnation_changed")
		}
		switch phase {
		case "stop_pending":
			if b.Workspace.State == "absent" {
				return fail("workspace_absent")
			}
			if b.Workspace.State == "running" && (r.Incarnation == "" || b.Incarnation != r.Incarnation) {
				return fail("workspace_incarnation_changed")
			}
		case "stopped":
			if b.Workspace.State != "stopped" {
				return fail("stop_unconfirmed")
			}
		case "start_pending":
			if b.Workspace.State != "absent" && b.Workspace.Fingerprint != r.Fingerprint {
				return fail("maintenance_required")
			}
			if b.Workspace.State == "running" && (r.Incarnation == "" || b.Incarnation != r.Incarnation) {
				return fail("workspace_incarnation_changed")
			}
		case "started":
			if b.Workspace.State != "running" || b.Workspace.Fingerprint != r.Fingerprint || b.Incarnation == "" {
				return fail("start_unconfirmed")
			}
		}
		// The phase and its runtime evidence commit in one record under the VM lock.
		return e.ledger.LifecycleCheckpoint(caller, r.ID, phase, have, b.Incarnation)
	}
}
func (e *Engine) registerLifecycle() {
	for _, action := range []string{"start", "stop", "restart"} {
		e.handlers[action] = func(ctx context.Context, caller Caller, r OperationRecord, b Binding) (Outcome, error) {
			if (r.Input.Action == "stop" || r.Input.Action == "restart") && r.Input.InterruptActive {
				e.mu.Lock()
				for _, other := range e.ledger.all() {
					if other.ID == r.ID || other.Input.Workspace != r.Input.Workspace || other.Input.Action != "exec" || terminal(other.State) {
						continue
					}
					if cancel := e.active[other.ID]; cancel != nil {
						cancel()
					}
				}
				e.mu.Unlock()
			}
			checkpoint := e.lifecycleCheckpoint(ctx, caller, r)
			wrapped := func(phase string) error {
				if err := checkpoint(phase); err != nil {
					return err
				}
				if phase == "stopped" {
					return e.resolveExecHolds(r)
				}
				return nil
			}
			action := r.Input.Action
			if action == "restart" && r.Phase == "stopped" && b.Workspace.State == "stopped" {
				action = "start"
			}
			return e.control.Lifecycle(ctx, b, action, r.Input.InterruptActive, wrapped)
		}
	}
}
func (e *Engine) resolveExecHolds(stopper OperationRecord) error {
	for _, r := range e.ledger.all() {
		if r.Input.Workspace != stopper.Input.Workspace || r.Input.Action != "exec" || terminal(r.State) || r.Incarnation != stopper.Incarnation {
			continue
		}
		if err := e.change(r.ID, func(v *OperationRecord) {
			v.HoldResolved = true
			v.State = "indeterminate"
			v.Outcome = Outcome{Category: "guest_stopped_command_outcome_unknown", TerminationProven: true}
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) reconcileLifecycle(ctx context.Context, r OperationRecord) {
	if r.CancelIntent || r.Input.Action == "exec" {
		return
	}
	caller, err := recordCaller(r)
	if err != nil {
		return
	}
	selector, err := e.control.selector(r.Input.Workspace)
	if err != nil || e.policy.Authorize(caller, selector, "lifecycle") != nil {
		return
	}
	b, err := e.control.Resolve(ctx, r.Input.Workspace)
	if err != nil {
		return
	}
	release, err := lock.AcquireContext(ctx, b.RuntimeName, time.Second)
	if err != nil {
		return
	}
	defer release()
	b, err = e.control.Resolve(ctx, r.Input.Workspace)
	if err != nil || !b.Workspace.Owned || b.Spec.Fingerprint != r.Fingerprint || recordIdentity(r) == "" || runtimeIdentity(b) != recordIdentity(r) {
		return
	}
	switch {
	case r.Phase == "started" && r.PhaseIncarnation != "" && b.Incarnation == r.PhaseIncarnation && b.Workspace.State == "running" && b.Workspace.Fingerprint == r.Fingerprint:
		e.finish(r.ID, Outcome{Completed: true})
	case r.Input.Action == "stop" && r.Phase == "stopped" && b.Workspace.State == "stopped":
		if e.resolveExecHolds(r) == nil {
			e.finish(r.ID, Outcome{Completed: true, TerminationProven: true})
		}
	case r.Input.Action == "restart" && r.Phase == "stopped" && b.Workspace.State == "stopped":
		if e.resolveExecHolds(r) != nil {
			return
		}
		_ = e.change(r.ID, func(v *OperationRecord) {
			if v.State == "indeterminate" && !v.CancelIntent {
				v.State = "accepted"
				v.Dispatched = false
				v.Outcome = Outcome{}
			}
		})
	}
}
