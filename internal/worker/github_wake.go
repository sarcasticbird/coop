package worker

import (
	"context"
	"sort"
	"strings"
)

type WakeConversation interface {
	RevalidateWake(context.Context, WakeRequest) (ConversationSource, Goal, error)
	WakeReply(context.Context, WakeRequest) (Comment, bool, error)
}

func (g *GitHubClient) wakeDiscussion(ctx context.Context, r WakeRequest) (graphDiscussion, []Comment, error) {
	ref := r.Source.DiscussionURL
	if !goalInScope(g.config, Goal{URL: ref}) {
		return graphDiscussion{}, nil, ghError("wake_scope")
	}
	id := r.Source.DiscussionID
	if id == "" {
		id = g.goalIDs[ref]
	}
	if id == "" {
		return graphDiscussion{}, nil, ghError("source_deleted")
	}
	owner := ""
	for _, repo := range g.config.DiscussionRepositories {
		candidate := strings.Split(repo, "/")[0]
		if strings.HasPrefix(ref, "https://github.com/orgs/"+candidate+"/discussions/") || strings.HasPrefix(ref, "https://github.com/"+repo+"/discussions/") {
			owner = candidate
			break
		}
	}
	if owner == "" {
		return graphDiscussion{}, nil, ghError("wake_scope")
	}
	query := `query WorkerWakeDiscussion($id:ID!){node(id:$id){... on Discussion {id url body createdAt lastEditedAt author{` + actorFields + `}}}}`
	data, err := g.graphql(ctx, owner, query, map[string]any{"id": id})
	if err != nil {
		return graphDiscussion{}, nil, err
	}
	raw, err := at(data, "node")
	if err != nil {
		return graphDiscussion{}, nil, ghError("source_deleted")
	}
	d, err := decode[graphDiscussion](raw)
	if err != nil || d.ID != id || d.URL != ref {
		return graphDiscussion{}, nil, ghError("source_superseded")
	}
	comments, err := g.goalComments(ctx, owner, d)
	return d, comments, err
}
func (g *GitHubClient) RevalidateWake(ctx context.Context, r WakeRequest) (ConversationSource, Goal, error) {
	d, comments, err := g.wakeDiscussion(ctx, r)
	if err != nil {
		return ConversationSource{}, Goal{}, err
	}
	goal, _ := g.goalFromDiscussion(d, comments)
	sources := discussionSources(d, comments)
	if r.Kind == "ping" {
		return sources[0], goal, nil
	}
	for _, s := range sources {
		if s.NodeID == r.Source.NodeID {
			trusted := false
			for _, a := range ConversationTargets(g.config, s) {
				trusted = trusted || sameActor(a, g.config.Self)
			}
			if !trusted {
				return ConversationSource{}, Goal{}, ghError("source_superseded")
			}
			if s.Revision != r.Source.Revision || !sameActor(s.Author, r.Source.Author) || s.ParentID != r.Source.ParentID || s.RootID != r.Source.RootID {
				return ConversationSource{}, Goal{}, ghError("source_superseded")
			}
			return s, goal, nil
		}
	}
	return ConversationSource{}, Goal{}, ghError("source_deleted")
}
func matchingWakeReply(c Config, r WakeRequest, comments []Comment) (Comment, bool, error) {
	var matches []Comment
	for _, comment := range comments {
		if !sameActor(comment.Author, c.Self) || comment.NodeID == "" || !validGitHubURL(strings.Split(comment.URL, "#")[0]) {
			continue
		}
		if r.Source.NodeID == r.Source.DiscussionID {
			if comment.ParentID != "" || comment.RootID != comment.ID {
				continue
			}
		} else if comment.RootID != r.Source.RootID || comment.ParentID != wakeReplyTarget(r.Source) {
			continue
		}
		var m wakeMarker
		if metadata(comment.Body, "wake", &m) != nil || m != (wakeMarker{r.ID, r.ChainID, r.PeerHop, "reply"}) {
			continue
		}
		if !substantiveWakeText(comment.Body) {
			continue
		}
		matches = append(matches, comment)
	}
	if len(matches) == 0 {
		return Comment{}, false, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].CreatedAt.Before(matches[j].CreatedAt)
	})
	if len(matches) > 1 {
		return matches[0], true, ghError("duplicate_reply")
	}
	return matches[0], true, nil
}
func (g *GitHubClient) WakeReply(ctx context.Context, r WakeRequest) (Comment, bool, error) {
	_, comments, err := g.wakeDiscussion(ctx, r)
	if err != nil {
		return Comment{}, false, err
	}
	return matchingWakeReply(g.config, r, comments)
}
