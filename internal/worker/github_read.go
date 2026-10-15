package worker

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type restActor struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Kind  string `json:"type"`
}

func (a restActor) actor() Actor { return Actor(a) }

type restComment struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id"`
	Body      string    `json:"body"`
	Author    restActor `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c restComment) comment() Comment {
	return Comment{ID: strconv.FormatInt(c.ID, 10), NodeID: c.NodeID, Body: c.Body, Author: c.Author.actor(), CreatedAt: c.CreatedAt, EditedAt: c.UpdatedAt}
}

type restIssue struct {
	URL         string          `json:"html_url"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	StateReason string          `json:"state_reason"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PullRequest json.RawMessage `json:"pull_request"`
}
type graphDiscussion struct {
	ID, URL, Body string
	CreatedAt     time.Time
	LastEditedAt  time.Time
	UpdatedAt     time.Time
	Author        graphActor
}
type graphComment struct {
	ID, Body, URL string
	RootID        string
	ReplyTo       *struct {
		ID     string
		Author graphActor
	}
	CreatedAt, LastEditedAt time.Time
	Author                  graphActor
}

func (c graphComment) comment() Comment {
	return Comment{ID: c.ID, NodeID: c.ID, Body: c.Body, Author: c.Author.actor(), CreatedAt: c.CreatedAt, EditedAt: c.LastEditedAt, URL: c.URL, RootID: c.RootID, ParentID: graphParentID(c), ParentAuthor: graphParentAuthor(c)}
}
func (g *GitHubClient) readSnapshot(ctx context.Context, previous Snapshot) (Snapshot, error) {
	c := g.config
	s := Snapshot{Cursor: map[string]string{}}
	started := time.Now().UTC()
	repos := append([]string(nil), c.Repositories...)
	if len(repos) == 0 {
		for _, owner := range c.CodeOwners {
			nodes, err := g.restList(ctx, owner, "installation/repositories", "repositories")
			if err != nil {
				return s, err
			}
			for _, raw := range nodes {
				r, err := decode[struct {
					FullName  string `json:"full_name"`
					Archived  bool   `json:"archived"`
					Disabled  bool   `json:"disabled"`
					HasIssues bool   `json:"has_issues"`
				}](raw)
				if err != nil {
					return s, err
				}
				if !validRepo(r.FullName) || !strings.EqualFold(strings.Split(r.FullName, "/")[0], owner) {
					return s, ghError("installation_scope_mismatch")
				}
				if !r.Archived && !r.Disabled && r.HasIssues {
					repos = append(repos, r.FullName)
				}
			}
		}
	}
	tasks := map[string]Task{}
	prRefs := map[string]bool{}
	if previous.Complete {
		for _, t := range previous.Tasks {
			if containsFold(repos, t.Repo) {
				tasks[t.URL] = t
			}
		}
		for _, p := range previous.PRs {
			repo, _, err := parseRef(p.URL)
			if err == nil && containsFold(repos, repo) {
				prRefs[p.URL] = true
			}
		}
	}
	for _, repo := range repos {
		owner := strings.Split(repo, "/")[0]
		endpoint := "repos/" + repo + "/issues?state=all&sort=updated&direction=desc"
		if previous.Complete {
			if since := previous.Cursor[repo]; since != "" {
				if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
					return s, ghError("invalid_snapshot_cursor")
				}
				endpoint += "&since=" + url.QueryEscape(since)
			}
		}
		nodes, err := g.restList(ctx, owner, endpoint, "")
		if err != nil {
			return s, err
		}
		for _, raw := range nodes {
			issue, err := decode[restIssue](raw)
			if err != nil {
				return s, err
			}
			if len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
				continue
			}
			t, err := g.readTask(ctx, repo, issue)
			if err != nil {
				return s, err
			}
			tasks[t.URL] = t
		}
		prs, err := g.restList(ctx, owner, "repos/"+repo+"/pulls?state=open", "")
		if err != nil {
			return s, err
		}
		for _, raw := range prs {
			p, err := decode[struct {
				URL string `json:"html_url"`
			}](raw)
			if err != nil || !validGitHubURL(p.URL) {
				return s, ghError("invalid_PR_reference")
			}
			prRefs[p.URL] = true
		}
		// GitHub documents second-resolution ISO timestamps. Overlap one second;
		// stable input hashes suppress duplicate work at the boundary.
		s.Cursor[repo] = started.Add(-time.Second).Truncate(time.Second).Format(time.RFC3339)
	}
	// Refresh queued/claimed tasks even when comment edits did not advance issue.updated_at.
	for _, old := range previous.Tasks {
		if !containsFold(repos, old.Repo) || old.GoalURL == "" {
			continue
		}
		t, err := g.taskByURL(ctx, old.URL)
		if err != nil {
			return s, err
		}
		tasks[t.URL] = t
	}
	for _, repo := range c.DiscussionRepositories {
		goals, events, sources, err := g.discussions(ctx, repo)
		if err != nil {
			return s, err
		}
		s.Goals = append(s.Goals, goals...)
		s.Events = append(s.Events, events...)
		s.WakeSources = append(s.WakeSources, sources...)
	}
	// Goal issues are supported for private coordination.
	for _, t := range tasks {
		if strings.Contains(t.Body, "<!-- coop-goal:v1 ") {
			goal, events, err := g.issueGoal(ctx, t)
			if err != nil {
				return s, err
			}
			s.Goals = append(s.Goals, goal)
			s.Events = append(s.Events, events...)
		}
	}
	projects, err := g.projectRefs(ctx)
	if err != nil {
		return s, err
	}
	for _, p := range projects {
		if err := g.readProject(ctx, p, tasks, &s); err != nil {
			return s, err
		}
	}
	for _, ref := range goalReferences(c, tasks, s.Goals) {
		if strings.Contains(ref, "/discussions/") {
			return s, ghError("goal_discussion_not_discovered")
		}
		repo, _, err := parseRef(ref)
		if err != nil || !goalInScope(c, Goal{URL: ref}) {
			return s, ghError("goal_outside_scope")
		}
		goal, events, err := g.issueGoal(ctx, Task{Repo: repo, URL: ref})
		if err != nil {
			return s, err
		}
		s.Goals = append(s.Goals, goal)
		s.Events = append(s.Events, events...)
	}
	// Resolve dependencies independently through the repository owner's token.
	var dependencies []string
	for _, t := range tasks {
		dependencies = append(dependencies, t.DependsOn...)
	}
	for _, ref := range dependencies {
		if _, ok := tasks[ref]; ok {
			continue
		}
		repo, _, err := parseRef(ref)
		if err != nil || !containsFold(c.CodeOwners, strings.Split(repo, "/")[0]) {
			return s, ghError("dependency_outside_scope")
		}
		t, err := g.taskByURL(ctx, ref)
		if err != nil {
			return s, err
		}
		tasks[t.URL] = t
	}
	for ref := range prRefs {
		p, err := g.readPR(ctx, ref)
		if err != nil {
			return s, err
		}
		if p.TaskURL != "" {
			if task, ok := tasks[p.TaskURL]; ok {
				bindReviewScope(&p, task)
			}
		}
		// Observe unlinked implementer PRs without admitting them. Otherwise a
		// manual handoff disappears and monitoring incorrectly reports idle.
		if p.TaskURL != "" || sameActor(p.Author, c.Implementer) {
			s.PRs = append(s.PRs, p)
		}
	}
	for _, t := range tasks {
		s.Tasks = append(s.Tasks, t)
	}
	sort.Slice(s.Tasks, func(i, j int) bool { return s.Tasks[i].URL < s.Tasks[j].URL })
	sort.Slice(s.PRs, func(i, j int) bool { return s.PRs[i].URL < s.PRs[j].URL })
	if len(s.Tasks) > MaxConnection || len(s.Goals) > MaxConnection || len(s.PRs) > MaxConnection {
		return s, ghError("snapshot_limit")
	}
	return s, nil
}

func goalReferences(c Config, tasks map[string]Task, goals []Goal) []string {
	seen := map[string]bool{}
	for _, g := range goals {
		seen[g.URL] = true
	}
	var refs []string
	add := func(ref string) {
		if ref != "" && !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	for _, b := range c.BootstrapGoals {
		add(b.URL)
	}
	for _, t := range tasks {
		add(t.GoalURL)
	}
	sort.Strings(refs)
	return refs
}
func (g *GitHubClient) readTask(ctx context.Context, repo string, issue restIssue) (Task, error) {
	if !validGitHubURL(issue.URL) || issue.Number <= 0 || !strings.HasPrefix(issue.URL, "https://github.com/"+repo+"/issues/") {
		return Task{}, ghError("invalid_issue_reference")
	}
	t := Task{URL: issue.URL, Repo: repo, Title: issue.Title, Body: issue.Body, BodySHA: TaskScopeHash(issue.Title, issue.Body), Revision: issue.UpdatedAt.Format(time.RFC3339Nano), Completed: issue.State == "closed" && issue.StateReason == "completed"}
	t.Closed = issue.State == "closed"
	comments, err := g.issueComments(ctx, repo, issue.Number)
	if err != nil {
		return t, err
	}
	// Missing metadata is an admission reason, not an API read failure.
	if ApplyTaskMetadata(g.config, &t, comments) != nil {
		t.MetadataAuthor = Actor{}
	}
	for _, c := range comments {
		if claim := claimFromComment(c); claim != nil {
			t.Claim = claim
		}
	}
	return t, nil
}
func (g *GitHubClient) taskByURL(ctx context.Context, ref string) (Task, error) {
	repo, n, err := parseRef(ref)
	if err != nil {
		return Task{}, err
	}
	if !containsFold(g.config.CodeOwners, strings.Split(repo, "/")[0]) {
		return Task{}, ghError("task_outside_scope")
	}
	raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/issues/"+stringID(n), nil)
	if err != nil {
		return Task{}, err
	}
	issue, err := decode[restIssue](raw)
	if err != nil {
		return Task{}, err
	}
	return g.readTask(ctx, repo, issue)
}
func (g *GitHubClient) goalRepositories(ref, body string) []string {
	repos, err := ParseGoalMetadata(body)
	if err == nil {
		return repos
	}
	for _, b := range g.config.BootstrapGoals {
		if b.URL == ref && b.BodySHA == BodyHash(body) {
			return append([]string(nil), b.Repositories...)
		}
	}
	return nil
}
func (g *GitHubClient) goalFromDiscussion(d graphDiscussion, comments []Comment) (Goal, []Event) {
	goal := Goal{URL: d.URL, Body: d.Body, BodySHA: BodyHash(d.Body), Repositories: g.goalRepositories(d.URL, d.Body), CreatedAt: d.CreatedAt, LastEditedAt: d.LastEditedAt, Readable: true}
	// A verified host approval can supply missing scope without editing an
	// already-approved goal body. Bot restatements cannot supply owner authority.
	if len(goal.Repositories) == 0 {
		var latest time.Time
		for _, comment := range comments {
			if record, ok := approvalRecord(g.config, goal, comment.Body); ok && sameActor(comment.Author, g.config.Owner) && !commentTime(comment).Before(latest) {
				goal.Repositories, latest = record.Repositories, commentTime(comment)
			}
		}
	}
	g.goalIDs[d.URL] = d.ID
	var events []Event
	all := append([]Comment{{ID: d.ID, Body: d.Body, Author: d.Author.actor(), CreatedAt: d.CreatedAt, EditedAt: d.LastEditedAt}}, comments...)
	for _, c := range all {
		if c.ID != d.ID {
			goal.Decisions = append(goal.Decisions, Decision{ID: c.ID, Command: c.Body, Author: c.Author, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt})
		}
		directed := wakeEnabled(g.config) && strings.Contains(d.URL, "/discussions/") && len(ConversationTargets(g.config, conversationSource(d, c))) > 0
		if !directed && (sameActor(c.Author, g.config.Owner) || (!wakeEnabled(g.config) && sameActor(c.Author, g.config.Implementer))) && !strings.Contains(c.Body, "<!-- coop-worker:v1 ") {
			events = append(events, Event{ID: c.ID, Revision: BodyHash(c.Body), URL: d.URL, Kind: "planning", Body: c.Body, Author: c.Author, OccurredAt: commentTime(c)})
		}
	}
	return goal, events
}
func (g *GitHubClient) discussions(ctx context.Context, repo string) ([]Goal, []Event, []ConversationSource, error) {
	p := strings.Split(repo, "/")
	query := `query WorkerDiscussions($owner:String!,$repo:String!,$after:String){repository(owner:$owner,name:$repo){discussions(first:100,after:$after){nodes{id url body createdAt lastEditedAt updatedAt author{` + actorFields + `}} pageInfo{hasNextPage endCursor}}}}`
	nodes, err := g.connection(ctx, p[0], query, map[string]any{"owner": p[0], "repo": p[1]}, []string{"repository", "discussions"})
	if err != nil {
		return nil, nil, nil, err
	}
	var goals []Goal
	var events []Event
	var sources []ConversationSource
	for _, raw := range nodes {
		d, err := decode[graphDiscussion](raw)
		if err != nil || d.ID == "" || !validGitHubURL(d.URL) {
			return nil, nil, nil, ghError("invalid_discussion")
		}
		comments, err := g.goalComments(ctx, p[0], d)
		if err != nil {
			return nil, nil, nil, err
		}
		goal, e := g.goalFromDiscussion(d, comments)
		goals = append(goals, goal)
		events = append(events, e...)
		if wakeEnabled(g.config) {
			sources = append(sources, discussionSources(d, comments)...)
		}
	}
	return goals, events, sources, nil
}

// Separate body and paginated comment reads are not a goal version. Collect
// comments twice, bracketing both scans with body checks, so an intervening edit
// or decision cannot authorize a mixed snapshot. Churn fails closed until the
// next poll rather than retrying indefinitely.
func (g *GitHubClient) goalComments(ctx context.Context, owner string, d graphDiscussion) ([]Comment, error) {
	readComments := func() ([]Comment, error) {
		if strings.Contains(d.URL, "/discussions/") {
			return g.discussionComments(ctx, owner, d.ID)
		}
		repo, n, err := parseRef(d.URL)
		if err != nil {
			return nil, err
		}
		return g.issueComments(ctx, repo, n)
	}
	var previous []Comment
	for scan := 0; scan < 2; scan++ {
		comments, err := readComments()
		if err != nil {
			return nil, err
		}
		query := `query WorkerGoalVersion($id:ID!){node(id:$id){... on Discussion {id url body createdAt lastEditedAt author{` + actorFields + `}} ... on Issue {id url body createdAt lastEditedAt author{` + actorFields + `}}}}`
		data, err := g.graphql(ctx, owner, query, map[string]any{"id": d.ID})
		if err != nil {
			return nil, err
		}
		raw, err := at(data, "node")
		if err != nil {
			return nil, err
		}
		current, err := decode[graphDiscussion](raw)
		if err != nil || d.ID == "" || current.ID != d.ID || current.URL != d.URL || current.Body != d.Body || !current.CreatedAt.Equal(d.CreatedAt) || !current.LastEditedAt.Equal(d.LastEditedAt) || current.Author != d.Author {
			return nil, ghError("goal_version_changed")
		}
		if scan > 0 && !slices.Equal(previous, comments) {
			return nil, ghError("goal_version_changed")
		}
		previous = comments
	}
	return previous, nil
}

func (g *GitHubClient) discussionComments(ctx context.Context, owner, id string) ([]Comment, error) {
	query := `query WorkerDiscussionComments($id:ID!,$after:String){node(id:$id){... on Discussion {comments(first:100,after:$after){nodes{id url body createdAt lastEditedAt replyTo{id author{` + actorFields + `}} author{` + actorFields + `}} pageInfo{hasNextPage endCursor}}}}}`
	nodes, err := g.connection(ctx, owner, query, map[string]any{"id": id}, []string{"node", "comments"})
	if err != nil {
		return nil, err
	}
	var result []Comment
	for _, raw := range nodes {
		c, err := decode[graphComment](raw)
		if err != nil || c.ID == "" {
			return nil, ghError("invalid_discussion_comment")
		}
		c.RootID = c.ID
		result = append(result, c.comment())
		replies := `query WorkerDiscussionReplies($id:ID!,$after:String){node(id:$id){... on DiscussionComment {replies(first:100,after:$after){nodes{id url body createdAt lastEditedAt replyTo{id author{` + actorFields + `}} author{` + actorFields + `}} pageInfo{hasNextPage endCursor}}}}}`
		nested, err := g.connection(ctx, owner, replies, map[string]any{"id": c.ID}, []string{"node", "replies"})
		if err != nil {
			return nil, err
		}
		for _, raw := range nested {
			reply, err := decode[graphComment](raw)
			if err != nil || reply.ID == "" || (wakeEnabled(g.config) && (reply.ReplyTo == nil || reply.ReplyTo.ID == "")) {
				return nil, ghError("invalid_discussion_reply")
			}
			reply.RootID = c.ID
			result = append(result, reply.comment())
		}
		if len(result) > MaxConnection {
			return nil, ghError("connection_limit")
		}
	}
	return result, nil
}
func (g *GitHubClient) issueGoal(ctx context.Context, t Task) (Goal, []Event, error) {
	p := strings.Split(t.Repo, "/")
	_, n, _ := parseRef(t.URL)
	query := `query WorkerIssueGoal($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){issue(number:$number){id url body createdAt lastEditedAt author{` + actorFields + `}}}}`
	data, err := g.graphql(ctx, p[0], query, map[string]any{"owner": p[0], "repo": p[1], "number": n})
	if err != nil {
		return Goal{}, nil, err
	}
	raw, err := at(data, "repository", "issue")
	if err != nil {
		return Goal{}, nil, err
	}
	d, err := decode[graphDiscussion](raw)
	if err != nil {
		return Goal{}, nil, err
	}
	comments, err := g.goalComments(ctx, p[0], d)
	if err != nil {
		return Goal{}, nil, err
	}
	goal, events := g.goalFromDiscussion(d, comments)
	return goal, events, nil
}
func (g *GitHubClient) readPR(ctx context.Context, ref string) (PR, error) {
	repo, n, err := parseRef(ref)
	if err != nil || !referenceInRepo(ref, repo, "pull") {
		return PR{}, ghError("invalid_PR_target")
	}
	p := strings.Split(repo, "/")
	query := `query WorkerPR($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){url body createdAt lastEditedAt state merged headRefOid headRefName baseRefName updatedAt mergeStateStatus author{` + actorFields + `} commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}`
	data, err := g.graphql(ctx, p[0], query, map[string]any{"owner": p[0], "repo": p[1], "number": n})
	if err != nil {
		return PR{}, err
	}
	raw, err := at(data, "repository", "pullRequest")
	if err != nil {
		return PR{}, err
	}
	var item struct {
		prBodyVersion
		State, HeadRefName, BaseRefName, MergeStateStatus string
		Merged                                            bool
		UpdatedAt                                         time.Time
		Author                                            graphActor
		Commits                                           struct {
			Nodes []struct {
				Commit struct{ StatusCheckRollup *struct{ State string } }
			}
		}
	}
	if json.Unmarshal(raw, &item) != nil || item.URL != ref {
		return PR{}, ghError("invalid_PR_response")
	}
	pr := PR{URL: ref, Author: item.Author.actor(), HeadSHA: item.HeadRefOid, Branch: item.HeadRefName, BaseBranch: item.BaseRefName, Revision: item.UpdatedAt.Format(time.RFC3339Nano), Merged: item.Merged, Closed: item.State == "CLOSED" || item.Merged, MergeState: item.MergeStateStatus, ChecksState: "unknown"}
	if !sameActor(pr.Author, g.config.Implementer) {
		return pr, nil
	}
	m, err := parsePRMetadata(item.Body)
	if err != nil || !referenceInRepo(m.Task, repo, "issues") {
		return pr, nil
	}
	pr.TaskURL = m.Task
	pr.GoalURL = m.Goal
	var scopeHistory string
	// Already-merged issue finalization needs current tracking, not a new review.
	if !pr.Merged {
		scopeHistory, err = g.readPRScope(ctx, repo, n, item.prBodyVersion, &pr)
		if err != nil {
			return PR{}, err
		}
	}
	if len(item.Commits.Nodes) == 1 {
		pr.ChecksState = "none"
		if rollup := item.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
			pr.ChecksState = strings.ToLower(rollup.State)
		}
	}
	reviews, err := g.restList(ctx, p[0], "repos/"+repo+"/pulls/"+stringID(n)+"/reviews", "")
	if err != nil {
		return pr, err
	}
	for _, raw := range reviews {
		r, err := decode[struct {
			ID          int64
			CommitID    string `json:"commit_id"`
			State       string
			User        restActor
			SubmittedAt time.Time `json:"submitted_at"`
		}](raw)
		if err != nil {
			return pr, err
		}
		pr.Reviews = append(pr.Reviews, Review{ID: strconv.FormatInt(r.ID, 10), HeadSHA: r.CommitID, State: r.State, Author: r.User.actor(), SubmittedAt: r.SubmittedAt})
	}
	// Whole-second body timestamps cannot detect a same-second A -> B -> A.
	// Recheck all edit identities/order after reviews, before the final version read.
	if scopeHistory != "" {
		freshHistory, err := g.readPRScope(ctx, repo, n, item.prBodyVersion, &pr)
		if err != nil {
			return PR{}, err
		}
		if freshHistory != scopeHistory {
			return PR{}, ghError("PR_scope_history_changed_during_read")
		}
	}
	if err := g.verifyPRVersion(ctx, repo, n, item.prBodyVersion); err != nil {
		return PR{}, err
	}
	return pr, nil
}
func (g *GitHubClient) postDiscussion(ctx context.Context, j Job, ref, mark, body string) (Comment, error) {
	goal, ok := goalFor(g.snapshot, ref)
	if !ok || !goalInScope(g.config, goal) || g.goalIDs[ref] == "" {
		return Comment{}, ghError("unknown_goal_thread")
	}
	u, _ := url.Parse(ref)
	p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	owner := p[0]
	if owner == "orgs" {
		owner = p[1]
	}
	comments, err := g.discussionComments(ctx, owner, g.goalIDs[ref])
	if err != nil {
		return Comment{}, err
	}
	for _, c := range comments {
		if sameActor(c.Author, g.config.Self) && strings.Contains(c.Body, mark) {
			return c, nil
		}
	}
	query := `mutation WorkerDiscussionPost($id:ID!,$body:String!){addDiscussionComment(input:{discussionId:$id,body:$body}){comment{id body createdAt author{` + actorFields + `}}}}`
	data, err := g.graphql(ctx, owner, query, map[string]any{"id": g.goalIDs[ref], "body": body + "\n\n" + mark})
	if err != nil {
		return Comment{}, err
	}
	raw, err := at(data, "addDiscussionComment", "comment")
	if err != nil {
		return Comment{}, err
	}
	c, err := decode[graphComment](raw)
	if err != nil {
		return Comment{}, err
	}
	if !sameActor(c.Author.actor(), g.config.Self) {
		return Comment{}, ghError("unexpected_comment_author")
	}
	return c.comment(), nil
}

func projectFragment(selection string) string {
	return `repositoryOwner(login:$owner){ ... on Organization {` + selection + `} ... on User {` + selection + `} }`
}
func (g *GitHubClient) projectRefs(ctx context.Context) ([]ProjectRef, error) {
	if len(g.config.Projects) > 0 {
		return append([]ProjectRef(nil), g.config.Projects...), nil
	}
	var projects []ProjectRef
	for _, owner := range g.config.ProjectOwners {
		query := `query WorkerProjects($owner:String!,$after:String){` + projectFragment(`projectsV2(first:100,after:$after){nodes{number closed} pageInfo{hasNextPage endCursor}}`) + `}`
		nodes, err := g.connection(ctx, owner, query, map[string]any{"owner": owner}, []string{"repositoryOwner", "projectsV2"})
		if err != nil {
			return nil, err
		}
		for _, raw := range nodes {
			p, err := decode[struct {
				Number int
				Closed bool
			}](raw)
			if err != nil {
				return nil, err
			}
			if !p.Closed {
				projects = append(projects, ProjectRef{Owner: owner, Number: p.Number, StatusField: "Status", ReadyValue: "Ready", InProgressValue: "In Progress", DoneValue: "Done"})
			}
		}
	}
	return projects, nil
}

type projectField struct {
	ID, Name string
	Options  []struct{ ID, Name string }
}

func (g *GitHubClient) readProject(ctx context.Context, p ProjectRef, tasks map[string]Task, s *Snapshot) error {
	query := `query WorkerProject($owner:String!,$number:Int!){` + projectFragment(`projectV2(number:$number){id}`) + `}`
	data, err := g.graphql(ctx, p.Owner, query, map[string]any{"owner": p.Owner, "number": p.Number})
	if err != nil {
		return err
	}
	raw, err := at(data, "repositoryOwner", "projectV2")
	if err != nil {
		return err
	}
	project, err := decode[struct{ ID string }](raw)
	if err != nil || project.ID == "" {
		return ghError("invalid_project")
	}
	fieldsQuery := `query WorkerProjectFields($id:ID!,$after:String){node(id:$id){... on ProjectV2{fields(first:100,after:$after){nodes{... on ProjectV2FieldCommon{id name} ... on ProjectV2SingleSelectField{options{id name}}} pageInfo{hasNextPage endCursor}}}}}`
	fields, err := g.connection(ctx, p.Owner, fieldsQuery, map[string]any{"id": project.ID}, []string{"node", "fields"})
	if err != nil {
		return err
	}
	var status projectField
	for _, raw := range fields {
		f, err := decode[projectField](raw)
		if err != nil {
			return err
		}
		if f.Name == p.StatusField {
			status = f
		}
	}
	if status.ID == "" {
		return ghError("project_status_field_missing")
	}
	inProgress, done := "", ""
	for _, o := range status.Options {
		if o.Name == p.InProgressValue {
			inProgress = o.ID
		}
		if o.Name == p.DoneValue {
			done = o.ID
		}
	}
	if inProgress == "" || done == "" {
		return ghError("project_status_options_missing")
	}
	itemsQuery := `query WorkerProjectItems($id:ID!,$after:String){node(id:$id){... on ProjectV2{items(first:100,after:$after){nodes{id isArchived content{__typename ... on Issue{url} ... on PullRequest{url} ... on DraftIssue{body}}} pageInfo{hasNextPage endCursor}}}}}`
	items, err := g.connection(ctx, p.Owner, itemsQuery, map[string]any{"id": project.ID}, []string{"node", "items"})
	if err != nil {
		return err
	}
	for ordinal, raw := range items {
		var item struct {
			ID         string
			IsArchived bool
			Content    *struct {
				Kind      string `json:"__typename"`
				URL, Body string
			}
		}
		if json.Unmarshal(raw, &item) != nil || item.ID == "" {
			return ghError("invalid_project_item")
		}
		if item.IsArchived {
			continue
		}
		if item.Content == nil {
			return ghError("unreadable_project_item")
		}
		ref := item.Content.URL
		if item.Content.Kind == "DraftIssue" {
			refs := issueURLs.FindAllString(item.Content.Body, -1)
			if len(refs) != 1 {
				continue
			}
			ref = refs[0]
		}
		if item.Content.Kind == "PullRequest" {
			continue
		}
		repo, _, err := parseRef(ref)
		if err != nil {
			continue
		}
		if !containsFold(g.config.CodeOwners, strings.Split(repo, "/")[0]) {
			continue
		}
		if len(g.config.Repositories) > 0 && !containsFold(g.config.Repositories, repo) {
			continue
		}
		t, err := g.taskByURL(ctx, ref)
		if err != nil {
			return err
		}
		valuesQuery := `query WorkerProjectValues($id:ID!,$after:String){node(id:$id){... on ProjectV2Item{fieldValues(first:100,after:$after){nodes{... on ProjectV2ItemFieldSingleSelectValue{name field{... on ProjectV2FieldCommon{id name}}} ... on ProjectV2ItemFieldNumberValue{number field{... on ProjectV2FieldCommon{id name}}}} pageInfo{hasNextPage endCursor}}}}}`
		values, err := g.connection(ctx, p.Owner, valuesQuery, map[string]any{"id": item.ID}, []string{"node", "fieldValues"})
		if err != nil {
			return err
		}
		t.ProjectID = project.ID
		t.ProjectOwner = p.Owner
		t.ItemID = item.ID
		t.StatusFieldID = status.ID
		t.InProgressID = inProgress
		t.DoneID = done
		t.Ordinal = ordinal
		t.Priority = 1000
		for _, raw := range values {
			v, err := decode[struct {
				Name   string
				Number *float64
				Field  struct{ ID, Name string }
			}](raw)
			if err != nil {
				return err
			}
			if v.Field.ID == status.ID {
				t.Ready = v.Name == p.ReadyValue
				for _, option := range status.Options {
					if option.Name == v.Name {
						t.CurrentStatusID = option.ID
					}
				}
			}
			if p.PriorityField != "" && v.Field.Name == p.PriorityField {
				if v.Number != nil {
					t.Priority = int(*v.Number)
				} else if n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(v.Name), "p")); err == nil {
					t.Priority = n
				}
			}
		}
		if old, exists := tasks[ref]; exists && old.ProjectID != "" && old.ProjectID != t.ProjectID {
			return ghError("ambiguous_task_projects")
		}
		tasks[ref] = t
	}
	return nil
}
func (g *GitHubClient) syncProject(ctx context.Context, j Job, state string) error {
	if g.config.Role != "planner" {
		return nil
	} // Robin's installation intentionally has Projects read-only.
	t, ok := taskFor(g.snapshot, j.TaskURL)
	if !ok || t.ProjectID == "" {
		return nil
	}
	option := ""
	switch state {
	case "in_progress":
		option = t.InProgressID
	case "done":
		option = t.DoneID
	default:
		return ghError("invalid_project_state")
	}
	if option == "" {
		return ghError("missing_project_option")
	}
	if option == t.CurrentStatusID {
		return nil
	}
	owner := t.ProjectOwner
	if !slug.MatchString(owner) {
		return ghError("ambiguous_project_owner")
	}
	query := `mutation WorkerProjectUpdate($project:ID!,$item:ID!,$field:ID!,$option:String!){updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}){projectV2Item{id}}}`
	_, err := g.graphql(ctx, owner, query, map[string]any{"project": t.ProjectID, "item": t.ItemID, "field": t.StatusFieldID, "option": option})
	return err
}

var issueURLs = regexpIssueURLs()

func regexpIssueURLs() *regexp.Regexp {
	return regexp.MustCompile(`https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*`)
}

func graphParentID(c graphComment) string {
	if c.ReplyTo != nil {
		return c.ReplyTo.ID
	}
	return ""
}
func graphParentAuthor(c graphComment) Actor {
	if c.ReplyTo != nil {
		return c.ReplyTo.Author.actor()
	}
	return Actor{}
}
