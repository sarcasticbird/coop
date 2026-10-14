package worker

import (
	"context"
	"strings"
)

// Revalidate reads only the selected goal, task, dependencies and PR after
// checkout preparation. Full catalog scans remain asynchronous.
func (g *GitHubClient) Revalidate(ctx context.Context, j Job, old Snapshot) (Snapshot, error) {
	s := old
	s.Goals = append([]Goal(nil), old.Goals...)
	s.Tasks = append([]Task(nil), old.Tasks...)
	s.PRs = append([]PR(nil), old.PRs...)
	s.Events = nil
	var goal Goal
	var events []Event
	var err error
	if strings.Contains(j.GoalURL, "/discussions/") {
		id := g.goalIDs[j.GoalURL]
		if id == "" {
			return Snapshot{}, ghError("goal_discussion_not_discovered")
		}
		owner := ""
		for _, repo := range g.config.DiscussionRepositories {
			candidate := strings.Split(repo, "/")[0]
			if strings.HasPrefix(j.GoalURL, "https://github.com/orgs/"+candidate+"/discussions/") || strings.HasPrefix(j.GoalURL, "https://github.com/"+repo+"/discussions/") {
				owner = candidate
				break
			}
		}
		if owner == "" {
			return Snapshot{}, ghError("goal_outside_scope")
		}
		query := `query WorkerGoalRefresh($id:ID!){node(id:$id){... on Discussion {id url body createdAt lastEditedAt author{` + actorFields + `}}}}`
		data, readErr := g.graphql(ctx, owner, query, map[string]any{"id": id})
		if readErr != nil {
			return Snapshot{}, readErr
		}
		raw, readErr := at(data, "node")
		if readErr != nil {
			return Snapshot{}, readErr
		}
		d, readErr := decode[graphDiscussion](raw)
		if readErr != nil || d.URL != j.GoalURL {
			return Snapshot{}, ghError("invalid_discussion")
		}
		comments, readErr := g.discussionComments(ctx, owner, id)
		if readErr != nil {
			return Snapshot{}, readErr
		}
		goal, events = g.goalFromDiscussion(d, comments)
	} else {
		repo, _, parseErr := parseRef(j.GoalURL)
		if parseErr != nil {
			return Snapshot{}, parseErr
		}
		goal, events, err = g.issueGoal(ctx, Task{URL: j.GoalURL, Repo: repo})
		if err != nil {
			return Snapshot{}, err
		}
	}
	for i := range s.Goals {
		if s.Goals[i].URL == goal.URL {
			s.Goals[i] = goal
		}
	}
	for _, event := range old.Events {
		if event.URL != goal.URL {
			s.Events = append(s.Events, event)
		}
	}
	s.Events = append(s.Events, events...)
	var deliveryPR *PR
	if j.Kind == "plan" && j.PRURL != "" {
		fresh, readErr := g.readPR(ctx, j.PRURL)
		if readErr != nil {
			return Snapshot{}, readErr
		}
		deliveryPR = &fresh
		for i := range s.PRs {
			if s.PRs[i].URL == fresh.URL {
				s.PRs[i] = fresh
			}
		}
	}
	if j.Kind != "plan" || j.PRURL != "" {
		taskURL := j.TaskURL
		if j.Kind == "plan" {
			p, ok := prFor(s, j.PRURL)
			if !ok || p.TaskURL == "" {
				return Snapshot{}, ghError("delivery_task_not_discovered")
			}
			taskURL = p.TaskURL
		}
		t, readErr := g.taskByURL(ctx, taskURL)
		if readErr != nil {
			return Snapshot{}, readErr
		}
		for i, previous := range s.Tasks {
			if previous.URL == t.URL {
				// Board values are not authorization; keep their last observed values.
				previous.Title, previous.Body, previous.BodySHA = t.Title, t.Body, t.BodySHA
				previous.MetadataScopeSHA, previous.MetadataAuthor = t.MetadataScopeSHA, t.MetadataAuthor
				previous.MetadataUpdatedAt = t.MetadataUpdatedAt
				previous.GoalURL, previous.DependsOn, previous.Claim = t.GoalURL, t.DependsOn, t.Claim
				previous.Completed, previous.Revision = t.Completed, t.Revision
				previous.Closed = t.Closed
				s.Tasks[i] = previous
			}
		}
		for _, ref := range t.DependsOn {
			dep, readErr := g.taskByURL(ctx, ref)
			if readErr != nil {
				return Snapshot{}, readErr
			}
			found := false
			for i := range s.Tasks {
				if s.Tasks[i].URL == ref {
					s.Tasks[i] = dep
					found = true
				}
			}
			if !found {
				s.Tasks = append(s.Tasks, dep)
			}
		}
	}
	if j.PRURL != "" {
		var pr PR
		if deliveryPR != nil {
			pr = *deliveryPR
		} else {
			fresh, readErr := g.readPR(ctx, j.PRURL)
			if readErr != nil {
				return Snapshot{}, readErr
			}
			pr = fresh
		}
		if task, ok := taskFor(s, pr.TaskURL); ok {
			bindReviewScope(&pr, task)
		}
		for i := range s.PRs {
			if s.PRs[i].URL == pr.URL {
				s.PRs[i] = pr
			}
		}
	}
	g.snapshot = s
	return s, nil
}

func bindReviewScope(pr *PR, task Task) {
	pr.ScopeUpdatedAt = task.MetadataUpdatedAt
	pr.ScopeRevision = BodyHash(task.BodySHA + ":" + task.MetadataUpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
}
