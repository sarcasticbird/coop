package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

type modelUnsubmittedCode struct {
	*fakeOpenCode
	native       *OpenCodeClient
	observations int
}

func (c *modelUnsubmittedCode) Submit(ctx context.Context, cfg Config, j Job, prompt string) (Turn, error) {
	c.calls++
	c.last = j
	return c.native.Submit(ctx, cfg, j, prompt)
}

func (c *modelUnsubmittedCode) Observe(ctx context.Context, j Job) (Turn, error) {
	c.observations++
	return c.native.Observe(ctx, j)
}

func TestTeamModelKnownUnsubmittedNeverReconciles(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, reason := range []string{"configured_model_variant_unavailable", "unsupported_model_variant_API"} {
			t.Run(map[bool]string{false: "engine", true: "wake"}[wake]+"/"+reason, func(t *testing.T) {
				e, base, clock, wakes := wakeEngineFixture(t)
				github := e.GitHub.(*wakeGitHub)
				e.Config.ModelVariant = "high"
				e.Wakes.Config = e.Config
				taskPosts := 0
				_, native := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/session/" + base.last.SessionID:
						writeJSON(w, map[string]string{"id": base.last.SessionID, "directory": base.last.Checkout})
					case "/session/status":
						writeJSON(w, map[string]any{})
					case "/global/health":
						writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
					case "/doc":
						if reason == "unsupported_model_variant_API" {
							writeJSON(w, map[string]any{})
						} else {
							writeJSON(w, variantSchemaFixture())
						}
					case "/config/providers":
						if r.URL.Query().Get("directory") != base.last.Checkout {
							t.Error("submission checked the wrong directory")
						}
						writeJSON(w, map[string]any{"providers": []any{map[string]any{"id": "example", "models": map[string]any{"model": map[string]any{}}}}})
					case "/session/" + base.last.SessionID + "/prompt_async":
						taskPosts++
						w.WriteHeader(500)
					default:
						t.Errorf("known rejection accessed nonexistent worker message or unexpected endpoint: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				})
				code := &modelUnsubmittedCode{fakeOpenCode: base, native: native}
				e.OpenCode, e.Wakes.OpenCode = code, code
				stateDir := filepath.Join(t.TempDir(), "state")
				e.Store = NewFileStore(stateDir)
				requestID := ""
				if wake {
					q, err := QueuePing(e.Config, wakes, PingInput{Prompt: "reject without native recovery"}, clock.Now())
					if err != nil {
						t.Fatal(err)
					}
					requestID = q.ID
					if err := e.Wakes.Tick(context.Background(), Snapshot{Complete: true}); err != nil {
						t.Fatal(err)
					}
				} else {
					_, github.snapshot, _ = policyFixture(t)
					github.snapshot.Cursor = map[string]string{}
					e.Wakes = nil
					if err := e.Tick(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				assertBlocked := func() {
					t.Helper()
					var j Job
					var blockedReason string
					if wake {
						v, err := NewWakeStore(wakes.Dir).Receipt(requestID)
						if err != nil {
							t.Fatal(err)
						}
						j, blockedReason = v.Job, v.Reason
						if v.Phase != "blocked" {
							t.Fatalf("wake not durably blocked: %+v", v)
						}
					} else {
						s, err := NewFileStore(stateDir).Load()
						if err != nil {
							t.Fatal(err)
						}
						j = s.Jobs[base.last.ID]
						blockedReason = j.WaitReason
					}
					if j.Phase != "blocked" || blockedReason != reason || j.Attempts != 0 || j.ReconcileFailures != 0 || j.ReconcileError != "" {
						t.Fatalf("known unsubmitted rejection consumed attempts or entered recovery: %+v, reason=%s", j, blockedReason)
					}
					if code.calls != 1 || code.observations != 0 || code.aborts != 0 || taskPosts != 0 {
						t.Fatalf("known rejection replayed/reconciled: submissions=%d observations=%d aborts=%d taskPOSTs=%d", code.calls, code.observations, code.aborts, taskPosts)
					}
				}
				assertBlocked()
				for range e.Config.Limits.MaxAttempts + 1 {
					clock.now = clock.now.Add(e.Config.Limits.TurnLimit + e.Config.Limits.RetryCap)
					if wake {
						restarted := NewWakeCoordinator(e.Config, NewWakeStore(wakes.Dir), github, code, clock)
						if err := restarted.Tick(context.Background(), Snapshot{Complete: true}); err != nil {
							t.Fatal(err)
						}
					} else {
						restarted := &Engine{Config: e.Config, Store: NewFileStore(stateDir), GitHub: github, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
						if err := restarted.Tick(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
					assertBlocked()
				}
			})
		}
	}
}

func TestTeamModelPostProbeTakeoverClearsUnsubmittedIdentity(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(map[bool]string{false: "engine", true: "wake"}[wake], func(t *testing.T) {
			e, base, clock, wakes := wakeEngineFixture(t)
			github := e.GitHub.(*wakeGitHub)
			e.Config.ModelVariant = "high"
			e.Wakes.Config = e.Config
			probeID := ""
			probes, deletes, idleChecks, taskPosts := 0, 0, 0, 0
			_, native := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/session/" + base.last.SessionID:
					writeJSON(w, map[string]string{"id": base.last.SessionID, "directory": base.last.Checkout})
				case "/session/status":
					idleChecks++
					if probes > 0 {
						writeJSON(w, map[string]any{base.last.SessionID: map[string]string{"type": "busy"}})
					} else {
						writeJSON(w, map[string]any{})
					}
				case "/global/health":
					writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
				case "/doc":
					writeJSON(w, variantSchemaFixture())
				case "/config/providers":
					writeJSON(w, map[string]any{"providers": []any{map[string]any{"id": "example", "models": map[string]any{"model": map[string]any{"variants": map[string]any{"high": map[string]any{}}}}}}})
				case "/session":
					writeJSON(w, map[string]string{"id": "ses_probe", "directory": base.last.Checkout, "title": "coop-model-preflight"})
				case "/session/ses_probe/prompt_async":
					var body struct {
						MessageID string
						NoReply   bool
						Variant   string
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.NoReply || body.Variant != "high" {
						t.Errorf("unsafe probe: %+v %v", body, err)
					}
					probeID = body.MessageID
					probes++
					w.WriteHeader(204)
				case "/session/ses_probe/message/" + probeID:
					writeJSON(w, map[string]any{"info": map[string]any{"id": probeID, "sessionID": "ses_probe", "role": "user", "model": ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}}})
				case "/session/ses_probe":
					if r.Method != "DELETE" {
						t.Error("probe cleanup must delete only its disposable session")
					}
					deletes++
					writeJSON(w, true)
				case "/session/" + base.last.SessionID + "/prompt_async":
					taskPosts++
					w.WriteHeader(500)
				default:
					t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			})
			code := &modelUnsubmittedCode{fakeOpenCode: base, native: native}
			e.OpenCode, e.Wakes.OpenCode = code, code
			stateDir := filepath.Join(t.TempDir(), "state")
			e.Store = NewFileStore(stateDir)
			var j Job
			if wake {
				r, err := QueuePing(e.Config, wakes, PingInput{Prompt: "wait for manual takeover"}, clock.Now())
				if err != nil {
					t.Fatal(err)
				}
				if err := e.Wakes.Tick(t.Context(), Snapshot{Complete: true}); err != nil {
					t.Fatal(err)
				}
				v, err := NewWakeStore(wakes.Dir).Receipt(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if v.Phase != "queued" || v.Reason != "session_busy" {
					t.Fatalf("wake did not yield to manual work: %+v", v)
				}
				j = v.Job
			} else {
				_, github.snapshot, _ = policyFixture(t)
				github.snapshot.Cursor = map[string]string{}
				e.Wakes = nil
				if err := e.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				s, err := NewFileStore(stateDir).Load()
				if err != nil {
					t.Fatal(err)
				}
				j = s.Jobs[base.last.ID]
				if j.WaitReason != "session_busy" || e.status.Current == nil || e.status.Current.SubmittedModel != nil || e.status.Current.ExecutedModel != nil {
					t.Fatalf("status claims submission after manual takeover: %+v", e.status.Current)
				}
			}
			if j.Phase != "waiting" || j.MessageID != "" || j.SubmissionState != "" || !j.SubmittedAt.IsZero() || !j.Deadline.IsZero() || j.SubmittedModel != nil || j.ExecutedModel != nil || j.Attempts != 0 || j.ReconcileFailures != 0 {
				t.Fatalf("known-unsubmitted busy retry retains model evidence: %+v", j)
			}
			if probes != 1 || deletes != 1 || idleChecks != 2 || taskPosts != 0 || code.observations != 0 || code.aborts != 0 {
				t.Fatalf("takeover mutated manual work or submitted: probes=%d deletes=%d idle=%d posts=%d observations=%d aborts=%d", probes, deletes, idleChecks, taskPosts, code.observations, code.aborts)
			}
		})
	}
}
