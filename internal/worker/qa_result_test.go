package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func qaResultFixture() (QAResult, QAPolicy) {
	q := QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: BodyHash("acceptance"), Environment: "fixture", EnvironmentRevision: "env-v2"}
	return QAResult{Version: 1, Conclusion: "pass", Repo: "code/repo", PRURL: "https://github.com/code/repo/pull/2", TaskURL: "https://github.com/code/repo/issues/1", GoalURL: "https://github.com/planning/board/discussions/1", HeadSHA: strings.Repeat("a", 40), AssignmentSHA: BodyHash("assignment"), AcceptanceSHA: q.AcceptanceSHA, Environment: q.Environment, EnvironmentRevision: q.EnvironmentRevision, ArtifactSHA256: BodyHash("tested artifact"), Evidence: []string{"https://github.com/planning/board/discussions/1#discussioncomment-1"}, Limitations: []string{}}, q
}

func qaMarker(r QAResult) string {
	raw, _ := json.Marshal(r)
	return "<!-- coop-qa:v1 " + string(raw) + " -->"
}

func TestQAResultStrictParsing(t *testing.T) {
	r, _ := qaResultFixture()
	body := qaMarker(r)
	for name, mutate := range map[string]func(string) string{
		"valid":          func(s string) string { return s },
		"unknown":        func(s string) string { return strings.Replace(s, `"version":1`, `"version":1,"extra":true`, 1) },
		"duplicate":      func(s string) string { return strings.Replace(s, `"version":1`, `"version":1,"version":1`, 1) },
		"case duplicate": func(s string) string { return strings.Replace(s, `"version":1`, `"version":1,"Version":1`, 1) },
		"case field":     func(s string) string { return strings.Replace(s, `"version":1`, `"Version":1`, 1) },
		"missing":        func(s string) string { return strings.Replace(s, `,"limitations":[]`, "", 1) },
		"null":           func(s string) string { return strings.Replace(s, `"limitations":[]`, `"limitations":null`, 1) },
		"mixed":          func(s string) string { return strings.Replace(s, "coop-qa:v1", "coop-qa:v2", 1) },
		"multiple":       func(s string) string { return s + s },
		"prose":          func(s string) string { return "private customer findings\n" + s },
		"comment escape": func(s string) string {
			return strings.Replace(s, `"limitations":[]`, `"limitations":["-->private prose<!--"]`, 1)
		},
		"trailing": func(s string) string { return strings.Replace(s, " -->", " {} -->", 1) },
		"oversized": func(s string) string {
			return strings.Replace(s, `"version":1`, strings.Repeat(" ", 32<<10)+`"version":1`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseQAResult(mutate(body))
			if (err == nil) != (name == "valid") {
				t.Fatalf("parse error=%v", err)
			}
		})
	}
}

func TestQAResultBounds(t *testing.T) {
	r, _ := qaResultFixture()
	r.Limitations = []string{strings.Repeat("x", 2048)}
	for range 31 {
		r.Evidence = append(r.Evidence, r.GoalURL)
	}
	body := qaMarker(r)
	if _, err := ParseQAResult(body); err != nil {
		t.Fatalf("32 links and 2KiB limitation rejected: %v", err)
	}
	rawBytes := len(body) - len("<!-- coop-qa:v1 ") - len(" -->")
	atLimit := strings.Replace(body, " -->", strings.Repeat(" ", (32<<10)-rawBytes)+" -->", 1)
	if _, err := ParseQAResult(atLimit); err != nil {
		t.Fatalf("32KiB result rejected: %v", err)
	}
	if _, err := ParseQAResult(strings.Replace(atLimit, " -->", "  -->", 1)); err == nil {
		t.Fatal("32KiB+1 result accepted")
	}
	r.Evidence = append(r.Evidence, r.GoalURL)
	if _, err := ParseQAResult(qaMarker(r)); err == nil {
		t.Fatal("33 links accepted")
	}
}

func TestQAResultValidationAndMatching(t *testing.T) {
	for name, mutate := range map[string]func(*QAResult){
		"valid":                      func(r *QAResult) {},
		"version":                    func(r *QAResult) { r.Version = 2 },
		"conclusion":                 func(r *QAResult) { r.Conclusion = "PASS" },
		"repo case":                  func(r *QAResult) { r.Repo = "Code/repo" },
		"url case":                   func(r *QAResult) { r.PRURL = "https://github.com/Code/repo/pull/2" },
		"leading zero":               func(r *QAResult) { r.PRURL = "https://github.com/code/repo/pull/02" },
		"foreign task":               func(r *QAResult) { r.TaskURL = "https://github.com/other/repo/issues/1" },
		"head":                       func(r *QAResult) { r.HeadSHA = "head" },
		"sha case":                   func(r *QAResult) { r.AcceptanceSHA = strings.Repeat("A", 64) },
		"assignment":                 func(r *QAResult) { r.AssignmentSHA = "assignment" },
		"artifact":                   func(r *QAResult) { r.ArtifactSHA256 = "artifact" },
		"changed environment":        func(r *QAResult) { r.EnvironmentRevision = "env-v1" },
		"environment too long":       func(r *QAResult) { r.Environment = strings.Repeat("x", 2049) },
		"foreign evidence":           func(r *QAResult) { r.Evidence = []string{r.PRURL} },
		"prefix evidence":            func(r *QAResult) { r.Evidence = []string{"https://github.com/planning/board-other/discussions/1"} },
		"evidence traversal":         func(r *QAResult) { r.Evidence = []string{"https://github.com/planning/board/../secret"} },
		"evidence encoded traversal": func(r *QAResult) { r.Evidence = []string{"https://github.com/planning/board/%2e%2e/secret"} },
		"evidence query":             func(r *QAResult) { r.Evidence = []string{r.GoalURL + "?secret=x"} },
		"evidence case":              func(r *QAResult) { r.Evidence = []string{r.GoalURL + "#Private"} },
		"too many evidence":          func(r *QAResult) { r.Evidence = make([]string, 33) },
		"too many limitations":       func(r *QAResult) { r.Limitations = make([]string, 33) },
		"long limitation":            func(r *QAResult) { r.Limitations = []string{strings.Repeat("x", 2049)} },
		"large total": func(r *QAResult) {
			for range 32 {
				r.Limitations = append(r.Limitations, strings.Repeat("x", 2048))
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			base, q := qaResultFixture()
			r := base
			mutate(&r)
			if got := QAResultMatches(r, base.Repo, base.PRURL, base.TaskURL, base.GoalURL, base.HeadSHA, base.AssignmentSHA, q, base.ArtifactSHA256); got != (name == "valid") {
				t.Fatalf("match=%t", got)
			}
		})
	}
	r, q := qaResultFixture()
	for _, conclusion := range []string{"pass", "changes-needed", "blocked"} {
		r.Conclusion = conclusion
		if !QAResultMatches(r, r.Repo, r.PRURL, r.TaskURL, r.GoalURL, r.HeadSHA, r.AssignmentSHA, q, r.ArtifactSHA256) {
			t.Fatalf("terminal %s rejected", conclusion)
		}
	}
	if QAResultMatches(r, r.Repo, r.PRURL, r.TaskURL, r.GoalURL, r.HeadSHA, r.AssignmentSHA, q, "") {
		t.Fatal("missing expected artifact accepted")
	}
}

func teamQAFixture(t *testing.T) (Config, Task, PR, QAPolicy, string) {
	t.Helper()
	c, s, _ := engineeringFixture(t, false)
	task, p := s.Tasks[0], s.PRs[0]
	r, q := qaResultFixture()
	qa := c.Team.Members[2]
	q.Reviewer, task.Assignment.QA = qa.Name, qa.Name
	task.QA = q
	sha, err := AssignmentSHA(*c.Team, task.Repo, task.URL, task.GoalURL, *task.Assignment, q)
	if err != nil {
		t.Fatal(err)
	}
	p.AssignmentSHA = sha
	r.Repo, r.PRURL, r.TaskURL, r.GoalURL, r.HeadSHA, r.AssignmentSHA = task.Repo, p.URL, task.URL, task.GoalURL, p.HeadSHA, sha
	r.Evidence = []string{task.GoalURL}
	p.Reviews = append(p.Reviews, Review{ID: "20", Author: qa.Actor, HeadSHA: p.HeadSHA, State: "APPROVED", SubmittedAt: p.Reviews[0].SubmittedAt.Add(time.Minute), Body: qaMarker(r)})
	return c, task, p, q, r.ArtifactSHA256
}

func TestTeamQANativeResult(t *testing.T) {
	for _, conclusion := range []string{"pass", "changes-needed", "blocked"} {
		t.Run(conclusion, func(t *testing.T) {
			c, task, p, q, digest := teamQAFixture(t)
			r, _ := ParseQAResult(p.Reviews[1].Body)
			r.Conclusion = conclusion
			p.Reviews[1].Body = qaMarker(r)
			p.Reviews[1].State = map[string]string{"pass": "APPROVED", "changes-needed": "CHANGES_REQUESTED", "blocked": "COMMENTED"}[conclusion]
			review, got, ok := latestTeamQA(c, task, p, q, digest)
			if !ok || got.Conclusion != conclusion || review.ID != "20" {
				t.Fatalf("terminal result=%+v review=%+v valid=%t", got, review, ok)
			}
		})
	}
}

func TestQAFieldsPreserveLegacySnapshotEncoding(t *testing.T) {
	for _, value := range []any{Task{}, PR{}, Review{}, Job{}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"QA", "QAArtifact", "QAArtifactError", "LastEditedAt"} {
			if _, exists := fields[key]; exists {
				t.Fatalf("new QA field %s in legacy encoding: %s", key, raw)
			}
		}
		if _, review := value.(Review); review {
			if _, exists := fields["Body"]; exists {
				t.Fatal("new review body in legacy encoding")
			}
		}
	}
}

func TestQAGoalRequiresExplicitApplicability(t *testing.T) {
	const prefix = `<!-- coop-goal:v2 {"repositories":["example/repo"],"base_branches":{"example/repo":"integration"}`
	for _, qa := range []string{"", `,"qa":null`, `,"qa":{}`, `,"qa":{"required":null}`, `,"qa":{"Required":false}`} {
		if _, err := ParseTeamGoalMetadata(prefix + qa + "} -->"); err == nil {
			t.Fatalf("implicit applicability accepted: %s", qa)
		}
	}
	if _, err := ParseTeamGoalMetadata(prefix + `,"qa":{"required":false}} -->`); err != nil {
		t.Fatal(err)
	}
}

func TestQAResultSupportsPrivateIssueGoal(t *testing.T) {
	r, q := qaResultFixture()
	r.GoalURL = "https://github.com/planning/goals/issues/1"
	r.Evidence = []string{r.GoalURL + "#issuecomment-1"}
	parsed, err := ParseQAResult(qaMarker(r))
	if err != nil || !QAResultMatches(parsed, r.Repo, r.PRURL, r.TaskURL, r.GoalURL, r.HeadSHA, r.AssignmentSHA, q, r.ArtifactSHA256) {
		t.Fatalf("private issue goal rejected: %v", err)
	}
}

func TestTeamQALatestBadResultNeverFallsBack(t *testing.T) {
	for _, fault := range []string{"dismissed", "wrong state", "edited", "malformed", "wrong artifact", "engineering revoked", "before engineering", "scope boundary", "copied actor", "old head", "contributor", "incomplete"} {
		t.Run(fault, func(t *testing.T) {
			c, task, p, q, digest := teamQAFixture(t)
			latest := p.Reviews[1]
			latest.ID, latest.SubmittedAt = "21", latest.SubmittedAt.Add(time.Minute)
			switch fault {
			case "dismissed":
				latest.State = "DISMISSED"
			case "wrong state":
				latest.State = "COMMENTED"
			case "edited":
				latest.LastEditedAt = latest.SubmittedAt
			case "malformed":
				latest.Body = "invalid"
			case "wrong artifact":
				digest = BodyHash("different artifact")
			case "engineering revoked":
				p.Reviews[0].State = "CHANGES_REQUESTED"
			case "before engineering":
				p.Reviews[0].SubmittedAt = latest.SubmittedAt
			case "scope boundary":
				p.ScopeUpdatedAt = latest.SubmittedAt
			case "copied actor":
				p.Reviews[1].Author.ID, latest.Author.ID = 999, 999
			case "old head":
				p.Reviews[1].HeadSHA, latest.HeadSHA = BodyHash("old"), BodyHash("old")
			case "contributor":
				p.Contributors = append(p.Contributors, latest.Author)
			case "incomplete":
				p.ReviewsComplete = false
			}
			p.Reviews = append(p.Reviews, latest)
			if _, _, ok := latestTeamQA(c, task, p, q, digest); ok {
				t.Fatal("unsafe latest result revived older pass")
			}
		})
	}
}
