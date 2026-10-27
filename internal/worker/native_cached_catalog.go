package worker

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"
)

type NativeCacheCoverage struct {
	OutOfScope map[c.GoalRef]bool
	Manifest   string
	Registered map[c.GoalRef]int
	Ready      map[string]bool
	FreshUntil map[string]time.Time
	Reasons    map[string]string
}
type cachedNativeReader struct {
	payloads     map[string][]byte
	payloadBytes int
	live         NativeCatalogReader
	cache        GitHubCacheClient
	clock        Clock
}

func NewCachedNativeReader(cfg Config, live NativeCatalogReader, cache GitHubCacheClient, clock Clock) NativeCatalogReader {
	if kataTaskSource(cfg) {
		return live
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &cachedNativeReader{live: live, cache: cache, clock: clock, payloads: map[string][]byte{}}
}

// Discovery wrappers never replace the concrete, live action client.
func nativeActionClient(reader NativeCatalogReader) *GitHubClient {
	for {
		switch value := reader.(type) {
		case *GitHubClient:
			return value
		case *cachedNativeReader:
			reader = value.live
		default:
			return nil
		}
	}
}
func (r *cachedNativeReader) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, j Job, extra ...string) (NativeCatalog, error) {
	return r.live.NativeTaskSnapshot(ctx, cfg, m, j, extra...)
}
func (r *cachedNativeReader) NativeLiveSnapshot(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	return r.live.NativeSnapshot(ctx, cfg, m)
}
func (r *cachedNativeReader) NativeSnapshot(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	cap, e := r.cache.GitHubCacheCapabilities(ctx, cfg.Master.Engagement)
	if e != nil {
		return NativeCatalog{}, e
	}
	if cap.Version != 1 {
		return NativeCatalog{}, c.ErrUnsupported
	}
	switch cap.Mode {
	case "off":
		return r.live.NativeSnapshot(ctx, cfg, m)
	case "on":
		return r.project(ctx, cfg, m)
	case "shadow":
		live, err := r.live.NativeSnapshot(ctx, cfg, m)
		if err != nil {
			return live, err
		}
		observed, e := r.project(ctx, cfg, m)
		if e != nil {
			slog.Warn("GitHub cache shadow unavailable", "category", errorCategory(e))
			return live, nil
		}
		a, b := NativeCandidates(cfg, live, nil, r.clock.Now()), NativeCandidates(cfg, observed, nil, r.clock.Now())
		same := len(a) == len(b)
		if same {
			for i := range a {
				same = same && a[i].Job.ID == b[i].Job.ID
			}
		}
		if !same {
			slog.Warn("GitHub cache shadow candidate mismatch", "live_count", len(a), "cached_count", len(b))
		}
		return live, nil
	default:
		return NativeCatalog{}, c.ErrUnsupported
	}
}
func (r *cachedNativeReader) project(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	out := NativeCatalog{Reports: map[string]c.Message{}, Cache: &NativeCacheCoverage{OutOfScope: map[c.GoalRef]bool{}, Registered: map[c.GoalRef]int{}, Ready: map[string]bool{}, FreshUntil: map[string]time.Time{}, Reasons: map[string]string{}}}
	var objects []c.GitHubCoverage
	cursor := ""
	epoch := int64(0)
	seen := map[string]bool{}
	for {
		page, e := r.cache.GitHubCatalog(ctx, c.GitHubCatalogQuery{Engagement: cfg.Master.Engagement, Manifest: out.Cache.Manifest, After: cursor})
		if e != nil {
			return out, e
		}
		if page.Version != 1 || page.MasterID != cfg.Master.MasterID || page.RestoreEpoch < 1 || page.Manifest == "" {
			return out, c.ErrConflict
		}
		if out.Cache.Manifest == "" {
			out.Cache.Manifest = page.Manifest
			out.Goals = page.Goals
			epoch = page.RestoreEpoch
		} else if page.Manifest != out.Cache.Manifest || page.RestoreEpoch != epoch || !reflect.DeepEqual(page.Goals, out.Goals) {
			return out, c.ErrConflict
		}
		objects = append(objects, page.Objects...)
		if len(objects) > MaxConnection {
			return out, c.ErrCapacity
		}
		if page.Next == "" {
			break
		}
		if seen[page.Next] || page.Next == cursor {
			return out, c.ErrConflict
		}
		seen[page.Next] = true
		cursor = page.Next
	}
	authorities := map[c.GoalRef]c.GoalAuthority{}
	for _, a := range out.Goals {
		if !a.Valid || a.State != "approved" || a.ActiveRevisionSHA256 != a.Revision.Ref.RevisionSHA256 || a.Revision.Ref.MasterID != cfg.Master.MasterID {
			return out, c.ErrConflict
		}
		authorities[a.Revision.Ref] = a
		for _, repo := range a.Revision.Proposal.Repositories {
			owner, _, _ := strings.Cut(repo.Name, "/")
			if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo.Name) {
				out.Cache.OutOfScope[a.Revision.Ref] = true
			}
		}
	}
	if err := loadCatalogHandoffs(ctx, m, &out); err != nil {
		return out, err
	}
	tasks := map[string]NativeTask{}
	registered := map[string]bool{}
	unknown := map[string]bool{}
	total := 0
	decode := func(v c.GitHubCoverage, kind string, value any) error {
		if v.Dirty || !v.DueAt.After(r.clock.Now()) {
			return errors.New("cache_stale")
		}
		for _, d := range v.Datasets {
			if d.Kind != kind {
				continue
			}
			if d.Version != 1 || d.Object != v.Object || d.HeaderRevision != v.Header.Revision || !d.DueAt.After(r.clock.Now()) {
				return errors.New("cache_stale")
			}
			until := v.DueAt
			if d.DueAt.Before(until) {
				until = d.DueAt
			}
			if prior := out.Cache.FreshUntil[v.Object.TaskURL]; prior.IsZero() || until.Before(prior) {
				out.Cache.FreshUntil[v.Object.TaskURL] = until
			}
			key := d.Object.RouteID + "\x00" + d.SHA256
			raw, ok := r.payloads[key]
			if !ok {
				var e error
				raw, e = readGitHubDataset(ctx, r.cache, d, out.Cache.Manifest)
				if e != nil {
					return e
				}
				if r.payloadBytes+len(raw) > MaxSnapshot {
					r.payloads = map[string][]byte{}
					r.payloadBytes = 0
				}
				r.payloads[key] = raw
				r.payloadBytes += len(raw)
			}
			total += len(raw)
			if total > MaxSnapshot {
				return c.ErrSize
			}
			return strictJSON(raw, value)
		}
		return errors.New("cache_unknown")
	}
	for _, v := range objects {
		if _, ok := authorities[v.Object.Goal]; !ok {
			return out, c.ErrConflict
		}
		if v.Object.Kind != "task" {
			continue
		}
		if registered[v.Object.URL] {
			return out, c.ErrConflict
		}
		registered[v.Object.URL] = true
		out.Cache.Registered[v.Object.Goal]++
		if out.Cache.OutOfScope[v.Object.Goal] {
			continue
		}
		var task NativeTask
		e := decode(v, "task", &task)
		if e == nil {
			repo, _, err := parseRef(v.Object.URL)
			if err != nil || task.Task.URL != v.Object.URL || task.Task.Repo != repo || task.Binding.Goal != v.Object.Goal || NativeAssignmentSHA(task.Binding) != v.Object.AssignmentSHA || task.Binding.TaskScopeSHA != task.Task.BodySHA || TaskScopeHash(task.Task.Title, task.Task.Body) != task.Task.BodySHA || validateNativeRoster(cfg, task.Binding) != nil || task.Task.Closed != (v.Header.State == "closed") || task.Task.Completed != v.Header.Completed {
				e = c.ErrConflict
			}
		}
		if e != nil {
			out.Cache.Reasons[v.Object.URL] = "cache_task_unknown"
			unknown[v.Object.URL] = true
			continue
		}
		task.PRs = nil
		tasks[v.Object.URL] = task
	}
	for _, v := range objects {
		if v.Object.Kind != "pull_request" {
			continue
		}
		task, ok := tasks[v.Object.TaskURL]
		if !ok {
			continue
		}
		followup := false
		for _, h := range out.Handoffs {
			if h.Input.Kind == "first-postmerge-qa" && h.Input.Goal == task.Binding.Goal && h.Input.TaskURL == task.Task.URL && h.Input.AssignmentSHA == NativeAssignmentSHA(task.Binding) && h.Input.PRURL == v.Object.URL {
				followup = true
			}
		}
		if task.Task.Closed && !followup {
			continue
		}
		if task.Binding.Goal == v.Object.Goal && (NativeAssignmentSHA(task.Binding) != v.Object.AssignmentSHA || task.Binding.ApprovalSequence < authorities[v.Object.Goal].ApprovalSequence) {
			// Registrations survive same-revision reapproval/rebinding. The live
			// catalog skips those superseded PRs without hydrating their bodies.
			continue
		}
		var p PR
		e := decode(v, "pr", &p)
		if e == nil && (p.URL != v.Object.URL || p.TaskURL != task.Task.URL || p.MasterGoal == nil || *p.MasterGoal != task.Binding.Goal || p.AssignmentSHA != NativeAssignmentSHA(task.Binding) || p.HeadSHA != v.Header.HeadSHA || p.BaseSHA != v.Header.BaseSHA || p.ScopeRevision != v.Header.ScopeRevision || p.Author.ID != task.Binding.AuthorID || p.BaseBranch != task.Binding.BaseBranch || !p.ReviewsComplete || !p.ContributorsComplete) {
			e = c.ErrConflict
		}
		if e != nil {
			unknown[task.Task.URL] = true
			out.Cache.Reasons[task.Task.URL] = "cache_pr_unknown"
			continue
		}
		if p.Merged && firstPostMergeQAHandoff(out, task, authorities[v.Object.Goal], p) != nil {
			live := nativeActionClient(r.live)
			if live == nil {
				e = c.ErrUnsupported
			} else {
				p, e = live.ReadNativeReleasePR(ctx, cfg, task, authorities[v.Object.Goal], p.URL)
			}
			if e != nil {
				unknown[task.Task.URL] = true
				out.Cache.Reasons[task.Task.URL] = "cache_postmerge_artifact_unknown"
				continue
			}
		}
		task.PRs = append(task.PRs, p)
		tasks[task.Task.URL] = task
	}
	for url, t := range tasks {
		if unknown[url] {
			continue
		}
		for _, p := range t.PRs {
			for _, review := range p.Reviews {
				if review.Author.ID != t.Binding.QAID || review.HeadSHA != p.HeadSHA {
					continue
				}
				var qa NativeQAResult
				if nativeMetadata(review.Body, "qa", &qa) != nil || qa.Goal != t.Binding.Goal || qa.AssignmentSHA != NativeAssignmentSHA(t.Binding) {
					continue
				}
				report, e := m.Message(ctx, qa.EvidenceMessageID)
				if e != nil {
					unknown[url] = true
					out.Cache.Reasons[url] = "cache_evidence_unknown"
					continue
				}
				out.Reports[report.ID] = report
			}
		}
		if !unknown[url] {
			out.Tasks = append(out.Tasks, t)
			out.Cache.Ready[url] = true
		}
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Task.URL < out.Tasks[j].Task.URL })
	out.ReadAt = r.clock.Now()
	out.Complete = true
	return out, nil
}
func nativeCacheTaskReady(catalog NativeCatalog, url string, now time.Time) bool {
	return catalog.Cache == nil || catalog.Cache.Ready[url] && catalog.Cache.FreshUntil[url].After(now)
}
