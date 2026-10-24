package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func TestRecoveryPreparedCheckpointRetainsActionWithoutReplay(t *testing.T) {
	for _, step := range []string{"", "push", "merge", "task-metadata", "project-ready"} {
		name := step
		if name == "" {
			name = "unstarted"
		}
		t.Run(name, func(t *testing.T) {
			cfg, comms, store, owner, bird, _, code := commsFixture(t)
			ctx := context.Background()
			action := nativeActionRecord{Version: 3, JobID: BodyHash("saved-job"), RequestID: "saved-request", Operation: "create-task", State: "prepared", Step: step}
			if err := saveNativeAction(cfg, action); err != nil {
				t.Fatal(err)
			}
			path := nativeActionPath(cfg, action.JobID, action.RequestID)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := canonicalRecoveryRaw(before)
			if err != nil {
				t.Fatal(err)
			}
			// Preserve an earlier inventory that misclassified this acknowledged
			// checkpoint. A new report must add proof rather than erase history.
			old := c.RecoveryEvidence{Kind: "actions", RecordKey: "actions/" + filepath.Base(path), Data: data, Uncertain: true}
			old.ID = BodyHash(string(mustRecoveryJSON(old)))
			snapshot := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1, Records: []c.RecoveryEvidence{old}}
			if err = atomicPrivate(recoverySnapshotPath(cfg, 1), mustRecoveryJSON(snapshot)); err != nil {
				t.Fatal(err)
			}
			if err = store.Quiesce(ctx, owner, 1); err != nil {
				t.Fatal(err)
			}
			client := comms.Client.(*masterclient.Client)
			if _, err = ReconcileMaster(ctx, cfg, client, code, false); err != nil {
				t.Fatal(err)
			}
			evidence, err := store.RecoveryEvidence(ctx, owner, bird.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			proof := ""
			for _, record := range evidence {
				if record.Kind == "actions" && record.Terminal && !record.Uncertain {
					proof = record.ID
				}
			}
			if proof == "" {
				t.Fatal("acknowledged checkpoint still requires an unknown remote outcome")
			}
			resolve := c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: old.ID, Reason: "Retain acknowledged checkpoint without replaying its action"}
			if err = store.ResolveRecovery(ctx, owner, resolve); !errors.Is(err, c.ErrConflict) {
				t.Fatalf("old uncertainty accepted without matching proof: %v", err)
			}
			resolve.ProofID = proof
			if err = store.ResolveRecovery(ctx, owner, resolve); err != nil {
				t.Fatal(err)
			}
			for _, record := range evidence {
				if record.ID == old.ID {
					continue
				}
				if err = store.ResolveRecovery(ctx, owner, c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: record.ID, Reason: "Reviewed retained checkpoint"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = ReconcileMaster(ctx, cfg, client, code, true); err != nil {
				t.Fatal(err)
			}
			if err = store.Resume(ctx, owner, 1); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("action journal changed: %v", err)
			}
			if code.calls != 0 {
				t.Fatalf("recovery submitted %d model calls", code.calls)
			}
		})
	}
}

func TestRecoveryUnknownActionOutcomeStillBlocksResume(t *testing.T) {
	for _, state := range []string{"uncertain", "unknown-future-state"} {
		t.Run(state, func(t *testing.T) {
			cfg, comms, store, owner, bird, _, code := commsFixture(t)
			ctx := context.Background()
			if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: BodyHash("saved-job"), RequestID: "saved-request", Operation: "create-task", State: state}); err != nil {
				t.Fatal(err)
			}
			if err := store.Quiesce(ctx, owner, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := ReconcileMaster(ctx, cfg, comms.Client.(*masterclient.Client), code, false); err != nil {
				t.Fatal(err)
			}
			evidence, err := store.RecoveryEvidence(ctx, owner, bird.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range evidence {
				if record.Kind != "actions" {
					continue
				}
				if !record.Uncertain || record.Terminal {
					t.Fatal("unknown outcome downgraded to a checkpoint")
				}
				if err = store.ResolveRecovery(ctx, owner, c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: record.ID, Reason: "Cannot waive an unknown outcome"}); !errors.Is(err, c.ErrConflict) {
					t.Fatalf("unknown outcome accepted without proof: %v", err)
				}
			}
			if err = store.Resume(ctx, owner, 1); !errors.Is(err, c.ErrEpoch) {
				t.Fatalf("unknown outcome allowed resume: %v", err)
			}
		})
	}
}
