package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"slices"
	"strconv"
	"strings"
	"time"
)

func cacheKey(ref c.GitHubObjectRef) string {
	ref.RepositoryID = 0
	ref.ObjectID = ""
	return digest(ref)
}
func cacheTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func cacheConfig(ctx context.Context, q queryer) (cfg GitHubCacheConfig, err error) {
	var raw string
	err = q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='github_cache_config'").Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &cfg)
	}
	return
}
func (s *Store) ConfigureGitHubCache(ctx context.Context, cfg GitHubCacheConfig) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT body FROM principals")
	if err != nil {
		return err
	}
	var ps []c.Principal
	for rows.Next() {
		var raw []byte
		var p c.Principal
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &p)
		}
		if err != nil {
			_ = rows.Close()
			return err
		}
		ps = append(ps, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if err = validateGitHubCacheConfig(cfg, ps); err != nil {
		return err
	}
	for _, r := range cfg.Routes {
		for _, id := range append(append([]string{}, r.Readers...), r.Collector) {
			p, e := principal(ctx, tx, id)
			if e != nil {
				return e
			}
			for _, th := range r.Threads {
				if e = member(ctx, tx, p, th); e != nil {
					return e
				}
			}
		}
	}
	old, err := cacheConfig(ctx, tx)
	if err != nil {
		return err
	}
	if digest(old) == digest(cfg) {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='github_cache_config'", string(encode(cfg))); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='github_cache_revision'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET generation=generation+1,due='',epoch=0"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, invalidateGitHubLeases); err != nil {
		return err
	}
	return tx.Commit()
}

// cacheManifest uses one database snapshot for membership, authority and links.
func (svc *Service) cacheManifest(ctx context.Context, q queryer, actor c.Principal, eng string) (GitHubCacheConfig, c.GitHubCatalogPage, error) {
	s := svc.Store
	out := c.GitHubCatalogPage{Version: 1, MasterID: s.id, RestoreEpoch: s.epoch, Goals: []c.GoalAuthority{}, Objects: []c.GitHubCoverage{}}
	p, err := authorize(ctx, q, actor, "read")
	if err != nil {
		return GitHubCacheConfig{}, out, err
	}
	if !slices.Contains(p.Engagements, eng) {
		return GitHubCacheConfig{}, out, c.ErrForbidden
	}
	cfg, err := cacheConfig(ctx, q)
	if err != nil {
		return cfg, out, err
	}
	out.Mode = cfg.mode(eng)
	collectors := map[string]c.Placement{}
	for _, route := range cfg.Routes {
		if route.Engagement != eng {
			continue
		}
		var raw []byte
		var place c.Placement
		e := q.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", route.Collector).Scan(&raw)
		if e == nil {
			if e = json.Unmarshal(raw, &place); e != nil {
				return cfg, out, e
			}
			collectors[route.ID] = place
		} else if !errors.Is(e, sql.ErrNoRows) {
			return cfg, out, e
		}
	}
	out.Manifest, err = s.cacheManifestToken(ctx, q, p, eng, cfg)
	if err != nil {
		return cfg, out, err
	}
	rows, err := q.QueryContext(ctx, "SELECT g.id,g.active_sha FROM goals g JOIN threads t ON t.id=g.thread_id WHERE g.state='approved' AND t.engagement=? AND EXISTS(SELECT 1 FROM members m WHERE m.thread_id=t.id AND m.principal_id=?) ORDER BY g.id", eng, p.ID)
	if err != nil {
		return cfg, out, err
	}
	var refs []c.GoalRef
	for rows.Next() {
		ref := c.GoalRef{Source: "master", MasterID: s.id}
		if err = rows.Scan(&ref.GoalID, &ref.RevisionSHA256); err != nil {
			_ = rows.Close()
			return cfg, out, err
		}
		refs = append(refs, ref)
		if len(refs) > c.GitHubCacheObjects {
			_ = rows.Close()
			return cfg, out, c.ErrCapacity
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return cfg, out, err
	}
	for _, ref := range refs {
		a, e := s.resolveGoal(ctx, q, p, ref)
		if e != nil {
			return cfg, out, e
		}
		if !a.Valid {
			continue
		}
		out.Goals = append(out.Goals, a)
		rows, e := q.QueryContext(ctx, "SELECT body FROM artifacts WHERE goal_id=? AND revision=? ORDER BY url", ref.GoalID, ref.RevisionSHA256)
		if e != nil {
			return cfg, out, e
		}
		var links []c.ArtifactLink
		for rows.Next() {
			var raw []byte
			var l c.ArtifactLink
			if e = rows.Scan(&raw); e == nil {
				e = json.Unmarshal(raw, &l)
			}
			if e != nil {
				_ = rows.Close()
				return cfg, out, e
			}
			links = append(links, l)
			if len(links) > c.GitHubCacheObjects {
				_ = rows.Close()
				return cfg, out, c.ErrCapacity
			}
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return cfg, out, e
		}
		for _, l := range links {
			kind := "issues"
			if l.Kind == "pull_request" {
				kind = "pull"
			}
			repo, e := artifactRepo(l.URL, kind)
			if e != nil {
				return cfg, out, e
			}
			route := ""
			for _, r := range cfg.Routes {
				if r.Engagement == eng && slices.Contains(r.Threads, a.Revision.Proposal.ThreadID) && slices.Contains(r.Repositories, repo) && (slices.Contains(r.Readers, p.ID) || r.Collector == p.ID) {
					route = r.ID
					break
				}
			}
			task := l.TaskURL
			if l.Kind == "task" {
				task = l.URL
			}
			v := c.GitHubCoverage{Object: c.GitHubObjectRef{RouteID: route, Goal: l.Goal, TaskURL: task, URL: l.URL, Kind: l.Kind, AssignmentSHA: l.AssignmentSHA}, Dirty: true, Reason: "cache_unknown"}
			v.Collector = collectors[route]
			if route == "" {
				v.Reason = "cache_not_granted"
			}
			out.Objects = append(out.Objects, v)
			if len(out.Objects) > c.GitHubCacheObjects {
				return cfg, out, c.ErrCapacity
			}
		}
	}
	return cfg, out, nil
}

func (s *Store) cacheManifestToken(ctx context.Context, q queryer, p c.Principal, eng string, cfg GitHubCacheConfig) (string, error) {
	var rev string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='github_cache_revision'").Scan(&rev)
	if err != nil {
		return "", err
	}
	return digest([]any{s.id, s.epoch, rev, p, eng, digest(cfg)}), nil
}
func (svc *Service) cacheCoverage(ctx context.Context, q queryer, v *c.GitHubCoverage) error {
	var raw []byte
	var epoch int64
	var due, retry, observed string
	err := q.QueryRowContext(ctx, "SELECT header,epoch,due,reason,retry,updated FROM github_cache_objects WHERE key=?", cacheKey(v.Object)).Scan(&raw, &epoch, &due, &v.Reason, &retry, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &v.Header); err != nil {
		return err
	}
	v.NextRetry, _ = time.Parse(time.RFC3339Nano, retry)
	v.ObservedAt, _ = time.Parse(time.RFC3339Nano, observed)
	deadline, _ := time.Parse(time.RFC3339Nano, due)
	v.DueAt = deadline
	v.Dirty = epoch != svc.Store.epoch || !deadline.After(svc.Store.now())
	rows, err := q.QueryContext(ctx, "SELECT d.ref FROM github_cache_current p JOIN github_cache_datasets d ON d.object_key=p.object_key AND d.kind=p.kind AND d.sha=p.sha WHERE p.object_key=? ORDER BY p.kind", cacheKey(v.Object))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var d c.GitHubDatasetRef
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &d)
		}
		if err != nil {
			return err
		}
		v.Datasets = append(v.Datasets, d)
	}
	return rows.Err()
}

// Hydrate a page with two bounded queries, rather than re-reading every object's
// coverage on every page. Object keys remain bound SQL arguments.
func (svc *Service) cachePageCoverage(ctx context.Context, q queryer, objects []c.GitHubCoverage) error {
	byKey := map[string]*c.GitHubCoverage{}
	var args []any
	for i := range objects {
		if objects[i].Object.RouteID != "" {
			key := cacheKey(objects[i].Object)
			byKey[key] = &objects[i]
			args = append(args, key)
		}
	}
	if len(args) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, err := q.QueryContext(ctx, "SELECT key,header,epoch,due,reason,retry,updated FROM github_cache_objects WHERE key IN ("+placeholders+")", args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, due, reason, retry, observed string
		var raw []byte
		var epoch int64
		if err = rows.Scan(&key, &raw, &epoch, &due, &reason, &retry, &observed); err != nil {
			break
		}
		v := byKey[key]
		if err = json.Unmarshal(raw, &v.Header); err != nil {
			break
		}
		v.Reason = reason
		v.NextRetry, _ = time.Parse(time.RFC3339Nano, retry)
		v.ObservedAt, _ = time.Parse(time.RFC3339Nano, observed)
		v.DueAt, _ = time.Parse(time.RFC3339Nano, due)
		v.Dirty = epoch != svc.Store.epoch || !v.DueAt.After(svc.Store.now())
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, "SELECT d.object_key,d.ref FROM github_cache_current p JOIN github_cache_datasets d ON d.object_key=p.object_key AND d.kind=p.kind AND d.sha=p.sha WHERE p.object_key IN ("+placeholders+") ORDER BY p.object_key,p.kind", args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var raw []byte
		var d c.GitHubDatasetRef
		if err = rows.Scan(&key, &raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return err
		}
		byKey[key].Datasets = append(byKey[key].Datasets, d)
	}
	return rows.Err()
}
func (svc *Service) GitHubCatalog(ctx context.Context, actor c.Principal, in c.GitHubCatalogQuery) (c.GitHubCatalogPage, error) {
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return c.GitHubCatalogPage{}, err
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.GitHubCatalogPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, out, err := svc.cacheManifest(ctx, tx, actor, in.Engagement)
	if err != nil {
		return out, err
	}
	if in.Manifest != "" && in.Manifest != out.Manifest {
		return out, c.ErrConflict
	}
	offset := 0
	if in.After != "" {
		offset, err = strconv.Atoi(in.After)
		if err != nil || offset < 0 || offset > len(out.Objects) {
			return out, c.ErrInvalid
		}
	}
	end := min(offset+c.MaxPageRecords, len(out.Objects))
	if end < len(out.Objects) {
		out.Next = strconv.Itoa(end)
	}
	out.Objects = out.Objects[offset:end]
	if err = svc.cachePageCoverage(ctx, tx, out.Objects); err != nil {
		return out, err
	}
	// Goals are separately bounded: never return an oversized or truncated manifest.
	if len(encode(out)) > c.MaxPageBytes-128 {
		return out, c.ErrSize
	}
	return out, tx.Commit()
}
func (svc *Service) cacheAccess(ctx context.Context, q queryer, actor c.Principal, ref c.GitHubObjectRef) (GitHubCacheRoute, c.GoalAuthority, error) {
	if err := c.ValidateGitHubObject(ref); err != nil {
		return GitHubCacheRoute{}, c.GoalAuthority{}, err
	}
	p, err := authorize(ctx, q, actor, "read")
	if err != nil {
		return GitHubCacheRoute{}, c.GoalAuthority{}, err
	}
	a, err := svc.Store.resolveGoal(ctx, q, p, ref.Goal)
	if err != nil || !a.Valid {
		return GitHubCacheRoute{}, a, c.ErrForbidden
	}
	cfg, err := cacheConfig(ctx, q)
	if err != nil {
		return GitHubCacheRoute{}, a, err
	}
	var eng string
	if err = q.QueryRowContext(ctx, "SELECT engagement FROM threads WHERE id=?", a.Revision.Proposal.ThreadID).Scan(&eng); err != nil {
		return GitHubCacheRoute{}, a, err
	}
	kind := "issues"
	if ref.Kind == "pull_request" {
		kind = "pull"
	}
	repo, err := artifactRepo(ref.URL, kind)
	if err != nil {
		return GitHubCacheRoute{}, a, c.ErrForbidden
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT body FROM artifacts WHERE goal_id=? AND revision=? AND url=?", ref.Goal.GoalID, ref.Goal.RevisionSHA256, ref.URL).Scan(&raw); err != nil {
		return GitHubCacheRoute{}, a, c.ErrForbidden
	}
	var l c.ArtifactLink
	if json.Unmarshal(raw, &l) != nil || l.Kind != ref.Kind || l.AssignmentSHA != ref.AssignmentSHA || ref.Kind == "task" && ref.TaskURL != ref.URL || ref.Kind == "pull_request" && ref.TaskURL != l.TaskURL {
		return GitHubCacheRoute{}, a, c.ErrForbidden
	}
	scope := false
	for _, r := range a.Revision.Proposal.Repositories {
		scope = scope || r.Name == repo
	}
	if !scope {
		return GitHubCacheRoute{}, a, c.ErrForbidden
	}
	for _, r := range cfg.Routes {
		if r.ID == ref.RouteID && r.Engagement == eng && slices.Contains(r.Threads, a.Revision.Proposal.ThreadID) && slices.Contains(r.Repositories, repo) && (slices.Contains(r.Readers, p.ID) || r.Collector == p.ID) {
			return r, a, nil
		}
	}
	return GitHubCacheRoute{}, a, c.ErrForbidden
}
func (svc *Service) GitHubDataset(ctx context.Context, actor c.Principal, in c.GitHubDatasetQuery) (c.GitHubDatasetChunk, error) {
	var out c.GitHubDatasetChunk
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return out, err
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	r, _, err := svc.cacheAccess(ctx, tx, actor, in.Ref.Object)
	if err != nil {
		return out, err
	}
	p, err := principal(ctx, tx, actor.ID)
	if err != nil {
		return out, err
	}
	cfg, err := cacheConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	manifest, err := svc.Store.cacheManifestToken(ctx, tx, p, r.Engagement, cfg)
	if err != nil {
		return out, err
	}
	if in.Manifest != manifest {
		return out, c.ErrConflict
	}
	var raw []byte
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT ref,substr(data,?,?) FROM github_cache_datasets WHERE object_key=? AND kind=? AND sha=?", in.Offset+1, c.GitHubChunkBytes, cacheKey(in.Ref.Object), in.Ref.Kind, in.Ref.SHA256).Scan(&raw, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return out, c.ErrForbidden
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out.Ref); err != nil {
		return out, err
	}
	if digest(out.Ref) != digest(in.Ref) || in.Offset > out.Ref.Total {
		return out, c.ErrConflict
	}
	out.Offset = in.Offset
	out.Data = data
	if in.Offset+len(data) < out.Ref.Total {
		out.Next = in.Offset + len(data)
	}
	return out, tx.Commit()
}
func (svc *Service) GitHubCacheStatus(ctx context.Context, actor c.Principal, eng string) (c.GitHubCacheStatus, error) {
	out := c.GitHubCacheStatus{Version: 1}
	page, err := svc.GitHubCatalog(ctx, actor, c.GitHubCatalogQuery{Engagement: eng})
	if err != nil {
		return out, err
	}
	out.Mode, out.Manifest, out.Objects = page.Mode, page.Manifest, page.Objects
	for page.Next != "" {
		page, err = svc.GitHubCatalog(ctx, actor, c.GitHubCatalogQuery{Engagement: eng, Manifest: out.Manifest, After: page.Next})
		if err != nil {
			return out, err
		}
		out.Objects = append(out.Objects, page.Objects...)
		if len(encode(out)) > c.MaxPageBytes-128 {
			return out, c.ErrSize
		}
	}
	return out, nil
}
