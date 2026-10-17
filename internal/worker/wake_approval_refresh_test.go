package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

type changedWakeApproval struct {
	*wakeGitHub
	reads int
}

func (g *changedWakeApproval) RevalidateWake(ctx context.Context, r WakeRequest) (ConversationSource, Goal, error) {
	g.reads++
	if g.reads == 2 {
		g.snapshot.Goals = append([]Goal(nil), g.snapshot.Goals...)
		g.snapshot.Goals[0].Decisions = []Decision{{ID: "pause", Author: r.Source.Author, Command: "/coop pause", CreatedAt: time.Now().Add(-time.Second)}}
	}
	return g.wakeGitHub.RevalidateWake(ctx, r)
}

type capturedWakePrompt struct {
	*fakeOpenCode
	prompt string
}

func (c *capturedWakePrompt) Submit(ctx context.Context, cfg Config, j Job, prompt string) (Turn, error) {
	c.prompt = prompt
	return c.fakeOpenCode.Submit(ctx, cfg, j, prompt)
}

func TestWakeUsesApprovalRefreshedAfterSessionPreparation(t *testing.T) {
	e, code, clock, store := wakeEngineFixture(t)
	g := &changedWakeApproval{wakeGitHub: e.GitHub.(*wakeGitHub)}
	capture := &capturedWakePrompt{fakeOpenCode: code}
	e.Wakes.GitHub, e.Wakes.OpenCode = g, capture
	if _, err := QueuePing(e.Config, store, PingInput{Prompt: "status"}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Wakes.Tick(context.Background(), g.snapshot); err != nil {
		t.Fatal(err)
	}
	if capture.calls != 1 || !strings.Contains(capture.prompt, "owner paused goal") {
		t.Fatal("wake prompt retained approval from before session preparation")
	}
}
