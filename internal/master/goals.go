package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

func (s *Store) goalMembers(ctx context.Context, q queryer, in c.GoalProposal) error {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", in.ThreadID).Scan(&raw); err != nil {
		return c.ErrForbidden
	}
	var source c.Thread
	if json.Unmarshal(raw, &source) != nil {
		return c.ErrInvalid
	}
	dest := in.QA.EvidenceDestination
	if dest.MasterID != s.id || dest.ThreadID == "" || dest.Engagement != source.Engagement {
		return c.ErrForbidden
	}
	var destEng string
	if err := q.QueryRowContext(ctx, "SELECT engagement FROM threads WHERE id=?", dest.ThreadID).Scan(&destEng); err != nil || destEng != dest.Engagement {
		return c.ErrForbidden
	}
	ids := []string{in.AuthorID, in.PeerID}
	if in.AuthorID == in.PeerID {
		return c.ErrInvalid
	}
	if in.QA.Required {
		if in.QA.ActorID == in.AuthorID || in.QA.ActorID == in.PeerID || strings.TrimSpace(in.QA.Environment) == "" || strings.TrimSpace(in.QA.EnvironmentRevision) == "" || len(in.QA.EnvironmentRevision) > 256 || strings.ContainsAny(in.QA.EnvironmentRevision, "\x00\r\n") {
			return c.ErrInvalid
		}
		ids = append(ids, in.QA.ActorID)
	}
	seenBots := map[int64]bool{}
	for _, id := range ids {
		p, err := principal(ctx, q, id)
		if err != nil || p.ID != id || p.Kind != "agent" || p.GitHubBotID < 1 || seenBots[p.GitHubBotID] {
			return c.ErrForbidden
		}
		seenBots[p.GitHubBotID] = true
		if err = member(ctx, q, p, in.ThreadID); err != nil {
			return err
		}
		if err = member(ctx, q, p, dest.ThreadID); err != nil {
			return err
		}
	}
	return nil
}
func (svc *Service) ProposeGoal(ctx context.Context, actor c.Principal, in c.GoalProposal) (c.GoalRevision, error) {
	s := svc.Store
	var out c.GoalRevision
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return out, err
	}
	if err = member(ctx, tx, p, in.ThreadID); err != nil {
		return out, err
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "propose", in, &out); yes || e != nil {
		return out, e
	}
	original := in
	in.Repositories = slices.Clone(in.Repositories)
	if strings.TrimSpace(in.Purpose) == "" || strings.TrimSpace(in.Scope) == "" || len(in.Repositories) == 0 || len(in.Repositories) > 64 || len(in.Acceptance) == 0 || len(encode(in)) > c.MaxText {
		return out, c.ErrInvalid
	}
	for i := range in.Repositories {
		r := &in.Repositories[i]
		if !repositoryName.MatchString(r.Name) || strings.HasSuffix(r.Name, "/.") || strings.HasSuffix(r.Name, "/..") || len(r.BaseBranches) == 0 {
			return out, c.ErrInvalid
		}
		r.BaseBranches = normalized(r.BaseBranches)
		for _, b := range r.BaseBranches {
			if strings.TrimSpace(b) == "" || strings.ContainsAny(b, "\x00\r\n") || len(b) > 255 {
				return out, c.ErrInvalid
			}
		}
	}
	slices.SortFunc(in.Repositories, func(a, b c.RepositoryScope) int { return strings.Compare(a.Name, b.Name) })
	for i := 1; i < len(in.Repositories); i++ {
		if in.Repositories[i-1].Name == in.Repositories[i].Name {
			return out, c.ErrInvalid
		}
	}
	if in.QA.EvidenceDestination.ThreadID == "" {
		var eng string
		if err = tx.QueryRowContext(ctx, "SELECT engagement FROM threads WHERE id=?", in.ThreadID).Scan(&eng); err != nil {
			return out, err
		}
		in.QA.EvidenceDestination = c.EvidenceDestination{MasterID: s.id, ThreadID: in.ThreadID, Engagement: eng}
	}
	if err = s.goalMembers(ctx, tx, in); err != nil {
		return out, err
	}
	if in.GoalID == "" {
		in.GoalID = newID("goal")
		if _, err = tx.ExecContext(ctx, "INSERT INTO goals(id,thread_id,state) VALUES(?,?,'draft')", in.GoalID, in.ThreadID); err != nil {
			return out, err
		}
	} else {
		var thread string
		if err = tx.QueryRowContext(ctx, "SELECT thread_id FROM goals WHERE id=?", in.GoalID).Scan(&thread); err != nil || thread != in.ThreadID {
			return out, c.ErrForbidden
		}
	}
	canonical := in
	canonical.RequestID = ""
	canonical.GoalID = ""
	sha := digest(canonical)
	out = c.GoalRevision{Ref: c.GoalRef{Source: "master", MasterID: s.id, GoalID: in.GoalID, RevisionSHA256: sha}, Proposal: in, ProposerID: p.ID, CreatedAt: s.now().UTC()}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM goal_revisions WHERE goal_id=? AND sha=?", in.GoalID, sha).Scan(&old)
	switch err {
	case nil:
		if err = json.Unmarshal(old, &out); err != nil {
			return out, err
		}
	case sql.ErrNoRows:
		if _, err = tx.ExecContext(ctx, "INSERT INTO goal_revisions(goal_id,sha,body) VALUES(?,?,?)", in.GoalID, sha, encode(out)); err != nil {
			return out, err
		}
		if err = event(ctx, tx, "goal_proposed", in.ThreadID, ""); err != nil {
			return out, err
		}
	default:
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, original.RequestID, "propose", original, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) resolveGoal(ctx context.Context, q queryer, p c.Principal, ref c.GoalRef) (c.GoalAuthority, error) {
	var out c.GoalAuthority
	if ref.Source != "master" || ref.MasterID != s.id {
		return out, c.ErrConflict
	}
	var raw []byte
	var thread string
	err := q.QueryRowContext(ctx, "SELECT g.thread_id,g.state,g.active_sha,g.decision_seq,g.approval_seq,r.body FROM goals g JOIN goal_revisions r ON r.goal_id=g.id WHERE g.id=? AND r.sha=?", ref.GoalID, ref.RevisionSHA256).Scan(&thread, &out.State, &out.ActiveRevisionSHA256, &out.DecisionSequence, &out.ApprovalSequence, &raw)
	if err == sql.ErrNoRows {
		return out, c.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err = member(ctx, q, p, thread); err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out.Revision); err != nil {
		return out, err
	}
	roles := []struct {
		id   string
		dest *c.GoalActor
	}{{out.Revision.Proposal.AuthorID, &out.Actors.Author}, {out.Revision.Proposal.PeerID, &out.Actors.Peer}}
	if out.Revision.Proposal.QA.Required {
		roles = append(roles, struct {
			id   string
			dest *c.GoalActor
		}{out.Revision.Proposal.QA.ActorID, &out.Actors.QA})
	}
	for _, role := range roles {
		actor, e := principal(ctx, q, role.id)
		if e != nil {
			return out, e
		}
		*role.dest = c.GoalActor{PrincipalID: actor.ID, GitHubBotID: actor.GitHubBotID}
	}
	var quiescent string
	if err = q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='quiescent'").Scan(&quiescent); err != nil {
		return out, err
	}
	out.Valid = out.State == "approved" && out.ActiveRevisionSHA256 == ref.RevisionSHA256 && quiescent == "0"
	if out.Valid {
		if err = s.goalMembers(ctx, q, out.Revision.Proposal); err != nil {
			out.Valid = false
		}
	}
	return out, nil
}
func (svc *Service) ResolveGoal(ctx context.Context, actor c.Principal, ref c.GoalRef) (c.GoalAuthority, error) {
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return c.GoalAuthority{}, err
	}
	return svc.Store.resolveGoal(ctx, svc.Store.db, p, ref)
}
