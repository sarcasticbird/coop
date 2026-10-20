package master

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
	"slices"
)

func cachePost[In, Out any](fn func(context.Context, c.Principal, In) (Out, error)) func(http.ResponseWriter, *http.Request, c.Principal) (any, error) {
	return func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in In
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		return fn(r.Context(), p, in)
	}
}
func cacheWrite[In any](fn func(context.Context, c.Principal, In) error) func(http.ResponseWriter, *http.Request, c.Principal) (any, error) {
	return cachePost(func(ctx context.Context, p c.Principal, in In) (map[string]bool, error) {
		e := fn(ctx, p, in)
		return map[string]bool{"ok": e == nil}, e
	})
}
func (svc *Service) GitHubCacheCapabilities(ctx context.Context, actor c.Principal, eng string) (c.GitHubCacheCapabilities, error) {
	p, e := authorize(ctx, svc.Store.db, actor, "read")
	if e != nil {
		return c.GitHubCacheCapabilities{}, e
	}
	if !slices.Contains(p.Engagements, eng) {
		return c.GitHubCacheCapabilities{}, c.ErrForbidden
	}
	cfg, e := cacheConfig(ctx, svc.Store.db)
	return c.GitHubCacheCapabilities{Version: 1, Mode: cfg.mode(eng)}, e
}
func registerGitHubCache(mux *http.ServeMux, svc *Service, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	root := "/api/v1/github-cache/"
	mux.HandleFunc("GET "+root+"capabilities", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return svc.GitHubCacheCapabilities(r.Context(), p, r.URL.Query().Get("engagement"))
	}))
	mux.HandleFunc("GET "+root+"status", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return svc.GitHubCacheStatus(r.Context(), p, r.URL.Query().Get("engagement"))
	}))
	mux.HandleFunc("POST "+root+"catalog", wrap(cachePost(svc.GitHubCatalog)))
	mux.HandleFunc("POST "+root+"dataset", wrap(cachePost(svc.GitHubDataset)))
	mux.HandleFunc("POST "+root+"claim", wrap(cachePost(svc.ClaimGitHubRefresh)))
	mux.HandleFunc("POST "+root+"upload", wrap(cacheWrite(svc.UploadGitHubDataset)))
	mux.HandleFunc("POST "+root+"commit", wrap(cacheWrite(svc.CommitGitHubRefresh)))
	mux.HandleFunc("POST "+root+"fail", wrap(cacheWrite(svc.FailGitHubRefresh)))
	mux.HandleFunc("POST "+root+"dirty", wrap(cacheWrite(svc.DirtyGitHubObject)))
}
