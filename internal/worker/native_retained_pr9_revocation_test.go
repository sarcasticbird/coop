package worker

import (
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestRetainedPR9OwnerDecideRevocationPrecedesEffects(t *testing.T) {
	for _, stage := range []string{"gate", "merge"} {
		t.Run(stage, func(t *testing.T) {
			f := newPR9Journey(t)
			gates := f.gates(false)
			name, kind := "hawk", "delivery-review"
			in := NativeAction{Operation: "delivery-readiness", Verdict: "ready", RequestID: "revoked-owner-gate"}
			if stage == "merge" {
				f.ready(gates, false)
				name, kind = "rook", "deliver"
				in = NativeAction{Operation: "deliver", RequestID: "revoked-owner-merge"}
			}
			cfg, client, execution := f.selected(name, kind)
			owner := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"uat"}, Capabilities: []string{"read", "send", "manage"}}
			if err := f.service.Store.Configure(t.Context(), []c.Principal{owner}, nil); err != nil {
				t.Fatal(err)
			}
			// Readability and membership are unchanged, but decide is gone.
			a, err := client.ResolveGoal(t.Context(), execution.Goal)
			if err != nil || !a.Valid {
				t.Fatal("revocation changed source approval/readability", err)
			}
			if _, err := client.Message(t.Context(), gates.Input.EvidenceMessageID); err != nil {
				t.Fatal("owner evidence became unreadable", err)
			}
			dispatcher := &NativeDispatcher{config: cfg, master: client, github: f.github, clock: RealClock{}}
			if _, err := dispatcher.freshCandidate(t.Context(), execution); err == nil {
				t.Fatal("revoked owner retained dispatch authority")
			}
			if _, err := RunNativeAction(t.Context(), cfg, client, f.github, execution.Job.ID, in); err == nil {
				t.Fatal("revoked decide capability authorized retained effect")
			}
			if len(f.replay.writes) != 0 || f.provider.completes != 0 {
				t.Fatal("owner revocation checked only after an external effect")
			}
		})
	}
}
