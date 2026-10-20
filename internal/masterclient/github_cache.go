package masterclient

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/url"
)

var errGitHubCacheRoute = errors.New("github_cache_route_missing")

func (cl *Client) cacheCall(ctx context.Context, method, path string, in, out any) error {
	err := cl.call(ctx, method, "/api/v1/github-cache/"+path, in, out)
	if errors.Is(err, errGitHubCacheRoute) {
		if _, e := cl.Self(ctx); e != nil {
			return e
		}
		return c.ErrUnsupported
	}
	return err
}
func (cl *Client) GitHubCacheCapabilities(ctx context.Context, eng string) (out c.GitHubCacheCapabilities, err error) {
	err = cl.cacheCall(ctx, "GET", "capabilities?engagement="+url.QueryEscape(eng), nil, &out)
	if err == nil && (out.Version != 1 || (out.Mode != "off" && out.Mode != "shadow" && out.Mode != "on")) {
		err = c.ErrUnsupported
	}
	return
}
func (cl *Client) GitHubCatalog(ctx context.Context, in c.GitHubCatalogQuery) (out c.GitHubCatalogPage, err error) {
	err = cl.cacheCall(ctx, "POST", "catalog", in, &out)
	return
}
func (cl *Client) GitHubDataset(ctx context.Context, in c.GitHubDatasetQuery) (out c.GitHubDatasetChunk, err error) {
	err = cl.cacheCall(ctx, "POST", "dataset", in, &out)
	if err == nil && (len(out.Data) > c.GitHubChunkBytes || out.Offset != in.Offset || out.Ref.SHA256 != in.Ref.SHA256 || out.Ref.Object != in.Ref.Object) {
		err = c.ErrConflict
	}
	return
}
func (cl *Client) ClaimGitHubRefresh(ctx context.Context, in c.GitHubClaim) (out c.GitHubLease, err error) {
	err = cl.cacheCall(ctx, "POST", "claim", in, &out)
	return
}
func (cl *Client) UploadGitHubDataset(ctx context.Context, in c.GitHubUpload) error {
	return cl.cacheCall(ctx, "POST", "upload", in, nil)
}
func (cl *Client) CommitGitHubRefresh(ctx context.Context, in c.GitHubCommit) error {
	return cl.cacheCall(ctx, "POST", "commit", in, nil)
}
func (cl *Client) FailGitHubRefresh(ctx context.Context, in c.GitHubFailure) error {
	return cl.cacheCall(ctx, "POST", "fail", in, nil)
}
func (cl *Client) DirtyGitHubObject(ctx context.Context, in c.GitHubDirty) error {
	return cl.cacheCall(ctx, "POST", "dirty", in, nil)
}
func (cl *Client) GitHubCacheStatus(ctx context.Context, eng string) (out c.GitHubCacheStatus, err error) {
	err = cl.cacheCall(ctx, "GET", "status?engagement="+url.QueryEscape(eng), nil, &out)
	return
}
