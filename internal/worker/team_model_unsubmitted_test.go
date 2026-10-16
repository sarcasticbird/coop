package worker

import (
	"context"
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
