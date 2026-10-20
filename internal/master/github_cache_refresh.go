package master

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"slices"
	"time"
)

func (svc *Service) ClaimGitHubRefresh(ctx context.Context, actor c.Principal, in c.GitHubClaim) (c.GitHubLease, error) {
	var out c.GitHubLease
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return out, err
	}
	s := svc.Store
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return out, err
	}
	cfg, err := cacheConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	var route GitHubCacheRoute
	for _, r := range cfg.Routes {
		if r.ID == in.RouteID && r.Collector == p.ID {
			route = r
			break
		}
	}
	if route.ID == "" || cfg.mode(route.Engagement) == "off" {
		return out, c.ErrForbidden
	}
	var placeRaw []byte
	var place c.Placement
	if err = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&placeRaw); err != nil {
		return out, c.ErrForbidden
	}
	if err = json.Unmarshal(placeRaw, &place); err != nil {
		return out, err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM github_cache_leases WHERE actor=? AND request_id=?", p.ID, in.RequestID).Scan(&prior)
	if err == nil {
		if json.Unmarshal(prior, &out) != nil || out.Object.RouteID != in.RouteID || out.Collector != place || out.RestoreEpoch != s.epoch {
			return out, c.ErrConflict
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	_, manifest, err := svc.cacheManifest(ctx, tx, p, route.Engagement)
	if err != nil {
		return out, err
	}
	if err = pruneGitHubCache(ctx, tx, s.now()); err != nil {
		return out, err
	}
	for _, v := range manifest.Objects {
		if v.Object.RouteID == route.ID {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO github_cache_objects(key,route,ref) VALUES(?,?,?)", cacheKey(v.Object), route.ID, encode(v.Object)); err != nil {
				return out, err
			}
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_cache_objects").Scan(&count); err != nil {
		return out, err
	}
	if count > c.GitHubCacheObjects {
		return out, c.ErrCapacity
	}
	// Every tenth selection serves oldest due work, rather than another dirty item.
	var rounds int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_cache_leases WHERE actor=?", p.ID).Scan(&rounds); err != nil {
		return out, err
	}
	var chosen *c.GitHubCoverage
	for i := range manifest.Objects {
		v := &manifest.Objects[i]
		if v.Object.RouteID != route.ID {
			continue
		}
		var due, retry, header string
		var epoch int64
		if err = tx.QueryRowContext(ctx, "SELECT due,retry,header_revision,epoch FROM github_cache_objects WHERE key=?", cacheKey(v.Object)).Scan(&due, &retry, &header, &epoch); err != nil {
			return out, err
		}
		dueAt, _ := time.Parse(time.RFC3339Nano, due)
		retryAt, _ := time.Parse(time.RFC3339Nano, retry)
		if retryAt.After(s.now()) || epoch == s.epoch && dueAt.After(s.now()) {
			continue
		}
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_cache_leases WHERE object_key=? AND result='' AND expires>?", cacheKey(v.Object), cacheTime(s.now())).Scan(&active); err != nil {
			return out, err
		}
		if active > 0 {
			continue
		}
		v.Header.Revision = header
		if chosen == nil || rounds%10 == 9 && due < cacheCoverageDue(*chosen) || rounds%10 != 9 && header == "" && chosen.Header.Revision != "" {
			chosen = v
			chosen.NextRetry = dueAt
		}
	}
	if chosen == nil {
		return out, tx.Commit()
	}
	if err = svc.cacheCoverage(ctx, tx, chosen); err != nil {
		return out, err
	}
	var routeActive int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_cache_leases l JOIN github_cache_objects o ON o.key=l.object_key WHERE o.route=? AND l.result='' AND l.expires>?", route.ID, cacheTime(s.now())).Scan(&routeActive); err != nil {
		return out, err
	}
	if routeActive > 0 {
		return out, tx.Commit()
	}
	var last string
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(started),'') FROM github_cache_leases l JOIN github_cache_objects o ON o.key=l.object_key WHERE o.route=?", route.ID).Scan(&last); err != nil {
		return out, err
	}
	lastAt, _ := time.Parse(time.RFC3339Nano, last)
	if s.now().Before(lastAt.Add(time.Second)) {
		return out, tx.Commit()
	}
	_, authority, err := svc.cacheAccess(ctx, tx, p, chosen.Object)
	if err != nil {
		return out, err
	}
	out = c.GitHubLease{ID: newID("gh"), Object: chosen.Object, Authority: authority, Collector: place, RestoreEpoch: s.epoch, PolicySHA: digest(cfg), Manifest: manifest.Manifest, StartedAt: s.now().UTC(), ExpiresAt: s.now().Add(5 * time.Minute).UTC(), AppID: route.AppID, InstallationID: route.InstallationID, Retained: chosen.Datasets}
	if err = tx.QueryRowContext(ctx, "SELECT generation,header_revision FROM github_cache_objects WHERE key=?", cacheKey(out.Object)).Scan(&out.Generation, &out.ExpectedHeader); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO github_cache_leases(id,object_key,actor,request_id,started,expires,body) VALUES(?,?,?,?,?,?,?)", out.ID, cacheKey(out.Object), p.ID, in.RequestID, cacheTime(out.StartedAt), cacheTime(out.ExpiresAt), encode(out)); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func cacheCoverageDue(v c.GitHubCoverage) string {
	if v.NextRetry.IsZero() {
		return ""
	}
	return cacheTime(v.NextRetry)
}

func (svc *Service) cacheLease(ctx context.Context, tx *sql.Tx, actor c.Principal, id string, completed bool) (c.GitHubLease, string, error) {
	var out c.GitHubLease
	var raw []byte
	var owner, result string
	if err := tx.QueryRowContext(ctx, "SELECT body,actor,result FROM github_cache_leases WHERE id=?", id).Scan(&raw, &owner, &result); err != nil {
		return out, result, c.ErrForbidden
	}
	if json.Unmarshal(raw, &out) != nil || owner != actor.ID {
		return out, result, c.ErrForbidden
	}
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return out, result, err
	}
	r, _, err := svc.cacheAccess(ctx, tx, p, out.Object)
	if err != nil || r.Collector != p.ID {
		return out, result, c.ErrForbidden
	}
	if err = placement(ctx, tx, p.ID, out.Collector.InstanceID, out.Collector.Generation); err != nil {
		return out, result, err
	}
	cfg, err := cacheConfig(ctx, tx)
	if err != nil {
		return out, result, err
	}
	if cfg.mode(r.Engagement) == "off" || out.PolicySHA != digest(cfg) || out.RestoreEpoch != svc.Store.epoch {
		return out, result, c.ErrConflict
	}
	if completed && result != "" {
		return out, result, nil
	}
	manifest, err := svc.Store.cacheManifestToken(ctx, tx, p, r.Engagement, cfg)
	if err != nil {
		return out, result, err
	}
	if !out.ExpiresAt.After(svc.Store.now()) || out.Manifest != manifest {
		return out, result, c.ErrConflict
	}
	var gen int64
	var rev string
	if err = tx.QueryRowContext(ctx, "SELECT generation,header_revision FROM github_cache_objects WHERE key=?", cacheKey(out.Object)).Scan(&gen, &rev); err != nil {
		return out, result, err
	}
	if gen != out.Generation || rev != out.ExpectedHeader || result != "" {
		return out, result, c.ErrConflict
	}
	return out, result, nil
}
func cacheBytes(ctx context.Context, q queryer) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT (SELECT COALESCE(SUM(length(data)),0) FROM github_cache_datasets)+(SELECT COALESCE(SUM(length(data)),0) FROM github_cache_chunks)+(SELECT COALESCE(SUM(length(body)+length(request_id)+256),0) FROM github_cache_leases)").Scan(&n)
	return n, err
}
func (svc *Service) UploadGitHubDataset(ctx context.Context, actor c.Principal, in c.GitHubUpload) error {
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return err
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err = svc.cacheLease(ctx, tx, actor, in.LeaseID, false); err != nil {
		return err
	}
	var raw []byte
	var total int
	var sum string
	err = tx.QueryRowContext(ctx, "SELECT data,total,sha FROM github_cache_chunks WHERE lease=? AND kind=? AND offset=?", in.LeaseID, in.Kind, in.Offset).Scan(&raw, &total, &sum)
	if err == nil {
		if !bytes.Equal(raw, in.Data) || total != in.Total || sum != in.SHA256 {
			return c.ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	n, err := cacheBytes(ctx, tx)
	if err != nil {
		return err
	}
	if n+int64(len(in.Data)) > c.GitHubCacheBytes {
		return c.ErrCapacity
	}
	var staged int
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(data)),0) FROM github_cache_chunks WHERE lease=?", in.LeaseID).Scan(&staged); err != nil {
		return err
	}
	if staged+len(in.Data) > c.GitHubSnapshotBytes {
		return c.ErrSize
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO github_cache_chunks(lease,kind,offset,total,sha,data) VALUES(?,?,?,?,?,?)", in.LeaseID, in.Kind, in.Offset, in.Total, in.SHA256, in.Data)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (svc *Service) CommitGitHubRefresh(ctx context.Context, actor c.Principal, in c.GitHubCommit) error {
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return err
	}
	s := svc.Store
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	l, result, err := svc.cacheLease(ctx, tx, actor, in.LeaseID, true)
	if err != nil {
		return err
	}
	if result != "" {
		if result != digest(in) {
			return c.ErrConflict
		}
		return tx.Commit()
	}
	key := cacheKey(l.Object)
	var prevRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT header FROM github_cache_objects WHERE key=?", key).Scan(&prevRaw); err != nil {
		return err
	}
	var prev c.GitHubHeader
	if err = json.Unmarshal(prevRaw, &prev); err != nil {
		return err
	}
	if prev.RepositoryID != 0 && (prev.RepositoryID != in.Header.RepositoryID || prev.ObjectID != in.Header.ObjectID) {
		return c.ErrConflict
	}
	headerTTL := time.Minute
	if in.Header.State != "open" {
		headerTTL = 24 * time.Hour
		var referenced int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_cache_objects o WHERE route=? AND json_extract(header,'$.state')='open'
AND EXISTS(SELECT 1 FROM goals g WHERE g.id=json_extract(o.ref,'$.goal.goal_id') AND g.state='approved' AND g.active_sha=json_extract(o.ref,'$.goal.revision_sha256'))
AND (json_extract(o.ref,'$.task_url')=? OR EXISTS(SELECT 1 FROM json_each(o.header,'$.depends_on') WHERE value=?)))`, l.Object.RouteID, l.Object.TaskURL, l.Object.URL).Scan(&referenced)
		if err != nil {
			return err
		}
		if referenced != 0 {
			headerTTL = 15 * time.Minute
		}
	}
	seen := map[string]bool{}
	headerObserved := in.HeaderObservedAt
	if headerObserved.IsZero() {
		headerObserved = l.StartedAt
	}
	if headerObserved.Before(l.StartedAt) || headerObserved.After(s.now()) {
		return c.ErrInvalid
	}
	total := 0
	for _, d := range in.Datasets {
		if seen[d.Kind] {
			return c.ErrInvalid
		}
		seen[d.Kind] = true
		rows, e := tx.QueryContext(ctx, "SELECT offset,total,sha,data FROM github_cache_chunks WHERE lease=? AND kind=? ORDER BY offset", l.ID, d.Kind)
		if e != nil {
			return e
		}
		var data []byte
		for rows.Next() {
			var offset, total int
			var sha string
			var chunk []byte
			if e = rows.Scan(&offset, &total, &sha, &chunk); e != nil {
				_ = rows.Close()
				return e
			}
			if offset != len(data) || total != d.Total || sha != d.SHA256 {
				_ = rows.Close()
				return c.ErrConflict
			}
			data = append(data, chunk...)
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return e
		}
		if len(data) == 0 {
			var raw []byte
			e = tx.QueryRowContext(ctx, "SELECT data FROM github_cache_datasets WHERE object_key=? AND kind=? AND sha=?", key, d.Kind, d.SHA256).Scan(&raw)
			if e != nil {
				return c.ErrConflict
			}
			data = raw
		}
		total += len(data)
		if total > c.GitHubSnapshotBytes {
			return c.ErrSize
		}
		if len(data) != d.Total || c.GitHubDigest(data) != d.SHA256 || !c.ValidGitHubPayload(data) {
			return c.ErrConflict
		}
		d.Object = l.Object
		d.Collector, d.RestoreEpoch, d.AppID, d.InstallationID = l.Collector, l.RestoreEpoch, l.AppID, l.InstallationID
		d.HeaderRevision = in.Header.Revision
		// The leased collector attests completion of the native verification.
		// Retained children carry their earlier validation time instead of
		// acquiring freshness from this publication. Future times are rejected.
		if d.ObservedAt.IsZero() {
			d.ObservedAt = l.StartedAt
		} else if d.ObservedAt.After(s.now()) {
			return c.ErrInvalid
		}
		ttl := 5 * time.Minute
		if d.Kind == "pr" {
			ttl = time.Minute
		}
		if d.Kind == "provider" {
			ttl = 24 * time.Hour
		}
		if in.Header.State != "open" {
			ttl = headerTTL
		}
		d.DueAt = d.ObservedAt.Add(ttl)
		if _, e = tx.ExecContext(ctx, "INSERT INTO github_cache_datasets(object_key,kind,sha,ref,data,created) VALUES(?,?,?,?,?,?) ON CONFLICT(object_key,kind,sha) DO UPDATE SET ref=excluded.ref", key, d.Kind, d.SHA256, encode(d), data, cacheTime(s.now())); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO github_cache_current(object_key,kind,sha) VALUES(?,?,?) ON CONFLICT(object_key,kind) DO UPDATE SET sha=excluded.sha", key, d.Kind, d.SHA256); e != nil {
			return e
		}
	}
	expected := "task"
	if l.Object.Kind == "pull_request" {
		expected = "pr"
	}
	if !seen[expected] {
		return c.ErrInvalid
	}
	if in.Header.Revision != prev.Revision {
		for _, kind := range []string{"task", "pr", "provider"} {
			if !seen[kind] {
				if _, err = tx.ExecContext(ctx, "DELETE FROM github_cache_current WHERE object_key=? AND kind=?", key, kind); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM github_cache_chunks WHERE lease=?", l.ID); err != nil {
		return err
	}
	for _, d := range in.Datasets {
		if _, err = tx.ExecContext(ctx, "DELETE FROM github_cache_datasets WHERE object_key=? AND kind=? AND sha NOT IN (SELECT sha FROM github_cache_datasets WHERE object_key=? AND kind=? ORDER BY created DESC,sha DESC LIMIT 2) AND sha NOT IN (SELECT sha FROM github_cache_current WHERE object_key=? AND kind=?)", key, d.Kind, key, d.Kind, key, d.Kind); err != nil {
			return err
		}
	}
	n, err := cacheBytes(ctx, tx)
	if err != nil {
		return err
	}
	if n > c.GitHubCacheBytes {
		return c.ErrCapacity
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET header=?,header_revision=?,epoch=?,due=?,reason='',retry='',updated=? WHERE key=?", encode(in.Header), in.Header.Revision, s.epoch, cacheTime(headerObserved.Add(headerTTL)), cacheTime(headerObserved), key); err != nil {
		return err
	}
	for _, dep := range in.Header.DependsOn {
		if slices.Contains(prev.DependsOn, dep) {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET due='',generation=generation+1 WHERE route=? AND json_extract(ref,'$.url')=?", l.Object.RouteID, dep); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_leases SET result=? WHERE id=?", digest(in), l.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (svc *Service) FailGitHubRefresh(ctx context.Context, actor c.Principal, in c.GitHubFailure) error {
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return err
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	l, _, err := svc.cacheLease(ctx, tx, actor, in.LeaseID, false)
	if err != nil {
		return err
	}
	now := svc.Store.now()
	if in.RetryAt.Before(now.Add(time.Minute)) {
		in.RetryAt = now.Add(time.Minute)
	}
	if in.RetryAt.After(now.Add(24 * time.Hour)) {
		in.RetryAt = now.Add(24 * time.Hour)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET due='',reason=?,retry=? WHERE key=?", in.Category, cacheTime(in.RetryAt), cacheKey(l.Object)); err != nil {
		return err
	}
	if in.Category == "rate_limited" || in.Category == "credentials_expired" || in.Category == "permission_denied" {
		if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET retry=?,reason=? WHERE route=?", cacheTime(in.RetryAt), in.Category, l.Object.RouteID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_leases SET result=? WHERE id=?", "failed", l.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (svc *Service) DirtyGitHubObject(ctx context.Context, actor c.Principal, in c.GitHubDirty) error {
	if err := c.ValidateGitHubCacheMessage(in); err != nil {
		return err
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return err
	}
	if _, _, err = svc.cacheAccess(ctx, tx, p, in.Object); err != nil {
		return err
	}
	// One stable per-source hint is sufficient until a successful refresh clears it.
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_objects SET generation=generation+1,due='' WHERE key=? AND NOT EXISTS(SELECT 1 FROM github_cache_hints WHERE actor=? AND request=?)", cacheKey(in.Object), p.ID, in.RequestID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_cache_leases SET result='invalidated' WHERE object_key=? AND result='' AND NOT EXISTS(SELECT 1 FROM github_cache_hints WHERE actor=? AND request=?)", cacheKey(in.Object), p.ID, in.RequestID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM github_cache_chunks WHERE lease IN (SELECT id FROM github_cache_leases WHERE result='invalidated')"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO github_cache_hints(actor,request,object_key) VALUES(?,?,?)", p.ID, in.RequestID, cacheKey(in.Object)); err != nil {
		return err
	}
	return tx.Commit()
}
func pruneGitHubCache(ctx context.Context, q queryer, now time.Time) error {
	for _, op := range []struct {
		sql string
		arg any
	}{
		{"DELETE FROM github_cache_chunks WHERE lease IN (SELECT id FROM github_cache_leases WHERE expires<=?)", cacheTime(now)},
		{"UPDATE github_cache_leases SET body='{}' WHERE expires<=?", cacheTime(now)},
		{"DELETE FROM github_cache_leases WHERE expires<=?", cacheTime(now.Add(-24 * time.Hour))},
		{"DELETE FROM github_cache_objects WHERE updated<>'' AND updated<? AND NOT EXISTS(SELECT 1 FROM artifacts a JOIN goals g ON g.id=a.goal_id WHERE g.state='approved' AND g.active_sha=a.revision AND a.url=json_extract(github_cache_objects.ref,'$.url') AND a.assignment_sha=json_extract(github_cache_objects.ref,'$.assignment_sha256'))", cacheTime(now.Add(-30 * 24 * time.Hour))},
	} {
		if _, err := q.ExecContext(ctx, op.sql, op.arg); err != nil {
			return err
		}
	}
	// Hints are disposable once their object has no outstanding refresh intent.
	_, err := q.ExecContext(ctx, "DELETE FROM github_cache_hints WHERE object_key NOT IN (SELECT key FROM github_cache_objects WHERE due='')")
	return err
}
