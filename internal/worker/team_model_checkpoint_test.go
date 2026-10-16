package worker

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

type modelCheckpointCode struct {
	*fakeOpenCode
	native     *OpenCodeClient
	observed   []Job
	observeErr error
}

func (c *modelCheckpointCode) EnsureSession(ctx context.Context, cfg Config, j Job) (Job, error) {
	j, err := c.fakeOpenCode.EnsureSession(ctx, cfg, j)
	// A new dispatch must not checkpoint a previous turn's executed identity.
	j.ExecutedModel = &ModelIdentity{ProviderID: "stale", ModelID: "previous", Variant: "low"}
	return j, err
}

func (c *modelCheckpointCode) Observe(ctx context.Context, j Job) (Turn, error) {
	c.observed = append(c.observed, j)
	turn, err := c.native.Observe(ctx, j)
	c.observeErr = err
	return turn, err
}

func TestTeamModelCheckpointIdentitySurvivesRestart(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, scenario := range []struct {
			name               string
			lost, fail, commit bool
		}{
			{name: "lost response", lost: true},
			{name: "accepted post-save failure", fail: true},
			{name: "accepted ambiguous post-save", fail: true, commit: true},
			{name: "lost response post-save failure", lost: true, fail: true},
			{name: "lost response ambiguous post-save", lost: true, fail: true, commit: true},
		} {
			t.Run(map[bool]string{false: "engine", true: "wake"}[wake]+"/"+scenario.name, func(t *testing.T) {
				e, base, clock, wakes := wakeEngineFixture(t)
				github := e.GitHub.(*wakeGitHub)
				e.Config.ModelVariant = "high"
				e.Wakes.Config = e.Config
				base.running, base.lost = true, scenario.lost
				code := &modelCheckpointCode{fakeOpenCode: base}
				e.OpenCode, e.Wakes.OpenCode = code, code
				stateDir := filepath.Join(t.TempDir(), "state")
				fileStore := NewFileStore(stateDir)
				checkpoint := &checkpointStore{Store: fileStore, commit: scenario.commit}
				e.Store = checkpoint
				requestID := ""
				if wake {
					q, err := QueuePing(e.Config, wakes, PingInput{Prompt: "durable model selection"}, clock.Now())
					if err != nil {
						t.Fatal(err)
					}
					requestID = q.ID
					if scenario.fail {
						e.Wakes.Store = &checkpointWakeStore{WakeStore: wakes, post: true, commit: scenario.commit}
					}
				} else {
					_, github.snapshot, _ = policyFixture(t)
					github.snapshot.Cursor = map[string]string{}
					e.Wakes = nil
					if scenario.fail {
						checkpoint.fail = func(s State) bool {
							for _, j := range s.Jobs {
								if j.Phase == "active" && j.SubmissionState != "submitting" {
									return true
								}
							}
							return false
						}
					}
				}
				// Always reopen the real files, bypassing the failure wrapper and live state.
				loadJob := func(id string) Job {
					t.Helper()
					if wake {
						v, err := NewWakeStore(wakes.Dir).Receipt(requestID)
						if err != nil {
							t.Fatal(err)
						}
						return v.Job
					}
					s, err := NewFileStore(stateDir).Load()
					if err != nil {
						t.Fatal(err)
					}
					return s.Jobs[id]
				}
				want := ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}
				checkedBeforePOST := false
				base.effect = func(_ Config, j Job) {
					saved := loadJob(j.ID)
					if saved.SubmissionState != "submitting" || saved.MessageID != j.MessageID || saved.SessionID != j.SessionID || saved.SubmittedModel == nil || *saved.SubmittedModel != want || saved.ExecutedModel != nil {
						t.Fatalf("pre-POST checkpoint lost selection or retained stale execution: %+v", saved)
					}
					checkedBeforePOST = true
				}
				var err error
				if wake {
					err = e.Wakes.Tick(context.Background(), Snapshot{Complete: true})
				} else {
					err = e.Tick(context.Background())
				}
				if (err != nil) != scenario.fail || (scenario.fail && !wake && !errors.Is(err, errCheckpoint)) {
					t.Fatalf("submission checkpoint error = %v", err)
				}
				if !checkedBeforePOST || base.calls != 1 || len(code.observed) != 0 {
					t.Fatalf("expected one submission and no native observation: checked=%t calls=%d observations=%d", checkedBeforePOST, base.calls, len(code.observed))
				}
				saved := loadJob(base.last.ID)
				wantState := "accepted"
				if scenario.lost {
					wantState = "unknown"
				}
				if scenario.fail && !scenario.commit {
					wantState = "submitting"
				}
				if saved.SubmissionState != wantState || saved.SubmittedModel == nil || *saved.SubmittedModel != want || saved.ExecutedModel != nil {
					t.Fatalf("durable checkpoint after failure = %+v, want state %s and model %+v", saved, wantState, want)
				}
				// Native user and assistant agree on low, so only the checkpoint can
				// establish that the original submission actually requested high.
				nativeReads := 0
				_, native := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
					nativeReads++
					user := map[string]any{"id": saved.MessageID, "sessionID": saved.SessionID, "role": "user", "model": ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "low"}}
					switch r.URL.Path {
					case "/session/" + saved.SessionID + "/message/" + saved.MessageID:
						writeJSON(w, map[string]any{"info": user})
					case "/session/" + saved.SessionID + "/message":
						writeJSON(w, []any{map[string]any{"info": user}, map[string]any{"info": map[string]any{"id": "msg_native", "sessionID": saved.SessionID, "role": "assistant", "parentID": saved.MessageID, "providerID": "example", "modelID": "model", "variant": "low", "finish": "stop", "time": map[string]int{"created": 1, "completed": 2}}}})
					default:
						t.Errorf("unexpected restart request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				})
				freshCode := &modelCheckpointCode{fakeOpenCode: &fakeOpenCode{}, native: native}
				cfg := e.Config
				cfg.ModelVariant = "low"
				// Discard both the original engine and client, including their model pointers.
				base.last.SubmittedModel.Variant = "corrupted-memory"
				clock.now = clock.now.Add(cfg.OpenCode.ReconcileInterval + time.Second)
				if wake {
					restarted := NewWakeCoordinator(cfg, NewWakeStore(wakes.Dir), github, freshCode, clock)
					err = restarted.Tick(context.Background(), Snapshot{Complete: true})
				} else {
					restarted := &Engine{Config: cfg, GitHub: github, Store: NewFileStore(stateDir), Checkouts: e.Checkouts, OpenCode: freshCode, Clock: clock}
					err = restarted.Tick(context.Background())
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(freshCode.observed) == 0 || nativeReads == 0 || errorCategory(freshCode.observeErr) != "submitted_model_mismatch" || freshCode.calls != 0 || freshCode.aborts != 0 {
					t.Fatalf("restart failed to reconcile original intent without replay: observations=%d reads=%d error=%v calls=%d aborts=%d", len(freshCode.observed), nativeReads, freshCode.observeErr, freshCode.calls, freshCode.aborts)
				}
				for _, j := range freshCode.observed {
					if j.SubmittedModel == nil || *j.SubmittedModel != want || j.MessageID != saved.MessageID || j.SessionID != saved.SessionID {
						t.Fatalf("restart observation did not receive checkpoint identity: %+v", j)
					}
				}
				after := loadJob(saved.ID)
				if after.SubmittedModel == nil || *after.SubmittedModel != want || after.ReconcileError != "submitted_model_mismatch" || after.Phase == "completed" {
					t.Fatalf("native echo overwrote checkpoint or falsely completed turn: %+v", after)
				}
			})
		}
	}
}
