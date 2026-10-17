package worker

import (
	"errors"
	"strings"
	"time"
)

// TaskMetadataForAuthor prepares the first planner comment from fresh authority.
// It never publishes metadata, changes a Project, or replaces an assignment.
func TaskMetadataForAuthor(c Config, s Snapshot, taskURL, goalURL, author string, dependencies []string, now time.Time) (string, error) {
	if c.Version != 2 || c.Team == nil || !isPlanner(c) || !s.Complete {
		return "", errors.New("initial author assignment requires v2 planner and complete snapshot")
	}
	t, ok := taskFor(s, taskURL)
	if !ok || !validGitHubURL(taskURL) || !referenceInRepo(taskURL, t.Repo, "issues") || t.Closed || t.Completed || t.Claim != nil || t.ClaimConflict {
		return "", errors.New("initial author assignment requires an unclaimed open task")
	}
	if t.TrustedMetadataPresent || t.Assignment != nil {
		return "", errors.New("existing assignment requires task-metadata without --author or --assignment")
	}
	if !t.AssigneesComplete || len(t.Assignees) != 0 {
		return "", errors.New("initial author assignment requires freshly verified empty GitHub assignees")
	}
	if !t.PRHistoryComplete {
		return "", errors.New("initial author assignment requires complete native PR history")
	}
	for _, p := range s.PRs {
		if strings.EqualFold(p.TaskURL, taskURL) {
			return "", errors.New("initial author assignment conflicts with an existing task PR")
		}
	}
	g, ok := goalFor(s, goalURL)
	if !ok || !validTeamGoalURL(goalURL) || EvaluateGoal(c, g, now).State != "approved" {
		return "", errors.New("initial author assignment requires a current approved goal")
	}
	m, err := ParseTeamGoalMetadata(g.Body)
	if err != nil {
		return "", err
	}
	policySHA, err := TeamPolicySHA(*c.Team)
	if err != nil {
		return "", err
	}
	if c.Team.EngineeringPeers[author] == "" {
		return "", errors.New("author is not a configured implementing member")
	}
	if err := validateInitialDependencies(s, t, dependencies); err != nil {
		return "", err
	}
	t.Assignment = &TaskAssignment{Version: 2, Author: author, Peer: c.Team.EngineeringPeers[author], QA: m.QA.Reviewer, BaseBranch: m.BaseBranches[strings.ToLower(t.Repo)], GoalBodySHA: g.BodySHA, TaskBodySHA: t.BodySHA, PolicySHA: policySHA}
	t.QA, t.GoalURL, t.MetadataScopeSHA, t.MetadataAuthor = m.QA, goalURL, t.BodySHA, c.Self
	t.DependsOn = append([]string{}, dependencies...)
	if _, err := ValidateTeamScope(c, t, g, now); err != nil {
		return "", err
	}
	s.Tasks = append([]Task(nil), s.Tasks...)
	for i := range s.Tasks {
		if s.Tasks[i].URL == taskURL {
			s.Tasks[i] = t
			break
		}
	}
	return TaskMetadataComment(c, s, taskURL, goalURL, now)
}

func validateInitialDependencies(s Snapshot, task Task, dependencies []string) error {
	if len(dependencies) > 100 {
		return errors.New("initial assignment has too many dependencies")
	}
	seen := map[string]bool{}
	for _, dep := range dependencies {
		key := strings.ToLower(dep)
		if !validGitHubURL(dep) || !referenceInRepo(dep, task.Repo, "issues") || strings.EqualFold(dep, task.URL) || seen[key] {
			return errors.New("invalid or duplicate initial dependency")
		}
		seen[key] = true
	}
	// Check the proposed reachable graph, not only its first edge. An unfinished
	// prerequisite is legal; a missing or cyclic prerequisite is not.
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(ref string) error {
		key := strings.ToLower(ref)
		if strings.EqualFold(ref, task.URL) || visiting[key] {
			return errors.New("initial assignment dependencies contain a cycle")
		}
		if visited[key] {
			return nil
		}
		dep, ok := taskFor(s, ref)
		if !ok || !validGitHubURL(ref) || !referenceInRepo(ref, task.Repo, "issues") {
			return errors.New("initial assignment dependency is not readable in the same repository")
		}
		visiting[key] = true
		for _, next := range dep.DependsOn {
			if err := visit(next); err != nil {
				return err
			}
		}
		delete(visiting, key)
		visited[key] = true
		return nil
	}
	for _, dep := range dependencies {
		if err := visit(dep); err != nil {
			return err
		}
	}
	return nil
}
