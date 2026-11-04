package worker

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestNativeExecutionPreservesHistoricalRecoveryAudit(t *testing.T) {
	cfg, master, catalog, code, execution := nativeStaleAttemptFixture(t)
	execution.Phase, execution.Reason = "completed", ""
	rejected := execution.Job
	rejected.MessageID = "msg_rejected_recovery"
	rejected.Phase, rejected.SubmissionState = "blocked", "prepared"
	rejected.ReconcileFailures = 2
	rejected.ReconcileError = "native_observation_unavailable"
	before, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(before, &wire); err != nil {
		t.Fatal(err)
	}
	wire["RejectedRecoveryContinuations"], err = json.Marshal([]Job{rejected})
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]string{"id": "historical-request", "expectedSHA256": BodyHash("original"), "originalMessageID": "msg_original", "successorMessageID": "msg_successor", "bindingSHA256": BodyHash("binding"), "text": "historical recovery text is not a new instruction"}
	legacy := map[string]any{"RecoveryID": "historical-recovery", "Request": request, "Original": execution.Job, "OriginalReason": "native_observation_unavailable", "RequestedAt": time.Now().UTC(), "Adopted": true}
	wire["Recovery"], err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	request["previousContinuationID"] = "msg_previous"
	delete(legacy, "RecoveryID")
	delete(legacy, "OriginalReason")
	wire["RecoveryFollowups"], err = json.Marshal([]any{legacy})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	path := nativeExecutionPath(cfg, execution.Job.ID)
	if err = atomicPrivate(path, raw); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadNativeExecution(cfg, execution.Job.ID)
	if err != nil {
		t.Fatal("historical rejected-continuation audit prevented startup", err)
	}
	if err = saveNativeExecution(cfg, loaded); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err = json.Unmarshal(after, &saved); err != nil {
		t.Fatal(err)
	}
	var audit []Job
	if err = json.Unmarshal(saved["RejectedRecoveryContinuations"], &audit); err != nil || !reflect.DeepEqual(audit, []Job{rejected}) {
		t.Fatal("rejected-continuation audit changed or was discarded", err)
	}
	for _, field := range []string{"Recovery", "RecoveryFollowups"} {
		var expected, actual any
		if err = json.Unmarshal(wire[field], &expected); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(saved[field], &actual); err != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("historical recovery audit changed or was discarded", field, err)
		}
	}
	dispatcher, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.HasActive() || dispatcher.history[execution.Job.ID].Phase != "completed" || !reflect.DeepEqual(loaded.Job, execution.Job) || len(loaded.Turns) != 0 || loaded.PendingContinuation != nil || len(loaded.Continuations) != 0 || code.calls != 0 || code.aborts != 0 {
		t.Fatal("audit was treated as accepted continuation work")
	}
	// Compatibility is specific to this historical field, not a relaxed reader.
	wire["UnknownRecoveryAudit"] = json.RawMessage(`true`)
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicPrivate(path, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = loadNativeExecution(cfg, execution.Job.ID); err == nil {
		t.Fatal("unrecognized journal field was accepted")
	}
}

func TestNativeExecutionReadsButNeverReplaysUnadoptedHistoricalRecovery(t *testing.T) {
	for _, field := range []string{"Recovery", "RecoveryFollowups"} {
		t.Run(field, func(t *testing.T) {
			cfg, _, _, _, execution := nativeStaleAttemptFixture(t)
			if field == "Recovery" {
				execution.Recovery = &nativeRecoveryAudit{Adopted: false}
			} else {
				execution.RecoveryFollowups = []nativeRecoveryAudit{{Adopted: false}}
			}
			if err := saveNativeExecution(cfg, execution); err == nil {
				t.Fatal("unadopted legacy protocol was saved as supported work")
			}
			raw, err := json.Marshal(execution)
			if err != nil {
				t.Fatal(err)
			}
			if err = atomicPrivate(nativeExecutionPath(cfg, execution.Job.ID), raw); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadNativeExecution(cfg, execution.Job.ID)
			if err != nil || !loaded.unresolvedHistoricalRecovery() {
				t.Fatal("historical recovery could not be read as blocked history", err)
			}
			if err := saveNativeExecution(cfg, loaded); err == nil {
				t.Fatal("historical recovery was adopted by reading it")
			}
		})
	}
}

func TestHistoricalRecoveryAuditDoesNotClearNativeHold(t *testing.T) {
	cfg, master, catalog, code, execution := nativeStaleAttemptFixture(t)
	execution.Reason = "native_observation_unavailable"
	execution.Job.Phase, execution.Job.SubmissionState = "active", "accepted"
	execution.Job.TerminalAt = time.Time{}
	execution.Recovery = &nativeRecoveryAudit{Adopted: true}
	if err := saveNativeExecution(cfg, execution); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if !dispatcher.RecoveryBlocked() || dispatcher.HasActive() || dispatcher.history[execution.Job.ID].Reason != "native_observation_unavailable" || code.calls != 0 || code.aborts != 0 {
		t.Fatal("historical audit bypassed native uncertainty")
	}
}
