package taskstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// TaskEditor uses the same pinned Kata provider and native API as engineering
// and conversations. Ordinary task maintenance does not create an assignment.
type TaskEditor interface {
	InspectIssue(context.Context, string) (Issue, error)
	CreateIssue(context.Context, c.Principal, c.TaskCreateInput) (c.ProviderTask, error)
	ListIssues(context.Context, c.TaskListInput) (c.ProviderTaskPage, error)
	UpdateIssue(context.Context, c.Principal, c.TaskUpdateInput) (c.ProviderTask, error)
}

func (k *kata) issueUID(ref string) (string, error) {
	if validUID(ref) {
		return ref, nil
	}
	project, uid, _, err := c.ParseKataTaskReference(ref)
	if err != nil {
		return "", err
	}
	if project != k.cfg.ProjectUID {
		return "", c.ErrForbidden
	}
	return uid, nil
}
func issueTask(issue Issue) c.ProviderTask {
	return c.ProviderTask{Reference: issue.UID, IssueUID: issue.UID, ProjectUID: issue.ProjectUID, Title: issue.Title, Body: issue.Body, State: issue.Status, ClosedReason: issue.ClosedReason, Owner: issue.Owner, Revision: issue.Revision}
}
func taskEditKey(actor c.Principal, request string) string {
	return "coop-task-" + hash([]string{actor.ID, request})
}
func (k *kata) CreateIssue(ctx context.Context, actor c.Principal, in c.TaskCreateInput) (c.ProviderTask, error) {
	if actor.ID == "" || !validRequest(in.RequestID) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 1024 || len(in.Body) > c.MaxText || len(in.Owner) > 128 || strings.ContainsAny(in.Owner, "\x00\r\n") || in.Goal != (c.GoalRef{}) || in.ExpectedApprovalSequence != 0 || in.Repository != "" || in.BaseBranch != "" || len(in.DependsOn) != 0 {
		return c.ProviderTask{}, c.ErrInvalid
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return c.ProviderTask{}, err
	}
	var out struct {
		Issue Issue `json:"issue"`
	}
	err = nativeCall(ctx, cl, token, "POST", fmt.Sprintf("/api/v1/projects/%d/issues", pid), taskEditKey(actor, in.RequestID), map[string]any{"actor": actor.ID, "title": in.Title, "body": in.Body, "owner": in.Owner}, &out)
	if err == nil && (!validUID(out.Issue.UID) || out.Issue.ProjectUID != k.cfg.ProjectUID || out.Issue.Revision < 1 || out.Issue.Title != in.Title || out.Issue.Body != in.Body || out.Issue.Owner != in.Owner) {
		err = c.ErrConflict
	}
	return issueTask(out.Issue), err
}
func (k *kata) ListIssues(ctx context.Context, in c.TaskListInput) (c.ProviderTaskPage, error) {
	result := c.ProviderTaskPage{Tasks: []c.ProviderTask{}}
	if in.Goal != (c.GoalRef{}) || len(in.After) > 4096 || (in.Status != "" && in.Status != "open" && in.Status != "closed") {
		return result, c.ErrInvalid
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return result, err
	}
	query := url.Values{"limit": {"100"}, "sort": {"created"}, "status": {in.Status}, "cursor": {in.After}}
	var out struct {
		Issues   []Issue `json:"issues"`
		Next     string  `json:"next_cursor"`
		Complete bool    `json:"complete"`
	}
	if err = nativeCall(ctx, cl, token, "GET", fmt.Sprintf("/api/v1/projects/%d/issues?%s", pid, query.Encode()), "", nil, &out); err != nil {
		return result, err
	}
	if len(out.Issues) > 100 || !out.Complete && (out.Next == "" || out.Next == in.After) || len(out.Next) > 4096 {
		return result, c.ErrConflict
	}
	for _, issue := range out.Issues {
		if !validUID(issue.UID) || issue.ProjectUID != k.cfg.ProjectUID || issue.Revision < 1 {
			return c.ProviderTaskPage{}, c.ErrConflict
		}
		result.Tasks = append(result.Tasks, issueTask(issue))
	}
	result.Next = out.Next
	return result, nil
}
func (k *kata) UpdateIssue(ctx context.Context, actor c.Principal, in c.TaskUpdateInput) (c.ProviderTask, error) {
	if err := in.Validate(); err != nil {
		return c.ProviderTask{}, err
	}
	uid, err := k.issueUID(in.Reference)
	if err != nil || actor.ID == "" {
		if err == nil {
			err = c.ErrForbidden
		}
		return c.ProviderTask{}, err
	}
	if _, err = k.InspectIssue(ctx, uid); err != nil {
		return c.ProviderTask{}, err
	}
	if in.Operation == "comment" {
		_, err = k.AppendComment(ctx, actor, CommentWrite{IssueUID: uid, Body: in.Message, ReplyToUID: in.ReplyToUID, RequestID: taskEditKey(actor, in.RequestID)})
		if err != nil {
			return c.ProviderTask{}, err
		}
		issue, err := k.InspectIssue(ctx, uid)
		return issueTask(issue), err
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return c.ProviderTask{}, err
	}
	path, method := fmt.Sprintf("/api/v1/projects/%d/issues/%s", pid, uid), "POST"
	input := map[string]any{"actor": actor.ID}
	switch in.Operation {
	case "edit":
		method = "PATCH"
		if in.Title != nil {
			input["title"] = *in.Title
		}
		if in.Body != nil {
			input["body"] = *in.Body
		}
		if in.Owner != nil {
			input["owner"] = *in.Owner
		}
	case "assign":
		path += "/actions/assign"
		input["owner"] = *in.Owner
	case "claim":
		path += "/actions/claim"
		input["if_unowned"] = true
	case "close":
		path += "/actions/close"
		input["reason"], input["message"], input["retry_protocol"] = in.Reason, in.Message, "close-v1"
		if len(in.Evidence) > 0 {
			input["evidence"] = in.Evidence
		}
	case "reopen":
		path += "/actions/reopen"
		input["message"] = in.Message
	}
	var out struct {
		Issue Issue `json:"issue"`
	}
	err = nativeCallIfMatch(ctx, cl, token, method, path, taskEditKey(actor, in.RequestID), input, &out, in.ExpectedRevision)
	if err == nil && (out.Issue.UID != uid || out.Issue.ProjectUID != k.cfg.ProjectUID || out.Issue.Revision < 1) {
		err = c.ErrConflict
	}
	return issueTask(out.Issue), err
}
