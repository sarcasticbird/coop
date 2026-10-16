package hostcontrol

import (
	"context"
	"time"
)

// Recovery makes at most four bounded read probes per pass. Exec is never replayed.
func (e *Engine) reconcile(ctx context.Context) {
	probes := 0
	for _, r := range e.ledger.all() {
		if r.State != "indeterminate" || r.Input.Action == "exec" || r.Input.Action == "cancel" {
			continue
		}
		if probes == 4 {
			return
		}
		probes++
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
