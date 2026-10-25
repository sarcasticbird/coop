package worker

import (
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseCannotBypassSourceActionRecovery(t *testing.T) {
	for _, state := range []string{"uncertain", "prepared", "completed", "no-effect-terminal", "active"} {
		t.Run(state, func(t *testing.T) {
			cfg, _, cat, _ := dispatchFixture(t)
			task := cat.catalog.Tasks[0]
			goal := task.Binding.Goal
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: goal, Phase: "blocked", Reason: "native_completed_without_result", Job: Job{ID: BodyHash("source-delivery"), TaskURL: task.Task.URL, Kind: "deliver", Phase: "completed", SubmissionState: "completed", TerminalAt: time.Now(), SessionID: "ses_source", MessageID: "msg_source", Master: &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ExecutorID: cfg.Self.ID, Goal: &goal}}}
			if state == "active" {
				x.Phase = "active"
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			if state != "no-effect-terminal" && state != "active" {
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "merge", Operation: "deliver", State: state}); err != nil {
					t.Fatal(err)
				}
			}
			err := releaseSourceSettled(cfg, task.Task.URL)
			allowed := state == "completed" || state == "no-effect-terminal"
			if (err == nil) != allowed {
				t.Fatalf("state %s source recovery bypass/misclassification: %v", state, err)
			}
		})
	}
}

func TestRetiredKnownReleaseCheckpointCannotResumeWrite(t *testing.T) {
	cfg, m, _, _ := dispatchFixture(t)
	f := &releaseTestAPI{}
	h := c.Handoff{ID: "ho_retired", Input: c.HandoffInput{Goal: c.GoalRef{Source: "master", MasterID: cfg.Master.MasterID, GoalID: "source", RevisionSHA256: BodyHash("source")}, Tag: "v0.2.7", Workflow: "release.yml"}}
	target := releaseTarget{Repository: "code/repo", SHA: strings.Repeat("c", 40)}
	calls := 0
	_, err := publishNativeRelease(t.Context(), cfg, f, h, target, func() error {
		calls++
		if calls == 2 {
			return c.ErrForbidden
		}
		return nil
	})
	if !errors.Is(err, c.ErrForbidden) {
		t.Fatal("known pre-ref checkpoint not retained", err)
	}
	records, _, err := gatherMasterRecovery(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1, Records: records}
	if err = atomicPrivate(filepath.Join(masterDir(cfg), "recovery", "accepted.json"), mustRecoveryJSON(snapshot)); err != nil {
		t.Fatal(err)
	}
	before := len(f.calls)
	if _, err = publishNativeRelease(t.Context(), cfg, f, h, target); !errors.Is(err, c.ErrEpoch) || len(f.calls) != before {
		t.Fatal("retired checkpoint resumed a tag-ref write", err)
	}
	m.self.RestoreEpoch++
	r, err := ObserveNativeRelease(t.Context(), cfg, m, NewGitHubClient(&fakeCommands{}), h.Input.Goal, h.ID)
	if err != nil || r.Phase != "checkpoint" || r.Reason != "tag_reference_not_started" {
		t.Fatal("known checkpoint missing read-only disposition", r, err)
	}
}

func TestReleaseJournalsParticipateInRecovery(t *testing.T) {
	for _, state := range []string{"uncertain", "object-created", "waiting", "published"} {
		t.Run(state, func(t *testing.T) {
			cfg, _, _, _ := dispatchFixture(t)
			f := &releaseTestAPI{published: state == "published"}
			if state == "uncertain" {
				f.fail = "repos/code/repo/git/tags"
			}
			h := c.Handoff{ID: "ho_recovery", Input: c.HandoffInput{Tag: "v0.2.7", Workflow: "release.yml"}}
			guards := []func() error{}
			if state == "object-created" {
				calls := 0
				guards = append(guards, func() error {
					calls++
					if calls == 2 {
						return c.ErrForbidden
					}
					return nil
				})
			}
			_, err := publishNativeRelease(t.Context(), cfg, f, h, releaseTarget{Repository: "code/repo", SHA: strings.Repeat("c", 40)}, guards...)
			if state != "uncertain" && state != "object-created" && err != nil {
				t.Fatal(err)
			}
			records, _, err := gatherMasterRecovery(t.Context(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range records {
				if r.Kind == "releases" {
					found = true
					if r.Terminal != (state != "uncertain") || r.Uncertain != (state == "uncertain") {
						t.Fatal("release recovery could retire pending writes/publication", r)
					}
				}
			}
			if !found {
				t.Fatal("release journal absent from recovery manifest")
			}
		})
	}
}
