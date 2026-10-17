package worker

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// InitialAssignmentSnapshot adds complete PR history only for the requested
// task. Periodic worker polling keeps its existing open-PR discovery cost.
func (g *GitHubClient) InitialAssignmentSnapshot(ctx context.Context, c Config, taskURL string) (Snapshot, error) {
	s, err := g.Snapshot(ctx, c, Snapshot{})
	if err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubRevalidationTimeout)
	defer cancel()
	return g.completeInitialPRHistory(ctx, s, taskURL)
}

type assignmentPRRecord struct {
	URL       string    `json:"html_url"`
	Body      string    `json:"body"`
	Number    int       `json:"number"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (g *GitHubClient) completeInitialPRHistory(ctx context.Context, s Snapshot, taskURL string) (Snapshot, error) {
	t, ok := taskFor(s, taskURL)
	if !ok || !s.Complete || !validRepo(t.Repo) || !referenceInRepo(taskURL, t.Repo, "issues") {
		return Snapshot{}, ghError("invalid_initial_assignment_task")
	}
	owner := strings.Split(t.Repo, "/")[0]
	read := func() ([]assignmentPRRecord, error) {
		nodes, err := g.restList(ctx, owner, "repos/"+t.Repo+"/pulls?state=all&sort=updated&direction=desc", "")
		if err != nil {
			return nil, err
		}
		rows := make([]assignmentPRRecord, 0, len(nodes))
		seen := map[string]bool{}
		for _, raw := range nodes {
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil || len(fields["body"]) == 0 {
				return nil, ghError("incomplete_initial_assignment_PR_history")
			}
			p, err := decode[assignmentPRRecord](raw)
			if err != nil || !validGitHubURL(p.URL) || p.URL != "https://github.com/"+t.Repo+"/pull/"+stringID(p.Number) || p.Number <= 0 || seen[p.URL] {
				return nil, ghError("invalid_initial_assignment_PR_history")
			}
			seen[p.URL] = true
			rows = append(rows, p)
		}
		return rows, nil
	}
	first, err := read()
	if err != nil {
		return Snapshot{}, err
	}
	second, err := read()
	if err != nil {
		return Snapshot{}, err
	}
	if !slices.Equal(first, second) {
		return Snapshot{}, ghError("initial_assignment_PR_history_changed")
	}
	s.PRs = append([]PR(nil), s.PRs...)
	for _, p := range second {
		var target string
		if strings.Contains(p.Body, "<!-- coop-pr:v2 ") {
			var m TeamPRMetadata
			if teamMetadata(p.Body, "pr", &m) == nil {
				target = m.Task
			}
		} else if m, err := parsePRMetadata(p.Body); err == nil {
			target = m.Task
		}
		if strings.EqualFold(target, taskURL) {
			s.PRs = append(s.PRs, PR{URL: p.URL, TaskURL: taskURL})
		} else if target == "" && strings.Contains(p.Body, "<!-- coop-pr:") && slices.ContainsFunc(issueURLs.FindAllString(strings.ToLower(p.Body), -1), func(ref string) bool { return strings.EqualFold(ref, taskURL) }) {
			return Snapshot{}, ghError("ambiguous_initial_assignment_PR_history")
		}
	}
	s.Tasks = append([]Task(nil), s.Tasks...)
	for i := range s.Tasks {
		if s.Tasks[i].URL == taskURL {
			s.Tasks[i].PRHistoryComplete = true
			break
		}
	}
	// The normal snapshot limit still applies after adding history evidence.
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxSnapshot {
		return Snapshot{}, ghError("snapshot_limit")
	}
	return s, nil
}
