package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
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
	Current []Job        `json:"current,omitempty"`
	History []Job        `json:"history,omitempty"`
}

func InspectWork(c Config, s Snapshot, state State, now time.Time) WorkInspection {
	result := WorkInspection{Jobs: Select(c, s, state, now), Blocked: []WorkReason{}}
	if result.Jobs == nil {
		result.Jobs = []Job{}
	}
	for _, j := range state.Jobs {
		if currentSavedJob(c, s, state, j, now) {
			result.Current = append(result.Current, j)
			if j.Phase == "blocked" {
				result.Blocked = append(result.Blocked, WorkReason{TaskURL: j.TaskURL, Reason: j.WaitReason})
			}
		} else {
			result.History = append(result.History, j)
		}
	}
	sort.Slice(result.History, func(i, j int) bool { return result.History[i].ID < result.History[j].ID })
	sort.Slice(result.Current, func(i, j int) bool { return result.Current[i].ID < result.Current[j].ID })
	for _, task := range s.Tasks {
		if task.Completed || task.Closed || !task.Ready {
			continue
		}
		j := Job{Kind: "implement", GoalURL: task.GoalURL, TaskURL: task.URL}
		if c.Version == 2 {
			bound, err := bindTeamJob(c, j, s, now)
			if err != nil {
				result.Blocked = append(result.Blocked, WorkReason{TaskURL: task.URL, Reason: err.Error()})
				continue
			}
			j = bound
		}
		if allowed, reason := Admit(c, j, s, now); !allowed {
			result.Blocked = append(result.Blocked, WorkReason{TaskURL: task.URL, Reason: reason})
		}
	}
	for _, p := range s.PRs {
		if !p.Closed && !p.Merged && configuredImplementer(c, p.Author) && (missingPRMetadata(p) || c.Version == 2 && p.AssignmentSHA == "") {
			reason := "PR missing task/goal metadata"
			if c.Version == 2 {
				reason = "PR missing task/goal/assignment metadata"
			}
			result.Blocked = append(result.Blocked, WorkReason{TaskURL: p.URL, Reason: reason})
		}
	}
	return result
}
func TaskMetadataComment(c Config, s Snapshot, taskURL, goalURL string, now time.Time) (string, error) {
	if !s.Complete || !isPlanner(c) || !validGitHubURL(taskURL) || !validGitHubURL(goalURL) {
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
	if c.Version == 2 {
		if _, err := ValidateTeamScope(c, t, g, now); err != nil {
			return "", err
		}
		raw, err := json.Marshal(TeamTaskMetadata{Goal: goalURL, ScopeSHA: t.BodySHA, DependsOn: deps, Assignment: *t.Assignment})
		if err != nil {
			return "", err
		}
		return "<!-- coop-task:v2 " + string(raw) + " -->", nil
	}
	return encodeMetadata("task", taskMetadata{Goal: goalURL, ScopeSHA: TaskScopeHash(t.Title, t.Body), DependsOn: deps}), nil
}

// TaskMetadataWithAssignment generates first publication from explicit private
// input. Existing trusted assignments must use the preserving repair path.
func TaskMetadataWithAssignment(c Config, s Snapshot, taskURL, goalURL, path string, now time.Time) (string, error) {
	if c.Version != 2 || !isPlanner(c) || !s.Complete {
		return "", errors.New("initial assignment requires v2 planner and complete snapshot")
	}
	t, ok := taskFor(s, taskURL)
	if !ok || t.TrustedMetadataPresent || t.Assignment != nil || t.Claim != nil || t.ClaimConflict || t.Closed || t.Completed || !referenceInRepo(t.URL, t.Repo, "issues") {
		return "", errors.New("initial assignment requires an unassigned, unclaimed open task")
	}
	g, ok := goalFor(s, goalURL)
	if !ok {
		return "", errors.New("goal is not readable")
	}
	raw, err := readPrivate(path, 32<<10)
	if err != nil {
		return "", err
	}
	var assignment TaskAssignment
	if err = strictTeamJSON(raw, &assignment); err != nil {
		return "", err
	}
	m, err := ParseTeamGoalMetadata(g.Body)
	if err != nil {
		return "", err
	}
	t.Assignment, t.QA, t.GoalURL, t.MetadataScopeSHA, t.MetadataAuthor = &assignment, m.QA, goalURL, t.BodySHA, c.Self
	if _, err = ValidateTeamScope(c, t, g, now); err != nil {
		return "", err
	}
	for i := range s.Tasks {
		if s.Tasks[i].URL == taskURL {
			s.Tasks = append([]Task(nil), s.Tasks...)
			s.Tasks[i] = t
			break
		}
	}
	return TaskMetadataComment(c, s, taskURL, goalURL, now)
}

// PRMetadataComment repairs tracking only. It neither assigns work nor changes
// the owner-approved goal or the task's reviewable scope.
func PRMetadataComment(c Config, s Snapshot, prURL, taskURL, goalURL string, now time.Time) (string, error) {
	if c.Version != 2 || !s.Complete || !validGitHubURL(prURL) {
		return "", errors.New("invalid v2 PR metadata request")
	}
	p, ok := prFor(s, prURL)
	t, taskOK := taskFor(s, taskURL)
	g, goalOK := goalFor(s, goalURL)
	if !ok || !taskOK || !goalOK || !referenceInRepo(p.URL, t.Repo, "pull") || p.Closed || p.Merged {
		return "", errors.New("PR or task is not readable in configured scope")
	}
	a, err := resolveTeamAssignment(c, t, g, now)
	if err != nil {
		return "", err
	}
	if p.BaseBranch != t.Assignment.BaseBranch || !sameActor(p.Author, a.Author) || (!isPlanner(c) && !sameActor(c.Self, a.Author)) {
		return "", errors.New("PR metadata requires assigned author or planner")
	}
	if p.TaskURL != "" && p.TaskURL != t.URL || p.GoalURL != "" && p.GoalURL != g.URL || p.AssignmentSHA != "" && p.AssignmentSHA != a.SHA {
		return "", errors.New("PR metadata conflicts with existing tracking")
	}
	raw, err := json.Marshal(TeamPRMetadata{Task: t.URL, Goal: g.URL, AssignmentSHA: a.SHA})
	if err != nil {
		return "", err
	}
	return "<!-- coop-pr:v2 " + string(raw) + " -->", nil
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
	if _, err = c.agentRules(ctx, cfg.WorkRoot); err != nil {
		return err
	}
	return c.preflightModelVariant(ctx, cfg)
}
