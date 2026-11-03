package coordination

import "slices"

// CanDeliver separates integration from authorship. Goal approval authorizes
// routine delivery; Rook still needs a live grant and independent source gates.
func CanDeliver(p Principal, actors GoalActors) bool {
	return p.ID == "rook" && p.Kind == "agent" && p.GitHubBotID > 0 &&
		slices.Contains(p.Capabilities, "read") && slices.Contains(p.Capabilities, "send") && slices.Contains(p.Capabilities, "deliver") &&
		p.ID != actors.Author.PrincipalID && p.ID != actors.Peer.PrincipalID && p.ID != actors.QA.PrincipalID &&
		p.GitHubBotID != actors.Author.GitHubBotID && p.GitHubBotID != actors.Peer.GitHubBotID && p.GitHubBotID != actors.QA.GitHubBotID
}
