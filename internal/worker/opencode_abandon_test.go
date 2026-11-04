package worker

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExactNativeRecoveryRuntimeHealth(t *testing.T) {
	for _, version := range []string{SupportedOpenCodeVersion, SupportedOpenCodeRecoveryVersion, "1.18.18-coop-recovery.1", "1.18.18-coop-recovery.3", "1.18.18-other", "1.18.19", ""} {
		t.Run(version, func(t *testing.T) {
			_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/global/health" {
					t.Fatal("unexpected health endpoint")
				}
				writeJSON(w, ServerHealth{Healthy: true, Version: version})
			})
			health, err := client.Health(t.Context())
			want := version == SupportedOpenCodeVersion || version == SupportedOpenCodeRecoveryVersion
			if (err == nil) != want || supportedNativeHealth(health) != want {
				t.Fatal("runtime support drift", version, err)
			}
			if supportedNativeHealth(ServerHealth{Healthy: false, Version: version}) {
				t.Fatal("unhealthy server accepted")
			}
		})
	}
}

func TestOpenCodeAbandonmentFenceProtocol(t *testing.T) {
	for _, scenario := range []string{"new-seal", "already-sealed", "native-missing-receipt", "generic-receipt-conflict", "foreign-missing-receipt", "near-miss-missing-receipt", "lost-ack", "message-present", "message-forbidden", "scope-drift", "scope-claims", "busy-child", "foreign-receipt", "tampered-snapshot", "foreign-child", "generic-conflict", "foreign-conflict", "bad-success", "old-receipt-as-new", "post-fence-drift", "lost-ack-scope-drift"} {
		t.Run(scenario, func(t *testing.T) {
			var job Job
			var scope nativeScope
			var prior, fresh nativeInterruptionReceipt
			posts, scopeReads := 0, 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("directory") != job.Checkout {
					t.Error("missing exact checkout context")
				}
				root := "/session/ses_missing/recovery"
				switch r.URL.Path {
				case "/session/ses_missing":
					writeJSON(w, map[string]any{"id": job.SessionID, "directory": job.Checkout})
				case "/session/status":
					statuses := map[string]any{}
					if scenario == "busy-child" {
						statuses["ses_child"] = map[string]string{"type": "busy"}
					}
					writeJSON(w, statuses)
				case "/session/ses_missing/message/msg_missing":
					if scenario == "message-present" {
						writeJSON(w, map[string]any{"info": map[string]string{"id": "msg_missing"}})
						return
					}
					if scenario == "message-forbidden" {
						w.WriteHeader(403)
						return
					}
					w.WriteHeader(404)
				case root + "/abandon-test":
					if strings.Contains(scenario, "missing-receipt") || scenario == "generic-receipt-conflict" {
						resource, message := job.SessionID, "missing_receipt"
						if scenario == "foreign-missing-receipt" {
							resource = "ses_foreign"
						}
						if scenario == "near-miss-missing-receipt" {
							message = "missing_receipt_extra"
						}
						if scenario == "generic-receipt-conflict" {
							message = "scope_changed"
						}
						w.WriteHeader(409)
						writeJSON(w, map[string]string{"_tag": "ConflictError", "message": message, "resource": resource})
					} else if scenario == "lost-ack" || scenario == "lost-ack-scope-drift" || scenario == "old-receipt-as-new" {
						r := fresh
						if scenario == "old-receipt-as-new" {
							r.TimeCreated = job.SubmittedAt.Add(-time.Hour).UnixMilli()
						}
						writeJSON(w, r)
					} else {
						w.WriteHeader(404)
					}
				case root + "/prior-test":
					writeJSON(w, prior)
				case root:
					if r.Method == "GET" {
						scopeReads++
						v := scope
						if scenario == "scope-drift" || scenario == "post-fence-drift" && scopeReads > 1 {
							v.SHA = BodyHash("changed")
						}
						if scenario == "scope-claims" {
							v.HasDurableClaims = true
						}
						if scenario == "lost-ack-scope-drift" {
							v.SessionIDs = append(v.SessionIDs, "ses_foreign")
						}
						writeJSON(w, v)
						return
					}
					if r.Method != "POST" {
						t.Fatal("unexpected recovery method")
					}
					posts++
					var input struct {
						ID             string
						ExpectedSHA256 string
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.ID != "abandon-test" || input.ExpectedSHA256 != scope.SHA {
						t.Fatal("fence input drift")
					}
					if scenario == "new-seal" || scenario == "bad-success" {
						v := fresh
						if scenario == "bad-success" {
							v.Effects = "none"
						}
						writeJSON(w, v)
						return
					}
					w.WriteHeader(409)
					resource, message := job.SessionID, "already_dispositioned"
					if scenario == "generic-conflict" {
						message = "scope_changed"
					}
					if scenario == "foreign-conflict" {
						resource = "ses_other"
					}
					writeJSON(w, map[string]string{"_tag": "ConflictError", "message": message, "resource": resource})
				default:
					t.Error("unexpected endpoint (no prompt/continuation/abort/tool allowed):", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			})
			job = Job{SessionID: "ses_missing", MessageID: "msg_missing", Checkout: filepath.Join(cfg.WorkRoot, "missing"), SubmittedAt: time.Now().Add(-time.Hour)}
			snapshot := func(childParent string) string {
				raw, _ := json.Marshal(map[string]any{"sessions": []any{map[string]any{"id": "ses_missing", "directory": job.Checkout, "parent_id": nil}, map[string]any{"id": "ses_child", "directory": job.Checkout, "parent_id": childParent}}})
				return string(raw)
			}
			fresh = nativeInterruptionReceipt{ID: "abandon-test", SessionID: job.SessionID, TimeCreated: time.Now().UnixMilli(), Snapshot: snapshot(job.SessionID), Disposition: "interrupted", Effects: "unknown"}
			fresh.ExpectedSHA256 = BodyHash(fresh.Snapshot)
			prior = fresh
			prior.ID, prior.TimeCreated = "prior-test", job.SubmittedAt.Add(-time.Hour).UnixMilli()
			if scenario == "foreign-receipt" {
				prior.SessionID = "ses_foreign"
			}
			if scenario == "tampered-snapshot" {
				prior.Snapshot += " "
			}
			if scenario == "foreign-child" {
				prior.Snapshot = snapshot("ses_foreign")
				prior.ExpectedSHA256 = BodyHash(prior.Snapshot)
			}
			scope = nativeScope{SHA: fresh.ExpectedSHA256, SessionIDs: []string{job.SessionID, "ses_child"}, MessageID: "msg_previous"}
			proof, err := client.FenceNativeScope(t.Context(), job, "abandon-test", scope.SHA, "prior-test")
			success := scenario == "new-seal" || scenario == "already-sealed" || scenario == "native-missing-receipt" || scenario == "lost-ack"
			if success {
				if err != nil || proof.SessionID != job.SessionID || proof.ScopeSHA != scope.SHA || proof.VerifiedAt.IsZero() {
					t.Fatal("valid fence refused", err)
				}
				wantPosts := 1
				if scenario == "lost-ack" {
					wantPosts = 0
				}
				if posts != wantPosts {
					t.Fatal("acknowledged fence repeated", posts)
				}
			} else if err == nil {
				t.Fatal("unsafe native fence accepted", scenario)
			}
			if !success && posts > 0 && !strings.Contains(scenario, "conflict") && scenario != "bad-success" && scenario != "post-fence-drift" {
				t.Fatal("unsafe input reached native fence", scenario)
			}
		})
	}
}
