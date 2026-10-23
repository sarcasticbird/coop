package worker

import (
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
)

type MasterJobBinding struct {
	AssignmentSHA string
	ExecutorID    int64
	Role          string
	BaseBranch    string
	MasterID      string
	ThreadID      string
	AgentID       string
	DeliveryID    string
	Goal          *c.GoalRef `json:",omitempty"`
}
type MasterConfig struct {
	BlockerRecipient string `json:"blocker_recipient,omitempty"`
	Backend          string `json:"backend"`
	ClientConfig     string `json:"client_config"`
	MasterID         string `json:"master_id"`
	Engagement       string `json:"engagement"`
	AgentID          string `json:"agent_id"`
	HostID           string `json:"host_id"`
	InstanceID       string `json:"worker_instance_id"`
	Generation       int64  `json:"generation"`
}

func validateMasterConfig(c Config) error {
	m := c.Master
	if m == nil || m.Backend != "master" || !cleanPrivatePath(m.ClientConfig) || m.Generation < 1 || !hexSHA.MatchString(m.InstanceID) {
		return errors.New("v3 requires explicit pinned master placement")
	}
	for _, v := range []string{m.MasterID, m.Engagement, m.AgentID, m.HostID} {
		if v == "" || len(v) > 128 || strings.ContainsAny(v, "\x00\r\n/\\") {
			return errors.New("invalid master identity")
		}
	}
	if m.BlockerRecipient != "" && (len(m.BlockerRecipient) > 128 || strings.ContainsAny(m.BlockerRecipient, "\x00\r\n/\\")) {
		return errors.New("invalid master blocker recipient")
	}
	legacy := c
	legacy.GitHubCache = nil
	legacy.TaskSource = nil
	legacy.Version = 2
	legacy.Master = nil
	return ValidateConfig(legacy)
}
