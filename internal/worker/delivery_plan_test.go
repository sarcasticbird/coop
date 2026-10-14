package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompletedDeliveryWakesPlannerOnceWithinApprovedGoal(t *testing.T) {
	c, s, n := policyFixture(t)
	c.Role = "planner"
	c.Self = c.Reviewer
	s.Tasks[0].Completed = true
	s.Tasks[0].Closed = true
	s.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, Merged: true, Closed: true}}
	jobs := Select(c, s, State{}, n)
	if len(jobs) != 1 || jobs[0].Kind != "plan" || jobs[0].PRURL != s.PRs[0].URL {
		t.Fatalf("no post-delivery planning handoff: %+v", jobs)
	}
	prompt, err := BuildPrompt(c, jobs[0], s)
	if err != nil {
		t.Fatal(err)
	}
	var input struct{ Event *Event }
	raw := strings.SplitN(prompt, "Quoted GitHub input and verified job identity:\n", 2)
	if len(raw) != 2 || json.Unmarshal([]byte(raw[1]), &input) != nil || input.Event == nil || input.Event.Kind != "delivery_completed" {
		t.Fatal("missing verified completion context")
	}
	state := State{Jobs: map[string]Job{jobs[0].ID: jobs[0]}}
	if len(Select(c, s, state, n)) != 0 {
		t.Fatal("completed handoff repeats")
	}
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Complete = false },
		func(s *Snapshot) { s.Goals[0].Decisions = nil },
		func(s *Snapshot) { s.Tasks[0].Completed = false },
		func(s *Snapshot) { s.Tasks[0].MetadataScopeSHA = "" },
		func(s *Snapshot) { s.PRs[0].Merged = false },
		func(s *Snapshot) { s.PRs[0].Author = c.Owner },
		func(s *Snapshot) { s.PRs[0].GoalURL = "https://github.com/code/repo/issues/99" },
	} {
		candidate := s
		candidate.Goals = append([]Goal(nil), s.Goals...)
		candidate.Tasks = append([]Task(nil), s.Tasks...)
		candidate.PRs = append([]PR(nil), s.PRs...)
		mutate(&candidate)
		for _, j := range Select(c, candidate, State{}, n) {
			if j.Kind == "plan" {
				t.Fatalf("unverified completion granted planning: %+v", candidate)
			}
		}
	}
}

func TestDeliveryPlanRequiresActualBoardAndQueueProgress(t *testing.T) {
	c, s, n := policyFixture(t)
	c.Role = "planner"
	c.Self = c.Reviewer
	s.Tasks[0].Completed = true
	s.Tasks[0].Closed = true
	s.Tasks[0].ProjectID = "project"
	s.Tasks[0].DoneID = "done"
	s.Tasks[0].CurrentStatusID = "in-progress"
	s.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, Merged: true, Closed: true}}
	dependent := s.Tasks[0]
	dependent.URL = "https://github.com/code/repo/issues/6"
	dependent.Completed = false
	dependent.Closed = false
	dependent.Ready = false
	dependent.DependsOn = []string{s.Tasks[0].URL}
	s.Tasks = append(s.Tasks, dependent)
	jobs := Select(c, s, State{}, n)
	if len(jobs) != 1 {
		t.Fatalf("jobs %+v", jobs)
	}
	j := jobs[0]
	s.Goals[0].Decisions = append(s.Goals[0].Decisions, Decision{Author: c.Self, Command: "I will update the board next.\n" + marker(j, "result"), CreatedAt: n})
	if planningResponded(c, j, s) {
		t.Fatal("promise consumed completion handoff")
	}
	s.Tasks[0].CurrentStatusID = "done"
	if planningResponded(c, j, s) {
		t.Fatal("ready dependent left permanently in Backlog")
	}
	s.Tasks[1].Ready = true
	if !planningResponded(c, j, s) {
		t.Fatal("actual board/queue progress not recognized")
	}
}

func TestDeliveryPlanCannotRetargetSameBodyTaskOrGoal(t *testing.T) {
	c, s, n := policyFixture(t)
	c.Role = "planner"
	c.Self = c.Reviewer
	s.Tasks[0].Completed = true
	s.Tasks[0].Closed = true
	s.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, Merged: true, Closed: true}}
	old := Select(c, s, State{}, n)[0]
	forged := old
	forged.GoalURL = "https://github.com/orgs/planning/discussions/2"
	if allowed, _ := Admit(c, forged, s, n); allowed {
		t.Fatal("event admitted under another goal")
	}
	goal := s.Goals[0]
	goal.URL = forged.GoalURL
	s.Goals = append(s.Goals, goal)
	task := s.Tasks[0]
	task.URL = "https://github.com/code/repo/issues/6"
	task.GoalURL = goal.URL
	s.Tasks = append(s.Tasks, task)
	s.PRs[0].TaskURL = task.URL
	s.PRs[0].GoalURL = goal.URL
	if allowed, _ := Admit(c, old, s, n); allowed {
		t.Fatal("old completion job survived retargeting")
	}
	jobs := Select(c, s, State{}, n)
	if len(jobs) != 1 || jobs[0].ID == old.ID {
		t.Fatal("retargeted source retained callback identity")
	}
}
