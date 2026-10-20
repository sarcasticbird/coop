package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Read only memberships of registered issues, rather than the Project's full
// item connection. Full discovery still owns board order; validation needs
// current membership and values, never unrelated board content.
func (g *GitHubClient) nativeProjectMemberships(ctx context.Context, p ProjectRef, projectID string, tasks map[string]Task) ([]json.RawMessage, error) {
	refs := make([]string, 0, len(tasks))
	for ref := range tasks {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var out []json.RawMessage
	query := `query WorkerNativeTaskProjectItems($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){issue(number:$number){projectItems(first:100,after:$after,includeArchived:false){nodes{id isArchived project{id} content{__typename ... on Issue{url}}} pageInfo{hasNextPage endCursor}}}}}`
	for _, ref := range refs {
		repo, number, err := parseRef(ref)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(repo, "/")
		items, err := g.connection(ctx, parts[0], query, map[string]any{"owner": parts[0], "repo": parts[1], "number": number}, []string{"repository", "issue", "projectItems"})
		if err != nil {
			return nil, err
		}
		found := false
		for _, raw := range items {
			var item struct {
				ID         string
				IsArchived bool
				Project    *struct{ ID string }
				Content    *struct {
					Kind string `json:"__typename"`
					URL  string
				}
			}
			if json.Unmarshal(raw, &item) != nil || item.ID == "" || item.Project == nil || item.Project.ID == "" {
				return nil, ghError("invalid_project_item")
			}
			if item.Project.ID != projectID || item.IsArchived {
				continue
			}
			if found || item.Content == nil || item.Content.Kind != "Issue" || item.Content.URL != ref {
				return nil, ghError("invalid_project_item")
			}
			found = true
			out = append(out, raw)
		}
	}
	return out, nil
}

func nativeProjectMembershipUnavailable(err error) bool {
	var gh *GitHubError
	if !errors.As(err, &gh) {
		return false
	}
	switch gh.Category {
	case "permission_denied", "not_found", "unreadable_content", "graphql_failed":
		return true
	default:
		return false
	}
}
