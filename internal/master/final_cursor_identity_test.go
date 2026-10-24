package master

import (
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"path/filepath"
	"testing"
)

func TestFinalReviewReacceptRejectsReusedNativeEventPosition(t *testing.T) {
	s, owner, _, _, n := mappedConversation(t)
	original := cutoverEvent(1)
	n.events = []taskstore.WorkEvent{original}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if _, err := s.Backup(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	cfg, lineage, id := s.conversations, s.epochPath, s.id
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(t.Context(), snapshot, target, lineage, false); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(t.Context(), StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: id, EpochPath: lineage})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err = restored.ConfigureConversations(t.Context(), n, cfg); err != nil {
		t.Fatal(err)
	}
	observed, err := restored.ConversationCursor(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	// Restoring the provider DB can roll its sequence back. New events may then
	// occupy the same numeric position in the same instance and project.
	replacement := original
	replacement.UID = "different-event-at-reused-position"
	replacement.Payload = []byte(`{"comment_uid":"01M5MSA8FZCCD7DWMTP6RWM3ME","body":"new post-restore comment that was never observed"}`)
	n.events = []taskstore.WorkEvent{replacement}
	accepted, err := restored.ReacceptConversationCursor(t.Context(), owner, c.ReacceptConversationCursor{Observed: observed, Reason: "reconcile restored source", RequestID: "reaccept-different-native-event"})
	if err == nil {
		t.Fatalf("different native event %q replaced persisted %q at ID=1 but cursor was accepted: %+v", replacement.UID, original.UID, accepted)
	}
}

func TestConversationPollChecksImmutableCheckpoint(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		for _, change := range []string{"unchanged", "behind", "reused", "anchorless"} {
			t.Run(fmt.Sprintf("baseline=%v/%s", baseline, change), func(t *testing.T) {
				s, o, _, _, n, mapping := cutoverFixture(t)
				n.events = []taskstore.WorkEvent{cutoverEvent(1)}
				if baseline {
					mapping.AfterEventID = 1
				}
				if _, err := s.MapConversation(t.Context(), o, mapping); err != nil {
					t.Fatal(err)
				}
				if !baseline {
					if err := s.PollConversations(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				cur, err := s.ConversationCursor(t.Context(), o)
				if err != nil || cur.AfterEventUID != "event-1" {
					t.Fatal(cur, err)
				}
				switch change {
				case "behind":
					n.events = nil
				case "reused":
					n.events[0].UID = "replacement"
				case "anchorless":
					if _, err = s.db.ExecContext(t.Context(), "UPDATE conversation_cursor SET body=json_remove(body,'$.after_uid')"); err != nil {
						t.Fatal(err)
					}
				}
				reads := n.reads
				err = s.PollConversations(t.Context())
				if change == "unchanged" {
					if err != nil {
						t.Fatal(err)
					}
					if n.reads-reads != 2 {
						t.Fatalf("checkpoint plus event page should take exactly two reads, got %d", n.reads-reads)
					}
				} else if !errors.Is(err, c.ErrEpoch) {
					t.Fatalf("unsafe checkpoint %s accepted: %v", change, err)
				}
				after, e := s.ConversationCursor(t.Context(), o)
				if e != nil || after.AfterEventID != cur.AfterEventID {
					t.Fatal("cursor changed on verification", after, e)
				}
			})
		}
	}
}

func TestLegacyConversationAnchorNeedsExplicitOwnerReconciliation(t *testing.T) {
	s, o, _, thread, n := mappedConversation(t)
	n.events = []taskstore.WorkEvent{cutoverEvent(1)}
	if err := s.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE conversation_cursor SET body=json_remove(body,'$.after_uid')"); err != nil {
		t.Fatal(err)
	}
	if s.ConversationActive(t.Context()) {
		t.Error("anchorless source advertised active")
	}
	if _, err := s.Send(t.Context(), o, c.SendMessage{ThreadID: thread.ID, Body: "do not publish before reconciliation", RequestID: "anchorless-send"}); !errors.Is(err, c.ErrEpoch) || n.writes != 0 {
		t.Fatal("anchorless source published before reconciliation", err, n.writes)
	}
	if err := s.Quiesce(t.Context(), o, s.epoch); err != nil {
		t.Fatal(err)
	}
	old, err := s.ConversationCursor(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	in := c.ReacceptConversationCursor{Observed: old, Reason: "verified retained independent native checkpoint record", RequestID: "repair-legacy-anchor"}
	if _, err = s.ReacceptConversationCursor(t.Context(), o, in); !errors.Is(err, c.ErrEpoch) {
		t.Fatal("silently adopted numeric cursor", err)
	}
	in.ReconciledEventUID = "wrong-event"
	if _, err = s.ReacceptConversationCursor(t.Context(), o, in); !errors.Is(err, c.ErrEpoch) {
		t.Fatal("wrong independent evidence accepted", err)
	}
	in.ReconciledEventUID = "event-1"
	result, err := s.ReacceptConversationCursor(t.Context(), o, in)
	if err != nil || result.AfterEventUID != "event-1" || result.AfterEventID != old.AfterEventID {
		t.Fatal(result, err)
	}
	if _, err = s.ReacceptConversationCursor(t.Context(), o, in); err != nil {
		t.Fatal("lost response retry", err)
	}
	var raw []byte
	if err = s.db.QueryRowContext(t.Context(), "SELECT body FROM conversation_cursor").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved taskstore.EventCursor
	if err = json.Unmarshal(raw, &saved); err != nil || saved.AfterUID != "event-1" {
		t.Fatal(saved, err)
	}
	in.Observed = result
	in.RequestID = "replace-known-anchor"
	if _, err = s.ReacceptConversationCursor(t.Context(), o, in); !errors.Is(err, c.ErrInvalid) {
		t.Fatal("overwrote immutable anchor", err)
	}
}
