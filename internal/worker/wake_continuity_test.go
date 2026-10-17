package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func continuityFixture(t *testing.T) (Config, *FileWakeStore, Snapshot, time.Time) {
	t.Helper()
	c, _, _ := activationFixture(t)
	store := NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), 2)
	now := time.Now().UTC()
	old := sourceFixture(c, "old", "@planner previous question", c.Owner, now)
	s := Snapshot{Complete: true, WakeSources: []ConversationSource{old}}
	r := newWakeRouting()
	r.Baselined, r.ScopeSHA = true, wakeScope(c)
	r.Seen[old.NodeID] = ConversationRevision(old)
	r.Chains["saved"] = WakeChain{ID: BodyHash("chain"), RootID: "saved", PeerHop: 2}
	r.Suppressed = map[string]string{"suppressed": "peer_hop_limit"}
	r.Bindings["saved"] = WakeBinding{SessionID: "ses_saved", DiscussionID: "discussion", ThreadID: "saved", Directory: c.WorkRoot}
	r.APINotBefore = now.Add(time.Minute)
	if err := store.SaveRouting(r); err != nil {
		t.Fatal(err)
	}
	return c, store, s, now
}

func TestWakeContinuitySeedsUnchangedLegacyAndRetainsQueuedDestinations(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	before, err := store.Routing()
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(c.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldID := BodyHash("old ping")
	if _, err = QueuePing(c, store, PingInput{RequestID: oldID, Prompt: "old destination"}, now); err != nil {
		t.Fatal(err)
	}
	requestBefore, err := os.ReadFile(filepath.Join(store.Dir, "inbox", oldID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	seeded, err := store.Routing()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(seeded)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if digest, ok := fields["ContinuitySHA"].(string); !ok || !hexSHA.MatchString(digest) {
		t.Fatal("unchanged legacy routing not seeded")
	}
	delete(fields, "ContinuitySHA")
	delete(fields, "ContinuityScopeSHA")
	without, _ := json.Marshal(fields)
	var preserved WakeRoutingState
	_ = json.Unmarshal(without, &preserved)
	if !reflect.DeepEqual(before, preserved) {
		t.Fatal("seeding reinterpreted legacy routing")
	}
	c.Wake.CoordinationURL = "https://github.com/planning/board/discussions/2"
	s.WakeSources = append(s.WakeSources, sourceFixture(c, "unread", "@planner please answer", c.Owner, now.Add(time.Second)))
	if err = IngestDiscussionWakes(c, s, NewVersionedWakeStore(store.Dir, 2), now); err != nil {
		t.Fatal(err)
	}
	if err = IngestDiscussionWakes(c, s, NewVersionedWakeStore(store.Dir, 2), now); err != nil {
		t.Fatal(err)
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != 2 {
		t.Fatalf("lost/replayed unread request: %d %v", len(requests), err)
	}
	got, err := os.ReadFile(filepath.Join(store.Dir, "inbox", oldID+".json"))
	if err != nil || !bytes.Equal(got, requestBefore) {
		t.Fatal("queued ping was redirected")
	}
	if _, err = QueuePing(c, store, PingInput{RequestID: BodyHash("new ping"), Prompt: "new destination"}, now); err != nil {
		t.Fatal(err)
	}
	newRequest, err := store.request(BodyHash("new ping"))
	if err != nil || newRequest.Source.DiscussionURL != c.Wake.CoordinationURL {
		t.Fatal("new ping uses old destination", err)
	}
	stateAfter, err := os.ReadFile(filepath.Join(c.StateDir, "state.json"))
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("routing modified parent state")
	}
	r, err := store.Routing()
	if err != nil || r.ScopeSHA != wakeScope(c) || r.Chains["saved"] != before.Chains["saved"] || r.Bindings["saved"] != before.Bindings["saved"] || r.Suppressed["suppressed"] != "peer_hop_limit" || !r.APINotBefore.Equal(before.APINotBefore) {
		t.Fatal("destination change reset routing evidence", err)
	}
}

func TestWakeContinuityUnseededChangeRefusesUnchanged(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	before, _ := os.ReadFile(filepath.Join(store.Dir, "routing.json"))
	c.Wake.CoordinationURL = "https://github.com/planning/board/discussions/2"
	if err := IngestDiscussionWakes(c, s, store, now); err == nil || !strings.Contains(err.Error(), "unchanged configuration") {
		t.Fatalf("unseeded change did not refuse clearly: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(store.Dir, "routing.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("refusal changed routing")
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != 0 {
		t.Fatal("refusal changed inbox", err)
	}
}

func TestWakeContinuityMigrationFromBaselinedV1(t *testing.T) {
	c, _, _, _, now := teamScopeFixture(t)
	team := c.Team
	c.Version, c.Team = 1, nil
	c.Implementer, c.Reviewer = team.Members[1].Actor, team.Members[2].Actor
	c.Name, c.Self = team.Members[1].Name, team.Members[1].Actor
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/planning/board/discussions/1", PeerHopLimit: 2}
	s := NewFileStore(c.StateDir)
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	w := NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
	old := sourceFixture(c, "old", "@"+c.Self.Login+" old question", c.Owner, now)
	snapshot := Snapshot{Complete: true, WakeSources: []ConversationSource{old}}
	if err = IngestDiscussionWakes(c, snapshot, w, now); err != nil {
		t.Fatal(err)
	}
	ping := BodyHash("retained migration ping")
	if _, err = QueuePing(c, w, PingInput{RequestID: ping, Prompt: "retained"}, now); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(w.Dir, "inbox", ping+".json")
	before, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	routingBefore, err := w.Routing()
	if err != nil || !routingBefore.Baselined {
		t.Fatal("v1 fixture was not genuinely baselined", err)
	}
	c.Version, c.Team, c.Implementer, c.Reviewer = 2, team, Actor{}, Actor{}
	if err = MigrateState(c, 1, 2, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	w = NewVersionedWakeStore(w.Dir, 2)
	converted, err := w.Routing()
	routingBefore.Baselined = false
	if err != nil || !reflect.DeepEqual(converted, routingBefore) {
		t.Fatal("conversion changed routing history beyond its baseline acknowledgement", err)
	}
	if err = IngestDiscussionWakes(c, snapshot, w, now); err != nil {
		t.Fatal("converted baseline cannot poll", err)
	}
	requests, err := w.Requests()
	if err != nil || len(requests) != 1 || requests[0].ID != ping {
		t.Fatal("conversion replayed historical mention or lost queued ping", err)
	}
	after, err := os.ReadFile(requestPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conversion changed queued request", err)
	}
	snapshot.WakeSources = append(snapshot.WakeSources, sourceFixture(c, "new", "@"+c.Self.Login+" new question", c.Owner, now.Add(time.Minute)))
	if err = IngestDiscussionWakes(c, snapshot, NewVersionedWakeStore(w.Dir, 2), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requests, err = w.Requests()
	if err != nil || len(requests) != 2 {
		t.Fatal("converted worker did not accept new mention", err)
	}
}

func TestWakeContinuityMigrationRoutingFailureHolds(t *testing.T) {
	for _, fault := range []string{"before", "after", "corrupt"} {
		t.Run(fault, func(t *testing.T) {
			c, s, state, w, _ := migrationFixture(t)
			routing, err := w.Routing()
			if err != nil {
				t.Fatal(err)
			}
			routing.Baselined, routing.ScopeSHA = true, BodyHash("legacy routing authority")
			if err = w.SaveRouting(routing); err != nil {
				t.Fatal(err)
			}
			write := func(path string, raw []byte) error {
				isRouting := path == filepath.Join(w.Dir, "routing.json")
				if isRouting && fault == "before" {
					return errors.New("injected before routing replacement")
				}
				if err := atomicPrivate(path, raw); err != nil {
					return err
				}
				if isRouting && fault == "after" {
					return errors.New("injected uncertain routing replacement")
				}
				if isRouting && fault == "corrupt" {
					return atomicPrivate(path, []byte(`{}`))
				}
				return nil
			}
			if err = migrateState(c, 1, 2, state.InstanceID, write); err == nil {
				t.Fatal("failed routing conversion accepted")
			}
			for _, version := range []int{1, 2} {
				if _, err = NewVersionedFileStore(s.Dir, version).Load(); err == nil {
					t.Fatal("failed routing conversion left runnable state")
				}
			}
			if err = MigrateState(c, 1, 2, state.InstanceID); err == nil {
				t.Fatal("failed conversion replayed")
			}
		})
	}
}

func TestWakeContinuityRejectsInvalidStoredDigest(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	path := filepath.Join(store.Dir, "routing.json")
	raw, _ := os.ReadFile(path)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["ContinuitySHA"] = "invalid"
	raw, _ = json.Marshal(fields)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := IngestDiscussionWakes(c, s, store, now); err == nil {
		t.Fatal("invalid continuity accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("invalid record overwritten")
	}
}

type failRoutingSave struct{ WakeStore }

func (f failRoutingSave) SaveRouting(WakeRoutingState) error {
	return errors.New("injected routing save failure")
}

func TestWakeContinuitySaveFailureRetainsOneUnreadRequest(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	if err := IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Dir, "routing.json"))
	c.Wake.CoordinationURL = "https://github.com/planning/board/discussions/2"
	s.WakeSources = append(s.WakeSources, sourceFixture(c, "unread", "@planner continue", c.Owner, now.Add(time.Second)))
	if err := IngestDiscussionWakes(c, s, failRoutingSave{store}, now); err == nil {
		t.Fatal("save failure swallowed")
	}
	after, _ := os.ReadFile(filepath.Join(store.Dir, "routing.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed persisted routing")
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != 1 {
		t.Fatalf("unread not queued before failed save: %d %v", len(requests), err)
	}
	if err := IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	requests, err = store.Requests()
	if err != nil || len(requests) != 1 {
		t.Fatal("retry replayed request", err)
	}
}

func TestWakeContinuityPreservesCompletedReplyBytes(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	id := BodyHash("completed reply")
	if _, err := QueuePing(c, store, PingInput{RequestID: id, Prompt: "already answered"}, now); err != nil {
		t.Fatal(err)
	}
	r, err := store.Receipt(id)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = "completed"
	r.ReplyNodeID = "reply"
	r.ReplyURL = "https://github.com/planning/board/discussions/1#discussioncomment-1"
	if err = store.SaveReceipt(r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir, "receipts", id+".json")
	before, _ := os.ReadFile(path)
	if err = IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	c.Wake.CoordinationURL = "https://github.com/planning/board/discussions/2"
	if err = IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("destination change reinterpreted completed reply")
	}
}

func TestWakeContinuityAuthorityChangesStillBaseline(t *testing.T) {
	for _, name := range []string{"owner", "self", "name", "alias", "board", "hop", "roster"} {
		t.Run(name, func(t *testing.T) {
			c, store, s, now := continuityFixture(t)
			if err := IngestDiscussionWakes(c, s, store, now); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "owner":
				c.Owner.ID++
			case "self":
				c.Self.ID++
			case "name":
				c.Name = "another"
			case "alias":
				c.Self.Login = "renamed[bot]"
			case "board":
				c.DiscussionRepositories = append(c.DiscussionRepositories, "planning/other")
			case "hop":
				c.Wake.PeerHopLimit++
			case "roster":
				c.Team.Members[2].Actor.ID++
			}
			s.WakeSources = append(s.WakeSources, sourceFixture(c, "unread", "@planner do not replay history", c.Owner, now.Add(time.Second)))
			if err := IngestDiscussionWakes(c, s, store, now); err != nil {
				t.Fatal(err)
			}
			requests, err := store.Requests()
			if err != nil || len(requests) != 0 {
				t.Fatal("authority change reused destination bridge", err)
			}
		})
	}
}

func TestWakeContinuityCapacityDoesNotMarkUnreadSeen(t *testing.T) {
	c, store, s, now := continuityFixture(t)
	if err := IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	for i := range MaxWakeQueued {
		id := BodyHash(string(rune(i)) + "capacity")
		if _, err := QueuePing(c, store, PingInput{RequestID: id, Prompt: id}, now); err != nil {
			t.Fatal(err)
		}
	}
	c.Wake.CoordinationURL = "https://github.com/planning/board/discussions/2"
	s.WakeSources = append(s.WakeSources, sourceFixture(c, "unread", "@planner answer after capacity frees", c.Owner, now.Add(time.Second)))
	if err := IngestDiscussionWakes(c, s, store, now); err != nil {
		t.Fatal(err)
	}
	r, err := store.Routing()
	if err != nil || r.Seen["unread"] != "" || !r.IntakeDeferred {
		t.Fatal("full queue consumed unread source", err)
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != MaxWakeQueued {
		t.Fatal("capacity exceeded", err)
	}
	receipt, err := store.Receipt(requests[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Phase = "completed"
	if err = store.SaveReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	if err = IngestDiscussionWakes(c, s, NewVersionedWakeStore(store.Dir, 2), now); err != nil {
		t.Fatal(err)
	}
	requests, err = store.Requests()
	if err != nil || len(requests) != MaxWakeQueued+1 {
		t.Fatal("deferred unread source lost", err)
	}
}
