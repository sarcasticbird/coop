package master

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func artifactRepo(ref, kind string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" {
		return "", c.ErrInvalid
	}
	p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(p) != 4 || p[2] != kind {
		return "", c.ErrInvalid
	}
	n, e := strconv.ParseInt(p[3], 10, 64)
	if e != nil || n < 1 || strconv.FormatInt(n, 10) != p[3] || !repositoryName.MatchString(p[0]+"/"+p[1]) {
		return "", c.ErrInvalid
	}
	return p[0] + "/" + p[1], nil
}
func (svc *Service) RegisterArtifact(ctx context.Context, actor c.Principal, in c.ArtifactLink) (c.ArtifactLink, error) {
	return svc.registerArtifact(ctx, actor, in, nil)
}
func (svc *Service) registerArtifact(ctx context.Context, actor c.Principal, in c.ArtifactLink, reservation *taskBindReservation) (c.ArtifactLink, error) {
	var out c.ArtifactLink
	s := svc.Store
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	capability := "send"
	if in.Kind == "task" && strings.HasPrefix(in.URL, "kata:") {
		capability = "read"
	}
	p, err := authorize(ctx, tx, actor, capability)
	if err != nil {
		return out, err
	}
	a, err := s.resolveGoal(ctx, tx, p, in.Goal)
	if err != nil {
		return out, err
	}
	if err = admission(ctx, tx); err != nil {
		return out, err
	}
	if !a.Valid {
		return out, c.ErrForbidden
	}
	ref := in.URL
	if in.Kind == "pull_request" {
		ref = in.TaskURL
	}
	pending, hasPending, err := taskMutation(ctx, tx, ref)
	if err != nil {
		return out, err
	}
	if hasPending {
		if reservation == nil || !sameTaskMutation(pending, *reservation) || in.Kind != "task" || in.URL != pending.Input.Binding.TaskURL || in.AssignmentSHA != digest(pending.Input.Binding) || in.ExpectedAssignmentSHA != pending.Input.ExpectedAssignmentSHA {
			return out, c.ErrConflict
		}
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "artifact", in, &out); yes || e != nil {
		if e != nil || !hasPending {
			return out, e
		}
		if err = finishTaskMutation(ctx, tx, ref); err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	if reservation != nil && !hasPending {
		return out, c.ErrConflict
	}
	sha, e := hex.DecodeString(in.AssignmentSHA)
	if e != nil || len(sha) != 32 {
		return out, c.ErrInvalid
	}
	kind := "issues"
	if in.Kind == "pull_request" {
		kind = "pull"
	} else if in.Kind != "task" {
		return out, c.ErrInvalid
	}
	repo, err := svc.artifactRepository(in.URL, kind)
	if err != nil {
		return out, err
	}
	allowed := false
	for _, r := range a.Revision.Proposal.Repositories {
		if r.Name == repo {
			allowed = true
		}
	}
	if !allowed {
		return out, c.ErrForbidden
	}
	if in.Kind == "task" {
		if p.Kind != "owner" && !slices.Contains(p.Capabilities, "plan") {
			return out, c.ErrForbidden
		}
		if in.TaskURL != "" {
			return out, c.ErrInvalid
		}
	} else {
		if p.ID != a.Actors.Author.PrincipalID {
			return out, c.ErrForbidden
		}
		parentRepo, e := svc.artifactRepository(in.TaskURL, "issues")
		if e != nil || parentRepo != repo {
			return out, c.ErrInvalid
		}
		var parentSHA string
		if e = tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", in.Goal.GoalID, in.Goal.RevisionSHA256, in.TaskURL).Scan(&parentSHA); e != nil || parentSHA != in.AssignmentSHA {
			return out, c.ErrConflict
		}
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=?", in.Goal.GoalID, in.Goal.RevisionSHA256, in.URL).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	if err == nil && existing != in.AssignmentSHA && strings.HasPrefix(in.URL, "kata:") {
		published, e := taskPublished(ctx, tx, in.Goal, in.URL)
		if e != nil {
			return out, e
		}
		if published {
			return out, c.ErrConflict
		}
	}
	if err == nil && existing != in.AssignmentSHA && in.ExpectedAssignmentSHA != existing {
		return out, c.ErrConflict
	}
	if err == sql.ErrNoRows {
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM artifacts").Scan(&count); e != nil {
			return out, e
		}
		if count >= 10000 {
			return out, c.ErrCapacity
		}
	}
	out = in
	out.ExpectedAssignmentSHA = ""
	out.RequestID = ""
	if _, err = tx.ExecContext(ctx, "INSERT INTO artifacts(goal_id,revision,url,kind,assignment_sha,body) VALUES(?,?,?,?,?,?) ON CONFLICT(goal_id,revision,url) DO UPDATE SET assignment_sha=excluded.assignment_sha,body=excluded.body", in.Goal.GoalID, in.Goal.RevisionSHA256, in.URL, in.Kind, in.AssignmentSHA, encode(out)); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "artifact", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "artifact", a.Revision.Proposal.ThreadID, ""); err != nil {
		return out, err
	}
	if hasPending {
		if err = finishTaskMutation(ctx, tx, ref); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
func (svc *Service) Artifacts(ctx context.Context, actor c.Principal, in c.ArtifactQuery) (c.ArtifactPage, error) {
	s := svc.Store
	out := c.ArtifactPage{Links: []c.ArtifactLink{}}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if _, err = s.resolveGoal(ctx, s.db, p, in.Goal); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM artifacts WHERE goal_id=? AND revision=? AND url>? ORDER BY url LIMIT ?", in.Goal.GoalID, in.Goal.RevisionSHA256, in.After, c.MaxPageRecords+1)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	size := 128
	for rows.Next() {
		var raw []byte
		var link c.ArtifactLink
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &link); err != nil {
			return out, err
		}
		if len(out.Links) == c.MaxPageRecords || size+len(raw) > c.MaxPageBytes-128 {
			if len(out.Links) == 0 {
				return out, c.ErrSize
			}
			out.Next = out.Links[len(out.Links)-1].URL
			break
		}
		size += len(raw) + 1
		out.Links = append(out.Links, link)
	}
	return out, rows.Err()
}

// Kata refs are immutable identities scoped to the configured private provider.
func (svc *Service) artifactRepository(ref, kind string) (string, error) {
	if kind == "issues" && strings.HasPrefix(ref, "kata:") {
		project, _, repo, err := c.ParseKataTaskReference(ref)
		if err != nil {
			return "", err
		}
		if svc.Tasks == nil || svc.Tasks.Capabilities().Provider != "kata" || project != svc.Tasks.Capabilities().ProjectUID {
			return "", c.ErrForbidden
		}
		return repo, nil
	}
	return artifactRepo(ref, kind)
}
