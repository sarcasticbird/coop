package worker

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type QAResult struct {
	Version             int      `json:"version"`
	Conclusion          string   `json:"conclusion"`
	Repo                string   `json:"repo"`
	PRURL               string   `json:"pr_url"`
	TaskURL             string   `json:"task_url"`
	GoalURL             string   `json:"goal_url"`
	HeadSHA             string   `json:"head_sha"`
	AssignmentSHA       string   `json:"assignment_sha"`
	AcceptanceSHA       string   `json:"acceptance_sha"`
	Environment         string   `json:"environment"`
	EnvironmentRevision string   `json:"environment_revision"`
	ArtifactSHA256      string   `json:"artifact_sha256"`
	Evidence            []string `json:"evidence"`
	Limitations         []string `json:"limitations"`
}

var qaHeadSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func canonicalQAReference(ref, repo, kind string) bool {
	if ref != strings.ToLower(ref) || !referenceInRepo(ref, repo, kind) {
		return false
	}
	parts := strings.Split(ref, "/")
	n, err := strconv.ParseUint(parts[len(parts)-1], 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == parts[len(parts)-1]
}

func validQAResult(r QAResult) bool {
	if r.Version != 1 || (r.Conclusion != "pass" && r.Conclusion != "changes-needed" && r.Conclusion != "blocked") || r.Repo != strings.ToLower(r.Repo) || !validRepo(r.Repo) {
		return false
	}
	goal := strings.Split(r.GoalURL, "/")
	if len(goal) != 7 || !validRepo(strings.Join(goal[3:5], "/")) || (goal[5] != "discussions" && goal[5] != "issues") || !canonicalQAReference(r.GoalURL, strings.Join(goal[3:5], "/"), goal[5]) || !canonicalQAReference(r.PRURL, r.Repo, "pull") || !canonicalQAReference(r.TaskURL, r.Repo, "issues") {
		return false
	}
	if !qaHeadSHA.MatchString(r.HeadSHA) || !hexSHA.MatchString(r.AssignmentSHA) || !hexSHA.MatchString(r.AcceptanceSHA) || !hexSHA.MatchString(r.ArtifactSHA256) {
		return false
	}
	for _, s := range []string{r.Environment, r.EnvironmentRevision} {
		if strings.TrimSpace(s) != s || s == "" || len(s) > 2<<10 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
			return false
		}
	}
	if len(r.Evidence) == 0 || len(r.Evidence) > 32 || len(r.Limitations) > 32 {
		return false
	}
	for _, list := range [][]string{r.Evidence, r.Limitations} {
		for _, s := range list {
			if strings.TrimSpace(s) == "" || len(s) > 2<<10 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				return false
			}
		}
	}
	for _, link := range r.Evidence {
		u, err := url.Parse(link)
		if err != nil || link != strings.ToLower(link) || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.String() != link || !strings.HasPrefix(u.Path, "/"+strings.Join(goal[3:5], "/")+"/") {
			return false
		}
		for _, part := range strings.Split(u.Path, "/")[1:] {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\r\n\t") {
				return false
			}
		}
	}
	raw, err := json.Marshal(r)
	return err == nil && len(raw) <= 32<<10
}

// ParseQAResult accepts only the bounded marker, not surrounding public prose
// that could accidentally disclose private evidence or customer context.
func ParseQAResult(body string) (QAResult, error) {
	var r QAResult
	const prefix = "<!-- coop-qa:v1 "
	body = strings.TrimSpace(body)
	if strings.Count(body, "<!-- coop-qa:") != 1 || !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, " -->") {
		return r, errors.New("missing, mixed or duplicate QA result")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(body, prefix), " -->")
	if len(raw) > 32<<10 || strings.Contains(raw, "-->") || strings.Contains(raw, "<!--") || !utf8.ValidString(raw) || strictTeamJSON([]byte(raw), &r) != nil || !validQAResult(r) {
		return QAResult{}, errors.New("invalid QA result")
	}
	// encoding/json is case insensitive and accepts null for scalar fields.
	// Require the exact wire field names and non-null values as well.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &fields)
	canonical, _ := json.Marshal(r)
	var expected map[string]json.RawMessage
	_ = json.Unmarshal(canonical, &expected)
	if len(fields) != len(expected) {
		return QAResult{}, errors.New("incomplete QA result")
	}
	for key := range expected {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return QAResult{}, errors.New("invalid QA result field")
		}
	}
	return r, nil
}

func QAResultMatches(r QAResult, repo, pr, task, goal, head, assignment string, q QAPolicy, artifactSHA string) bool {
	return validQAResult(r) && q.Required && q.Reviewer != "" && hexSHA.MatchString(artifactSHA) &&
		r.Repo == repo && r.PRURL == pr && r.TaskURL == task && r.GoalURL == goal && r.HeadSHA == head && r.AssignmentSHA == assignment &&
		r.AcceptanceSHA == q.AcceptanceSHA && r.Environment == q.Environment && r.EnvironmentRevision == q.EnvironmentRevision && r.ArtifactSHA256 == artifactSHA
}

// The bool means a valid terminal result, not clearance. Only pass clears QA.
// Select before parsing so a bad or dismissed latest review cannot revive pass.
func latestTeamQA(c Config, t Task, p PR, q QAPolicy, artifactSHA string) (Review, QAResult, bool) {
	engineering, cleared := EngineeringReviewFor(c, t, p)
	if !cleared || c.Team == nil || t.Assignment == nil || !q.Required || t.Assignment.QA != q.Reviewer {
		return Review{}, QAResult{}, false
	}
	var actor Actor
	for _, member := range c.Team.Members {
		if member.Name == q.Reviewer && slices.Contains(member.Capabilities, "customer-qa") {
			actor = member.Actor
		}
	}
	if !knownTeamActor(actor) || actor.Kind != "Bot" || sameActor(actor, p.Author) || actor.ID == engineering.ActorID {
		return Review{}, QAResult{}, false
	}
	for _, contributor := range p.Contributors {
		if sameActor(actor, contributor) {
			return Review{}, QAResult{}, false
		}
	}
	var latest Review
	for _, review := range p.Reviews {
		if review.ID == "" || !sameActor(review.Author, actor) || review.HeadSHA != p.HeadSHA || !review.SubmittedAt.After(p.ScopeUpdatedAt) {
			continue
		}
		if review.SubmittedAt.After(latest.SubmittedAt) || review.SubmittedAt.Equal(latest.SubmittedAt) && reviewIDAfter(review.ID, latest.ID) {
			latest = review
		}
	}
	if !latest.LastEditedAt.IsZero() || !latest.SubmittedAt.After(engineering.SubmittedAt) {
		return latest, QAResult{}, false
	}
	r, err := ParseQAResult(latest.Body)
	state := map[string]string{"pass": "APPROVED", "changes-needed": "CHANGES_REQUESTED", "blocked": "COMMENTED"}[r.Conclusion]
	if err != nil || latest.State != state || !QAResultMatches(r, t.Repo, p.URL, t.URL, t.GoalURL, p.HeadSHA, p.AssignmentSHA, q, artifactSHA) {
		return latest, QAResult{}, false
	}
	return latest, r, true
}
