package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func TestFinalReviewSSEClient(t *testing.T) {
	_, comms, s, o, b, th, _ := commsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := comms.Client.(*masterclient.Client)
	stream, e := client.Events(ctx, c.Cursor{})
	if e != nil {
		t.Fatalf("configured SSE client cannot open stream: %v", e)
	}
	first, e := stream.Next(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = stream.Close(); e != nil {
		t.Fatal(e)
	}
	sent, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "after cursor", RequestID: "event-reconnect"})
	if e != nil {
		t.Fatal(e)
	}
	stream, e = client.Events(ctx, first.Cursor)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = stream.Close() }()
	next, e := stream.Next(ctx)
	if e != nil || next.MessageID != sent.MessageID || next.Sequence <= first.Sequence {
		t.Fatalf("cursor reconnect: %+v %v", next, e)
	}
	_, e = client.Events(ctx, c.Cursor{RestoreEpoch: 999, Sequence: next.Sequence})
	if !errors.Is(e, c.ErrEpoch) {
		t.Fatalf("stale cursor: %v", e)
	}
}
func TestFinalReviewThreadInventory(t *testing.T) {
	cfg, comms, s, o, b, _, _ := commsFixture(t)
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		if _, e := s.CreateThread(ctx, o, c.CreateThread{Title: fmt.Sprintf("thread %03d", i), Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: fmt.Sprintf("thread-%03d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	cl := comms.Client.(*masterclient.Client)
	var all []c.Thread
	for after := ""; ; {
		page, e := cl.Threads(ctx, c.ThreadQuery{After: after})
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Threads) > 100 {
			t.Fatal("oversize page")
		}
		all = append(all, page.Threads...)
		if page.Next == "" {
			break
		}
		if page.Next <= after {
			t.Fatal("cursor did not advance")
		}
		after = page.Next
	}
	if len(all) != 102 {
		t.Fatalf("lost threads: %d", len(all))
	}
	last, e := cl.Thread(ctx, all[len(all)-1].ID)
	if e != nil || last.ID != all[len(all)-1].ID {
		t.Fatalf("targeted thread: %+v %v", last, e)
	}
	peer := c.Principal{ID: "page-peer", Alias: "page-peer", Kind: "agent", GitHubBotID: 9871, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	qa := peer
	qa.ID = "page-qa"
	qa.Alias = "page-qa"
	qa.GitHubBotID = 9872
	if e = s.Configure(ctx, []c.Principal{peer, qa}, nil); e != nil {
		t.Fatal(e)
	}
	source, evidence := all[100], all[101]
	for _, th := range []c.Thread{source, evidence} {
		if _, e = s.SetMembers(ctx, o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: []string{o.ID, b.ID, peer.ID, qa.ID}, RequestID: "members-" + th.ID}); e != nil {
			t.Fatal(e)
		}
	}
	service := &master.Service{Store: s}
	revision, e := service.ProposeGoal(ctx, b, c.GoalProposal{ThreadID: source.ID, Purpose: "pagination regression", Scope: "bounded", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"membership remains valid"}, AuthorID: b.ID, PeerID: peer.ID, QA: c.QAPolicy{Required: true, ActorID: qa.ID, Environment: "test", EnvironmentRevision: "revision", EvidenceDestination: c.EvidenceDestination{MasterID: cfg.Master.MasterID, ThreadID: evidence.ID, Engagement: "coop"}}, RequestID: "page-goal"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "page-approve"}); e != nil {
		t.Fatal(e)
	}
	saveNativeFixtureConnection(t, cfg)
	authority, e := nativeCurrentGoal(ctx, cfg, cl, revision.Ref)
	if e != nil || !authority.Valid {
		t.Fatalf("native goal and QA threads beyond page1 rejected: %+v %v", authority, e)
	}
	if _, e = s.SetMembers(ctx, o, c.SetMembers{ThreadID: evidence.ID, ExpectedMembers: []string{o.ID, b.ID, peer.ID, qa.ID}, Members: []string{o.ID, b.ID, peer.ID}, RequestID: "remove-qa"}); e != nil {
		t.Fatal(e)
	}
	if _, e = nativeCurrentGoal(ctx, cfg, cl, revision.Ref); e == nil {
		t.Fatal("targeted lookup bypassed QA membership revocation")
	}

}

func TestFinalReviewThreadBytePages(t *testing.T) {
	_, comms, s, o, b, _, _ := commsFixture(t)
	ctx := context.Background()
	members := []string{o.ID, b.ID}
	principals := []c.Principal{}
	for i := 0; i < 98; i++ {
		id := fmt.Sprintf("long-%03d-%s", i, strings.Repeat("x", 110))
		members = append(members, id)
		principals = append(principals, c.Principal{ID: id, Alias: id, Kind: "agent", GitHubBotID: int64(1000 + i), Engagements: []string{"coop"}, Capabilities: []string{"read"}})
	}
	if e := s.Configure(ctx, principals, nil); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 90; i++ {
		if _, e := s.CreateThread(ctx, o, c.CreateThread{Title: strings.Repeat("title", 45), Engagement: "coop", Members: members, RequestID: fmt.Sprintf("large-%d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	cl := comms.Client.(*masterclient.Client)
	count, pages := 0, 0
	for after := ""; ; {
		page, e := cl.Threads(ctx, c.ThreadQuery{After: after})
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(page)
		if e != nil || len(raw) > c.MaxPageBytes || len(page.Threads) > 100 {
			t.Fatal("unbounded page", e)
		}
		count += len(page.Threads)
		pages++
		if page.Next == "" {
			break
		}
		if page.Next <= after || pages > 100 {
			t.Fatal("invalid continuation")
		}
		after = page.Next
	}
	if count != 91 || pages < 2 {
		t.Fatalf("byte bound lost inventory: count=%d pages=%d", count, pages)
	}
}
