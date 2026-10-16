package worker

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func teamPolicyFixture() TeamPolicy {
	return TeamPolicy{Version: 2, Planner: "planner", Members: []TeamMember{
		{Name: "planner", Actor: Actor{ID: 11, Login: "planner[bot]", Kind: "Bot"}, Capabilities: []string{"plan", "engineering-review"}},
		{Name: "engineer", Actor: Actor{ID: 12, Login: "engineer[bot]", Kind: "Bot"}, Capabilities: []string{"implement", "engineering-review"}},
		{Name: "qa", Actor: Actor{ID: 13, Login: "qa[bot]", Kind: "Bot"}, Capabilities: []string{"customer-qa"}},
	}, EngineeringPeers: map[string]string{"engineer": "planner"}}
}

func assignmentFixture(t *testing.T, p TeamPolicy) TaskAssignment {
	t.Helper()
	sha, err := TeamPolicySHA(p)
	if err != nil {
		t.Fatal(err)
	}
	return TaskAssignment{Version: 2, Author: "engineer", Peer: "planner", GoalBodySHA: BodyHash("goal"), TaskBodySHA: BodyHash("task"), PolicySHA: sha}
}

func TestTeamPolicyFourthEngineerFromConfig(t *testing.T) {
	p := teamPolicyFixture()
	p.Members = append(p.Members, TeamMember{Name: "fourth-engineer", Actor: Actor{ID: 14, Login: "fourth[bot]", Kind: "Bot"}, Capabilities: []string{"implement"}})
	p.EngineeringPeers["fourth-engineer"] = "engineer"
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TeamPolicy
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	a := assignmentFixture(t, decoded)
	a.Author, a.Peer = "fourth-engineer", "engineer"
	if err := ValidateTaskAssignment(decoded, a, QAPolicy{}); err != nil {
		t.Fatal(err)
	}
	m, ok := TeamMemberForActor(decoded, Actor{ID: 14, Login: "renamed[bot]", Kind: "Bot"})
	if !ok || m.Name != "fourth-engineer" {
		t.Fatalf("member = %+v, %v", m, ok)
	}
	for _, actor := range []Actor{{ID: 14, Kind: "User"}, {ID: 99, Login: "fourth[bot]", Kind: "Bot"}, {}} {
		if _, ok := TeamMemberForActor(decoded, actor); ok {
			t.Fatalf("matched invalid actor %+v", actor)
		}
	}
}

func TestTeamPolicyInvalid(t *testing.T) {
	for name, mutate := range map[string]func(*TeamPolicy){
		"version":              func(p *TeamPolicy) { p.Version = 1 },
		"no members":           func(p *TeamPolicy) { p.Members = nil },
		"uppercase name":       func(p *TeamPolicy) { p.Members[0].Name = "Planner" },
		"invalid name":         func(p *TeamPolicy) { p.Members[0].Name = "bad_name" },
		"empty name":           func(p *TeamPolicy) { p.Members[0].Name = "" },
		"duplicate name":       func(p *TeamPolicy) { p.Members[1].Name = p.Members[0].Name },
		"duplicate ID":         func(p *TeamPolicy) { p.Members[1].Actor.ID = p.Members[0].Actor.ID },
		"zero ID":              func(p *TeamPolicy) { p.Members[0].Actor.ID = 0 },
		"user":                 func(p *TeamPolicy) { p.Members[0].Actor.Kind = "User" },
		"empty login":          func(p *TeamPolicy) { p.Members[0].Actor.Login = "" },
		"bad login":            func(p *TeamPolicy) { p.Members[0].Actor.Login = "bad login" },
		"unknown capability":   func(p *TeamPolicy) { p.Members[0].Capabilities = []string{"review"} },
		"duplicate capability": func(p *TeamPolicy) { p.Members[0].Capabilities = []string{"plan", "plan"} },
		"missing planner":      func(p *TeamPolicy) { p.Planner = "missing" },
		"unqualified planner":  func(p *TeamPolicy) { p.Planner = "engineer" },
		"missing peer":         func(p *TeamPolicy) { delete(p.EngineeringPeers, "engineer") },
		"unknown source":       func(p *TeamPolicy) { p.EngineeringPeers["missing"] = "planner" },
		"unqualified source":   func(p *TeamPolicy) { p.EngineeringPeers["qa"] = "planner" },
		"unknown target":       func(p *TeamPolicy) { p.EngineeringPeers["engineer"] = "missing" },
		"unqualified target":   func(p *TeamPolicy) { p.EngineeringPeers["engineer"] = "qa" },
		"self peer":            func(p *TeamPolicy) { p.EngineeringPeers["engineer"] = "engineer" },
	} {
		t.Run(name, func(t *testing.T) {
			p := teamPolicyFixture()
			mutate(&p)
			if err := ValidateTeamPolicy(p); err == nil {
				t.Fatal("accepted invalid policy")
			}
			if sha, err := TeamPolicySHA(p); err == nil || sha != "" {
				t.Fatalf("invalid policy digest = %q, %v", sha, err)
			}
		})
	}
}

func TestTaskAssignmentInvalid(t *testing.T) {
	p := teamPolicyFixture()
	for name, mutate := range map[string]func(*TaskAssignment, *QAPolicy){
		"version":            func(a *TaskAssignment, q *QAPolicy) { a.Version = 1 },
		"missing author":     func(a *TaskAssignment, q *QAPolicy) { a.Author = "" },
		"unknown author":     func(a *TaskAssignment, q *QAPolicy) { a.Author = "missing" },
		"unqualified author": func(a *TaskAssignment, q *QAPolicy) { a.Author = "qa" },
		"unknown peer":       func(a *TaskAssignment, q *QAPolicy) { a.Peer = "missing" },
		"self peer":          func(a *TaskAssignment, q *QAPolicy) { a.Peer = a.Author },
		"goal hash":          func(a *TaskAssignment, q *QAPolicy) { a.GoalBodySHA = "bad" },
		"task hash":          func(a *TaskAssignment, q *QAPolicy) { a.TaskBodySHA = strings.Repeat("A", 64) },
		"policy hash":        func(a *TaskAssignment, q *QAPolicy) { a.PolicySHA = "bad" },
		"stale policy":       func(a *TaskAssignment, q *QAPolicy) { a.PolicySHA = BodyHash("old policy") },
		"extra QA":           func(a *TaskAssignment, q *QAPolicy) { a.QA = "qa" },
		"extra reviewer":     func(a *TaskAssignment, q *QAPolicy) { q.Reviewer = "qa" },
		"extra acceptance":   func(a *TaskAssignment, q *QAPolicy) { q.AcceptanceSHA = BodyHash("acceptance") },
		"extra environment":  func(a *TaskAssignment, q *QAPolicy) { q.Environment = "staging" },
		"extra revision":     func(a *TaskAssignment, q *QAPolicy) { q.EnvironmentRevision = "v1" },
	} {
		t.Run(name, func(t *testing.T) {
			a, q := assignmentFixture(t, p), QAPolicy{}
			mutate(&a, &q)
			if err := ValidateTaskAssignment(p, a, q); err == nil {
				t.Fatal("accepted invalid assignment")
			}
			if sha, err := AssignmentSHA(p, "owner/repo", "https://github.com/owner/repo/issues/1", "https://github.com/orgs/owner/discussions/2", a, q); err == nil || sha != "" {
				t.Fatalf("invalid assignment digest = %q, %v", sha, err)
			}
		})
	}
}

func TestTaskAssignmentQAUnsupported(t *testing.T) {
	p := teamPolicyFixture()
	a := assignmentFixture(t, p)
	a.QA = "qa"
	q := QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: BodyHash("acceptance"), Environment: "staging", EnvironmentRevision: "v1"}
	if err := ValidateTaskAssignment(p, a, q); err == nil || err.Error() != "qa_not_supported" {
		t.Fatalf("QA error = %v", err)
	}
	if sha, err := AssignmentSHA(p, "owner/repo", "https://github.com/owner/repo/issues/1", "https://github.com/orgs/owner/discussions/2", a, q); err == nil || err.Error() != "qa_not_supported" || sha != "" {
		t.Fatalf("QA digest = %q, %v", sha, err)
	}
	for name, mutate := range map[string]func(*TaskAssignment, *QAPolicy){
		"missing":     func(a *TaskAssignment, q *QAPolicy) { a.QA, q.Reviewer = "", "" },
		"unknown":     func(a *TaskAssignment, q *QAPolicy) { a.QA, q.Reviewer = "missing", "missing" },
		"unqualified": func(a *TaskAssignment, q *QAPolicy) { a.QA, q.Reviewer = "planner", "planner" },
		"mismatch":    func(a *TaskAssignment, q *QAPolicy) { q.Reviewer = "engineer" },
		"acceptance":  func(a *TaskAssignment, q *QAPolicy) { q.AcceptanceSHA = "invalid" },
		"environment": func(a *TaskAssignment, q *QAPolicy) { q.Environment = " " },
		"revision":    func(a *TaskAssignment, q *QAPolicy) { q.EnvironmentRevision = "" },
	} {
		t.Run(name, func(t *testing.T) {
			a, q := a, q
			mutate(&a, &q)
			if err := ValidateTaskAssignment(p, a, q); err == nil || err.Error() == "qa_not_supported" {
				t.Fatalf("expected validation before unsupported QA, got %v", err)
			}
		})
	}
}

func TestTeamPolicyDigestCanonical(t *testing.T) {
	p := teamPolicyFixture()
	before, _ := json.Marshal(p)
	want, err := TeamPolicySHA(p)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(p)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("hash mutated policy")
	}
	slices.Reverse(p.Members)
	for i := range p.Members {
		slices.Reverse(p.Members[i].Capabilities)
		p.Members[i].Actor.Login = "renamed[bot]"
	}
	got, err := TeamPolicySHA(p)
	if err != nil || got != want {
		t.Fatalf("canonical digest = %q, %v; want %q", got, err, want)
	}
	for name, mutate := range map[string]func(*TeamPolicy){
		"actor":      func(p *TeamPolicy) { p.Members[0].Actor.ID = 99 },
		"capability": func(p *TeamPolicy) { p.Members[2].Capabilities = append(p.Members[2].Capabilities, "plan") },
		"planner": func(p *TeamPolicy) {
			p.Members[1].Capabilities = append(p.Members[1].Capabilities, "plan")
			p.Planner = "engineer"
		},
		"peer": func(p *TeamPolicy) {
			p.Members[2].Capabilities = append(p.Members[2].Capabilities, "engineering-review")
			p.EngineeringPeers["engineer"] = "qa"
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := teamPolicyFixture()
			mutate(&p)
			got, err := TeamPolicySHA(p)
			if err != nil || got == want {
				t.Fatalf("changed policy digest = %q, %v", got, err)
			}
		})
	}
}

func TestAssignmentDigestCanonicalAndBound(t *testing.T) {
	p := teamPolicyFixture()
	a := assignmentFixture(t, p)
	repo, task, goal := "owner/repo", "https://github.com/owner/repo/issues/1", "https://github.com/orgs/owner/discussions/2"
	want, err := AssignmentSHA(p, repo, task, goal, a, QAPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	p.Members[0].Actor.Login = "renamed[bot]"
	got, err := AssignmentSHA(p, strings.ToUpper(repo), strings.ToUpper(task), strings.ToUpper(goal), a, QAPolicy{})
	if err != nil || got != want {
		t.Fatalf("canonical assignment = %q, %v; want %q", got, err, want)
	}
	for _, field := range []string{"repo", "task", "goal", "goal hash", "task hash", "actor"} {
		t.Run(field, func(t *testing.T) {
			p := teamPolicyFixture()
			a, repo, task, goal := a, repo, task, goal
			switch field {
			case "repo":
				repo, task = "other/repo", "https://github.com/other/repo/issues/1"
			case "task":
				task = "https://github.com/owner/repo/issues/3"
			case "goal":
				goal = "https://github.com/orgs/owner/discussions/3"
			case "goal hash":
				a.GoalBodySHA = BodyHash("changed goal")
			case "task hash":
				a.TaskBodySHA = BodyHash("changed task")
			case "actor":
				p.Members[1].Actor.ID = 99
				a.PolicySHA, _ = TeamPolicySHA(p)
			}
			got, err := AssignmentSHA(p, repo, task, goal, a, QAPolicy{})
			if err != nil || got == want {
				t.Fatalf("changed assignment digest = %q, %v", got, err)
			}
		})
	}
	for _, bad := range []string{"https://github.com/other/repo/issues/1", "https://github.com/owner/repo/pull/1", task + "?x=1", task + "#fragment", "https://github.com/owner/repo/issues/01"} {
		if _, err := AssignmentSHA(p, repo, bad, goal, a, QAPolicy{}); err == nil {
			t.Fatalf("accepted invalid task reference %q", bad)
		}
	}
	if _, err := AssignmentSHA(p, repo, task, "https://example.com/goal", a, QAPolicy{}); err == nil {
		t.Fatal("accepted invalid goal reference")
	}
}

func TestTeamPolicyDigestPeerMapOrdering(t *testing.T) {
	p := teamPolicyFixture()
	p.Members[0].Capabilities = append(p.Members[0].Capabilities, "implement")
	p.EngineeringPeers["planner"] = "engineer"
	want, err := TeamPolicySHA(p)
	if err != nil {
		t.Fatal(err)
	}
	p.EngineeringPeers = map[string]string{}
	p.EngineeringPeers["planner"] = "engineer"
	p.EngineeringPeers["engineer"] = "planner"
	for range 20 {
		got, err := TeamPolicySHA(p)
		if err != nil || got != want {
			t.Fatalf("reordered peer map digest = %q, %v; want %q", got, err, want)
		}
	}
}
