package worker

import (
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"time"
)

type cachedProjectOrder struct {
	At       time.Time
	Ordinals map[string]int
}

// GitHub exposes board order through the item connection, not memberships.
// Read identifiers only; retain same-repository ranks transiently in the
// collector. Unrelated board identifiers never enter a shared SQL payload.
func (s *githubObservationSource) projectOrdinal(ctx context.Context, g *GitHubClient, lease c.GitHubLease, t Task) (int, error) {
	key := BodyHash(lease.Object.RouteID + "\x00" + stringID(int(lease.AppID)) + "\x00" + stringID(int(lease.InstallationID)) + "\x00" + t.ProjectID + "\x00" + t.Repo)
	cached, found := s.projectOrder[key]
	if !found || !cached.At.Add(5*time.Minute).After(s.clock.Now()) {
		query := `query WorkerCacheProjectOrder($id:ID!,$after:String){node(id:$id){... on ProjectV2{items(first:100,after:$after){nodes{isArchived content{__typename ... on Issue{url}}} pageInfo{hasNextPage endCursor}}}}}`
		nodes, err := g.connection(ctx, t.ProjectOwner, query, map[string]any{"id": t.ProjectID}, []string{"node", "items"})
		if err != nil {
			return 0, err
		}
		cached = cachedProjectOrder{At: s.clock.Now(), Ordinals: map[string]int{}}
		for i, raw := range nodes {
			var item struct {
				IsArchived bool
				Content    *struct{ URL string }
			}
			if json.Unmarshal(raw, &item) != nil {
				return 0, c.ErrInvalid
			}
			if item.IsArchived || item.Content == nil || !strings.HasPrefix(item.Content.URL, "https://github.com/"+t.Repo+"/issues/") {
				continue
			}
			cached.Ordinals[item.Content.URL] = i
		}
		if s.projectOrder == nil {
			s.projectOrder = map[string]cachedProjectOrder{}
		}
		count := len(cached.Ordinals)
		for key, v := range s.projectOrder {
			if !v.At.Add(5 * time.Minute).After(s.clock.Now()) {
				delete(s.projectOrder, key)
			} else {
				count += len(v.Ordinals)
			}
		}
		if count > MaxConnection {
			s.projectOrder = map[string]cachedProjectOrder{}
		}
		s.projectOrder[key] = cached
	}
	ordinal, ok := cached.Ordinals[t.URL]
	if !ok {
		return 0, ghError("cache_project_order_unknown")
	}
	if runner, ok := g.runner.(*observationRunner); ok {
		runner.reused["project-order"] = cached.At
	}
	return ordinal, nil
}
