package worker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type WorkReason struct {
	TaskURL string `json:"task"`
	Reason  string `json:"reason"`
}
type WorkInspection struct {
	Jobs    []Job        `json:"jobs"`
	Blocked []WorkReason `json:"blocked"`
}

func InspectWork(c Config, s Snapshot, state State, now time.Time) WorkInspection {
	result := WorkInspection{Jobs: Select(c, s, state, now), Blocked: []WorkReason{}}
	if result.Jobs == nil {
		result.Jobs = []Job{}
	}
	for _, task := range s.Tasks {
		if task.Completed {
			continue
		}
		j := Job{Kind: "implement", GoalURL: task.GoalURL, TaskURL: task.URL}
		if allowed, reason := Admit(c, j, s, now); !allowed {
			result.Blocked = append(result.Blocked, WorkReason{TaskURL: task.URL, Reason: reason})
		}
	}
	for _, p := range s.PRs {
		if !p.Closed && !p.Merged && sameActor(p.Author, c.Implementer) && missingPRMetadata(p) {
			result.Blocked = append(result.Blocked, WorkReason{TaskURL: p.URL, Reason: "PR missing task/goal metadata"})
		}
	}
	return result
}
func TaskMetadataComment(c Config, s Snapshot, taskURL, goalURL string, now time.Time) (string, error) {
	if !s.Complete || c.Role != "planner" || !validGitHubURL(taskURL) || !validGitHubURL(goalURL) {
		return "", errors.New("invalid task metadata request")
	}
	t, ok := taskFor(s, taskURL)
	if !ok {
		return "", errors.New("task is not readable in configured scope")
	}
	g, ok := goalFor(s, goalURL)
	if !ok || EvaluateGoal(c, g, now).State != "approved" || !containsFold(g.Repositories, t.Repo) {
		return "", errors.New("task needs an approved in-scope goal")
	}
	deps := append([]string{}, t.DependsOn...)
	return encodeMetadata("task", taskMetadata{Goal: goalURL, ScopeSHA: TaskScopeHash(t.Title, t.Body), DependsOn: deps}), nil
}
func (c *OpenCodeClient) Preflight(ctx context.Context, cfg Config) error {
	request, err := http.NewRequestWithContext(ctx, "GET", c.config.BaseURL+"/global/health", nil)
	if err != nil {
		return &OpenCodeError{Category: "invalid_target"}
	}
	response, err := c.http.Do(request)
	if err != nil {
		return &OpenCodeError{Category: "transport_failed"}
	}
	_ = response.Body.Close() // No response data is consumed by this auth probe.
	if response.StatusCode != http.StatusUnauthorized {
		return &OpenCodeError{Category: "server_auth_not_enforced"}
	}
	if _, err := c.Health(ctx); err != nil {
		return err
	}
	var schema struct{ Paths map[string]map[string]any }
	if err := c.request(ctx, "GET", "/doc", "", nil, &schema); err != nil {
		return err
	}
	for _, path := range []string{"/session", "/session/status", "/session/{sessionID}/prompt_async", "/session/{sessionID}/message/{messageID}", "/session/{sessionID}/abort", "/permission", "/question", "/agent"} {
		if len(schema.Paths[path]) == 0 {
			return &OpenCodeError{Category: "incompatible_server_API"}
		}
	}
	var providers struct {
		Providers []struct {
			ID     string
			Models map[string]any
		}
	}
	// /provider includes the entire upstream model catalog (about 7 MiB).
	// /config/providers lists only runtime-loaded providers and their models.
	if err := c.request(ctx, "GET", "/config/providers", "", nil, &providers); err != nil {
		return err
	}
	model := strings.SplitN(cfg.Model, "/", 2)
	if len(model) != 2 {
		return &OpenCodeError{Category: "invalid_model"}
	}
	available := false
	for _, p := range providers.Providers {
		if p.ID == model[0] {
			_, available = p.Models[model[1]]
		}
	}
	if !available {
		return &OpenCodeError{Category: "configured_model_unavailable"}
	}
	_, err = c.agentRules(ctx, cfg.WorkRoot)
	return err
}
