package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func requiredQAPolicyFixture() QAPolicy {
	return QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: BodyHash("acceptance"), Environment: "staging", EnvironmentRevision: "v1"}
}

func TestTeamQAPolicyBounds(t *testing.T) {
	for _, field := range []string{"environment", "revision"} {
		for _, value := range []string{"", " ", " staging", "staging ", "staging\n", "stage\rprod", "stage\nprod", "stage\x00prod", string([]byte{0xff}), strings.Repeat("x", (2<<10)+1)} {
			t.Run(field+"/"+BodyHash(value), func(t *testing.T) {
				q := requiredQAPolicyFixture()
				if field == "environment" {
					q.Environment = value
				} else {
					q.EnvironmentRevision = value
				}
				if err := validateQAPolicy(q); err == nil {
					t.Fatal("accepted invalid QA policy")
				}
				body := teamMarker("goal", TeamGoalMetadata{Repositories: []string{"code/repo"}, BaseBranches: map[string]string{"code/repo": "main"}, QA: q})
				// JSON replaces invalid UTF-8; test that case directly above.
				if value != string([]byte{0xff}) {
					if _, err := ParseTeamGoalMetadata(body); err == nil {
						t.Fatal("goal accepted invalid QA policy")
					}
				}
			})
		}
	}
	q := requiredQAPolicyFixture()
	q.Environment, q.EnvironmentRevision = strings.Repeat("x", 2<<10), strings.Repeat("y", 2<<10)
	if err := validateQAPolicy(q); err != nil {
		t.Fatal("rejected boundary QA policy:", err)
	}
}

func TestTeamQAApprovedScopeAndBinding(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	q := requiredQAPolicyFixture()
	goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{task.Repo: "feature/integration"}, QA: q})
	goal.BodySHA = BodyHash(goal.Body)
	var m TeamTaskMetadata
	if err := teamMetadata(comment.Body, "task", &m); err != nil {
		t.Fatal(err)
	}
	m.Assignment.QA, m.Assignment.GoalBodySHA = q.Reviewer, goal.BodySHA
	comment.Body = teamMarker("task", m)
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal("structural QA assignment rejected before goal read:", err)
	}
	if _, err := ValidateTeamScope(c, task, goal, now); err == nil {
		t.Fatal("missing task QA accepted")
	}
	task.QA = q
	a, err := resolveTeamAssignment(c, task, goal, now)
	if err != nil || a.QA.ID != 13 {
		t.Fatalf("resolved QA = %+v, %v", a, err)
	}
	s := Snapshot{Tasks: []Task{task}, Goals: []Goal{goal}}
	j, err := bindTeamJob(c, Job{Kind: "qa", TaskURL: task.URL, GoalURL: goal.URL}, s, now)
	if err != nil || j.Binding == nil || *j.Binding != (JobBinding{Version: 2, AssignmentSHA: a.SHA, ExecutorID: a.QA.ID, Role: "customer-qa"}) {
		t.Fatalf("QA binding = %+v, %v", j, err)
	}
	for _, mutate := range []func(*QAPolicy){
		func(q *QAPolicy) { q.Required = false },
		func(q *QAPolicy) { q.Reviewer = "engineer" },
		func(q *QAPolicy) { q.AcceptanceSHA = BodyHash("other") },
		func(q *QAPolicy) { q.Environment = "other" },
		func(q *QAPolicy) { q.EnvironmentRevision = "other" },
	} {
		changed := task
		mutate(&changed.QA)
		if _, err := ValidateTeamScope(c, changed, goal, now); err == nil {
			t.Fatal("task QA differing from approved goal accepted")
		}
	}
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil || task.QA != (QAPolicy{}) {
		t.Fatal("metadata refresh retained previous QA policy", err)
	}
}

func TestTeamQAIndependentEvenWhenQualified(t *testing.T) {
	for _, name := range []string{"engineer", "planner"} {
		t.Run(name, func(t *testing.T) {
			p := teamPolicyFixture()
			for i := range p.Members {
				if p.Members[i].Name == name {
					p.Members[i].Capabilities = append(p.Members[i].Capabilities, "customer-qa")
				}
			}
			a, q := assignmentFixture(t, p), requiredQAPolicyFixture()
			a.QA, q.Reviewer = name, name
			if err := validateTaskAssignmentStructure(p, a); err == nil {
				t.Fatal("structural validation accepted non-independent QA")
			}
			if err := ValidateTaskAssignment(p, a, q); err == nil {
				t.Fatal("goal validation accepted non-independent QA")
			}
		})
	}
}

func TestTeamQABindingRejectsNonrequired(t *testing.T) {
	c, task, goal, comment, now := teamScopeFixture(t)
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Tasks: []Task{task}, Goals: []Goal{goal}}
	if _, err := bindTeamJob(c, Job{Kind: "qa", TaskURL: task.URL, GoalURL: goal.URL}, s, now); err == nil {
		t.Fatal("QA job bound without required QA policy")
	}
}

func TestTeamQAAssignmentDigestBound(t *testing.T) {
	p := teamPolicyFixture()
	a := assignmentFixture(t, p)
	a.QA = "qa"
	q := requiredQAPolicyFixture()
	digest := func() string {
		t.Helper()
		s, err := AssignmentSHA(p, "code/repo", "https://github.com/code/repo/issues/1", "https://github.com/planning/board/discussions/1", a, q)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	want := digest()
	p.Members[2].Actor.Login = "renamed[bot]"
	if digest() != want {
		t.Fatal("mutable QA login changed assignment digest")
	}
	for _, field := range []string{"actor", "acceptance", "environment", "revision"} {
		t.Run(field, func(t *testing.T) {
			oldP, oldA, oldQ := p.Members[2].Actor.ID, a, q
			defer func() { p.Members[2].Actor.ID, a, q = oldP, oldA, oldQ }()
			switch field {
			case "actor":
				p.Members[2].Actor.ID = 99
				a.PolicySHA, _ = TeamPolicySHA(p)
			case "acceptance":
				q.AcceptanceSHA = BodyHash("changed")
			case "environment":
				q.Environment = "changed"
			case "revision":
				q.EnvironmentRevision = "changed"
			}
			if digest() == want {
				t.Fatal("QA change did not invalidate assignment")
			}
		})
	}
}

func TestTeamQAEngineeringClearanceUsesTaskPolicy(t *testing.T) {
	c, s, _ := engineeringFixture(t, false)
	task, p := s.Tasks[0], s.PRs[0]
	task.QA = requiredQAPolicyFixture()
	task.Assignment.QA = task.QA.Reviewer
	sha, err := AssignmentSHA(*c.Team, task.Repo, task.URL, task.GoalURL, *task.Assignment, task.QA)
	if err != nil {
		t.Fatal(err)
	}
	p.AssignmentSHA = sha
	if _, ok := EngineeringReviewFor(c, task, p); !ok {
		t.Fatal("required QA prevented valid engineering clearance")
	}
	task.QA.EnvironmentRevision = "changed"
	if _, ok := EngineeringReviewFor(c, task, p); ok {
		t.Fatal("clearance survived changed QA policy")
	}
}

func TestTeamQASnapshotPopulatesCurrentPolicy(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "coherent", true: "changed_goal"}[stale], func(t *testing.T) {
			c, task, goal, comment, now := teamScopeFixture(t)
			c.Projects = nil
			c.Repositories = []string{task.Repo}
			q := requiredQAPolicyFixture()
			goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{task.Repo: "feature/integration"}, QA: q})
			goal.BodySHA = BodyHash(goal.Body)
			var metadata TeamTaskMetadata
			if err := teamMetadata(comment.Body, "task", &metadata); err != nil {
				t.Fatal(err)
			}
			metadata.Assignment.QA, metadata.Assignment.GoalBodySHA = q.Reviewer, goal.BodySHA
			comment.Body = teamMarker("task", metadata)
			if stale {
				goal.Body += " changed acceptance"
			}
			issue := restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: now}
			f := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
				switch {
				case strings.HasPrefix(endpoint, "repos/code/repo/issues?"):
					return jsonResponse([]restIssue{issue}), nil
				case endpoint == "repos/code/repo/issues/1":
					return jsonResponse(issue), nil
				case strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?"):
					return jsonResponse([]restComment{{ID: 2, Author: restActor(comment.Author), Body: comment.Body, CreatedAt: now}}), nil
				case strings.HasPrefix(endpoint, "repos/code/repo/pulls?"):
					return jsonResponse([]any{}), nil
				}
				var req struct{ Query string }
				if endpoint != "graphql" || json.Unmarshal(raw, &req) != nil {
					t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
				}
				d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt}
				page := map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}
				switch {
				case strings.Contains(req.Query, "WorkerDiscussions"):
					page["nodes"] = []graphDiscussion{d}
					return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"discussions": page}}}), nil
				case strings.Contains(req.Query, "WorkerDiscussionComments"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page}}}), nil
				case strings.Contains(req.Query, "WorkerGoalVersion"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": d}}), nil
				default:
					t.Fatalf("unexpected query: %s", req.Query)
					return CommandResult{}, nil
				}
			}}
			g := NewGitHubClient(f)
			g.config = c
			s, err := g.readSnapshot(context.Background(), Snapshot{})
			if err != nil || len(s.Tasks) != 1 {
				t.Fatalf("snapshot read failed: %+v, %v", s, err)
			}
			want := q
			if stale {
				want = QAPolicy{}
			}
			if s.Tasks[0].QA != want {
				t.Fatalf("task QA = %+v, want %+v", s.Tasks[0].QA, want)
			}
		})
	}
}
