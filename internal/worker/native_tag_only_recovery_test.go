package worker

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type tagOnlyDiskEntry struct {
	Bytes    string
	Mode     fs.FileMode
	Modified time.Time
	Inode    uint64
}

// Include directories and file identity so even an identical atomic rewrite or
// a newly created lock file violates the read-only contract.
func tagOnlyDiskSnapshot(t *testing.T, cfg Config) map[string]tagOnlyDiskEntry {
	t.Helper()
	out := map[string]tagOnlyDiskEntry{}
	for _, root := range []string{cfg.StateDir, cfg.WorkRoot} {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) && path == root {
				return nil
			}
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			saved := tagOnlyDiskEntry{Mode: info.Mode(), Modified: info.ModTime(), Inode: info.Sys().(*syscall.Stat_t).Ino}
			if !entry.IsDir() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				saved.Bytes = string(raw)
			}
			out[path] = saved
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func tagOnlyAssertDiskUnchanged(t *testing.T, cfg Config, before map[string]tagOnlyDiskEntry) {
	t.Helper()
	after := tagOnlyDiskSnapshot(t, cfg)
	if !reflect.DeepEqual(before, after) {
		for path, old := range before {
			if next, ok := after[path]; !ok || next != old {
				t.Errorf("read-only call changed %s", path)
			}
		}
		for path := range after {
			if _, ok := before[path]; !ok {
				t.Errorf("read-only call created %s", path)
			}
		}
		t.Fatal("local state changed")
	}
}

func TestTagOnlyObserveNeverWrites(t *testing.T) {
	for _, mode := range []string{"current", "paused", "revoked", "disabled", "held", "worker-paused", "admission-paused", "object-only", "foreign-object", "foreign-ref", "wrong-placement", "wrong-observer", "wrong-goal", "missing-intent", "completed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, false)
			api.lost = "ref"
			if mode == "object-only" {
				api.lost = "object"
			}
			if mode == "completed" {
				api.lost = ""
			}
			_, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"})
			if (err == nil) != (mode == "completed") {
				t.Fatal("seed action", err)
			}
			goal, id := x.Goal, x.Job.Master.DeliveryHandoffID
			switch mode {
			case "paused":
				m.authority.State = "paused"
			case "revoked":
				m.handoffs[len(m.handoffs)-1].Input.Revoked = true
			case "disabled":
				for i := range cfg.Team.Members {
					if cfg.Team.Members[i].Name == "rook" {
						cfg.Team.Members[i].Capabilities = nil
					}
				}
			case "held":
				cfg.DeliveryHold = true
			case "worker-paused":
				if err := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "admission-paused":
				if err := atomicPrivate(filepath.Join(cfg.StateDir, "admission-control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "foreign-object":
				api.drift = "object"
			case "foreign-ref":
				api.drift = "ref"
			case "wrong-placement":
				m.self.Placement.Generation++
			case "wrong-observer":
				m.self.Principal.GitHubBotID++
			case "wrong-goal":
				goal.RevisionSHA256 = BodyHash("foreign goal")
			case "missing-intent":
				id = "missing"
			}
			if err := os.Remove(filepath.Join(masterDir(cfg), "actions", "producer.lock")); err != nil {
				t.Fatal(err)
			}
			before, writes, validations := tagOnlyDiskSnapshot(t, cfg), len(api.writes), m.validations
			result, err := ObserveNativeTagOnly(t.Context(), cfg, m, g, goal, id)
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if len(api.writes) != writes || len(transport.writes) != 0 || m.validations != validations {
				t.Fatal("observer invoked a producer or authorization replay")
			}
			refused := strings.HasPrefix(mode, "foreign-") || strings.HasPrefix(mode, "wrong-") || mode == "missing-intent"
			if refused {
				if err == nil {
					t.Fatal("unverified facts admitted", result)
				}
				return
			}
			want := "observed"
			if mode == "object-only" {
				want = "checkpoint"
			}
			if err != nil || result.Phase != want {
				t.Fatal("observation", result, err)
			}
			if yes, err := nativeJobHasResult(cfg, x); err != nil || yes != (mode == "completed") {
				t.Fatal("observation changed native success", yes, err)
			}
		})
	}
}

func TestTagOnlyTerminalConfirmationRequiresCurrentAuthority(t *testing.T) {
	for _, version := range []bool{false, true} {
		name := "source"
		if version {
			name = "version"
		}
		for _, mode := range []string{"current", "receipt-interrupted", "paused", "revoked", "held", "disabled", "gate-disabled", "master-refusal", "report-drift", "wrong-placement", "worker-paused", "admission-paused", "tree-drift", "late-revocation", "request-lineage", "missing-tag-intent", "missing-action", "missing-request", "completed-without-tag-intent"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, version)
				in := NativeAction{Operation: "tag-only", RequestID: "tag-request"}
				api.lost = "ref"
				if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil {
					t.Fatal("expected uncertain ref acknowledgment")
				}
				actionPath := nativeActionPath(cfg, x.Job.ID, in.RequestID)
				requestPath := nativeActionIntentPath(cfg, x.Job.ID, in.RequestID)
				original, err := os.ReadFile(actionPath)
				if err != nil {
					t.Fatal(err)
				}
				request, err := os.ReadFile(requestPath)
				if err != nil {
					t.Fatal(err)
				}
				tagIndex := len(m.handoffs) - 1
				if version {
					tagIndex--
				}
				switch mode {
				case "receipt-interrupted":
					j, err := readTagOnlyIntent(cfg, x.Job.Master.DeliveryHandoffID)
					if err != nil {
						t.Fatal(err)
					}
					j.Phase = "completed"
					j.Result = NativeActionResult{Phase: "completed", URL: "https://github.com/" + j.Binding.Target.Repository + "/tree/" + j.Binding.Handoff.Input.Tag}
					if err := saveTagOnlyIntent(cfg, j); err != nil {
						t.Fatal(err)
					}
				case "missing-tag-intent", "completed-without-tag-intent":
					if mode == "completed-without-tag-intent" {
						if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Remove(tagOnlyPath(cfg, x.Job.Master.DeliveryHandoffID)); err != nil {
						t.Fatal(err)
					}
				case "missing-action", "missing-request":
					path := actionPath
					if mode == "missing-request" {
						path = requestPath
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "paused":
					m.authority.State = "paused"
				case "revoked":
					m.handoffs[tagIndex].Input.Revoked = true
				case "held":
					cfg.DeliveryHold = true
				case "disabled", "gate-disabled":
					actor := "rook"
					if mode == "gate-disabled" {
						actor = "hawk"
					}
					for i := range cfg.Team.Members {
						if cfg.Team.Members[i].Name == actor {
							cfg.Team.Members[i].Capabilities = nil
						}
					}
				case "master-refusal":
					m.deny = true
				case "report-drift":
					id := m.handoffs[tagIndex].Input.TagOnly.QAReportID
					msg := m.reports[id]
					msg.Body += "drift"
					m.reports[id] = msg
				case "wrong-placement":
					m.self.Placement.Generation++
				case "worker-paused":
					if err := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
						t.Fatal(err)
					}
				case "admission-paused":
					if err := atomicPrivate(filepath.Join(cfg.StateDir, "admission-control.json"), []byte(`{"paused":true}`)); err != nil {
						t.Fatal(err)
					}
				case "request-lineage":
					var saved nativeActionIntent
					if json.Unmarshal(request, &saved) != nil {
						t.Fatal("invalid serialized request")
					}
					saved.Action.Body = "caller replacement"
					raw, _ := json.Marshal(saved)
					if err := atomicPrivate(requestPath, raw); err != nil {
						t.Fatal(err)
					}
				}
				previous := transport.commands.respond
				targetReads := 0
				transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
					if endpoint == "repos/code/repo/git/commits/"+api.intent.Binding.Target.SHA {
						targetReads++
						// Let the first full authority/target validation pass. Only
						// the extra terminal commit/tree reread sees this drift.
						if mode == "tree-drift" && targetReads == 2 {
							return jsonResponse(map[string]any{"sha": api.intent.Binding.Target.SHA, "tree": map[string]string{"sha": strings.Repeat("f", 40)}}), nil
						}
					}
					if mode == "late-revocation" && strings.Contains(endpoint, "/git/tags/") {
						m.handoffs[tagIndex].Input.Revoked = true
					}
					return previous(owner, endpoint, method, body)
				}
				before, writes := tagOnlyDiskSnapshot(t, cfg), len(api.writes)
				result, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
				if len(api.writes) != writes || len(transport.writes) != 0 {
					t.Fatal("terminal recovery replayed a remote mutation", api.writes, transport.writes)
				}
				if mode != "current" && mode != "receipt-interrupted" {
					if err == nil {
						t.Fatal("terminal success without current authority/target", result)
					}
					tagOnlyAssertDiskUnchanged(t, cfg, before)
					if yes, err := nativeJobHasResult(cfg, x); yes != (mode == "completed-without-tag-intent") || err != nil {
						t.Fatal("blocked confirmation created success", yes, err)
					}
					return
				}
				if err != nil || result.Phase != "completed" || targetReads < 3 {
					t.Fatal("current-authority confirmation", result, err, targetReads)
				}
				var want, got nativeActionRecord
				if json.Unmarshal(original, &want) != nil {
					t.Fatal("invalid original receipt")
				}
				want.State, want.Result = "completed", result
				raw, err := os.ReadFile(actionPath)
				if err != nil || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(want, got) {
					t.Fatal("confirmation lost original action lineage", err)
				}
				if raw, err := os.ReadFile(requestPath); err != nil || string(raw) != string(request) {
					t.Fatal("confirmation rewrote serialized request", err)
				}
				if mode == "receipt-interrupted" {
					path := tagOnlyPath(cfg, x.Job.Master.DeliveryHandoffID)
					if tagOnlyDiskSnapshot(t, cfg)[path] != before[path] {
						t.Fatal("confirmation rewrote an already-completed tag intent")
					}
				}
				before = tagOnlyDiskSnapshot(t, cfg)
				for range 2 {
					again, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
					if err != nil || again != result || len(api.writes) != writes {
						t.Fatal("completed receipt not idempotent", again, err)
					}
				}
				tagOnlyAssertDiskUnchanged(t, cfg, before)
			})
		}
	}
}

func TestTagOnlyObjectCheckpointCannotResumeWrites(t *testing.T) {
	cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, false)
	in := NativeAction{Operation: "tag-only", RequestID: "tag-request"}
	api.lost = "object"
	if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil {
		t.Fatal("expected lost object acknowledgment")
	}
	before := tagOnlyDiskSnapshot(t, cfg)
	for range 2 {
		result, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
		if err != nil || result.Phase != "checkpoint" || len(api.writes) != 1 || api.ref || len(transport.writes) != 0 {
			t.Fatal("object checkpoint replayed an effect", result, err, api.writes)
		}
	}
	tagOnlyAssertDiskUnchanged(t, cfg, before)
	if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
		t.Fatal("object checkpoint became native completion", yes, err)
	}
}
