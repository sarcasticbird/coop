package worker

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func configuredImplementer(c Config, actor Actor) bool {
	if c.Version != 2 {
		return sameActor(actor, c.Implementer)
	}
	if c.Team == nil {
		return false
	}
	m, ok := TeamMemberForActor(*c.Team, actor)
	return ok && slices.Contains(m.Capabilities, "implement")
}

// Evidence connections need an explicit terminal page and a stable total count.
// Missing pagination fields must not look like an empty, complete connection.
func (g *GitHubClient) teamConnection(ctx context.Context, owner, query string, variables map[string]any, path ...string) ([]json.RawMessage, error) {
	vars := make(map[string]any, len(variables)+1)
	for k, v := range variables {
		vars[k] = v
	}
	var nodes []json.RawMessage
	total := -1
	seen := map[string]bool{}
	for {
		data, err := g.graphql(ctx, owner, query, vars)
		if err != nil {
			return nil, err
		}
		raw, err := at(data, path...)
		if err != nil {
			return nil, err
		}
		var page struct {
			Nodes      *[]json.RawMessage
			TotalCount *int
			PageInfo   *struct {
				HasNextPage *bool
				EndCursor   string
			}
		}
		if json.Unmarshal(raw, &page) != nil || page.Nodes == nil || page.TotalCount == nil || page.PageInfo == nil || page.PageInfo.HasNextPage == nil {
			return nil, ghError("incomplete_team_connection")
		}
		if total == -1 {
			total = *page.TotalCount
		}
		if total < 0 || total > MaxConnection || total != *page.TotalCount {
			return nil, ghError("incoherent_team_connection")
		}
		nodes = append(nodes, (*page.Nodes)...)
		if len(nodes) > total {
			return nil, ghError("incoherent_team_connection")
		}
		if !*page.PageInfo.HasNextPage {
			// Timeline totalCount includes filtered/hidden entries even on an
			// explicit terminal page. Identity connections require exact counts.
			if len(nodes) != total && (len(path) == 0 || path[len(path)-1] != "timelineItems") {
				return nil, ghError("incomplete_team_connection")
			}
			return nodes, nil
		}
		cursor := page.PageInfo.EndCursor
		if len(*page.Nodes) == 0 || len(nodes) >= total || cursor == "" || seen[cursor] {
			return nil, ghError("stalled_team_cursor")
		}
		seen[cursor] = true
		vars["after"] = cursor
	}
}

func (g *GitHubClient) readTeamReviews(ctx context.Context, repo string, number int) ([]Review, error) {
	readAt := time.Now().UTC()
	parts := strings.Split(repo, "/")
	vars := map[string]any{"owner": parts[0], "repo": parts[1], "number": number}
	query := `query WorkerTeamReviews($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){reviews(first:100,after:$after){totalCount nodes{fullDatabaseId state body submittedAt lastEditedAt commit{oid} author{` + actorFields + `}} pageInfo{hasNextPage endCursor}}}}}`
	nodes, err := g.teamConnection(ctx, parts[0], query, vars, "repository", "pullRequest", "reviews")
	if err != nil {
		return nil, err
	}
	seenReviews := map[int64]bool{}
	var reviews []Review
	for _, raw := range nodes {
		r, err := decode[struct {
			FullDatabaseID json.Number
			State          string
			Body           string
			SubmittedAt    time.Time
			LastEditedAt   time.Time
			Commit         *struct{ OID string }
			Author         graphActor
		}](raw)
		id, idErr := strconv.ParseInt(r.FullDatabaseID.String(), 10, 64)
		if err != nil || idErr != nil || id <= 0 || seenReviews[id] || !knownTeamActor(r.Author.actor()) || r.Commit == nil || r.Commit.OID == "" {
			return nil, ghError("unresolved_team_review")
		}
		if r.SubmittedAt.After(readAt) {
			return nil, ghError("invalid_team_review_time")
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED":
			if r.SubmittedAt.IsZero() {
				return nil, ghError("invalid_team_review_time")
			}
		case "PENDING":
			if !r.SubmittedAt.IsZero() {
				return nil, ghError("invalid_team_review_time")
			}
		default:
			return nil, ghError("invalid_team_review_state")
		}
		seenReviews[id] = true
		reviews = append(reviews, Review{ID: strconv.FormatInt(id, 10), HeadSHA: r.Commit.OID, State: r.State, Body: r.Body, Author: r.Author.actor(), SubmittedAt: r.SubmittedAt.UTC(), LastEditedAt: r.LastEditedAt.UTC()})
	}
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].ID < reviews[j].ID })
	return reviews, nil
}

func (g *GitHubClient) readTeamEvidence(ctx context.Context, repo string, number int, pr *PR) error {
	reviews, err := g.readTeamReviews(ctx, repo, number)
	if err != nil {
		return err
	}
	parts := strings.Split(repo, "/")
	vars := map[string]any{"owner": parts[0], "repo": parts[1], "number": number}
	query := `query WorkerTeamCommits($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(first:100,after:$after){totalCount nodes{commit{id oid committer{user{__typename databaseId login}}}} pageInfo{hasNextPage endCursor}}}}}`
	commits, err := g.teamConnection(ctx, parts[0], query, vars, "repository", "pullRequest", "commits")
	if err != nil {
		return err
	}
	if len(commits) == 0 || !knownTeamActor(pr.Author) {
		return ghError("unresolved_team_contributors")
	}
	contributors := map[int64]Actor{pr.Author.ID: pr.Author}
	resolved := map[int64]Actor{}
	seenCommits := map[string]bool{}
	for _, raw := range commits {
		c, err := decode[struct {
			Commit struct {
				ID, OID   string
				Committer *struct{ User *graphActor }
			}
		}](raw)
		if err != nil || c.Commit.ID == "" || c.Commit.OID == "" || seenCommits[c.Commit.OID] {
			return ghError("invalid_team_commit")
		}
		seenCommits[c.Commit.OID] = true
		if c.Commit.Committer == nil || c.Commit.Committer.User == nil || c.Commit.Committer.User.ID <= 0 {
			return ghError("unresolved_team_contributors")
		}
		// Commit.authors includes the primary author and GitHub-resolved
		// co-authors. Never infer identity from names, emails or login suffixes.
		query := `query WorkerTeamAuthors($id:ID!,$after:String){node(id:$id){... on Commit{authors(first:100,after:$after){totalCount nodes{user{__typename databaseId login}} pageInfo{hasNextPage endCursor}}}}}`
		authors, err := g.teamConnection(ctx, parts[0], query, map[string]any{"id": c.Commit.ID}, "node", "authors")
		if err != nil {
			return err
		}
		if len(authors) == 0 {
			return ghError("unresolved_team_contributors")
		}
		seenAuthors := map[int64]bool{}
		identities := []int64{c.Commit.Committer.User.ID}
		for _, raw := range authors {
			a, err := decode[struct{ User *graphActor }](raw)
			if err != nil || a.User == nil || a.User.ID <= 0 || seenAuthors[a.User.ID] {
				return ghError("unresolved_team_contributors")
			}
			seenAuthors[a.User.ID] = true
			identities = append(identities, a.User.ID)
		}
		for _, id := range identities {
			actor, ok := resolved[id]
			if !ok {
				// GitActor.user has GraphQL User shape even for some bots.
				// Resolve by database ID through REST to retain the actual kind.
				body, _, err := g.api(ctx, parts[0], "GET", "user/"+strconv.FormatInt(id, 10), nil)
				if err != nil {
					return err
				}
				account, err := decode[restActor](body)
				actor = account.actor()
				if err != nil || actor.ID != id || !knownTeamActor(actor) {
					return ghError("unresolved_team_contributors")
				}
				resolved[actor.ID] = actor
			}
			contributors[actor.ID] = actor
			if len(contributors) > MaxConnection {
				return ghError("connection_limit")
			}
		}
	}
	if !seenCommits[pr.HeadSHA] {
		return ghError("incoherent_team_head")
	}
	// A stable PR head/body does not prove native reviews stayed unchanged
	// while contributor connections were paged. Reject mixed evidence.
	freshReviews, err := g.readTeamReviews(ctx, repo, number)
	if err != nil {
		return err
	}
	if !slices.Equal(reviews, freshReviews) {
		return ghError("team_reviews_changed_during_read")
	}
	var actors []Actor
	for _, actor := range contributors {
		actors = append(actors, actor)
	}
	sort.Slice(actors, func(i, j int) bool { return actors[i].ID < actors[j].ID })
	pr.Reviews, pr.Contributors = reviews, actors
	pr.ReviewsComplete, pr.ContributorsComplete = true, true
	return nil
}

func knownTeamActor(a Actor) bool {
	return a.ID > 0 && a.Login != "" && (a.Kind == "User" || a.Kind == "Bot")
}
