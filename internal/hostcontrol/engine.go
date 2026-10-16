package hostcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ActionHandler func(context.Context, Caller, OperationRecord, Binding) (Outcome, error)
type Engine struct {
	host              Host
	ledger            *Ledger
	policy            *Policy
	control           *Control
	mu                sync.Mutex
	active            map[string]context.CancelFunc
	handlers          map[string]ActionHandler
	running           bool
	notify            chan struct{}
	retentionInterval time.Duration
	reconcileCursor   int
}

func NewEngine(host Host, ledger *Ledger, policy *Policy, control *Control) (*Engine, error) {
	e := &Engine{host: host, ledger: ledger, policy: policy, control: control, active: map[string]context.CancelFunc{}, handlers: map[string]ActionHandler{}, notify: make(chan struct{}, 1)}
	for _, r := range ledger.all() {
		if terminal(r.State) || r.State == "indeterminate" {
			continue
		}
		if r.Dispatched {
			r.State = "indeterminate"
			r.Outcome = Outcome{Category: "service_restarted_outcome_unknown"}
		} else if r.State == "running" {
			r.State = "accepted"
		}
		if err := ledger.Update(r); err != nil {
			return nil, err
		}
	}
	e.registerLifecycle()
	e.registerWorker()
	if err := e.recoverCancellations(); err != nil {
		return nil, err
	}
	return e, nil
}
func capability(action string) string {
	switch action {
	case "exec":
		return "exec"
	case "ping":
		return "ping"
	case "pause", "resume":
		return "worker_control"
	default:
		return "lifecycle"
	}
}
func validateMutation(in Mutation) error {
	if len(in.Stdin) > 64<<10 || len(in.Prompt) > 64<<10 {
		return fail("input_too_large")
	}
	switch in.Action {
	case "exec":
		if len(in.Argv) == 0 || in.Argv[0] == "" || len(in.Argv) > 4096 || in.Timeout <= 0 || in.Timeout > 10*time.Minute {
			return fail("invalid_exec")
		}
		size := 0
		for _, s := range in.Argv {
			size += len(s) + 1
			if strings.ContainsRune(s, 0) {
				return fail("invalid_argv")
			}
		}
		if size > 64<<10 {
			return fail("argv_too_large")
		}
		if in.Cwd != "" && (!filepath.IsAbs(in.Cwd) || filepath.Clean(in.Cwd) != in.Cwd || strings.ContainsRune(in.Cwd, 0)) {
			return fail("invalid_guest_cwd")
		}
	case "start", "stop", "restart", "ping", "pause", "resume":
		if len(in.Argv) > 0 || len(in.Stdin) > 0 || in.Cwd != "" {
			return fail("invalid_action_input")
		}
	default:
		return fail("invalid_action")
	}
	return nil
}
func accepted(host Host, r OperationRecord) Accepted {
	return Accepted{Host: host, OperationID: r.ID, State: r.State, WakeID: r.WakeID, WakePhase: r.WakePhase}
}
func (e *Engine) Submit(ctx context.Context, caller Caller, in Mutation) (Accepted, error) {
	if err := ctx.Err(); err != nil {
		return Accepted{}, err
	}
	if err := validateMutation(in); err != nil {
		return Accepted{}, err
	}
	if err := validateKey(in.Key, time.Now().UTC()); err != nil {
		return Accepted{}, err
	}
	selector, err := e.control.selector(in.Workspace)
	if err != nil {
		return Accepted{}, err
	}
	if err = e.policy.Authorize(caller, selector, capability(in.Action)); err != nil {
		return Accepted{}, err
	}
	e.mu.Lock()
	defer e.signal()
	defer e.mu.Unlock()
	// Resolve a retry before current VM state/config: it remains the old operation.
	for _, r := range e.ledger.all() {
		if r.CallerID == caller.ID && r.Input.Key.ID == in.Key.ID {
			found, _, err := e.ledger.Accept(caller, in, "", "", time.Now().UTC())
			return accepted(e.host, found), err
		}
	}
	b, err := e.control.Resolve(ctx, in.Workspace)
	if err != nil {
		return Accepted{}, err
	}
	if b.Workspace.State != "absent" && !b.Workspace.Owned {
		return Accepted{}, fail("ownership_required")
	}
	if in.Action != "start" && b.Workspace.State != "running" && in.Action != "stop" && in.Action != "restart" {
		return Accepted{}, fail("workspace_not_running")
	}
	if in.Action == "exec" && b.Workspace.Fingerprint != b.Spec.Fingerprint {
		return Accepted{}, fail("maintenance_required")
	}
	for _, r := range e.ledger.all() {
		if r.Input.Workspace != in.Workspace || terminal(r.State) || r.HoldResolved || r.Input.Action == "cancel" {
			continue
		}
		resolving := (in.Action == "stop" || in.Action == "restart") && in.InterruptActive && r.Input.Action == "exec"
		if !resolving {
			return Accepted{}, fail("workspace_busy")
		}
	}
	r, _, err := e.ledger.Accept(caller, in, b.Spec.Fingerprint, b.Incarnation, time.Now().UTC(), runtimeIdentity(b))
	if err != nil {
		return Accepted{}, err
	}
	return accepted(e.host, r), nil
}
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fail("engine_already_running")
	}
	e.running = true
	e.mu.Unlock()
	var wg sync.WaitGroup
	defer func() {
		e.mu.Lock()
		for _, cancel := range e.active {
			cancel()
		}
		e.mu.Unlock()
		wg.Wait()
	}()
	if err := e.ledger.Prune(time.Now().UTC()); err != nil {
		return err
	}
	e.reconcile(ctx)
	e.signal()
	recovery := time.NewTicker(5 * time.Second)
	defer recovery.Stop()
	interval := e.retentionInterval
	if interval <= 0 {
		interval = time.Minute
	}
	retention := time.NewTicker(interval)
	defer retention.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-retention.C:
			if err := e.ledger.Prune(time.Now().UTC()); err != nil {
				return err
			}
		case <-recovery.C:
			e.reconcile(ctx)
			continue
		case <-ticker.C:
			e.signal()
			continue
		case <-e.notify:
			e.mu.Lock()
			if err := e.recoverCancellations(); err != nil {
				e.mu.Unlock()
				return err
			}
			for _, r := range e.ledger.all() {
				if len(e.active) >= e.control.cfg.Limits.Active {
					break
				}
				if r.State != "accepted" {
					continue
				}
				if _, ok := e.active[r.ID]; ok {
					continue
				}
				opctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				e.active[r.ID] = cancel
				wg.Add(1)
				go func(r OperationRecord) {
					defer wg.Done()
					defer cancel()
					defer func() { e.mu.Lock(); delete(e.active, r.ID); e.mu.Unlock() }()
					e.execute(opctx, r)
				}(r)
			}
			e.mu.Unlock()
		}
	}
}
func (e *Engine) change(id string, fn func(*OperationRecord)) error {
	for range 16 {
		var r OperationRecord
		found := false
		for _, v := range e.ledger.all() {
			if v.ID == id {
				r = v
				found = true
				break
			}
		}
		if !found {
			return fail("operation_not_found")
		}
		fn(&r)
		err := e.ledger.Update(r)
		var coded *Error
		if errors.As(err, &coded) && coded.Code == "operation_revision_conflict" {
			continue
		}
		return err
	}
	return fail("operation_revision_conflict")
}
func (e *Engine) execute(ctx context.Context, record OperationRecord) {
	if record.Input.Action == "cancel" {
		e.mu.Lock()
		defer e.mu.Unlock()
		if err := e.applyCancellation(record); err != nil {
			slog.Error("MCP cancellation recovery failed", "operation_id", record.ID, "category", "persistence_failed")
		}
		return
	}
	caller, err := recordCaller(record)
	if err != nil {
		return
	}
	r, err := e.ledger.Lookup(caller, record.ID)
	if err != nil || r.State != "accepted" {
		return
	}
	if err = e.change(r.ID, func(v *OperationRecord) {
		if v.State == "accepted" {
			v.State = "running"
		}
	}); err != nil {
		return
	}
	r, err = e.ledger.Lookup(caller, r.ID)
	if err != nil || r.State != "running" {
		return
	}
	selector, err := e.control.selector(r.Input.Workspace)
	if err == nil {
		err = e.policy.Authorize(caller, selector, capability(r.Input.Action))
	}
	if err != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "forbidden"})
		return
	}
	b, err := e.control.Resolve(ctx, r.Input.Workspace)
	if err != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "workspace_unavailable"})
		return
	}
	if handler := e.handlers[r.Input.Action]; handler != nil {
		out, err := handler(ctx, caller, r, b)
		if err != nil && out.Category == "" {
			out.Category = "action_failed"
		}
		e.finish(r.ID, out)
		return
	}
	if r.Input.Action != "exec" {
		e.finish(r.ID, Outcome{Completed: true, Category: "unsupported_action"})
		return
	}
	release, err := lock.AcquireContext(ctx, b.RuntimeName, 30*time.Second)
	if err != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "workspace_busy"})
		return
	}
	defer release()
	b, err = e.control.Resolve(ctx, r.Input.Workspace)
	if err != nil || !b.Workspace.Owned || b.Workspace.State != "running" || b.Spec.Fingerprint != r.Fingerprint || b.Workspace.Fingerprint != r.Fingerprint || b.Incarnation != r.Incarnation {
		e.finish(r.ID, Outcome{Completed: true, Category: "workspace_changed"})
		return
	}
	if err = e.policy.Authorize(caller, b.Selector, "exec"); err != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "forbidden"})
		return
	}
	if ctx.Err() != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "cancelled_before_dispatch"})
		return
	}
	nonce, err := randomHex(16)
	if err != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "dispatch_failed"})
		return
	}
	r, err = e.ledger.Dispatch(caller, r.ID, nonce)
	if err != nil {
		return
	}

	out, errout := runtime.NewBoundedCapture(int64(e.control.cfg.Limits.OutputBytes)), runtime.NewBoundedCapture(int64(e.control.cfg.Limits.OutputBytes))
	execctx := context.WithValue(ctx, receiptKey{}, nonce)
	execctx, cancel := context.WithTimeout(execctx, r.Input.Timeout)
	defer cancel()
	outcome, runerr := e.control.Exec(execctx, b, r.Input, out, errout)
	if runerr != nil && outcome.Category == "" {
		outcome.Category = "transport_outcome_unknown"
	}
	if err = e.ledger.SaveOutput(r.ID, "stdout", out.Bytes(), out.Total()); err != nil {
		e.finish(r.ID, Outcome{Category: "output_persistence_failed"})
		return
	}
	if err = e.ledger.SaveOutput(r.ID, "stderr", errout.Bytes(), errout.Total()); err != nil {
		e.finish(r.ID, Outcome{Category: "output_persistence_failed"})
		return
	}
	e.finish(r.ID, outcome)
}
func (e *Engine) finish(id string, out Outcome) {
	defer e.signal()
	err := e.change(id, func(r *OperationRecord) {
		if terminal(r.State) {
			return
		}
		r.Outcome = out
		switch {
		case !out.Completed:
			r.State = "indeterminate"
		case out.Category == "cancelled_before_dispatch":
			r.State = "cancelled"
		case out.Category != "" || out.ExitCode != nil && *out.ExitCode != 0:
			r.State = "failed"
		default:
			r.State = "succeeded"
		}
	})
	if err != nil {
		slog.Error("MCP operation checkpoint failed", "operation_id", id, "category", "persistence_failed")
	}
}
func (e *Engine) Status(ctx context.Context, caller Caller, id string, outOffset, errOffset int64, pageBytes int) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	if err := e.ledger.Prune(time.Now().UTC()); err != nil {
		return Operation{}, fail("retention_unavailable")
	}
	r, err := e.ledger.Lookup(caller, id)
	if err != nil {
		return Operation{}, err
	}
	selector, err := e.control.selector(r.Input.Workspace)
	if err != nil {
		return Operation{}, err
	}
	if err = e.policy.Authorize(caller, selector, "read"); err != nil {
		return Operation{}, err
	}
	out := Operation{Host: e.host, ID: r.ID, Workspace: r.Input.Workspace, Action: r.Input.Action, State: r.State, Phase: r.Phase, ErrorCategory: r.Outcome.Category, ExitCode: r.Outcome.ExitCode, WakeID: r.WakeID, WakePhase: r.WakePhase}
	if r.Pruned {
		out.ErrorCategory = "output_pruned"
		return out, nil
	}
	out.Stdout, err = e.ledger.Page(caller, id, "stdout", outOffset, pageBytes)
	if err != nil {
		return Operation{}, err
	}
	out.Stderr, err = e.ledger.Page(caller, id, "stderr", errOffset, pageBytes)
	return out, err
}
func (e *Engine) Cancel(ctx context.Context, caller Caller, id string) (Operation, error) {
	r, err := e.ledger.Lookup(caller, id)
	if err != nil {
		return Operation{}, err
	}
	selector, err := e.control.selector(r.Input.Workspace)
	if err != nil {
		return Operation{}, err
	}
	if err = e.policy.Authorize(caller, selector, capability(r.Input.Action)); err != nil {
		return Operation{}, err
	}
	e.mu.Lock()
	err = e.change(id, func(v *OperationRecord) {
		v.CancelIntent = true
		if terminal(v.State) || v.State == "indeterminate" {
			return
		}
		if !v.Dispatched {
			v.State = "cancelled"
			v.Outcome = Outcome{Completed: true, TerminationProven: true}
		} else {
			v.State = "cancel_requested"
		}
	})
	if cancel := e.active[id]; cancel != nil && err == nil {
		cancel()
	}
	e.mu.Unlock()
	if err != nil {
		return Operation{}, err
	}
	return e.Status(ctx, caller, id, 0, 0, 64<<10)
}

// MarshalAudit excludes argv, stdin, prompts, output, nonces and credentials.
func MarshalAudit(r OperationRecord) ([]byte, error) {
	return json.Marshal(struct {
		Host, Caller, Workspace, Operation, Action, State string
		At                                                time.Time
	}{r.HostID, r.CallerID, r.Input.Workspace, r.ID, r.Input.Action, r.State, r.UpdatedAt})
}

var _ io.Writer = (*runtime.BoundedCapture)(nil)

// CancelRequest journals cancellation identity before touching the target. It
// runs immediately rather than waiting behind the operations it must cancel.
func (e *Engine) CancelRequest(ctx context.Context, caller Caller, id string, key RequestKey) (Accepted, error) {
	if err := ctx.Err(); err != nil {
		return Accepted{}, err
	}
	target, err := e.ledger.Lookup(caller, id)
	if err != nil {
		return Accepted{}, err
	}
	selector, err := e.control.selector(target.Input.Workspace)
	if err != nil {
		return Accepted{}, err
	}
	if err = e.policy.Authorize(caller, selector, capability(target.Input.Action)); err != nil {
		return Accepted{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, _, err := e.ledger.Accept(caller, Mutation{Key: key, Workspace: target.Input.Workspace, Action: "cancel", TargetOperation: id}, target.Fingerprint, target.Incarnation, time.Now().UTC())
	if err != nil {
		return Accepted{}, err
	}
	if err = e.applyCancellation(r); err != nil {
		slog.Error("MCP cancellation checkpoint failed", "operation_id", r.ID, "category", "persistence_failed")
		e.signal()
	}
	r, err = e.ledger.Lookup(caller, r.ID)
	if err != nil {
		return Accepted{}, err
	}
	return accepted(e.host, r), nil
}

// recoverCancellations runs before queued work is scheduled, including startup.
// It needs e.mu when a running engine can concurrently accept cancellations.
func (e *Engine) recoverCancellations() error {
	for _, r := range e.ledger.all() {
		if r.Input.Action == "cancel" && !terminal(r.State) {
			if err := e.applyCancellation(r); err != nil {
				return err
			}
		}
	}
	return nil
}
func (e *Engine) applyCancellation(record OperationRecord) error {
	caller, err := recordCaller(record)
	if err != nil {
		return err
	}
	r, err := e.ledger.Lookup(caller, record.ID)
	if err != nil {
		return err
	}
	if terminal(r.State) {
		return nil
	}
	target, err := e.ledger.Lookup(caller, r.Input.TargetOperation)
	if err != nil || target.Input.Workspace != r.Input.Workspace {
		e.finish(r.ID, Outcome{Completed: true, Category: "operation_not_found"})
		return nil
	}
	selector, err := e.control.selector(target.Input.Workspace)
	if err != nil || e.policy.Authorize(caller, selector, capability(target.Input.Action)) != nil {
		e.finish(r.ID, Outcome{Completed: true, Category: "forbidden"})
		return nil
	}
	if err = e.change(r.ID, func(v *OperationRecord) {
		if !terminal(v.State) {
			v.State = "running"
		}
	}); err != nil {
		return err
	}
	if err = e.ledger.Checkpoint(caller, r.ID, "cancel_dispatch_pending"); err != nil {
		return err
	}
	if err = e.change(target.ID, func(v *OperationRecord) {
		v.CancelIntent = true
		if terminal(v.State) || v.State == "indeterminate" {
			return
		}
		if !v.Dispatched {
			v.State = "cancelled"
			v.Outcome = Outcome{Completed: true, TerminationProven: true}
		} else {
			v.State = "cancel_requested"
		}
	}); err != nil {
		return err
	}
	if cancel := e.active[target.ID]; cancel != nil {
		cancel()
	}
	e.finish(r.ID, Outcome{Completed: true})
	return nil
}

func (e *Engine) signal() {
	select {
	case e.notify <- struct{}{}:
	default:
	}
}
