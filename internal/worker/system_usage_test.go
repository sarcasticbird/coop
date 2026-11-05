package worker

import (
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestConversationLoadsSharedKataContractWithoutGrantingCodeDelivery(t *testing.T) {
	_, comms, store, owner, bird, thread, _ := commsFixture(t)
	comms.Config.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	if _, err := store.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "Please maintain this Kata task", RequestID: "kata-question"}); err != nil {
		t.Fatal(err)
	}
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(t.Context())
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = comms.Advance(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{BirdSystemUsage, "Ordinary Kata task maintenance is permitted", "do not implement, formally review, clear QA or merge", "stable request_id", "do not activate Flox"} {
		if !strings.Contains(turn.Prompt, text) {
			t.Fatal("missing contract", text)
		}
	}
	if strings.Contains(turn.Prompt, "merge, reopen tasks") {
		t.Fatal("conversation forbids ordinary Kata reopening")
	}
}
