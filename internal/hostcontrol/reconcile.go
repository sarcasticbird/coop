package hostcontrol

import (
	"context"
	"time"
)

// Recovery makes at most four bounded read probes per pass. Exec is never replayed.
func (e *Engine) reconcile(ctx context.Context) {
	var eligible []OperationRecord
	for _, r := range e.ledger.all() {
		if r.State == "indeterminate" && r.Input.Action != "exec" && r.Input.Action != "cancel" {
			eligible = append(eligible, r)
		}
	}
	if len(eligible) == 0 {
		e.reconcileCursor = 0
		return
	}
	start := e.reconcileCursor % len(eligible)
	count := min(4, len(eligible))
	e.reconcileCursor = (start + count) % len(eligible)
	for i := range count {
		r := eligible[(start+i)%len(eligible)]
		probe, stop := context.WithTimeout(ctx, time.Second)
		switch r.Input.Action {
		case "ping", "pause", "resume":
			e.reconcileWorker(probe, r)
		case "start", "stop", "restart":
			e.reconcileLifecycle(probe, r)
		}
		stop()
	}
}
