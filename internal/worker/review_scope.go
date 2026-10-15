package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type prBodyVersion struct {
	URL, Body, HeadRefOid   string
	CreatedAt, LastEditedAt time.Time
}

// GitHub retains full body versions in UserContentEdit.diff, newest first.
// Use that durable history rather than poll-local observations: A -> B -> A
// must not resurrect A's old review, even across restarts or between polls.
func (g *GitHubClient) readPRScope(ctx context.Context, repo string, number int, version prBodyVersion, pr *PR) error {
	if version.CreatedAt.IsZero() || version.LastEditedAt.Before(version.CreatedAt) && !version.LastEditedAt.IsZero() {
		return ghError("invalid_PR_body_version")
	}
	boundary := version.CreatedAt
	if !version.LastEditedAt.IsZero() {
		parts := strings.Split(repo, "/")
		query := `query WorkerScopeHistory($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){userContentEdits(first:100,after:$after){nodes{id editedAt deletedAt diff} pageInfo{hasNextPage endCursor}}}}}`
		vars := map[string]any{"owner": parts[0], "repo": parts[1], "number": number}
		edits, err := g.connection(ctx, parts[0], query, vars, []string{"repository", "pullRequest", "userContentEdits"})
		if err != nil {
			return err
		}
		// A second complete scan rejects pagination churn and deleted history.
		check, err := g.connection(ctx, parts[0], query, vars, []string{"repository", "pullRequest", "userContentEdits"})
		if err != nil {
			return err
		}
		a, _ := json.Marshal(edits)
		b, _ := json.Marshal(check)
		if string(a) != string(b) || len(edits) == 0 {
			return ghError("incoherent_PR_scope_history")
		}
		previous := version.LastEditedAt
		continuous := true
		for i, raw := range edits {
			edit, err := decode[struct {
				ID, Diff            string
				EditedAt, DeletedAt time.Time
			}](raw)
			if err != nil || edit.ID == "" || !edit.DeletedAt.IsZero() || edit.EditedAt.Before(version.CreatedAt) || edit.EditedAt.After(previous) || i > 0 && !edit.EditedAt.Before(previous) {
				return ghError("invalid_PR_scope_history")
			}
			if i == 0 && (!edit.EditedAt.Equal(version.LastEditedAt) || edit.Diff != version.Body) {
				return ghError("incoherent_PR_scope_history")
			}
			previous = edit.EditedAt
			metadata, err := parsePRMetadata(edit.Diff)
			if err != nil || metadata.Task != pr.TaskURL || metadata.Goal != pr.GoalURL {
				continuous = false
			}
			if continuous {
				boundary = edit.EditedAt
			}
		}
		// The original, pre-edit body is not exposed. Even if every retained
		// edit agrees, reviews before the oldest known version lack evidence.
	}
	pr.ScopeUpdatedAt = boundary
	pr.ScopeRevision = BodyHash(strings.Join([]string{pr.URL, pr.TaskURL, pr.GoalURL, boundary.Format(time.RFC3339Nano)}, "\x00"))
	return nil
}

func (g *GitHubClient) verifyPRVersion(ctx context.Context, repo string, number int, version prBodyVersion) error {
	parts := strings.Split(repo, "/")
	query := `query WorkerPRVersion($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){url body headRefOid createdAt lastEditedAt}}}`
	data, err := g.graphql(ctx, parts[0], query, map[string]any{"owner": parts[0], "repo": parts[1], "number": number})
	if err != nil {
		return err
	}
	raw, err := at(data, "repository", "pullRequest")
	if err != nil {
		return err
	}
	fresh, err := decode[prBodyVersion](raw)
	if err != nil || fresh != version {
		return ghError("PR_body_changed_during_read")
	}
	return nil
}
