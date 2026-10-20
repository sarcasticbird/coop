package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"log/slog"
	"time"
)

func enqueueGitHubDirty(ctx context.Context, cfg Config, client GitHubCacheClient, job Job, requestID string) error {
	manifest, cursor := "", ""
	seen := map[string]bool{}
	for {
		page, err := client.GitHubCatalog(ctx, c.GitHubCatalogQuery{Engagement: cfg.Master.Engagement, Manifest: manifest, After: cursor})
		if err != nil {
			return err
		}
		if manifest != "" && manifest != page.Manifest {
			return c.ErrConflict
		}
		manifest = page.Manifest
		for _, v := range page.Objects {
			if v.Object.RouteID == "" || v.Object.TaskURL != job.TaskURL {
				continue
			}
			if err = client.DirtyGitHubObject(ctx, c.GitHubDirty{Object: v.Object, RequestID: "hint_" + BodyHash(job.ID+"\x00"+requestID+"\x00"+v.Object.URL)}); err != nil {
				return err
			}
		}
		if page.Next == "" {
			return nil
		}
		if seen[page.Next] {
			return c.ErrConflict
		}
		seen[page.Next] = true
		cursor = page.Next
	}
}
func notifyGitHubCacheAction(ctx context.Context, cfg Config, master NativeEngineeringMaster, jobID, requestID string) {
	if cfg.GitHubCache == nil || !cfg.GitHubCache.Enabled {
		return
	}
	client, ok := master.(GitHubCacheClient)
	if !ok {
		return
	}
	x, err := loadNativeExecution(cfg, jobID)
	if err == nil {
		hintCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		err = enqueueGitHubDirty(hintCtx, cfg, client, x.Job, requestID)
	}
	if err != nil {
		slog.Warn("completed native action cache hint unavailable", "category", errorCategory(err))
	}
}
