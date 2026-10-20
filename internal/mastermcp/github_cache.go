package mastermcp

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func registerGitHubCacheStatus(server *mcp.Server, service *master.Service, p c.Principal) {
	bind(server, service.Store, "coop_master_github_cache_status", "Read scoped GitHub cache freshness. Does not contact GitHub, wake agents or authorize actions.", true, func(ctx context.Context, in struct {
		Engagement string `json:"engagement"`
	}) (any, error) {
		return service.GitHubCacheStatus(ctx, p, in.Engagement)
	})
}
