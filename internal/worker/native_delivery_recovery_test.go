package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativeDeliveryRecoveryRefusesUnsafeInputsWithoutEffects(t *testing.T) {
	for _, failure := range []string{"session", "message", "operation", "input", "request", "running", "missing-evidence", "uncertain", "started", "known-merge-unconfirmed", "completed-action", "missing-action", "other-uncertain", "other-prepared", "other-engineering", "conversation", "worker-lock", "producer-lock", "paused", "revoked", "placement", "nonterminal-checkpoint", "retired"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, cat, code := dispatchFixture(t)
			v := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "blocked", Reason: "native_completed_without_result"}
			x.Job.Kind = "deliver"
			x.Job.SessionID, x.Job.MessageID = "ses_recovery", "msg_recovery"
			x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
			x.Job.TerminalAt = time.Now().UTC()
			code.running = false
			in := NativeAction{Operation: "deliver", RequestID: "original-delivery"}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			record := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "prepared"}
			session, message := x.Job.SessionID, x.Job.MessageID
			var evidence OpenCode = code
			switch failure {
			case "session":
				session = "ses_foreign"
			case "message":
				message = "msg_foreign"
			case "operation":
				in.Operation = "publish-pr"
			case "input":
				in.Body = "changed original input"
			case "request":
				in.RequestID = "replacement"
			case "running":
				code.running = true
			case "missing-evidence":
				evidence = nativeObserveFailure{code}
			case "uncertain":
				record.State = "uncertain"
			case "started":
				record.Step = "review-published"
			case "known-merge-unconfirmed":
				record.Step = "merge"
			case "completed-action":
				record.State = "completed"
			case "other-uncertain", "other-prepared":
				other := record
				other.RequestID, other.State = "other", "uncertain"
				if failure == "other-prepared" {
					other.State, other.Step = "prepared", "merge"
				}
				if err = saveNativeAction(cfg, other); err != nil {
					t.Fatal(err)
				}
			case "other-engineering":
				other := x
				other.Job.ID, other.Phase = BodyHash("other-active"), "active"
				if err = saveNativeExecution(cfg, other); err != nil {
					t.Fatal(err)
				}
			case "conversation":
				turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: 1, Delivery: c.Delivery{ID: "held-delivery", MessageID: "held-message"}, Message: c.Message{ID: "held-message", Body: "status"}, BodySHA: BodyHash("status"), Phase: "active"}
				if err = saveMasterTurn(cfg, &turn); err != nil {
					t.Fatal(err)
				}
			case "worker-lock":
				release, e := NewVersionedFileStore(cfg.StateDir, 3).Lock()
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(release)
			case "producer-lock":
				release, e := lockNativeProducer(cfg)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(release)
			case "paused":
				if err = atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				m.authority.State, m.authority.Valid = "paused", false
			case "placement":
				m.self.Placement.Generation++
			case "nonterminal-checkpoint":
				x.Job.SubmissionState = "accepted"
			}
			if err = saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			if err = saveNativeAction(cfg, record); err != nil {
				t.Fatal(err)
			}
			path := nativeActionPath(cfg, x.Job.ID, record.RequestID)
			if failure == "missing-action" {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "retired" {
				data, e := json.Marshal(x)
				if e != nil {
					t.Fatal(e)
				}
				canonical, e := canonicalRecoveryRaw(data)
				if e != nil {
					t.Fatal(e)
				}
				snapshot := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1, Records: []c.RecoveryEvidence{{Kind: "engineering", RecordKey: "engineering/" + x.Job.ID + ".json", Data: canonical}}}
				accepted, e := json.Marshal(snapshot)
				if e != nil {
					t.Fatal(e)
				}
				dir := filepath.Join(masterDir(cfg), "recovery")
				if e = ensurePrivateDir(dir, true); e != nil {
					t.Fatal(e)
				}
				if e = atomicPrivate(filepath.Join(dir, "accepted.json"), accepted); e != nil {
					t.Fatal(e)
				}
			}
			before, err := readPrivate(nativeExecutionPath(cfg, x.Job.ID), MaxSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			journal, _ := os.ReadFile(path)
			requests := 0
			api := NewGitHubClient(&fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
				requests++
				return CommandResult{}, c.ErrForbidden
			}})
			if _, err = RecoverNativeDelivery(t.Context(), cfg, m, api, evidence, x.Job.ID, session, message, in); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			after, err := readPrivate(nativeExecutionPath(cfg, x.Job.ID), MaxSnapshot)
			if err != nil || string(before) != string(after) {
				t.Fatal("refused recovery changed saved execution", err)
			}
			afterJournal, _ := os.ReadFile(path)
			expectedReads := 0
			if failure == "known-merge-unconfirmed" {
				expectedReads = 1
			}
			if string(journal) != string(afterJournal) || requests != expectedReads || code.calls != 0 || code.aborts != 0 {
				t.Fatal("refused recovery changed journal or crossed remote/model boundary", requests)
			}
		})
	}
}
