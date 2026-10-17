package worker

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// hostApprovalRunner uses the owner's host gh login. Credentials never enter a VM.
type hostApprovalRunner struct{ CommandRunner }

func (r hostApprovalRunner) Run(ctx context.Context, args, env []string, in io.Reader) (CommandResult, error) {
	if len(args) < 7 || args[0] != "/usr/local/libexec/coop-github" || args[1] != "gh" || args[4] != "--" || args[5] != "api" {
		return CommandResult{}, errors.New("invalid host approval request")
	}
	command := append([]string{"gh", "api", args[6], "--hostname", "github.com"}, args[7:]...)
	return r.CommandRunner.Run(ctx, command, env, in)
}

// ApproveGoal publishes an exact-body, repository-bound decision as the configured
// human owner. It does not start a VM, expose credentials, or bypass task/review gates.
func ApproveGoal(ctx context.Context, c Config, ref string, repos []string, runner CommandRunner) (Comment, error) {
	repo, number, err := parseRef(ref)
	if err != nil || !strings.Contains(ref, "/discussions/") || !containsFold(c.DiscussionRepositories, repo) {
		return Comment{}, errors.New("approve requires a Discussion on a configured coordination board")
	}
	owner, name, _ := strings.Cut(repo, "/")
	g := NewGitHubClient(hostApprovalRunner{runner})
	g.config = c
	raw, _, err := g.api(ctx, owner, "GET", "user", nil)
	if err != nil {
		return Comment{}, err
	}
	viewer, err := decode[restActor](raw)
	if err != nil || !sameActor(viewer.actor(), c.Owner) || viewer.Kind != "User" {
		return Comment{}, errors.New("host gh must be logged in as the configured human owner")
	}
	read := func() (graphDiscussion, error) {
		q := `query HostApprovalGoal($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){discussion(number:$number){id url body createdAt lastEditedAt}}}`
		data, err := g.graphql(ctx, owner, q, map[string]any{"owner": owner, "repo": name, "number": number})
		if err != nil {
			return graphDiscussion{}, err
		}
		raw, err := at(data, "repository", "discussion")
		if err != nil {
			return graphDiscussion{}, err
		}
		d, err := decode[graphDiscussion](raw)
		if err != nil || d.ID == "" || d.URL != ref {
			return graphDiscussion{}, errors.New("goal Discussion unavailable")
		}
		return d, nil
	}
	d, err := read()
	if err != nil {
		return Comment{}, err
	}
	comments, err := g.discussionComments(ctx, owner, d.ID)
	if err != nil {
		return Comment{}, err
	}
	goal, _ := g.goalFromDiscussion(d, comments)
	if len(repos) == 0 {
		repos = goal.Repositories
	}
	body, err := GoalApprovalComment(c, goal, repos)
	if err != nil {
		return Comment{}, err
	}
	if EvaluateGoal(c, goal, time.Now()).State == "approved" {
		for _, comment := range comments {
			if sameActor(comment.Author, c.Owner) && comment.Body == body {
				return comment, nil
			}
		}
	}
	current, err := read()
	if err != nil {
		return Comment{}, err
	}
	if current.Body != d.Body || !current.LastEditedAt.Equal(d.LastEditedAt) {
		return Comment{}, errors.New("goal changed during approval; inspect its current scope and retry")
	}
	q := `mutation HostApproveGoal($input:AddDiscussionCommentInput!){addDiscussionComment(input:$input){comment{id url body createdAt author{` + actorFields + `}}}}`
	data, err := g.graphql(ctx, owner, q, map[string]any{"input": map[string]any{"discussionId": d.ID, "body": body}})
	if err != nil {
		return Comment{}, err
	}
	raw, err = at(data, "addDiscussionComment", "comment")
	if err != nil {
		return Comment{}, err
	}
	posted, err := decode[graphComment](raw)
	if err != nil || posted.ID == "" || posted.Body != body || !sameActor(posted.Author.actor(), c.Owner) {
		return Comment{}, errors.New("owner approval not confirmed; inspect the Discussion before retrying")
	}
	return posted.comment(), nil
}
