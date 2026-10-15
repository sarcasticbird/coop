package worker

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// This models native 1.18.18 auto compaction, not a second owner prompt.
func compactionMessages(j Job, rounds int) []any {
	rows := []any{ownershipMessage(j, j.MessageID, "user", "")}
	parent := j.MessageID
	for n := 0; n < rounds; n++ {
		tool := ownershipMessage(j, fmt.Sprintf("msg_tool%d", n), "assistant", parent)
		tool["info"].(map[string]any)["finish"] = "tool-calls"
		rows = append(rows, tool)
		compactID := fmt.Sprintf("msg_compact%d", n)
		compact := ownershipMessage(j, compactID, "user", "")
		compact["parts"] = []any{map[string]any{"type": "compaction", "auto": true, "overflow": false}}
		rows = append(rows, compact)
		summary := ownershipMessage(j, fmt.Sprintf("msg_summary%d", n), "assistant", compactID)
		summary["info"].(map[string]any)["summary"] = true
		summary["info"].(map[string]any)["finish"] = "stop"
		summary["info"].(map[string]any)["time"] = map[string]int{"created": n*10 + 2, "completed": n*10 + 3}
		rows = append(rows, summary)
		parent = fmt.Sprintf("msg_continue%d", n)
		continuation := ownershipMessage(j, parent, "user", "")
		continuation["parts"] = []any{map[string]any{"type": "text", "synthetic": true, "metadata": map[string]any{"compaction_continue": true}}}
		rows = append(rows, continuation)
	}
	final := ownershipMessage(j, "msg_final", "assistant", parent)
	final["info"].(map[string]any)["finish"] = "stop"
	final["info"].(map[string]any)["time"] = map[string]int{"created": rounds*10 + 2, "completed": rounds*10 + 3}
	return append(rows, final)
}

func TestOpenCodeCompactionBoundedAncestry(t *testing.T) {
	for _, tc := range []struct {
		count        int
		errorOverlap bool
	}{{35, false}, {35, true}, {400, false}} {
		t.Run(fmt.Sprintf("%d/error-overlap=%t", tc.count, tc.errorOverlap), func(t *testing.T) {
			count := tc.count
			j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
			rows := compactionMessages(j, 1)
			final := rows[len(rows)-1]
			rows = rows[:len(rows)-1]
			for len(rows) < count-1 {
				tool := ownershipMessage(j, fmt.Sprintf("msg_loop%d", len(rows)), "assistant", "msg_continue0")
				tool["info"].(map[string]any)["finish"] = "tool-calls"
				rows = append(rows, tool)
			}
			rows = append(rows, final)
			if tc.errorOverlap {
				// Index 3 of the initial 20-message tail overlaps the successful
				// summary at index 3 when the full 35-message response is decoded.
				rows[18].(map[string]any)["info"].(map[string]any)["error"] = map[string]string{"name": "ContextOverflowError"}
			}
			requests := 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatal("history reconciliation mutated session")
				}
				switch r.URL.Path {
				case "/session/ses_task/message/msg_worker":
					writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
				case "/session/ses_task/message":
					requests++
					limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
					if err != nil || limit < 1 || limit > 320 {
						t.Fatalf("unbounded history request: %d %v", limit, err)
					}
					start := len(rows) - limit
					if start < 0 {
						start = 0
					}
					writeJSON(w, rows[start:])
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			j.Checkout = filepath.Join(cfg.WorkRoot, "task")
			turn, err := client.Observe(context.Background(), j)
			if count == 35 && (err != nil || !turn.Terminal) {
				t.Fatalf("valid ancestry not recovered: %+v %v", turn, err)
			}
			if count == 400 && (err == nil || turn.Terminal) {
				t.Fatalf("missing ancestry did not fail closed: %+v %v", turn, err)
			}
			if requests > 3 {
				t.Fatalf("unbounded request count: %d", requests)
			}
		})
	}
}

func TestCompactedWakeReconcilesAfterRestartWithoutReplay(t *testing.T) {
	e, code, clock, wakes := wakeEngineFixture(t)
	e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
	g := e.GitHub.(*wakeGitHub)
	g.snapshot.Cursor = map[string]string{}
	first, err := QueuePing(e.Config, wakes, PingInput{Prompt: "first"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	code.running = true
	tick(t, e, clock)
	j := code.last
	second, err := QueuePing(e.Config, wakes, PingInput{Prompt: "second"}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.replies[first.ID] = Comment{NodeID: "actual", URL: "https://github.com/orgs/planning/discussions/1#discussioncomment-5"}
	mutations := 0
	_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/global/health":
			writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
		case "/session/" + j.SessionID + "/message/" + j.MessageID:
			writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
		case "/session/" + j.SessionID + "/message":
			writeJSON(w, compactionMessages(j, 1))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	restarted := &Engine{Config: e.Config, Store: e.Store, GitHub: g, Checkouts: e.Checkouts, OpenCode: client, Clock: clock}
	restarted.Wakes = NewWakeCoordinator(e.Config, NewWakeStore(wakes.Dir), g, client, clock)
	clock.now = clock.now.Add(time.Hour) // Include an expired saved turn.
	tick(t, restarted, clock)
	after, err := wakes.Receipt(first.ID)
	if err != nil || after.Phase != "completed" || after.ReplyNodeID != "actual" || after.Job.MessageID != j.MessageID || after.Job.Attempts != j.Attempts || mutations != 0 {
		t.Fatalf("lost original completion or replayed: %+v err=%v mutations=%d", after, err, mutations)
	}
	next, found, err := restarted.Wakes.Next(clock.Now())
	if err != nil || !found || next.ID != second.ID {
		t.Fatalf("next owner request did not become eligible: %+v %t %v", next, found, err)
	}
}

func TestOpenCodeCompactionTurnOwnership(t *testing.T) {
	for _, mode := range []string{"completed", "repeated", "later_manual", "summary_only", "incomplete_summary", "unmarked_continue", "non_synthetic", "manual_compaction", "overflow_replay", "manual_interleaved", "missing_anchor", "missing_summary", "running_manual", "prior_terminal_summary", "prior_error_continuation", "unfinished_continue"} {
		t.Run(mode, func(t *testing.T) {
			j := Job{SessionID: "ses_task", MessageID: "msg_worker", SubmittedAt: time.Now().Add(-time.Hour)}
			rounds := 1
			if mode == "repeated" {
				rounds = 2
			}
			rows := compactionMessages(j, rounds)
			switch mode {
			case "later_manual":
				rows = append(rows, ownershipMessage(j, "msg_manual", "user", ""))
			case "summary_only":
				rows = rows[:4]
			case "incomplete_summary":
				delete(rows[3].(map[string]any)["info"].(map[string]any), "finish")
			case "unmarked_continue":
				rows[4].(map[string]any)["parts"] = []any{map[string]any{"type": "text", "synthetic": true}}
			case "non_synthetic":
				rows[4].(map[string]any)["parts"] = []any{map[string]any{"type": "text", "metadata": map[string]any{"compaction_continue": true}}}
			case "manual_compaction":
				rows[2].(map[string]any)["parts"] = []any{map[string]any{"type": "compaction", "auto": false}}
			case "overflow_replay":
				rows[2].(map[string]any)["parts"] = []any{map[string]any{"type": "compaction", "auto": true, "overflow": true}}
			case "manual_interleaved":
				rows = append(rows[:4], append([]any{ownershipMessage(j, "msg_manual", "user", "")}, rows[4:]...)...)
			case "missing_anchor":
				rows = rows[2:]
			case "missing_summary":
				rows = append(rows[:3], rows[4:]...)
			case "running_manual":
				rows = append(rows[:5], ownershipMessage(j, "msg_manual", "user", ""))
			case "prior_terminal_summary":
				info := rows[1].(map[string]any)["info"].(map[string]any)
				info["finish"], info["time"] = "length", map[string]int{"created": 1, "completed": 2}
				rows = rows[:4]
			case "prior_error_continuation":
				rows[1].(map[string]any)["info"].(map[string]any)["error"] = map[string]string{"name": "ContextOverflowError"}
				rows = rows[:5]
			case "unfinished_continue":
				rows = rows[:5]
			}
			mutations := 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/session/ses_task":
					writeJSON(w, map[string]string{"id": j.SessionID, "directory": j.Checkout})
				case "/session/ses_task/message/msg_worker":
					writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
				case "/session/ses_task/message":
					writeJSON(w, rows)
				case "/session/status":
					writeJSON(w, map[string]any{})
				case "/permission", "/question":
					writeJSON(w, []any{})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			j.Checkout = filepath.Join(cfg.WorkRoot, "task")
			turn, err := client.Observe(context.Background(), j)
			terminal := mode == "completed" || mode == "repeated" || mode == "later_manual"
			if terminal {
				if err != nil || !turn.Terminal || turn.State != "completed" || turn.MessageID != j.MessageID {
					t.Fatalf("lost compacted turn: %+v %v", turn, err)
				}
				if err := client.Abort(context.Background(), j); err != nil {
					t.Fatalf("terminal abort: %v", err)
				}
			} else if err == nil && turn.Terminal {
				t.Fatalf("unproven continuation completed turn: %+v", turn)
			} else if mode != "summary_only" && mode != "prior_terminal_summary" && mode != "prior_error_continuation" && mode != "unfinished_continue" {
				if err := client.Abort(context.Background(), j); err == nil {
					t.Fatal("uncertain/manual continuation allowed abort")
				}
			}
			if mutations != 0 {
				t.Fatalf("mutated another turn: %d", mutations)
			}
		})
	}
}
