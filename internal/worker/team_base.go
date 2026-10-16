package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func validBaseBranch(branch string) bool {
	if branch == "" || len(branch) > 255 || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\x7f") {
		return false
	}
	for _, r := range branch {
		if r < 32 {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Retargeting does not edit the PR body. Bind the complete native timeline so
// returning to an earlier base cannot resurrect a pre-retarget approval.
func (g *GitHubClient) readTeamBaseScope(ctx context.Context, repo string, number int, version prBodyVersion) (string, time.Time, error) {
	if !validBaseBranch(version.BaseRefName) {
		return "", time.Time{}, ghError("invalid_PR_base")
	}
	parts := strings.Split(repo, "/")
	// GitHub's timeline totalCount includes filtered and hidden entries. Read
	// all visible pages, hashing only retarget events, not comments or reviews.
	query := `query WorkerTeamBaseScope($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){timelineItems(first:100,after:$after){totalCount nodes{__typename ... on BaseRefChangedEvent{id createdAt previousRefName currentRefName}} pageInfo{hasNextPage endCursor}}}}}`
	nodes, err := g.teamConnection(ctx, parts[0], query, map[string]any{"owner": parts[0], "repo": parts[1], "number": number}, "repository", "pullRequest", "timelineItems")
	if err != nil {
		return "", time.Time{}, err
	}
	current := version.BaseRefName
	var latest, previous time.Time
	seen := map[string]bool{}
	var baseEvents []json.RawMessage
	for i := len(nodes) - 1; i >= 0; i-- {
		e, err := decode[struct {
			Kind                                string `json:"__typename"`
			ID, PreviousRefName, CurrentRefName string
			CreatedAt                           time.Time
		}](nodes[i])
		if err != nil || e.Kind == "" {
			return "", time.Time{}, ghError("incoherent_PR_base_history")
		}
		if e.Kind != "BaseRefChangedEvent" {
			continue
		}
		if err != nil || e.ID == "" || seen[e.ID] || !validBaseBranch(e.PreviousRefName) || !validBaseBranch(e.CurrentRefName) || e.CurrentRefName != current || e.CreatedAt.Before(version.CreatedAt) || e.CreatedAt.After(time.Now()) || !previous.IsZero() && e.CreatedAt.After(previous) {
			return "", time.Time{}, ghError("incoherent_PR_base_history")
		}
		if latest.IsZero() {
			latest = e.CreatedAt
		}
		previous, current, seen[e.ID] = e.CreatedAt, e.PreviousRefName, true
		baseEvents = append(baseEvents, nodes[i])
	}
	raw, _ := json.Marshal(baseEvents)
	return BodyHash(version.BaseRefName + "\x00" + string(raw)), latest, nil
}
