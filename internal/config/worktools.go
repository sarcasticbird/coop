package config

import (
	"errors"
	"net/url"
	"regexp"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// WorkTools contains routing pins only. Native provider paths and secrets are
// read by the receiver from its existing private master.toml.
type WorkTools struct {
	MasterID     string `toml:"master_id"`
	ProjectUID   string `toml:"project_uid"`
	HostName     string `toml:"host_name"`
	TeamIssueUID string `toml:"team_issue_uid"`
	HistoryURL   string `toml:"history_url"`
}

func LoadWorkTools() (WorkTools, error) {
	var cfg WorkTools
	raw, err := masterclient.ReadPrivateConfig(masterclient.DefaultConfigPath("work-tools.toml"))
	if err != nil {
		return cfg, errors.New("configure private work-tools.toml in the Coop profile before opening Tasks or Chat")
	}
	md, err := toml.Decode(string(raw), &cfg)
	if err != nil || len(md.Undecoded()) > 0 {
		return WorkTools{}, errors.New("invalid work tools configuration")
	}
	if err := cfg.Validate(); err != nil {
		return WorkTools{}, err
	}
	return cfg, nil
}

func (cfg WorkTools) Validate() error {
	issue := cfg.TeamIssueUID
	if issue == "" {
		issue = cfg.ProjectUID
	}
	_, err := c.KataTaskReference(cfg.ProjectUID, issue, "validation/repository")
	if err != nil || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`).MatchString(cfg.MasterID) || !regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`).MatchString(cfg.HostName) {
		return errors.New("work tools require pinned master, project and host identities")
	}
	if cfg.HistoryURL != "" {
		u, err := url.Parse(cfg.HistoryURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return errors.New("invalid historical inspection URL")
		}
	}
	return nil
}
