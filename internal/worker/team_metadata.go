package worker

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type TeamGoalMetadata struct {
	BaseBranches map[string]string `json:"base_branches"`
	Repositories []string          `json:"repositories"`
	QA           QAPolicy          `json:"qa"`
}

type TeamTaskMetadata struct {
	Goal       string         `json:"goal"`
	ScopeSHA   string         `json:"scope_sha256"`
	DependsOn  []string       `json:"depends_on"`
	Assignment TaskAssignment `json:"assignment"`
}

type TeamPRMetadata struct {
	Task          string `json:"task"`
	Goal          string `json:"goal"`
	AssignmentSHA string `json:"assignment_sha256"`
}

func validTeamGoalURL(ref string) bool {
	if !validGitHubURL(ref) {
		return false
	}
	parts := strings.Split(ref, "/")
	kind := parts[len(parts)-2]
	return kind == "issues" || kind == "discussions"
}

// V2 rejects duplicate object keys as well as unknown fields. The v1 decoder
// remains unchanged, including its historical wire semantics.
func strictTeamJSON(raw []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	var scan func() error
	scan = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				name = strings.ToLower(name)
				if !ok || seen[name] {
					return errors.New("duplicate JSON field")
				}
				seen[name] = true
			}
			if err := scan(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := scan(); err != nil {
		return errors.New("invalid v2 JSON document")
	}
	return strictJSON(raw, value)
}

func teamMetadata(body, kind string, value any) error {
	stem := "<!-- coop-" + kind + ":"
	prefix := stem + "v2 "
	if strings.Count(body, stem) != 1 || !strings.Contains(body, prefix) {
		return errors.New("missing, mixed or duplicate v2 metadata")
	}
	tail := strings.SplitN(body, prefix, 2)[1]
	end := strings.Index(tail, " -->")
	if end < 0 || end > 32<<10 {
		return errors.New("invalid v2 metadata")
	}
	return strictTeamJSON([]byte(tail[:end]), value)
}

func ParseTeamGoalMetadata(body string) (TeamGoalMetadata, error) {
	var m TeamGoalMetadata
	if err := teamMetadata(body, "goal", &m); err != nil {
		return m, err
	}
	if len(m.Repositories) == 0 || len(m.Repositories) > 64 {
		return m, errors.New("invalid goal repository scope")
	}
	seen := map[string]bool{}
	for _, repo := range m.Repositories {
		key := strings.ToLower(repo)
		if !validRepo(repo) || seen[key] {
			return m, errors.New("invalid or duplicate goal repository")
		}
		seen[key] = true
	}
	if len(m.BaseBranches) != len(seen) {
		return m, errors.New("goal requires an explicit base branch for every repository")
	}
	for repo, branch := range m.BaseBranches {
		if repo != strings.ToLower(repo) || !seen[repo] || !validBaseBranch(branch) {
			return m, errors.New("invalid goal base branch scope")
		}
	}
	if m.QA.Required {
		return m, errors.New("qa_not_supported")
	}
	if m.QA != (QAPolicy{}) {
		return m, errors.New("nonrequired QA must have no extra fields")
	}
	return m, nil
}

func applyTeamTaskMetadata(c Config, t *Task, comments []Comment) error {
	t.Assignment = nil
	t.MetadataAuthor, t.GoalURL, t.MetadataScopeSHA = Actor{}, "", ""
	t.DependsOn, t.MetadataUpdatedAt = nil, time.Time{}
	if c.Team == nil {
		return errors.New("missing team policy")
	}
	if err := ValidateTeamPolicy(*c.Team); err != nil {
		return err
	}
	var planner Actor
	for _, m := range c.Team.Members {
		if m.Name == c.Team.Planner {
			planner = m.Actor
		}
	}
	sorted := append([]Comment(nil), comments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := commentTime(sorted[i]), commentTime(sorted[j])
		if a.Equal(b) {
			return sorted[i].ID < sorted[j].ID
		}
		return a.Before(b)
	})
	var candidate Comment
	for _, comment := range sorted {
		if (!sameActor(comment.Author, c.Owner) && !sameActor(comment.Author, planner)) || !strings.Contains(comment.Body, "<!-- coop-task:") {
			continue
		}
		candidate = comment
	}
	// Only the latest trusted record supplies authority. Historical assignments
	// may reference an older roster; an invalid latest record never falls back.
	var latest TeamTaskMetadata
	if err := teamMetadata(candidate.Body, "task", &latest); err != nil {
		return err
	}
	if !validTeamGoalURL(latest.Goal) || !hexSHA.MatchString(latest.ScopeSHA) || len(latest.DependsOn) > 100 {
		return errors.New("invalid v2 task metadata")
	}
	seen := map[string]bool{}
	for _, dep := range latest.DependsOn {
		key := strings.ToLower(dep)
		if !referenceInRepo(dep, t.Repo, "issues") || strings.EqualFold(dep, t.URL) || seen[key] {
			return errors.New("invalid or duplicate v2 dependency")
		}
		seen[key] = true
	}
	if err := ValidateTaskAssignment(*c.Team, latest.Assignment, QAPolicy{}); err != nil {
		return err
	}
	if candidate.Author.ID == 0 || latest.ScopeSHA != t.BodySHA || latest.Assignment.TaskBodySHA != t.BodySHA || t.BodySHA != TaskScopeHash(t.Title, t.Body) || !referenceInRepo(t.URL, t.Repo, "issues") {
		return errors.New("v2 task metadata missing or stale")
	}
	t.Assignment, t.MetadataAuthor = &latest.Assignment, candidate.Author
	t.GoalURL, t.MetadataScopeSHA = latest.Goal, latest.ScopeSHA
	t.DependsOn, t.MetadataUpdatedAt = latest.DependsOn, commentTime(candidate)
	return nil
}

// ValidateTeamScope binds a fresh task snapshot to the coherent, owner-approved
// goal body. Board fields never participate in this authority decision.
func ValidateTeamScope(c Config, t Task, g Goal, now time.Time) (string, error) {
	if t.ClaimConflict {
		return "", errors.New("conflicting task claims")
	}
	if c.Version != 2 || c.Team == nil || t.Assignment == nil {
		return "", errors.New("missing v2 assignment")
	}
	if !validTeamGoalURL(g.URL) || !validRepo(t.Repo) || !containsFold(c.CodeOwners, strings.Split(t.Repo, "/")[0]) || (len(c.Repositories) > 0 && !containsFold(c.Repositories, t.Repo)) {
		return "", errors.New("assignment outside configured repository scope")
	}
	if EvaluateGoal(c, g, now).State != "approved" {
		return "", errors.New("current goal is not approved")
	}
	m, err := ParseTeamGoalMetadata(g.Body)
	if err != nil {
		return "", err
	}
	if !sameRepositories(m.Repositories, g.Repositories) || !containsFold(m.Repositories, t.Repo) || t.GoalURL != g.URL || t.Assignment.GoalBodySHA != g.BodySHA || t.Assignment.TaskBodySHA != t.BodySHA || t.MetadataScopeSHA != t.BodySHA || t.BodySHA != TaskScopeHash(t.Title, t.Body) {
		return "", errors.New("assignment scope changed")
	}
	if t.Assignment.BaseBranch != m.BaseBranches[strings.ToLower(t.Repo)] {
		return "", errors.New("assignment base branch outside approved goal")
	}
	planner := Actor{}
	for _, member := range c.Team.Members {
		if member.Name == c.Team.Planner {
			planner = member.Actor
		}
	}
	if !sameActor(t.MetadataAuthor, c.Owner) && !sameActor(t.MetadataAuthor, planner) {
		return "", errors.New("untrusted assignment author")
	}
	sha, err := AssignmentSHA(*c.Team, t.Repo, t.URL, g.URL, *t.Assignment, m.QA)
	if err != nil {
		return "", err
	}
	if t.Claim != nil {
		var author Actor
		for _, member := range c.Team.Members {
			if member.Name == t.Assignment.Author {
				author = member.Actor
			}
		}
		if t.Claim.Binding == nil || *t.Claim.Binding != (JobBinding{Version: 2, AssignmentSHA: sha, ExecutorID: author.ID, Role: "implementer"}) || !sameActor(t.Claim.Author, author) || t.Claim.Marker == "" || !hexSHA.MatchString(t.Claim.InstanceID) {
			return "", errors.New("conflicting or unbound v2 claim")
		}
	}
	return sha, nil
}

func ParseTeamPRMetadata(body, repo, taskURL, goalURL, assignmentSHA string) (TeamPRMetadata, error) {
	var m TeamPRMetadata
	if err := teamMetadata(body, "pr", &m); err != nil {
		return m, err
	}
	if !referenceInRepo(m.Task, repo, "issues") || !validTeamGoalURL(m.Goal) || !strings.EqualFold(m.Task, taskURL) || !strings.EqualFold(m.Goal, goalURL) || !hexSHA.MatchString(m.AssignmentSHA) || m.AssignmentSHA != assignmentSHA {
		return m, errors.New("PR assignment or scope mismatch")
	}
	return m, nil
}
