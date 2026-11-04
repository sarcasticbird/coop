package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

type abandonmentCode struct {
	*fakeOpenCode
	fences int
	fail   error
}

func (c *abandonmentCode) FenceNativeScope(_ context.Context, j Job, _ string, sha, prior string) (NativeFenceProof, error) {
	c.fences++
	if c.fail != nil {
		return NativeFenceProof{}, c.fail
	}
	return NativeFenceProof{ReceiptID: prior, ReceiptSHA: BodyHash("receipt"), ScopeSHA: sha, SessionID: j.SessionID, Disposition: "already_dispositioned", VerifiedAt: time.Now().UTC()}, nil
}

func abandonmentFixture(t *testing.T) (Config, NativeExecution, *abandonmentCode, NativeAbandonmentInput) {
	t.Helper()
	cfg, _, _, code, x := nativeStaleAttemptFixture(t)
	cfg.DeliveryHold = true
	x.Phase, x.Reason = "blocked", "native_observation_unavailable"
	x.Job.Kind, x.Job.SubmissionState = "fix", "accepted"
	x.Job.TerminalAt = time.Time{}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	state := newState()
	state.Version, state.InstanceID = 3, cfg.Master.InstanceID
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(filepath.Join(cfg.StateDir, "state.json"), raw); err != nil {
		t.Fatal(err)
	}
	if err := NewVersionedFileStore(cfg.StateDir, 3).SetPaused(true); err != nil {
		t.Fatal(err)
	}
	in := NativeAbandonmentInput{JobID: x.Job.ID, SessionID: x.Job.SessionID, MessageID: x.Job.MessageID, RequestID: "abandon-test", ScopeSHA: BodyHash("scope"), PriorReceiptID: "prior-test", Reason: "Owner abandons this invocation; outcome and effects remain unknown"}
	return cfg, x, &abandonmentCode{fakeOpenCode: code}, in
}

func TestNativeAbandonmentPreservesUnknownOutcomeAndOriginalJob(t *testing.T) {
	cfg, original, code, in := abandonmentFixture(t)
	beforeCalls := code.calls
	x, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in)
	if err != nil || x.Phase != "cancelled" || !reflect.DeepEqual(x.Job, original.Job) || x.Abandonment.State != "fenced" || x.Abandonment.Outcome != "unknown" || x.Abandonment.Effects != "unknown" {
		t.Fatal("incorrect abandonment", x.Phase, err)
	}
	if code.calls != beforeCalls || code.aborts != 0 || code.fences != 1 || x.Job.SubmissionState != "accepted" || !x.Job.TerminalAt.IsZero() {
		t.Fatal("abandonment fabricated native completion or submitted work")
	}
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err != nil || code.fences != 1 {
		t.Fatal("completed disposition repeated a fence", err)
	}
	in.Reason += " changed"
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err == nil {
		t.Fatal("new input adopted a prior disposition")
	}
	records, _, err := gatherMasterRecovery(t.Context(), cfg, nativeObserveFailure{code.fakeOpenCode})
	if err != nil {
		t.Fatal("fenced cancellation still observed the missing message", err)
	}
	found := false
	for _, r := range records {
		if r.RecordKey == "engineering/"+in.JobID+".json" {
			found = true
			if !r.Terminal || r.Uncertain || r.Intent.MessageID != in.MessageID {
				t.Fatal("missing interruption-only recovery evidence")
			}
			var audit NativeAbandonment
			if json.Unmarshal(r.Observation, &audit) != nil || audit.Outcome != "unknown" || audit.Effects != "unknown" {
				t.Fatal("unknown effects were lost")
			}
		}
	}
	if !found {
		t.Fatal("abandoned invocation omitted from inventory")
	}
	loaded, err := loadNativeExecution(cfg, in.JobID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Job.HeadSHA = "tampered"
	if err := saveNativeExecution(cfg, loaded); err == nil {
		t.Fatal("original accepted job could be rewritten after abandonment")
	}
}

func TestNativeAbandonmentFailureRemainsUncertainAndRetryBindsInput(t *testing.T) {
	cfg, original, code, in := abandonmentFixture(t)
	code.fail = errors.New("lost fence response")
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err == nil {
		t.Fatal("unverified fence accepted")
	}
	loaded, err := loadNativeExecution(cfg, in.JobID)
	if err != nil || loaded.Phase != "blocked" || loaded.Abandonment.State != "intent" || !reflect.DeepEqual(loaded.Job, original.Job) {
		t.Fatal("failed fence changed original execution", err)
	}
	records, _, err := gatherMasterRecovery(t.Context(), cfg, nativeObserveFailure{code.fakeOpenCode})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.RecordKey == "engineering/"+in.JobID+".json" && (!r.Uncertain || r.Terminal) {
			t.Fatal("unacknowledged fence became terminal proof")
		}
	}
	changed := in
	changed.RequestID = "new-attempt"
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, changed); err == nil || code.fences != 1 {
		t.Fatal("changed input retried an interrupted fence")
	}
	if _, err := ResolveNativeEngineering(t.Context(), cfg, code, in.JobID, in.SessionID, in.MessageID, "cancel"); err == nil {
		t.Fatal("ordinary cancel bypassed pending fence")
	}
	code.fail = nil
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err != nil {
		t.Fatal("exact retry failed", err)
	}
}

func TestNativeAbandonmentRefusesUnsafeInputsBeforeFence(t *testing.T) {
	for _, scenario := range []string{"identity", "session", "message", "reason", "scope", "hold", "pause", "worker-lock", "producer-lock", "prepared-action", "uncertain-action", "deliver-job", "another-invocation", "foreign-placement"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, x, code, in := abandonmentFixture(t)
			before, err := os.ReadFile(nativeExecutionPath(cfg, x.Job.ID))
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "identity":
				in.JobID = BodyHash("other")
			case "session":
				in.SessionID = "ses_other"
			case "message":
				in.MessageID = "msg_other"
			case "reason":
				in.Reason = ""
			case "scope":
				in.ScopeSHA = "bad"
			case "hold":
				cfg.DeliveryHold = false
			case "pause":
				if err := NewVersionedFileStore(cfg.StateDir, 3).SetPaused(false); err != nil {
					t.Fatal(err)
				}
			case "worker-lock":
				release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "producer-lock":
				release, err := lockNativeProducer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "prepared-action", "uncertain-action":
				state := "prepared"
				if scenario == "uncertain-action" {
					state = "uncertain"
				}
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "unfinished", State: state}); err != nil {
					t.Fatal(err)
				}
			case "deliver-job":
				x.Job.Kind = "deliver"
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(nativeExecutionPath(cfg, x.Job.ID))
			case "another-invocation":
				x.Job.ID = BodyHash("another")
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
			case "foreign-placement":
				cfg.Master.Generation++
			}
			if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err == nil || code.fences != 0 {
				t.Fatal("unsafe input reached native fence", err, code.fences)
			}
			after, _ := os.ReadFile(nativeExecutionPath(cfg, in.JobID))
			if scenario != "identity" && string(before) != string(after) {
				t.Fatal("refused input mutated saved execution")
			}
		})
	}
}

func TestNativeAbandonmentMasterRecoveryRetainsUncertaintyAndRequiresProof(t *testing.T) {
	cfg, comms, store, owner, bird, _, native := commsFixture(t)
	cfg.DeliveryHold = true
	if err := NewVersionedFileStore(cfg.StateDir, 3).SetPaused(true); err != nil {
		t.Fatal(err)
	}
	goal := c.GoalRef{Source: "master", MasterID: cfg.Master.MasterID, GoalID: "goal_abandon", RevisionSHA256: BodyHash("source")}
	job := Job{ID: BodyHash("missing-job"), Kind: "fix", SessionID: "ses_missing", MessageID: "msg_missing", Checkout: filepath.Join(cfg.WorkRoot, "missing"), SubmissionState: "accepted", SubmittedAt: time.Now().Add(-time.Hour), Master: &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ExecutorID: cfg.Self.ID, Goal: &goal}}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: goal, Job: job, Phase: "blocked", Reason: "native_observation_unavailable"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	if err := store.Quiesce(t.Context(), owner, 1); err != nil {
		t.Fatal(err)
	}
	in := NativeAbandonmentInput{JobID: job.ID, SessionID: job.SessionID, MessageID: job.MessageID, RequestID: "abandon-test", ScopeSHA: BodyHash("scope"), PriorReceiptID: "prior-test", Reason: "Owner abandons missing invocation; outcome/effects unknown"}
	code := &abandonmentCode{fakeOpenCode: native, fail: errors.New("unacknowledged fence")}
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err == nil {
		t.Fatal("missing fence accepted")
	}
	client := comms.Client.(*masterclient.Client)
	if _, err := ReconcileMaster(t.Context(), cfg, client, nativeObserveFailure{native}, false); err != nil {
		t.Fatal(err)
	}
	records, err := store.RecoveryEvidence(t.Context(), owner, bird.ID, "")
	if err != nil || len(records) != 1 || !records[0].Uncertain {
		t.Fatal("pending fence not reported honestly", err, len(records))
	}
	if err := store.ResolveRecovery(t.Context(), owner, c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: records[0].ID, Reason: "cannot waive missing fence"}); err == nil {
		t.Fatal("unverified interruption resolved")
	}
	if err := store.Resume(t.Context(), owner, 1); err == nil {
		t.Fatal("master resumed with unknown execution")
	}
	code.fail = nil
	if _, err := AbandonMissingNativeTurn(t.Context(), cfg, code, in); err != nil {
		t.Fatal(err)
	}
	// A separate unknown managed action must still block global resume.
	action := nativeActionRecord{Version: 3, JobID: BodyHash("other-job"), RequestID: "unknown-effect", State: "uncertain"}
	if err := saveNativeAction(cfg, action); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileMaster(t.Context(), cfg, client, nativeObserveFailure{native}, false); err != nil {
		t.Fatal(err)
	}
	records, err = store.RecoveryEvidence(t.Context(), owner, bird.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var terminalNative c.RecoveryEvidence
	for _, record := range records {
		if record.Kind == "engineering" && record.Terminal && !record.Uncertain {
			terminalNative = record
		}
	}
	if terminalNative.ID == "" {
		t.Fatal("no interrupted execution proof")
	}
	for _, record := range records {
		if record.Kind != "engineering" {
			continue
		}
		if err := store.ResolveRecovery(t.Context(), owner, c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: record.ID, ProofID: terminalNative.ID, Reason: in.Reason}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Resume(t.Context(), owner, 1); err == nil {
		t.Fatal("interruption waived unrelated unknown effect")
	}
	if _, err := ReconcileMaster(t.Context(), cfg, client, nativeObserveFailure{native}, true); err == nil {
		t.Fatal("guest accepted unknown effect")
	}
	// Only this test-created effect receives its own explicit acknowledged proof.
	action.State = "completed"
	if err := saveNativeAction(cfg, action); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileMaster(t.Context(), cfg, client, nativeObserveFailure{native}, false); err != nil {
		t.Fatal(err)
	}
	records, err = store.RecoveryEvidence(t.Context(), owner, bird.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var effectProof string
	for _, record := range records {
		if record.Kind == "actions" && record.Terminal {
			effectProof = record.ID
		}
	}
	for _, record := range records {
		if record.Kind != "actions" {
			continue
		}
		if err := store.ResolveRecovery(t.Context(), owner, c.RecoveryResolution{RestoreEpoch: 1, AgentID: bird.ID, EvidenceID: record.ID, ProofID: effectProof, Reason: "Independent acknowledged test-action evidence"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReconcileMaster(t.Context(), cfg, client, nativeObserveFailure{native}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Resume(t.Context(), owner, 1); err != nil {
		t.Fatal(err)
	}
	saved, err := loadNativeExecution(cfg, job.ID)
	beforeJob, _ := json.Marshal(job)
	afterJob, _ := json.Marshal(saved.Job)
	if err != nil || string(beforeJob) != string(afterJob) || saved.Abandonment.Effects != "unknown" || saved.Abandonment.Outcome != "unknown" {
		t.Fatal("recovery rewrote missing outcome", err)
	}
	if native.calls != 0 || native.aborts != 0 {
		t.Fatal("recovery submitted or replayed a native turn")
	}
}
