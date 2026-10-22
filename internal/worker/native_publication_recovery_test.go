package worker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativePublicationRecoveryRefusesUnsafeEvidence(t *testing.T) {
	for _, fault := range []string{"uncertain", "unstarted", "wrong-kind", "missing-head", "changed-input", "missing-intent", "foreign-session", "running", "worker-lock", "producer-lock", "other-engineering"} {
		t.Run(fault, func(t *testing.T) {
			cfg, m, cat, code := dispatchFixture(t)
			v := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "blocked", Reason: "native_completed_without_result"}
			x.Job.Kind = "fix"
			x.Job.SessionID, x.Job.MessageID = "ses_publication", "msg_publication"
			x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now()
			code.running = false
			in := NativeAction{Operation: "publish-pr", RequestID: "original-publication", Title: "Original repair"}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			r := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "prepared", Step: "push", PublishedHeadSHA: strings.Repeat("b", 40)}
			if fault != "missing-intent" {
				if err := persistNativeActionIntent(cfg, x, in); err != nil {
					t.Fatal(err)
				}
			}
			session := x.Job.SessionID
			switch fault {
			case "uncertain":
				r.State = "uncertain"
			case "unstarted":
				r.Step = ""
			case "wrong-kind":
				x.Job.Kind = "deliver"
			case "missing-head":
				r.PublishedHeadSHA = ""
			case "changed-input":
				in.Title = "Replacement repair"
			case "foreign-session":
				session = "ses_foreign"
			case "running":
				code.running = true
			case "worker-lock":
				release, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
			case "producer-lock":
				release, err := lockNativeProducer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
			case "other-engineering":
				other := x
				other.Job.ID, other.Phase = BodyHash("other-active-publication"), "active"
				if err := saveNativeExecution(cfg, other); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			if err := saveNativeAction(cfg, r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(nativeExecutionPath(cfg, x.Job.ID))
			if err != nil {
				t.Fatal(err)
			}
			journal, err := os.ReadFile(nativeActionPath(cfg, x.Job.ID, r.RequestID))
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			g := NewGitHubClient(&fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
				requests++
				return CommandResult{}, c.ErrForbidden
			}})
			if _, err := RecoverNativePublication(t.Context(), cfg, m, g, code, x.Job.ID, session, x.Job.MessageID, in); err == nil {
				t.Fatal("unsafe publication recovery accepted")
			}
			after, err := os.ReadFile(nativeExecutionPath(cfg, x.Job.ID))
			if err != nil {
				t.Fatal(err)
			}
			afterJournal, err := os.ReadFile(nativeActionPath(cfg, x.Job.ID, r.RequestID))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || string(journal) != string(afterJournal) || requests != 0 || code.calls != 0 || code.aborts != 0 {
				t.Fatal("refusal changed evidence or crossed a remote boundary")
			}
		})
	}
}
