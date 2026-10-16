package worker

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Comment struct {
	ID, NodeID, Body      string
	URL, RootID, ParentID string
	ParentAuthor          Actor
	Author                Actor
	CreatedAt, EditedAt   time.Time
}
type taskMetadata struct {
	Goal      string   `json:"goal"`
	ScopeSHA  string   `json:"scope_sha256"`
	DependsOn []string `json:"depends_on"`
}
type prMetadata struct {
	Task string `json:"task"`
	Goal string `json:"goal"`
}

func metadata(body, kind string, value any) error {
	prefix := "<!-- coop-" + kind + ":v1 "
	start := strings.Index(body, prefix)
	if start < 0 || strings.Count(body, prefix) != 1 {
		return errors.New("missing or duplicate worker metadata")
	}
	tail := body[start+len(prefix):]
	end := strings.Index(tail, " -->")
	if end < 0 || end > 32<<10 {
		return errors.New("invalid worker metadata")
	}
	return strictJSON([]byte(tail[:end]), value)
}
func ParseGoalMetadata(body string) ([]string, error) {
	var m struct {
		Repositories []string `json:"repositories"`
	}
	if err := metadata(body, "goal", &m); err != nil {
		return nil, err
	}
	if len(m.Repositories) == 0 || len(m.Repositories) > 64 {
		return nil, errors.New("invalid goal repository scope")
	}
	seen := map[string]bool{}
	for _, r := range m.Repositories {
		key := strings.ToLower(r)
		if !validRepo(r) || seen[key] {
			return nil, errors.New("invalid goal repository scope")
		}
		seen[key] = true
	}
	return m.Repositories, nil
}
func commentTime(c Comment) time.Time {
	if c.EditedAt.After(c.CreatedAt) {
		return c.EditedAt
	}
	return c.CreatedAt
}
func ApplyTaskMetadata(cfg Config, t *Task, comments []Comment) error {
	if cfg.Version == 2 {
		return applyTeamTaskMetadata(cfg, t, comments)
	}
	t.MetadataAuthor = Actor{}
	t.GoalURL = ""
	t.MetadataScopeSHA = ""
	t.DependsOn = nil
	t.MetadataUpdatedAt = time.Time{}
	sorted := append([]Comment(nil), comments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := commentTime(sorted[i]), commentTime(sorted[j])
		if a.Equal(b) {
			return sorted[i].ID < sorted[j].ID
		}
		return a.Before(b)
	})
	found := false
	for _, c := range sorted {
		if (!sameActor(c.Author, cfg.Owner) && !sameActor(c.Author, cfg.Reviewer)) || !strings.Contains(c.Body, "<!-- coop-task:v1 ") {
			continue
		}
		var m taskMetadata
		if err := metadata(c.Body, "task", &m); err != nil {
			return err
		}
		if !validGitHubURL(m.Goal) || !hexSHA.MatchString(m.ScopeSHA) || len(m.DependsOn) > 100 {
			return errors.New("invalid task scope metadata")
		}
		for _, d := range m.DependsOn {
			if !validGitHubURL(d) || d == t.URL {
				return errors.New("invalid task dependency")
			}
		}
		t.MetadataAuthor = c.Author
		t.GoalURL = m.Goal
		t.MetadataScopeSHA = m.ScopeSHA
		t.DependsOn = m.DependsOn
		t.MetadataUpdatedAt = commentTime(c)
		found = true
	}
	if !found || t.MetadataScopeSHA != t.BodySHA {
		return errors.New("task metadata missing or stale")
	}
	return nil
}
func parsePRMetadata(body string) (prMetadata, error) {
	var m prMetadata
	err := metadata(body, "pr", &m)
	if err == nil && (!validGitHubURL(m.Task) || !validGitHubURL(m.Goal)) {
		err = errors.New("invalid PR metadata")
	}
	return m, err
}
func marker(j Job, phase string) string {
	return "<!-- coop-worker:v1 job=" + j.ID + " phase=" + phase + " -->"
}

var workerMarker = regexp.MustCompile(`<!-- coop-worker:v1 job=([a-f0-9]{64}) phase=([a-z-]+) -->`)

func claimFromComment(c Comment) *Claim {
	m := workerMarker.FindStringSubmatch(c.Body)
	if len(m) != 3 || m[2] != "claim" {
		return nil
	}
	var data struct {
		Instance string `json:"instance"`
	}
	if strings.Contains(c.Body, "<!-- coop-claim:v2 ") {
		var v2 struct {
			Instance string     `json:"instance"`
			Binding  JobBinding `json:"binding"`
		}
		if teamMetadata(c.Body, "claim", &v2) != nil || !hexSHA.MatchString(v2.Instance) || v2.Binding.Version != 2 || !hexSHA.MatchString(v2.Binding.AssignmentSHA) || v2.Binding.ExecutorID != c.Author.ID || (v2.Binding.Role != "implementer" && v2.Binding.Role != "engineering-review") {
			return nil
		}
		return &Claim{ID: c.ID, Marker: m[0], InstanceID: v2.Instance, Author: c.Author, Binding: &v2.Binding}
	}
	if metadata(c.Body, "claim", &data) != nil || !hexSHA.MatchString(data.Instance) {
		return nil
	}
	return &Claim{ID: c.ID, Marker: m[0], InstanceID: data.Instance, Author: c.Author}
}
func encodeMetadata(kind string, value any) string {
	raw, _ := json.Marshal(value)
	return "<!-- coop-" + kind + ":v1 " + string(raw) + " -->"
}
