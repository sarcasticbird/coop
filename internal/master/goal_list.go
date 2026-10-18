package master

import (
	"context"
	"slices"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (svc *Service) ApprovedGoals(ctx context.Context, actor c.Principal, engagement, after string) (c.GoalPage, error) {
	out := c.GoalPage{Goals: []c.GoalAuthority{}}
	s := svc.Store
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if !slices.Contains(p.Engagements, engagement) {
		return out, c.ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, "SELECT g.id,g.active_sha FROM goals g JOIN threads t ON t.id=g.thread_id WHERE g.id>? AND g.state='approved' AND t.engagement=? AND EXISTS(SELECT 1 FROM members m WHERE m.thread_id=g.thread_id AND m.principal_id=?) ORDER BY g.id LIMIT ?", after, engagement, p.ID, c.MaxPageRecords+1)
	if err != nil {
		return out, err
	}
	type ref struct{ id, sha string }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if err = rows.Scan(&v.id, &v.sha); err != nil {
			_ = rows.Close()
			return out, err
		}
		refs = append(refs, v)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	size := 128
	for i, r := range refs {
		if i == c.MaxPageRecords {
			out.Next = refs[i-1].id
			break
		}
		a, e := s.resolveGoal(ctx, s.db, p, c.GoalRef{Source: "master", MasterID: s.id, GoalID: r.id, RevisionSHA256: r.sha})
		if e != nil {
			return out, e
		}
		if !a.Valid {
			continue
		}
		n := len(encode(a)) + 1
		if size+n > c.MaxPageBytes-128 {
			if len(out.Goals) == 0 {
				return out, c.ErrSize
			}
			out.Next = out.Goals[len(out.Goals)-1].Revision.Ref.GoalID
			break
		}
		size += n
		out.Goals = append(out.Goals, a)
	}
	return out, nil
}
