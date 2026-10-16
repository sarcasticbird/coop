package hostcontrol

import (
	"context"
	"github.com/sarcasticbird/coop/internal/runtime"
	"testing"
	"time"
)

func TestRestartRecoveryRequiresCurrentAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "revoked"}[revoked], func(t *testing.T) {
			e, caller, b := fixtureEngine(t)
			b.state.Store(int32(runtime.StateStopped))
			in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "restart", InterruptActive: true}
			r, _, err := e.ledger.Accept(caller, in, "fp", "incarnation-1", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r.State = "indeterminate"
			r.Dispatched = true
			r.Phase = "stopped"
			if err = e.ledger.Update(r); err != nil {
				t.Fatal(err)
			}
			r, err = e.ledger.Lookup(caller, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revoked {
				cfg := e.control.cfg
				cfg.Callers = nil
				if err = e.policy.Reload(cfg); err != nil {
					t.Fatal(err)
				}
			}
			e.reconcileLifecycle(context.Background(), r)
			saved, err := e.ledger.Lookup(caller, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "accepted"
			if revoked {
				want = "indeterminate"
			}
			if saved.State != want || b.starts.Load() != 0 {
				t.Fatal("restart recovery bypassed authority or started inline")
			}
		})
	}
}
