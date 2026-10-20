package master

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"slices"
)

type GitHubCacheConfig struct {
	Modes  map[string]string  `toml:"modes" json:"modes"`
	Routes []GitHubCacheRoute `toml:"routes" json:"routes"`
}
type GitHubCacheRoute struct {
	ID             string   `toml:"id" json:"id"`
	Engagement     string   `toml:"engagement" json:"engagement"`
	Collector      string   `toml:"collector" json:"collector"`
	Host           string   `toml:"host" json:"host"`
	AppID          int64    `toml:"app_id" json:"app_id"`
	InstallationID int64    `toml:"installation_id" json:"installation_id"`
	Repositories   []string `toml:"repositories" json:"repositories"`
	Threads        []string `toml:"threads" json:"threads"`
	Readers        []string `toml:"readers" json:"readers"`
}

func (cfg GitHubCacheConfig) mode(e string) string {
	if v := cfg.Modes[e]; v != "" {
		return v
	}
	return "off"
}
func validateGitHubCacheConfig(cfg GitHubCacheConfig, principals []c.Principal) error {
	for e, m := range cfg.Modes {
		if !validID(e) || (m != "off" && m != "shadow" && m != "on") {
			return c.ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, r := range cfg.Routes {
		if !validID(r.ID) || seen[r.ID] || !validID(r.Engagement) || r.Host != "github.com" || r.AppID < 1 || r.InstallationID < 1 || len(r.Repositories) == 0 || len(r.Threads) == 0 || len(r.Readers) == 0 {
			return c.ErrInvalid
		}
		seen[r.ID] = true
		for _, repo := range r.Repositories {
			if !repositoryName.MatchString(repo) {
				return c.ErrInvalid
			}
		}
		for _, id := range append(append([]string{}, r.Readers...), r.Collector) {
			found := false
			for _, p := range principals {
				if p.ID == id && slices.Contains(p.Engagements, r.Engagement) && (id != r.Collector || p.Kind == "agent") {
					found = true
				}
			}
			if !found {
				return c.ErrInvalid
			}
		}
		for _, th := range r.Threads {
			if !validID(th) {
				return c.ErrInvalid
			}
		}
		for _, prior := range cfg.Routes {
			if prior.ID == r.ID {
				break
			}
			if prior.Engagement == r.Engagement {
				for _, repo := range prior.Repositories {
					if slices.Contains(r.Repositories, repo) {
						return c.ErrInvalid
					}
				}
			}
		}
	}
	return nil
}
