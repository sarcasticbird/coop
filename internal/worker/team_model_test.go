package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func variantSchemaFixture() map[string]any {
	props := map[string]any{"variant": map[string]string{"type": "string"}, "noReply": map[string]string{"type": "boolean"}}
	post := map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"properties": props}}}}}}
	paths := map[string]any{}
	for _, path := range []string{"/session", "/session/status", "/session/{sessionID}/message/{messageID}", "/session/{sessionID}/abort", "/permission", "/question", "/agent"} {
		paths[path] = map[string]any{"get": map[string]any{}}
	}
	paths["/session/{sessionID}/prompt_async"], paths["/session/{sessionID}/message"] = post, post
	return map[string]any{"paths": paths, "components": map[string]any{"schemas": map[string]any{
		"UserMessage":      map[string]any{"properties": map[string]any{"model": map[string]any{"properties": props}}},
		"AssistantMessage": map[string]any{"properties": props},
	}}}
}

func TestTeamModelVariantConfig(t *testing.T) {
	for _, variant := range []string{"", "high", "xhigh", "custom.variant-1", "bad variant", "bad\nvariant", "a/b", strings.Repeat("a", 129)} {
		t.Run(variant, func(t *testing.T) {
			cfg := Defaults(configFixture(t))
			cfg.ModelVariant = variant
			wantValid := variant == "" || variant == "high" || variant == "xhigh" || variant == "custom.variant-1"
			if err := ValidateConfig(cfg); (err == nil) != wantValid {
				t.Fatalf("validation = %v", err)
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Config
			if err := strictJSON(raw, &decoded); err != nil || decoded.ModelVariant != variant {
				t.Fatalf("variant round trip = %q, %v", decoded.ModelVariant, err)
			}
			if variant == "" && strings.Contains(string(raw), "model_variant") {
				t.Fatal("default config gained a variant")
			}
		})
	}
}

func TestTeamModelVariantPreflightAndSubmission(t *testing.T) {
	for _, mode := range []string{"supported", "missing schema", "wrong field type", "missing metadata schema", "unavailable", "dropped", "wrong model", "cleanup failure", "checkout unavailable", "checkout dropped", "checkout takeover", "POST error"} {
		t.Run(mode, func(t *testing.T) {
			dir, root := "", ""
			probeDirectory := ""
			probeID := ""
			probes, deletes, submits := 0, 0, 0
			idleChecks := 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/config/providers" || r.URL.Path == "/session" || strings.HasPrefix(r.URL.Path, "/session/ses_probe") {
					if got := r.URL.Query().Get("directory"); got != probeDirectory {
						t.Errorf("%s directory = %q, want %q", r.URL.Path, got, probeDirectory)
					}
				}
				switch r.URL.Path {
				case "/global/health":
					writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
				case "/doc":
					doc := variantSchemaFixture()
					if mode == "missing schema" {
						doc["paths"].(map[string]any)["/session/{sessionID}/prompt_async"] = map[string]any{"post": map[string]any{}}
					}
					if mode == "wrong field type" {
						doc["components"].(map[string]any)["schemas"].(map[string]any)["AssistantMessage"].(map[string]any)["properties"].(map[string]any)["variant"] = map[string]string{"type": "boolean"}
					}
					if mode == "missing metadata schema" {
						delete(doc["components"].(map[string]any)["schemas"].(map[string]any), "AssistantMessage")
					}
					writeJSON(w, doc)
				case "/config/providers":
					variants := map[string]any{"high": map[string]any{"reasoningEffort": "high"}}
					if mode == "unavailable" || (mode == "checkout unavailable" && r.URL.Query().Get("directory") == dir) {
						variants = nil
					}
					writeJSON(w, map[string]any{"providers": []any{map[string]any{"id": "example", "models": map[string]any{"model": map[string]any{"variants": variants}}}}})
				case "/session":
					if r.Method != "POST" {
						t.Error("probe session must be created separately")
					}
					writeJSON(w, map[string]string{"id": "ses_probe", "directory": probeDirectory, "title": "coop-model-preflight"})
				case "/session/ses_probe/prompt_async":
					probes++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["noReply"] != true || body["variant"] != "high" {
						t.Errorf("unsafe or incomplete probe: %v, %v", body, err)
					}
					probeID, _ = body["messageID"].(string)
					w.WriteHeader(204)
				case "/session/ses_probe/message/" + probeID:
					model := map[string]string{"providerID": "example", "modelID": "model", "variant": "high"}
					if mode == "dropped" || (mode == "checkout dropped" && r.URL.Query().Get("directory") == dir) {
						delete(model, "variant")
					}
					if mode == "wrong model" {
						model["modelID"] = "other"
					}
					writeJSON(w, map[string]any{"info": map[string]any{"id": probeID, "sessionID": "ses_probe", "role": "user", "model": model}})
				case "/session/ses_probe":
					if r.Method != "DELETE" {
						t.Error("probe cleanup must delete only its disposable session")
					}
					deletes++
					if mode == "cleanup failure" {
						w.WriteHeader(500)
						return
					}
					writeJSON(w, true)
				case "/session/ses_task":
					writeJSON(w, map[string]string{"id": "ses_task", "directory": dir})
				case "/session/status":
					idleChecks++
					if mode == "checkout takeover" && probes == 2 {
						writeJSON(w, map[string]any{"ses_task": map[string]string{"type": "busy"}})
					} else {
						writeJSON(w, map[string]any{})
					}
				case "/session/ses_task/prompt_async":
					submits++
					if mode == "POST error" {
						w.WriteHeader(500)
						return
					}
					var body struct {
						Model     map[string]string
						Variant   string
						MessageID string
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Variant != "high" || body.Model["variant"] != "" || body.Model["providerID"] != "example" || body.Model["modelID"] != "model" || body.MessageID != "msg_worker" {
						t.Errorf("incorrect wire model: %+v, %v", body, err)
					}
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			})
			cfg.ModelVariant = "high"
			root, dir = cfg.WorkRoot, filepath.Join(cfg.WorkRoot, "task")
			probeDirectory = root
			preflightErr := client.preflightModelVariant(context.Background(), cfg)
			if (preflightErr == nil) != (mode == "supported" || mode == "POST error" || strings.HasPrefix(mode, "checkout ")) {
				t.Fatalf("variant preflight = %v", preflightErr)
			}
			j := Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: dir, Deadline: time.Now().Add(time.Minute)}
			probeDirectory = dir
			turn, err := client.Submit(context.Background(), cfg, j, "Continue approved work")
			if mode == "supported" {
				if err != nil || !turn.Accepted || submits != 1 || turn.SubmittedModel == nil || turn.SubmittedModel.Variant != "high" || turn.ExecutedModel != nil {
					t.Fatalf("submission = %+v, %v; posts %d", turn, err, submits)
				}
			} else if mode == "POST error" {
				if err == nil || submits != 1 || turn.Accepted || turn.State != "unknown" {
					t.Fatalf("uncertain POST did not retain unknown delivery: %+v, %v, posts=%d", turn, err, submits)
				}
			} else if err == nil || submits != 0 || turn.Accepted || turn.State != "not_submitted" {
				t.Fatalf("unsupported/dropped variant submitted: %+v, %v", turn, err)
			}
			if mode == "checkout unavailable" && errorCategory(err) != "configured_model_variant_unavailable" {
				t.Fatalf("checkout catalog rejection = %v", err)
			}
			if mode == "checkout dropped" && errorCategory(err) != "model_variant_not_preserved" {
				t.Fatalf("checkout probe rejection = %v", err)
			}
			if mode == "checkout takeover" && (errorCategory(err) != "session_busy" || idleChecks != 2) {
				t.Fatalf("post-probe takeover rejection = %v, idle checks %d", err, idleChecks)
			}
			if probes != deletes {
				t.Fatalf("probe cleanup: %d probes, %d deletes", probes, deletes)
			}
			if (mode == "supported" || mode == "checkout dropped") && probes != 2 {
				t.Fatalf("startup and checkout each require a probe, got %d", probes)
			}
			if mode == "checkout unavailable" && probes != 1 {
				t.Fatalf("missing checkout variant must block before probing, got %d probes", probes)
			}
		})
	}
}

func TestTeamModelMismatchAbortOwnership(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, metadata := range []string{"match", "missing", "dropped variant", "different model"} {
			t.Run(metadata+"/"+map[bool]string{false: "owned running mismatch", true: "later manual turn"}[manual], func(t *testing.T) {
				dir := ""
				aborts := 0
				cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
					user := map[string]any{"id": "msg_worker", "sessionID": "ses_task", "role": "user", "model": ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}}
					switch metadata {
					case "missing":
						delete(user, "model")
					case "dropped variant":
						user["model"] = ModelIdentity{ProviderID: "example", ModelID: "model"}
					case "different model":
						user["model"] = ModelIdentity{ProviderID: "example", ModelID: "other", Variant: "high"}
					}
					switch r.URL.Path {
					case "/session/ses_task":
						writeJSON(w, map[string]string{"id": "ses_task", "directory": dir})
					case "/session/ses_task/message/msg_worker":
						writeJSON(w, map[string]any{"info": user})
					case "/session/ses_task/message":
						messages := []any{
							map[string]any{"info": user},
							map[string]any{"info": map[string]any{"id": "msg_reply", "parentID": "msg_worker", "sessionID": "ses_task", "role": "assistant", "providerID": "example", "modelID": "model", "variant": "low", "time": map[string]int{"created": 1}}},
						}
						if manual {
							messages = append(messages, map[string]any{"info": map[string]string{"id": "msg_manual", "sessionID": "ses_task", "role": "user"}})
						}
						writeJSON(w, messages)
					case "/session/ses_task/abort":
						aborts++
						writeJSON(w, true)
					case "/session/status":
						if aborts == 0 {
							writeJSON(w, map[string]any{"ses_task": map[string]string{"type": "busy"}})
						} else {
							writeJSON(w, map[string]any{})
						}
					default:
						t.Errorf("unexpected abort request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				})
				dir = filepath.Join(cfg.WorkRoot, "task")
				expected := &ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}
				job := Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: dir, SubmittedModel: expected}
				turn, latest, err := client.readTurn(context.Background(), job)
				wantLatest := "msg_worker"
				if manual {
					wantLatest = "msg_manual"
				}
				if err != nil || latest != wantLatest || turn.Terminal || turn.ExecutedModel == nil || turn.ExecutedModel.Variant != "low" {
					t.Fatalf("history scan = %+v, %q, %v", turn, latest, err)
				}
				observed, err := client.Observe(context.Background(), job)
				wantCategory := "executed_model_mismatch"
				if metadata != "match" {
					wantCategory = "submitted_model_mismatch"
				}
				if manual {
					wantCategory = "turn_ownership_unknown"
				}
				if errorCategory(err) != wantCategory {
					t.Fatalf("observation = %v, want %s", err, wantCategory)
				}
				if observed.SubmittedModel == nil || *observed.SubmittedModel != *expected || observed.Terminal {
					t.Fatalf("durable submission intent changed or mismatch became terminal: %+v", observed)
				}
				err = client.Abort(context.Background(), job)
				if manual {
					if errorCategory(err) != "turn_ownership_unknown" || aborts != 0 {
						t.Fatalf("manual turn abort: %v, calls %d", err, aborts)
					}
				} else if err != nil || aborts != 1 {
					t.Fatalf("owned mismatch not aborted: %v, calls %d", err, aborts)
				}
			})
		}
	}
}

func TestTeamModelDefaultDoesNotProbeOrSendVariant(t *testing.T) {
	dir := ""
	cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/ses_task":
			writeJSON(w, map[string]string{"id": "ses_task", "directory": dir})
		case "/session/status":
			writeJSON(w, map[string]any{})
		case "/session/ses_task/prompt_async":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, exists := body["variant"]; exists {
				t.Error("default prompt gained variant")
			}
			if _, exists := body["model"].(map[string]any)["variant"]; exists {
				t.Error("default prompt gained nested variant")
			}
			w.WriteHeader(204)
		default:
			t.Errorf("default selection made a probe request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir = filepath.Join(cfg.WorkRoot, "task")
	turn, err := client.Submit(context.Background(), cfg, Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: dir, Deadline: time.Now().Add(time.Minute)}, "Continue approved work")
	if err != nil || !turn.Accepted || turn.SubmittedModel == nil || turn.SubmittedModel.Variant != "" || turn.ExecutedModel != nil {
		t.Fatalf("default submission = %+v, %v", turn, err)
	}
}

func TestTeamModelCompactionIdentityExcludesSummary(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary only", true: "continued"}[continuation], func(t *testing.T) {
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				user := map[string]any{"id": "msg_worker", "sessionID": "ses_task", "role": "user", "model": ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}}
				switch r.URL.Path {
				case "/session/ses_task/message/msg_worker":
					writeJSON(w, map[string]any{"info": user})
				case "/session/ses_task/message":
					messages := []any{
						map[string]any{"info": user},
						map[string]any{"info": map[string]string{"id": "msg_compact", "sessionID": "ses_task", "role": "user"}, "parts": []any{map[string]any{"type": "compaction", "auto": true}}},
						map[string]any{"info": map[string]any{"id": "msg_summary", "sessionID": "ses_task", "role": "assistant", "parentID": "msg_compact", "summary": true, "providerID": "other", "modelID": "small", "finish": "stop", "time": map[string]int{"completed": 2}}},
					}
					if continuation {
						messages = append(messages,
							map[string]any{"info": map[string]string{"id": "msg_continue", "sessionID": "ses_task", "role": "user"}, "parts": []any{map[string]any{"type": "text", "synthetic": true, "metadata": map[string]bool{"compaction_continue": true}}}},
							map[string]any{"info": map[string]any{"id": "msg_reply", "sessionID": "ses_task", "role": "assistant", "parentID": "msg_continue", "providerID": "example", "modelID": "model", "variant": "high", "finish": "stop", "time": map[string]int{"created": 3, "completed": 4}}})
					}
					writeJSON(w, messages)
				default:
					t.Errorf("unexpected compaction request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			turn, latest, err := client.readTurn(context.Background(), Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: cfg.WorkRoot})
			if err != nil || latest != "msg_worker" || turn.Terminal != continuation {
				t.Fatalf("compaction observation = %+v, %q, %v", turn, latest, err)
			}
			if continuation {
				if turn.ExecutedModel == nil || turn.ExecutedModel.Variant != "high" || turn.ExecutedModel.ModelID != "model" {
					t.Fatalf("continuation identity = %+v", turn.ExecutedModel)
				}
				observed, err := client.Observe(context.Background(), Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: cfg.WorkRoot, SubmittedModel: &ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}})
				if err != nil || !observed.Terminal || observed.State != "completed" {
					t.Fatalf("durable intent rejected compaction continuation: %+v, %v", observed, err)
				}
			} else if turn.ExecutedModel != nil {
				t.Fatal("compaction summary was reported as worker execution")
			}
		})
	}
}

func TestTeamModelAssistantMetadata(t *testing.T) {
	for _, mode := range []string{"match", "dropped", "wrong provider", "missing metadata", "manual", "missing user model", "different user model", "dropped user variant"} {
		t.Run(mode, func(t *testing.T) {
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				user := map[string]any{"id": "msg_worker", "sessionID": "ses_task", "role": "user", "model": ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}}
				assistant := map[string]any{"id": "msg_reply", "parentID": "msg_worker", "sessionID": "ses_task", "role": "assistant", "providerID": "example", "modelID": "model", "variant": "high", "finish": "stop", "time": map[string]int{"created": 1, "completed": 2}}
				switch mode {
				case "dropped":
					delete(assistant, "variant")
				case "wrong provider":
					assistant["providerID"] = "other"
				case "missing metadata":
					delete(assistant, "providerID")
				case "manual":
					assistant["parentID"], assistant["variant"] = "msg_manual", "other"
				case "missing user model":
					delete(user, "model")
				case "different user model":
					user["model"] = ModelIdentity{ProviderID: "other", ModelID: "other", Variant: "high"}
				case "dropped user variant":
					user["model"] = ModelIdentity{ProviderID: "example", ModelID: "model"}
				}
				switch r.URL.Path {
				case "/session/ses_task/message/msg_worker":
					writeJSON(w, map[string]any{"info": user})
				case "/session/ses_task/message":
					messages := []any{map[string]any{"info": user}}
					if mode == "manual" {
						messages = append(messages, map[string]any{"info": map[string]string{"id": "msg_manual", "sessionID": "ses_task", "role": "user"}})
					}
					writeJSON(w, append(messages, map[string]any{"info": assistant}))
				default:
					t.Errorf("unexpected metadata request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			turn, err := client.Observe(context.Background(), Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: cfg.WorkRoot, SubmittedModel: &ModelIdentity{ProviderID: "example", ModelID: "model", Variant: "high"}})
			if mode == "match" {
				if err != nil || !turn.Terminal || turn.SubmittedModel == nil || turn.ExecutedModel == nil || *turn.SubmittedModel != *turn.ExecutedModel {
					t.Fatalf("identity = %+v, %v", turn, err)
				}
			} else if mode == "manual" {
				if errorCategory(err) != "turn_ownership_unknown" || turn.ExecutedModel != nil {
					t.Fatalf("manual identity attributed to worker: %+v, %v", turn, err)
				}
			} else if strings.Contains(mode, "user") {
				if errorCategory(err) != "submitted_model_mismatch" || turn.Terminal || turn.SubmittedModel == nil || turn.SubmittedModel.Variant != "high" {
					t.Fatalf("changed user identity completed or replaced intent: %+v, %v", turn, err)
				}
			} else if errorCategory(err) != "executed_model_mismatch" || turn.Terminal {
				t.Fatalf("mismatched identity completed: %+v, %v", turn, err)
			}
		})
	}
}
