package worker

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"log/slog"
	"time"
)

type GitHubCacheClient interface {
	GitHubCacheCapabilities(context.Context, string) (c.GitHubCacheCapabilities, error)
	GitHubCatalog(context.Context, c.GitHubCatalogQuery) (c.GitHubCatalogPage, error)
	GitHubDataset(context.Context, c.GitHubDatasetQuery) (c.GitHubDatasetChunk, error)
	ClaimGitHubRefresh(context.Context, c.GitHubClaim) (c.GitHubLease, error)
	UploadGitHubDataset(context.Context, c.GitHubUpload) error
	CommitGitHubRefresh(context.Context, c.GitHubCommit) error
	FailGitHubRefresh(context.Context, c.GitHubFailure) error
	DirtyGitHubObject(context.Context, c.GitHubDirty) error
}
type GitHubCacheWorkerConfig struct {
	Enabled       bool     `json:"enabled"`
	CollectRoutes []string `json:"collect_routes,omitempty"`
}

func validateGitHubCacheWorker(cfg Config) error {
	if cfg.GitHubCache == nil {
		return nil
	}
	if cfg.Version != 3 {
		return errors.New("GitHub cache requires a native master worker")
	}
	if !cfg.GitHubCache.Enabled && len(cfg.GitHubCache.CollectRoutes) > 0 {
		return errors.New("collector requires enabled cache")
	}
	seen := map[string]bool{}
	for _, r := range cfg.GitHubCache.CollectRoutes {
		if r == "" || len(r) > 128 || seen[r] {
			return errors.New("invalid cache collector route")
		}
		seen[r] = true
	}
	return nil
}

type GitHubCollector struct {
	currentRoute string
	retry        map[string]time.Time
	config       Config
	client       GitHubCacheClient
	source       GitHubObservationSource
	clock        Clock
	step         chan error
	cancel       context.CancelFunc
	next         time.Time
	route        int
	closed       bool
}

func NewGitHubCollector(cfg Config, client GitHubCacheClient, source GitHubObservationSource, clock Clock) *GitHubCollector {
	if clock == nil {
		clock = RealClock{}
	}
	return &GitHubCollector{config: cfg, client: client, source: source, clock: clock, retry: map[string]time.Time{}}
}
func (g *GitHubCollector) Tick(ctx context.Context) {
	if g.closed || g.config.GitHubCache == nil || !g.config.GitHubCache.Enabled || len(g.config.GitHubCache.CollectRoutes) == 0 {
		return
	}
	if g.step != nil {
		select {
		case err := <-g.step:
			g.step = nil
			g.cancel()
			g.cancel = nil
			g.next = g.clock.Now().Add(time.Second)
			if err != nil {
				retry := g.clock.Now().Add(time.Minute)
				var gh *GitHubError
				if errors.As(err, &gh) && gh.RetryAt.After(retry) {
					retry = gh.RetryAt
				}
				if cacheRouteFailure(err) {
					g.retry[g.currentRoute] = retry
				}
				slog.Warn("GitHub cache refresh waiting", "category", errorCategory(err))
			}
		default:
			return
		}
	}
	if g.clock.Now().Before(g.next) {
		return
	}
	route := ""
	for range len(g.config.GitHubCache.CollectRoutes) {
		candidate := g.config.GitHubCache.CollectRoutes[g.route%len(g.config.GitHubCache.CollectRoutes)]
		g.route++
		if !g.clock.Now().Before(g.retry[candidate]) {
			route = candidate
			break
		}
	}
	if route == "" {
		return
	}
	g.currentRoute = route
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	g.cancel = cancel
	g.step = make(chan error, 1)
	step := g.step
	go func() { step <- g.round(runCtx, route) }()
}

func cacheRouteFailure(err error) bool {
	if errors.Is(err, c.ErrUnavailable) || errors.Is(err, c.ErrForbidden) || errors.Is(err, c.ErrEpoch) || errors.Is(err, c.ErrUnsupported) {
		return true
	}
	var gh *GitHubError
	if errors.As(err, &gh) {
		switch gh.Category {
		case "rate_limited", "credentials_expired", "permission_denied", "transport_failed", "cache_credential_identity_changed", "cache_credential_identity_unavailable":
			return true
		}
	}
	return false
}
func (g *GitHubCollector) Close() {
	g.closed = true
	if g.cancel != nil {
		g.cancel()
		<-g.step
		g.step = nil
		g.cancel = nil
	}
}
func readGitHubDataset(ctx context.Context, client GitHubCacheClient, ref c.GitHubDatasetRef, manifest string) ([]byte, error) {
	if ref.Total < 1 || ref.Total > MaxSnapshot {
		return nil, c.ErrSize
	}
	raw := make([]byte, 0, ref.Total)
	for offset := 0; ; {
		page, err := client.GitHubDataset(ctx, c.GitHubDatasetQuery{Ref: ref, Manifest: manifest, Offset: offset})
		if err != nil {
			return nil, err
		}
		if page.Ref != ref || page.Offset != offset || len(page.Data) == 0 || len(page.Data) > c.GitHubChunkBytes || len(raw)+len(page.Data) > ref.Total {
			return nil, c.ErrConflict
		}
		raw = append(raw, page.Data...)
		if page.Next == 0 {
			break
		}
		if page.Next != len(raw) {
			return nil, c.ErrConflict
		}
		offset = page.Next
	}
	if len(raw) != ref.Total || c.GitHubDigest(raw) != ref.SHA256 {
		return nil, c.ErrConflict
	}
	return raw, nil
}
func (g *GitHubCollector) round(ctx context.Context, route string) error {
	cap, err := g.client.GitHubCacheCapabilities(ctx, g.config.Master.Engagement)
	if err != nil {
		return err
	}
	if cap.Mode == "off" {
		return nil
	}
	if cap.Version != 1 {
		return c.ErrUnsupported
	}
	lease, err := g.client.ClaimGitHubRefresh(ctx, c.GitHubClaim{RouteID: route, RequestID: "refresh_" + randomID()})
	if err != nil || lease.ID == "" {
		return err
	}
	if lease.Object.RouteID != route || lease.RestoreEpoch < 1 || lease.Collector.AgentID != g.config.Master.AgentID || lease.Collector.InstanceID != g.config.Master.InstanceID || lease.Collector.Generation != g.config.Master.Generation {
		return c.ErrConflict
	}
	fail := func(problem error) error {
		retry := g.clock.Now().Add(time.Minute)
		category := "refresh_failed"
		var gh *GitHubError
		if errors.As(problem, &gh) {
			category = gh.Category
			if gh.RetryAt.After(retry) {
				retry = gh.RetryAt
			}
		}
		if errors.Is(problem, c.ErrCapacity) || errors.Is(problem, c.ErrSize) {
			category = "capacity_exceeded"
			retry = g.clock.Now().Add(24 * time.Hour)
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := g.client.FailGitHubRefresh(cleanup, c.GitHubFailure{LeaseID: lease.ID, Category: category, RetryAt: retry}); e != nil {
			slog.Warn("GitHub cache failure checkpoint unavailable", "category", errorCategory(e))
		}
		return problem
	}
	var prior []c.GitHubDatasetChunk
	for _, ref := range lease.Retained {
		raw, e := readGitHubDataset(ctx, g.client, ref, lease.Manifest)
		if e != nil {
			return fail(e)
		}
		prior = append(prior, c.GitHubDatasetChunk{Ref: ref, Data: raw})
	}
	// Leave time inside the server's five-minute lease to publish/fail safely.
	readDeadline := lease.ExpiresAt.Add(-15 * time.Second)
	readCtx, cancel := context.WithDeadline(ctx, readDeadline)
	observation, err := g.source.Observe(readCtx, g.config, lease, prior)
	cancel()
	if err != nil {
		return fail(err)
	}
	if observation.ProviderRequests > 256 || observation.Commit.LeaseID != lease.ID {
		return fail(c.ErrCapacity)
	}
	total := 0
	for kind, raw := range observation.Payloads {
		total += len(raw)
		if total > MaxSnapshot || !c.ValidGitHubPayload(raw) {
			return fail(c.ErrCapacity)
		}
		sha := c.GitHubDigest(raw)
		for offset := 0; offset < len(raw); offset += c.GitHubChunkBytes {
			end := min(offset+c.GitHubChunkBytes, len(raw))
			err = g.client.UploadGitHubDataset(ctx, c.GitHubUpload{LeaseID: lease.ID, Kind: kind, Offset: offset, Total: len(raw), SHA256: sha, Data: raw[offset:end]})
			if err != nil {
				return fail(err)
			}
		}
	}
	if err = g.client.CommitGitHubRefresh(ctx, observation.Commit); err != nil {
		return fail(err)
	}
	return nil
}
