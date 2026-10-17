package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func nativeDiffSummary(m map[string]any, size int) map[string]any {
	m["info"].(map[string]any)["summary"] = map[string]any{"diffs": []any{map[string]string{"before": strings.Repeat("x", size)}}}
	return m
}

func TestNativeObservationLargePayloads(t *testing.T) {
	for _, mode := range []string{"saved_summary", "history_tool", "expanded_compaction", "later_manual"} {
		t.Run(mode, func(t *testing.T) {
			j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
			saved := ownershipMessage(j, j.MessageID, "user", "")
			final := ownershipMessage(j, "msg_final", "assistant", j.MessageID)
			final["info"].(map[string]any)["finish"] = "stop"
			final["info"].(map[string]any)["time"] = map[string]int{"created": 2, "completed": 3}
			rows := []any{saved, final}
			switch mode {
			case "saved_summary":
				nativeDiffSummary(saved, 3<<20)
			case "history_tool", "later_manual":
				final["parts"] = []any{map[string]any{"type": "tool", "state": map[string]string{"output": strings.Repeat("x", 3<<20)}}}
				if mode == "later_manual" {
					final["info"].(map[string]any)["finish"] = "tool-calls"
					rows = append(rows, ownershipMessage(j, "msg_manual", "user", ""))
				}
			case "expanded_compaction":
				rows = compactionMessages(j, 1)
				nativeDiffSummary(rows[0].(map[string]any), 3<<20)
				final = rows[len(rows)-1].(map[string]any)
				rows = rows[:len(rows)-1]
				for len(rows) < 34 {
					rows = append(rows, ownershipMessage(j, "msg_loop"+strconv.Itoa(len(rows)), "assistant", "msg_continue0"))
				}
				rows = append(rows, final)
			}
			requests, mutations := 0, 0
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/session/ses_task/message/msg_worker":
					writeJSON(w, saved)
				case "/session/ses_task/message":
					requests++
					limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
					if limit != 20 && limit != 80 && limit != 320 {
						t.Errorf("unbounded history limit: %d", limit)
						w.WriteHeader(500)
						return
					}
					start := max(0, len(rows)-limit)
					writeJSON(w, rows[start:])
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			j.Checkout = filepath.Join(cfg.WorkRoot, "task")
			turn, err := client.Observe(context.Background(), j)
			if mode == "later_manual" {
				if err == nil || err.Error() != "OpenCode: turn_ownership_unknown" || turn.Terminal {
					t.Fatalf("manual work was not protected: %+v %v", turn, err)
				}
			} else if err != nil || !turn.Terminal || turn.MessageID != j.MessageID {
				t.Fatalf("large native payload lost completion: %+v %v", turn, err)
			}
			if mutations != 0 || requests > 3 {
				t.Fatalf("observation mutated native state or exceeded request bound: %d/%d", mutations, requests)
			}
		})
	}
}

func TestNativeObservationSummaryProjection(t *testing.T) {
	for _, summary := range []any{true, false, nil, "true", map[string]any{"diffs": strings.Repeat("x", 3<<20)}} {
		raw, err := json.Marshal(map[string]any{"info": map[string]any{"summary": summary}})
		if err != nil {
			t.Fatal(err)
		}
		var m messageEnvelope
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m.summary() != (summary == any(true)) {
			t.Fatal("diff summary confused with native compaction marker")
		}
		projected, err := json.Marshal(m)
		if err != nil || len(projected) > 1024 {
			t.Fatalf("observation retained irrelevant diff contents: bytes=%d err=%v", len(projected), err)
		}
	}
}

func TestNativeObservationHardLimits(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(strconv.FormatBool(native), func(t *testing.T) {
			j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
			size := 2 << 20
			if native {
				size = 16 << 20
			}
			cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if native {
					writeJSON(w, nativeDiffSummary(ownershipMessage(j, j.MessageID, "user", ""), size+1))
					return
				}
				_, _ = w.Write([]byte(strings.Repeat(" ", size+1)))
			})
			j.Checkout = filepath.Join(cfg.WorkRoot, "task")
			var err error
			if native {
				_, err = client.Observe(context.Background(), j)
			} else {
				_, err = client.Health(context.Background())
			}
			if err == nil || err.Error() != "OpenCode: response_too_large" {
				t.Fatalf("hard response limit weakened: %v", err)
			}
		})
	}
}
