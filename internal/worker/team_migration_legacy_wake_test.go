package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// This records the old active-turn completion branch: it saved the resolved
// source and reply, but retained running and never assigned TerminalAt.
func legacyCompletedWakeFixture(t *testing.T, kind string) (Config, *FileStore, State, *FileWakeStore, WakeRequest, WakeReceipt) {
	t.Helper()
	c, _, _, _, now := teamScopeFixture(t)
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/planning/board/discussions/1", PeerHopLimit: 2}
	s := NewFileStore(c.StateDir)
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	old := Job{Kind: "implement", Phase: "completed", TaskURL: "https://github.com/code/repo/issues/1", SessionID: "ses_task", MessageID: "msg_task", SubmissionState: "completed", TerminalAt: now.Add(-time.Hour), Attempts: 2}
	old.ID = JobKey(old)
	state.Jobs[old.ID] = old
	state.Sessions["historical task"] = SessionBinding{SessionID: old.SessionID, Checkout: c.WorkRoot, TaskURL: old.TaskURL, Kind: old.Kind}
	state.Seen["historical"] = "revision"
	state.Cursor["historical"] = "cursor"
	state.Attempts = map[string]int{old.ID: 2}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	w := NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
	source := ConversationSource{NodeID: "DC_source", URL: c.Wake.CoordinationURL + "#discussioncomment-1", DiscussionID: "D_discussion", DiscussionURL: c.Wake.CoordinationURL, RootID: "DC_source", Body: "Please explain the saved result", Author: c.Owner, CreatedAt: now.Add(-time.Minute)}
	if kind == "ping" {
		source.NodeID, source.RootID, source.URL = source.DiscussionID, source.DiscussionID, source.DiscussionURL
	}
	source.Revision = ConversationRevision(source)
	r := WakeRequest{Version: 1, ID: BodyHash("legacy completed " + kind), Target: c.Name, Kind: kind, Source: source, ChainID: BodyHash("legacy chain"), CreatedAt: now}
	if kind == "ping" {
		r.Prompt = "Explain the saved result"
		r.ChainID = r.ID
		r.Source = ConversationSource{URL: c.Wake.CoordinationURL, DiscussionURL: c.Wake.CoordinationURL, Author: c.Owner}
	}
	if _, err := w.Enqueue(r); err != nil {
		t.Fatal(err)
	}
	v := WakeReceipt{Version: 1, RequestID: r.ID, Phase: "completed", Source: source, ReplyNodeID: "DC_reply", ReplyURL: c.Wake.CoordinationURL + "#discussioncomment-2", UpdatedAt: now.Add(2 * time.Minute), Job: Job{ID: r.ID, Kind: "wake", Phase: "completed", SessionID: "ses_saved", MessageID: "msg_saved", SubmissionState: "running", SubmittedAt: now.Add(time.Minute), Attempts: 1}}
	v.Job.GoalURL, v.Job.TaskURL = source.DiscussionURL, source.RootID
	// Historical checkouts need not exist or remain under the current WorkRoot.
	v.Job.Checkout = filepath.Join("/historical", "coop", "wake-"+BodyHash(kind)[:32], "conversation")
	if err := w.SaveReceipt(v); err != nil {
		t.Fatal(err)
	}
	routing := newWakeRouting()
	routing.Bindings["historical wake"] = WakeBinding{DiscussionID: source.DiscussionID, ThreadID: source.RootID, Directory: c.WorkRoot, SessionID: v.Job.SessionID}
	routing.Seen[source.NodeID] = source.Revision
	if err := w.SaveRouting(routing); err != nil {
		t.Fatal(err)
	}
	return c, s, state, w, r, v
}

func TestTeamMigrationLegacyCompletedWakePreservesBytesAndNeverDispatches(t *testing.T) {
	for _, kind := range []string{"discussion", "ping"} {
		t.Run(kind, func(t *testing.T) {
			c, s, before, w, request, receipt := legacyCompletedWakeFixture(t, kind)
			originals := map[string][]byte{}
			for _, rel := range []string{"state.json", "wake/v1/routing.json", "wake/v1/inbox/" + request.ID + ".json", "wake/v1/receipts/" + request.ID + ".json"} {
				raw, err := os.ReadFile(filepath.Join(s.Dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				originals[rel] = raw
			}
			if err := migrationJob(receipt.Job, true); err == nil {
				t.Fatal("generic job validator accepted missing terminal evidence")
			}
			if err := MigrateState(c, 1, 2, before.InstanceID); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(s.Dir, "migration-backup.json"))
			var backup map[string][]byte
			if err != nil || json.Unmarshal(raw, &backup) != nil {
				t.Fatal("invalid migration backup", err)
			}
			if len(backup) != len(originals) {
				t.Fatal("migration backup inventory changed")
			}
			for rel, original := range originals {
				if !bytes.Equal(backup[rel], original) {
					t.Fatalf("backup changed %s", rel)
				}
			}
			_, github, code, clock, _ := engineFixture(t)
			conversation := &wakeGitHub{workflowGitHub: github, replies: map[string]Comment{}}
			for range 2 {
				// Use the real migrated startup path and a fresh coordinator on restart.
				e := &Engine{Config: c, Store: NewVersionedFileStore(s.Dir, 2), GitHub: conversation, OpenCode: code, Clock: clock}
				if err := e.initialize(); err != nil {
					t.Fatal(err)
				}
				before.Version = 2
				if !reflect.DeepEqual(e.state, before) {
					t.Fatal("startup changed identity or historical jobs")
				}
				if _, active, err := e.Wakes.Active(); err != nil || active {
					t.Fatal("completed wake became active", err)
				}
				if _, pending, err := e.Wakes.Next(clock.Now()); err != nil || pending {
					t.Fatal("completed wake became pending", err)
				}
				if err := e.Wakes.Tick(context.Background(), Snapshot{Complete: true}); err != nil {
					t.Fatal(err)
				}
				w2 := NewVersionedWakeStore(w.Dir, 2)
				summary, err := w2.Enqueue(request)
				if err != nil || !summary.Coalesced || summary.Phase != "completed" || summary.ID != request.ID {
					t.Fatalf("completed request did not deduplicate: %+v %v", summary, err)
				}
				got, err := w2.Receipt(request.ID)
				if err != nil || !reflect.DeepEqual(got, receipt) {
					t.Fatal("completed receipt changed", err)
				}
				for rel, original := range originals {
					if rel == "state.json" {
						continue
					}
					got, err := os.ReadFile(filepath.Join(s.Dir, rel))
					if err != nil || !bytes.Equal(got, original) {
						t.Fatalf("startup or deduplication changed %s: %v", rel, err)
					}
				}
			}
			if code.calls != 0 || code.aborts != 0 || len(conversation.replies) != 0 {
				t.Fatal("completed legacy wake caused an external effect")
			}
		})
	}
}

func TestTeamMigrationLegacyCompletedWakeRejectsAlteredEvidence(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*Config, *WakeRequest, *WakeReceipt)
	}{
		{"request version", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.Version = 2 }},
		{"receipt version", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Version = 2 }},
		{"request identity", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.ID = BodyHash("foreign") }},
		{"receipt identity", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.RequestID = BodyHash("foreign") }},
		{"job identity", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.ID = BodyHash("foreign") }},
		{"missing job identity", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.ID = "" }},
		{"foreign target", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.Target = "foreign" }},
		{"missing chain", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.ChainID = "" }},
		{"negative peer hop", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.PeerHop = -1 }},
		{"wrong kind", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Kind = "implement" }},
		{"missing job goal", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.GoalURL = "" }},
		{"conflicting job goal", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.Job.GoalURL = "https://github.com/planning/board/discussions/2"
		}},
		{"missing job thread", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.TaskURL = "" }},
		{"conflicting job thread", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.TaskURL = "DC_foreign" }},
		{"missing job checkout", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Checkout = "" }},
		{"relative job checkout", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Checkout = "wake/conversation" }},
		{"root job checkout", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Checkout = "/" }},
		{"unclean job checkout", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Checkout += "/../foreign" }},
		{"active receipt", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Phase = "running" }},
		{"recovery receipt", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Phase = "recovery" }},
		{"failed receipt", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Phase = "failed" }},
		{"queued receipt", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Phase = "queued" }},
		{"active job", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Phase = "active" }},
		{"incomplete job", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Phase = "waiting" }},
		{"unknown submission", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SubmissionState = "unknown" }},
		{"submitting observation", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SubmissionState = "submitting" }},
		{"accepted observation", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SubmissionState = "accepted" }},
		{"compacting observation", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SubmissionState = "compacting" }},
		{"missing session", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SessionID = "" }},
		{"malformed session", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SessionID = "session" }},
		{"missing message", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.MessageID = "" }},
		{"malformed message", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.MessageID = "message" }},
		{"missing submission time", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.SubmittedAt = time.Time{} }},
		{"submission before request", func(_ *Config, r *WakeRequest, v *WakeReceipt) { v.Job.SubmittedAt = r.CreatedAt.Add(-time.Second) }},
		{"missing update time", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.UpdatedAt = time.Time{} }},
		{"update before submission", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.UpdatedAt = v.Job.SubmittedAt.Add(-time.Second) }},
		{"missing request time", func(_ *Config, r *WakeRequest, _ *WakeReceipt) { r.CreatedAt = time.Time{} }},
		{"negative attempts", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.Attempts = -1 }},
		{"negative reviews", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.ReviewCycles = -1 }},
		{"negative reconciliations", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Job.ReconcileFailures = -1 }},
		{"missing source", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source = ConversationSource{} }},
		{"source node", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.NodeID = "" }},
		{"malformed source identity", func(_ *Config, r *WakeRequest, v *WakeReceipt) {
			v.Source.DiscussionID = "bad\nidentity"
			if r.Kind == "ping" {
				v.Source.NodeID, v.Source.RootID = v.Source.DiscussionID, v.Source.DiscussionID
			}
			v.Source.Revision = ConversationRevision(v.Source)
			if r.Kind == "discussion" {
				r.Source = v.Source
			}
		}},
		{"source discussion", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.DiscussionID = "" }},
		{"source root", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.RootID = "" }},
		{"source revision", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.Revision = BodyHash("foreign") }},
		{"source author", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.Source.Author.ID = 0
			v.Source.Revision = ConversationRevision(v.Source)
		}},
		{"source time", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.CreatedAt = time.Time{} }},
		{"source created after submission", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.Source.CreatedAt = v.Job.SubmittedAt.Add(time.Second)
		}},
		{"source edited after submission", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.Source.EditedAt = v.Job.SubmittedAt.Add(time.Second)
		}},
		{"source outside scope", func(_ *Config, r *WakeRequest, v *WakeReceipt) {
			r.Source.DiscussionURL, v.Source.DiscussionURL = "https://github.com/foreign/repo/discussions/1", "https://github.com/foreign/repo/discussions/1"
			v.Source.Revision = ConversationRevision(v.Source)
		}},
		{"source discussion URL", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.Source.DiscussionURL += "2" }},
		{"source URL", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.Source.URL = "https://github.com/foreign/repo/discussions/1"
			v.Source.Revision = ConversationRevision(v.Source)
		}},
		{"missing reply node", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.ReplyNodeID = "" }},
		{"malformed reply node", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.ReplyNodeID = "bad\nnode" }},
		{"missing reply URL", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.ReplyURL = "" }},
		{"foreign reply", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.ReplyURL = "https://github.com/foreign/repo/discussions/1#discussioncomment-2"
		}},
		{"wrong discussion reply", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.ReplyURL = "https://github.com/planning/board/discussions/2#discussioncomment-2"
		}},
		{"reply lacks comment", func(_ *Config, _ *WakeRequest, v *WakeReceipt) { v.ReplyURL = v.Source.DiscussionURL }},
		{"reply malformed comment", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.ReplyURL = v.Source.DiscussionURL + "#discussioncomment-abc"
		}},
		{"reply query", func(_ *Config, _ *WakeRequest, v *WakeReceipt) {
			v.ReplyURL = v.Source.DiscussionURL + "?extra=1#discussioncomment-2"
		}},
	}
	for _, kind := range []string{"discussion", "ping"} {
		for _, mutation := range mutations {
			t.Run(kind+"/"+mutation.name, func(t *testing.T) {
				c, s, state, w, r, v := legacyCompletedWakeFixture(t, kind)
				id := r.ID
				mutation.edit(&c, &r, &v)
				for rel, value := range map[string]any{"inbox/" + id + ".json": r, "receipts/" + id + ".json": v} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := atomicPrivate(filepath.Join(w.Dir, rel), raw); err != nil {
						t.Fatal(err)
					}
				}
				originals := map[string][]byte{}
				for _, rel := range []string{"state.json", "wake/v1/inbox/" + id + ".json", "wake/v1/receipts/" + id + ".json"} {
					raw, err := os.ReadFile(filepath.Join(s.Dir, rel))
					if err != nil {
						t.Fatal(err)
					}
					originals[rel] = raw
				}
				if err := MigrateState(c, 1, 2, state.InstanceID); err == nil {
					t.Fatal("altered legacy completion accepted")
				}
				for rel, original := range originals {
					got, err := os.ReadFile(filepath.Join(s.Dir, rel))
					if err != nil || !bytes.Equal(got, original) {
						t.Fatalf("refusal changed %s: %v", rel, err)
					}
				}
				for _, rel := range []string{"migration.json", "migration-backup.json", "wake/v1/inbox/" + wakeFenceName} {
					if _, err := os.Lstat(filepath.Join(s.Dir, rel)); !os.IsNotExist(err) {
						t.Fatalf("refusal published %s: %v", rel, err)
					}
				}
			})
		}
	}
}

func TestTeamMigrationLegacyWakeDoesNotRelaxTaskUncertainty(t *testing.T) {
	for _, observation := range []string{"running", "submitting", "unknown", "accepted", "compacting", "blocked"} {
		t.Run(observation, func(t *testing.T) {
			c, s, state, _, _, v := legacyCompletedWakeFixture(t, "discussion")
			j := v.Job
			j.Kind, j.SubmissionState = "implement", observation
			j.TaskURL = "https://github.com/code/repo/issues/1"
			j.ID = JobKey(j)
			state.Jobs[j.ID] = j
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
			if err := MigrateState(c, 1, 2, state.InstanceID); err == nil {
				t.Fatal("legacy wake compatibility relaxed generic task uncertainty")
			}
		})
	}
}
