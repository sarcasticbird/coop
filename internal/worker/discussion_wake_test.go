package worker

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sourceFixture(c Config, id, body string, author Actor, n time.Time) ConversationSource {
	return ConversationSource{NodeID: id, URL: "https://github.com/planning/board/discussions/1#discussioncomment-1", DiscussionID: "discussion", DiscussionURL: "https://github.com/orgs/planning/discussions/1", RootID: "root", Body: body, Author: author, CreatedAt: n}
}

func TestMentionOverridesImplicitReplyRecipient(t *testing.T) {
	c, _, n := policyFixture(t)
	s := sourceFixture(c, "comment-1", "@builder[bot] please answer", c.Owner, n)
	s.ParentID, s.ParentAuthor = "hawk-comment", c.Reviewer
	got := ConversationTargets(c, s)
	if len(got) != 1 || !sameActor(got[0], c.Implementer) {
		t.Fatalf("wrong targets: %+v", got)
	}
}

func TestDiscussionWakeTargetBoundaries(t *testing.T) {
	c, _, n := policyFixture(t)
	for _, row := range []struct {
		body   string
		author Actor
		parent Actor
		want   int
	}{
		{"@builder @REVIEWER[bot] both", c.Owner, Actor{}, 2},
		{"@builder @builder[bot] repeated", c.Owner, c.Implementer, 1},
		{"a reply", c.Owner, c.Implementer, 1},
		{"@builder self", c.Implementer, Actor{}, 0},
		{"@builder forged", Actor{ID: 99, Login: c.Owner.Login, Kind: "User"}, Actor{}, 0},
		{"@builder-extra", c.Owner, Actor{}, 0},
		{"mail@builder.example", c.Owner, Actor{}, 0},
		{"`@builder` example", c.Owner, Actor{}, 0},
		{"```text\n@builder\n```", c.Owner, Actor{}, 0},
		{"> @builder quoted", c.Owner, Actor{}, 0},
		{"ordinary status", c.Reviewer, Actor{}, 0},
	} {
		s := sourceFixture(c, "comment", row.body, row.author, n)
		if row.parent.ID != 0 {
			s.ParentID, s.ParentAuthor = "parent", row.parent
		}
		if got := ConversationTargets(c, s); len(got) != row.want {
			t.Errorf("body=%q got=%+v want=%d", row.body, got, row.want)
		}
	}
}

func TestDiscussionWakeBaselineRestartAndEdits(t *testing.T) {
	c, store := wakeFixture(t)
	n := time.Now().UTC()
	old := sourceFixture(c, "old", "@builder old question", c.Owner, n)
	s := Snapshot{Complete: true, WakeSources: []ConversationSource{old}}
	if err := IngestDiscussionWakes(c, s, store, n); err != nil {
		t.Fatal(err)
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != 0 {
		t.Fatal("first activation replied to history")
	}
	newSource := sourceFixture(c, "new", "@builder new question", c.Owner, n.Add(time.Minute))
	s.WakeSources = append(s.WakeSources, newSource)
	s.Complete = false
	if err := IngestDiscussionWakes(c, s, store, n); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	requests, _ = store.Requests()
	if len(requests) != 0 {
		t.Fatal("partial snapshot advanced queue")
	}
	s.Complete = true
	if err := IngestDiscussionWakes(c, s, store, n); err != nil {
		t.Fatal(err)
	}
	restarted := NewWakeStore(store.Dir)
	if err := IngestDiscussionWakes(c, s, restarted, n); err != nil {
		t.Fatal(err)
	}
	requests, _ = store.Requests()
	if len(requests) != 1 {
		t.Fatal("restart replayed or dropped a message")
	}
	s.WakeSources[1].Body = "  @builder new question\r\n"
	if err := IngestDiscussionWakes(c, s, restarted, n); err != nil {
		t.Fatal(err)
	}
	requests, _ = store.Requests()
	if len(requests) != 1 {
		t.Fatal("outer whitespace created another wake")
	}
	s.WakeSources[1].Body = "@builder clarified question"
	s.WakeSources[1].EditedAt = n.Add(2 * time.Minute)
	if err := IngestDiscussionWakes(c, s, restarted, n); err != nil {
		t.Fatal(err)
	}
	requests, _ = store.Requests()
	if len(requests) != 2 {
		t.Fatal("actionable edit did not wake")
	}
}

func TestWakeRoutingUnmarkedPeerBudgetPersistsAndOwnerResets(t *testing.T) {
	c, store := wakeFixture(t)
	n := time.Now().UTC()
	s := Snapshot{Complete: true}
	if err := IngestDiscussionWakes(c, s, store, n); err != nil {
		t.Fatal(err)
	}
	sequence := []ConversationSource{
		sourceFixture(c, "owner", "@builder begin", c.Owner, n),
		sourceFixture(c, "robin", "@reviewer please answer", c.Implementer, n.Add(time.Second)),
		sourceFixture(c, "hawk", "@builder response", c.Reviewer, n.Add(2*time.Second)),
		sourceFixture(c, "robin-again", "@reviewer next", c.Implementer, n.Add(3*time.Second)),
		sourceFixture(c, "hawk-again", "@builder endless", c.Reviewer, n.Add(4*time.Second)),
	}
	for _, input := range sequence {
		s.WakeSources = append(s.WakeSources, input)
		if err := IngestDiscussionWakes(c, s, NewWakeStore(store.Dir), n); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := store.Requests()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].PeerHop != 0 || requests[1].PeerHop != 2 {
		t.Fatalf("peer chain replayed/reset: %+v", requests)
	}
	owner := sourceFixture(c, "owner-new", "@builder continue", c.Owner, n.Add(time.Minute))
	s.WakeSources = append(s.WakeSources, owner)
	if err := IngestDiscussionWakes(c, s, store, n); err != nil {
		t.Fatal(err)
	}
	requests, _ = store.Requests()
	if len(requests) != 3 || requests[2].PeerHop != 0 || requests[2].ChainID == requests[0].ChainID {
		t.Fatal("owner input failed to start fresh chain")
	}
}

func TestDiscussionDirectedMessageDoesNotAlsoPlanForHawk(t *testing.T) {
	c, _, n := policyFixture(t)
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	g := NewGitHubClient(nil)
	g.config = c
	d := graphDiscussion{ID: "discussion", URL: c.Wake.CoordinationURL, Body: "Current planning", CreatedAt: n, Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}}
	_, events := g.goalFromDiscussion(d, []Comment{{ID: "directed", NodeID: "directed", Body: "@builder please answer", Author: c.Owner, CreatedAt: n}})
	for _, event := range events {
		if event.ID == "directed" {
			t.Fatal("directed Robin message also created generic Hawk planning")
		}
	}
	c.Wake.Enabled = false
	g.config = c
	_, events = g.goalFromDiscussion(d, []Comment{{ID: "directed", NodeID: "directed", Body: "@builder please answer", Author: c.Owner, CreatedAt: n}})
	found := false
	for _, event := range events {
		found = found || event.ID == "directed"
	}
	if !found {
		t.Fatal("disabled v2 changed legacy planning")
	}
}

func TestConversationRevisionBindsTargetParentAndText(t *testing.T) {
	c, _, n := policyFixture(t)
	s := sourceFixture(c, "id", " @builder question\r\n", c.Owner, n)
	a := ConversationRevision(s)
	s.Body = strings.TrimSpace(strings.ReplaceAll(s.Body, "\r\n", "\n"))
	if a != ConversationRevision(s) {
		t.Fatal("outer whitespace changed actionable revision")
	}
	s.ParentID = "other-thread"
	if a == ConversationRevision(s) {
		t.Fatal("changed reply target kept old revision")
	}
}

func TestWakeRoutingBothVMsDeriveSameChain(t *testing.T) {
	c, a := wakeFixture(t)
	other := c
	other.Name = "hawk"
	other.Self = other.Reviewer
	other.Role = "planner"
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := NewWakeStore(base + "/wake")
	n := time.Now().UTC()
	snapshot := Snapshot{Complete: true}
	for _, row := range []struct {
		c Config
		s WakeStore
	}{{c, a}, {other, b}} {
		if err := IngestDiscussionWakes(row.c, snapshot, row.s, n); err != nil {
			t.Fatal(err)
		}
	}
	for i, input := range []ConversationSource{sourceFixture(c, "o", "@builder go", c.Owner, n), sourceFixture(c, "r", "@reviewer answer", c.Implementer, n.Add(time.Second)), sourceFixture(c, "h", "@builder answer", c.Reviewer, n.Add(2*time.Second))} {
		snapshot.WakeSources = append(snapshot.WakeSources, input)
		for _, row := range []struct {
			c Config
			s WakeStore
		}{{c, a}, {other, b}} {
			if err := IngestDiscussionWakes(row.c, snapshot, row.s, n); err != nil {
				t.Fatal(err)
			}
		}
		ar, _ := a.Routing()
		br, _ := b.Routing()
		if ar.Chains["discussion:root"] != br.Chains["discussion:root"] {
			t.Fatalf("VMs disagree at hop %d", i)
		}
	}
}

func TestWakePeerRepliesCannotBypassBudgetThroughLegacyPlanning(t *testing.T) {
	c, _ := wakeFixture(t)
	g := NewGitHubClient(nil)
	g.config = c
	d := graphDiscussion{ID: "discussion", URL: c.Wake.CoordinationURL, Body: "Planning", Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}}
	r := testWakeRequest(c)
	for _, body := range []string{"Here is the answer.\n" + WakeResultMarker(r), "An unaddressed peer status"} {
		_, events := g.goalFromDiscussion(d, []Comment{{ID: "peer", NodeID: "peer", RootID: "owner-root", ParentID: "owner-root", ParentAuthor: c.Owner, Author: c.Implementer, Body: body}})
		for _, event := range events {
			if event.ID == "peer" {
				t.Fatal("peer bypassed bounded wake routing through generic planning")
			}
		}
	}
}
func TestWakeMarkerOnlyAndAcknowledgementsDoNotWakePeers(t *testing.T) {
	c, _ := wakeFixture(t)
	r := testWakeRequest(c)
	for _, body := range []string{WakeResultMarker(r), "Acknowledged.\n" + WakeResultMarker(r), "I will reply later.\n" + WakeResultMarker(r)} {
		s := sourceFixture(c, "peer", body, c.Reviewer, time.Now())
		s.ParentID = "root"
		s.ParentAuthor = c.Implementer
		if got := ConversationTargets(c, s); len(got) != 0 {
			t.Fatal("non-answer status woke peer")
		}
	}
}

type failWakeCheckpoint struct{ WakeStore }

func (s failWakeCheckpoint) SaveRouting(WakeRoutingState) error { return errors.New("checkpoint lost") }
func TestWakeCosmeticEditAfterLostCheckpointReconcilesRequest(t *testing.T) {
	c, store := wakeFixture(t)
	n := time.Now().UTC()
	if err := IngestDiscussionWakes(c, Snapshot{Complete: true}, store, n); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Complete: true, WakeSources: []ConversationSource{sourceFixture(c, "source", "@builder question", c.Owner, n)}}
	if err := IngestDiscussionWakes(c, snapshot, failWakeCheckpoint{store}, n); err == nil {
		t.Fatal("expected lost checkpoint")
	}
	records, _ := store.Requests()
	if len(records) != 1 {
		t.Fatal("request not durable before checkpoint")
	}
	snapshot.WakeSources[0].Body = "  @builder question\r\n"
	snapshot.WakeSources[0].EditedAt = n.Add(time.Minute)
	if err := IngestDiscussionWakes(c, snapshot, NewWakeStore(store.Dir), n); err != nil {
		t.Fatal(err)
	}
	records, _ = store.Requests()
	if len(records) != 1 {
		t.Fatal("cosmetic edit duplicated wake")
	}
}
