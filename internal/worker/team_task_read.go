package worker

import (
	"context"
	"slices"
	"strings"
)

// The initial issue can come from an earlier catalog page. After assembling the
// task, bracket a complete second comment scan with exact issue version checks.
// Never retry churn into a different assignment or publish a mixed snapshot.
func (g *GitHubClient) verifyTeamTaskSnapshot(ctx context.Context, repo string, issue restIssue, comments []Comment) error {
	if err := g.verifyTeamTaskIssue(ctx, repo, issue); err != nil {
		return err
	}
	fresh, err := g.issueComments(ctx, repo, issue.Number)
	if err != nil {
		return err
	}
	if !slices.Equal(comments, fresh) {
		return ghError("task_version_changed")
	}
	return g.verifyTeamTaskIssue(ctx, repo, issue)
}

func (g *GitHubClient) verifyTeamTaskIssue(ctx context.Context, repo string, issue restIssue) error {
	raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/issues/"+stringID(issue.Number), nil)
	if err != nil {
		return err
	}
	fresh, err := decode[restIssue](raw)
	if err != nil {
		return err
	}
	// readTask accepts only issues; absent and null both mean no pull request.
	if fresh.URL != issue.URL || fresh.Number != issue.Number || fresh.Title != issue.Title || fresh.Body != issue.Body || fresh.State != issue.State || fresh.StateReason != issue.StateReason || !fresh.UpdatedAt.Equal(issue.UpdatedAt) || !sameIssueAssignees(fresh.Assignees, issue.Assignees) || len(fresh.PullRequest) > 0 && string(fresh.PullRequest) != "null" {
		return ghError("task_version_changed")
	}
	return nil
}

func sameIssueAssignees(a, b *[]restActor) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return slices.Equal(*a, *b)
}
