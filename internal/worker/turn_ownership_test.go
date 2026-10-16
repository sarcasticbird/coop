package worker

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestOpenCodeAbortReconcilesTurnOwnership(t *testing.T) {
	for _, mode := range []string{"completed_manual", "interrupted_manual", "uncertain_manual", "missing_message", "missing_tail", "foreign_message", "foreign_session", "history_error", "tool_calls_manual", "queued_manual", "assistant_tail_manual", "assistant_tail_manual_then_worker"} {
		t.Run(mode, func(t *testing.T) {
			j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
			mutations := 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/session/ses_task":
					dir := j.Checkout
					if mode == "foreign_session" {
						dir += "/manual"
					}
					writeJSON(w, map[string]string{"id": j.SessionID, "directory": dir})
				case "/session/ses_task/message/msg_worker":
					if mode == "missing_message" {
						w.WriteHeader(404)
						return
					}
					writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
				case "/session/ses_task/message":
					if mode == "history_error" {
						w.WriteHeader(500)
						return
					}
					messages := []any{ownershipMessage(j, j.MessageID, "user", "")}
					if mode == "missing_tail" {
						messages = nil
					}
					if mode == "completed_manual" || mode == "interrupted_manual" || mode == "tool_calls_manual" {
						reply := ownershipMessage(j, "msg_reply", "assistant", j.MessageID)
						info := reply["info"].(map[string]any)
						info["time"] = map[string]int{"created": 2, "completed": 3}
						info["finish"] = "stop"
						if mode == "interrupted_manual" {
							info["error"] = map[string]string{"name": "MessageAbortedError"}
						}
						if mode == "tool_calls_manual" {
							info["finish"] = "tool-calls"
						}
						messages = append(messages, reply)
					}
					if mode != "missing_tail" {
						manual := ownershipMessage(j, "msg_manual", "user", "")
						if mode == "foreign_message" {
							manual["info"].(map[string]any)["sessionID"] = "ses_foreign"
						}
						messages = append(messages, manual)
						if mode == "queued_manual" {
							messages = append(messages, ownershipMessage(j, "msg_stillfinishing", "assistant", j.MessageID))
						}
					}
					if mode == "assistant_tail_manual" || mode == "assistant_tail_manual_then_worker" {
						// Both user messages have fallen outside the native tail.
						// A late owned tool response does not prove the manual turn ended.
						messages = []any{ownershipMessage(j, "msg_ownedtool", "assistant", j.MessageID), ownershipMessage(j, "msg_manualtool", "assistant", "msg_manual")}
						if mode == "assistant_tail_manual_then_worker" {
							messages = append(messages, ownershipMessage(j, "msg_lateownedtool", "assistant", j.MessageID))
						}
					}
					writeJSON(w, messages)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			j.Checkout = filepath.Join(cfg.WorkRoot, "task")
			err := client.Abort(context.Background(), j)
			terminal := mode == "completed_manual" || mode == "interrupted_manual"
			if (err == nil) != terminal || mutations != 0 {
				t.Fatalf("terminal=%v err=%v mutations=%d", terminal, err, mutations)
			}
			if terminal {
				turn, err := client.Observe(context.Background(), j)
				if err != nil || !turn.Terminal || turn.MessageID != j.MessageID {
					t.Fatalf("manual activity hid owned terminal result: %+v %v", turn, err)
				}
			}
		})
	}
}

func ownershipMessage(j Job, id, role, parent string) map[string]any {
	info := map[string]any{"id": id, "sessionID": j.SessionID, "role": role, "parentID": parent}
	if m := j.SubmittedModel; m != nil {
		if role == "user" {
			info["model"] = m
		} else {
			info["providerID"], info["modelID"], info["variant"] = m.ProviderID, m.ModelID, m.Variant
		}
	}
	return map[string]any{"info": info}
}

func TestOpenCodeAbortLongOwnedToolLoop(t *testing.T) {
	j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
	aborts := 0
	cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/ses_task":
			writeJSON(w, map[string]string{"id": j.SessionID, "directory": j.Checkout})
		case "/session/ses_task/message/msg_worker":
			writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
		case "/session/ses_task/message":
			reply := ownershipMessage(j, "msg_tool", "assistant", j.MessageID)
			reply["info"].(map[string]any)["finish"] = "tool-calls"
			writeJSON(w, []any{reply})
		case "/session/ses_task/abort":
			aborts++
			writeJSON(w, true)
		case "/session/status":
			writeJSON(w, map[string]any{})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	j.Checkout = filepath.Join(cfg.WorkRoot, "task")
	if err := client.Abort(context.Background(), j); err != nil || aborts != 1 {
		t.Fatalf("owned tool loop not aborted: %v aborts=%d", err, aborts)
	}
}

func TestInterruptionProtectsLaterManualTurn(t *testing.T) {
	for _, path := range []string{"pause", "revoked", "deadline", "wake_pause", "wake_deadline"} {
		for _, terminal := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/uncertain", true: "/completed"}[terminal], func(t *testing.T) {
				e, code, clock, wakeStore := wakeEngineFixture(t)
				wake := path == "wake_pause" || path == "wake_deadline"
				if wake {
					if _, err := QueuePing(e.Config, wakeStore, PingInput{}, clock.Now()); err != nil {
						t.Fatal(err)
					}
				} else {
					_, snapshot, _ := policyFixture(t)
					e.GitHub.(*wakeGitHub).snapshot = snapshot
				}
				code.running = true
				tick(t, e, clock)
				j := code.last
				if j.MessageID == "" {
					t.Fatal("no owned turn dispatched")
				}
				mutations := 0
				_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						mutations++
						w.WriteHeader(500)
						return
					}
					switch r.URL.Path {
					case "/session/" + j.SessionID:
						writeJSON(w, map[string]string{"id": j.SessionID, "directory": j.Checkout})
					case "/session/" + j.SessionID + "/message/" + j.MessageID:
						writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
					case "/session/" + j.SessionID + "/message":
						messages := []any{ownershipMessage(j, j.MessageID, "user", "")}
						if terminal {
							reply := ownershipMessage(j, "msg_reply", "assistant", j.MessageID)
							info := reply["info"].(map[string]any)
							info["finish"] = "stop"
							info["time"] = map[string]int{"created": 2, "completed": 3}
							messages = append(messages, reply)
						}
						messages = append(messages, ownershipMessage(j, "msg_manual", "user", ""))
						writeJSON(w, messages)
					case "/global/health":
						writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(404)
					}
				})
				e.OpenCode, e.Wakes.OpenCode = client, client
				switch path {
				case "pause", "wake_pause":
					e.Store.(*memoryStore).paused = true
				case "revoked":
					e.GitHub.(*wakeGitHub).snapshot.Goals[0].Decisions = nil
				case "deadline", "wake_deadline":
					clock.now = j.Deadline
				}
				clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
				err := e.Tick(context.Background())
				if terminal && err != nil {
					t.Fatal(err)
				}
				if mutations != 0 {
					t.Fatal("interruption mutated the later manual turn")
				}
				if !terminal {
					if wake {
						v, active, err := e.Wakes.Active()
						if err != nil || !active || v.Job.MessageID != j.MessageID || (v.Reason != "abort_unconfirmed" && v.Job.ReconcileError != "turn_ownership_unknown") {
							t.Fatalf("lost uncertain wake ownership: %+v %v", v, err)
						}
					} else if current, active := e.active(); !active || current.MessageID != j.MessageID || current.WaitReason != "abort_unconfirmed" {
						t.Fatalf("lost uncertain task ownership: %+v", current)
					}
				} else if wake {
					if v, active, err := e.Wakes.Active(); err != nil || active {
						t.Fatalf("completed wake still reserves slot: %+v %v", v, err)
					}
				} else if current, active := e.active(); active {
					t.Fatalf("completed worker still reserves slot: %+v", current)
				}
			})
		}
	}
}
