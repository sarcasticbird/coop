package coordination

import "testing"

func TestCanDeliverRequiresIndependentRookAndLiveGrants(t *testing.T) {
	actors := GoalActors{Author: GoalActor{PrincipalID: "hawk", GitHubBotID: 11}, Peer: GoalActor{PrincipalID: "robin", GitHubBotID: 12}, QA: GoalActor{PrincipalID: "wren", GitHubBotID: 13}}
	for _, change := range []string{"none", "alias", "owner", "no-bot", "no-read", "no-send", "no-deliver", "author-id", "peer-id", "qa-id", "author-bot", "peer-bot", "qa-bot"} {
		t.Run(change, func(t *testing.T) {
			p := Principal{ID: "rook", Alias: "rook", Kind: "agent", GitHubBotID: 14, Capabilities: []string{"read", "send", "deliver"}}
			a := actors
			switch change {
			case "alias":
				p.ID = "imposter"
			case "owner":
				p.Kind = "owner"
			case "no-bot":
				p.GitHubBotID = 0
			case "no-read":
				p.Capabilities = []string{"send", "deliver"}
			case "no-send":
				p.Capabilities = []string{"read", "deliver"}
			case "no-deliver":
				p.Capabilities = []string{"read", "send"}
			case "author-id":
				a.Author.PrincipalID = p.ID
			case "peer-id":
				a.Peer.PrincipalID = p.ID
			case "qa-id":
				a.QA.PrincipalID = p.ID
			case "author-bot":
				p.GitHubBotID = a.Author.GitHubBotID
			case "peer-bot":
				p.GitHubBotID = a.Peer.GitHubBotID
			case "qa-bot":
				p.GitHubBotID = a.QA.GitHubBotID
			}
			if got := CanDeliver(p, a); got != (change == "none") {
				t.Fatalf("CanDeliver = %v", got)
			}
		})
	}
}
