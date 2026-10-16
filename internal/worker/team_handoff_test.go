package worker

import (
	"strings"
	"testing"
	"time"
)

func teamHandoffFixture(t *testing.T, author string) (Config, Snapshot, Job, time.Time) {
	t.Helper()
	c, task, goal, comment, now := teamScopeFixture(t)
	c.Team.Members[0].Capabilities = append(c.Team.Members[0].Capabilities, "implement")
	c.Team.EngineeringPeers["planner"] = "engineer"
	a := assignmentFixture(t, *c.Team)
	a.Author, a.Peer = author, c.Team.EngineeringPeers[author]
	a.GoalBodySHA, a.TaskBodySHA = goal.BodySHA, task.BodySHA
	comment.Body = teamMarker("task", TeamTaskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA, Assignment: a})
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	digest, err := ValidateTeamScope(c, task, goal, now)
	if err != nil {
		t.Fatal(err)
	}
	c.Name, c.Self = c.Team.Members[0].Name, c.Team.Members[0].Actor
	var assignedAuthor Actor
	for _, m := range c.Team.Members {
		if m.Name == author {
			assignedAuthor = m.Actor
		}
	}
	p := PR{URL: "https://github.com/code/repo/pull/5", TaskURL: task.URL, GoalURL: goal.URL, BaseBranch: a.BaseBranch, HeadSHA: strings.Repeat("a", 40), AssignmentSHA: digest, Author: assignedAuthor,
		ScopeRevision: BodyHash("reviewable scope"), ScopeUpdatedAt: now.Add(-time.Minute), ReviewsComplete: true, ContributorsComplete: true, Contributors: []Actor{assignedAuthor}}
	e := Event{ID: "team-review-request", URL: goal.URL, Kind: "planning", Revision: "request-1", Body: "Ready for review: " + p.URL, Author: assignedAuthor, OccurredAt: now}
	s := Snapshot{Complete: true, Tasks: []Task{task}, Goals: []Goal{goal}, PRs: []PR{p}, Events: []Event{e}}
	j := Job{Kind: "plan", GoalURL: goal.URL, TaskURL: e.ID, InputRevision: e.Revision}
	j.ID = JobKey(j)
	return c, s, j, now
}

func TestTeamReviewHandoffUsesAssignedPeerAndCurrentReview(t *testing.T) {
	c, s, j, now := teamHandoffFixture(t, "engineer")
	h := reviewHandoffs(c, j, s, now)
	if len(h) != 1 || h[0].State != "needs_review" {
		t.Fatalf("assigned planner peer did not receive review: %+v", h)
	}
	p := &s.PRs[0]
	p.Reviews = []Review{{ID: "1", Author: c.Self, State: "APPROVED", HeadSHA: "old-head", SubmittedAt: now.Add(time.Minute)}}
	if got := reviewHandoffs(c, j, s, now); got[0].State != "needs_review" {
		t.Fatalf("old-head review completed handoff: %+v", got)
	}
	p.Reviews[0].HeadSHA = p.HeadSHA
	if got := reviewHandoffs(c, j, s, now); got[0].State != "reviewed" {
		t.Fatalf("current assigned peer review not recognized: %+v", got)
	}
	p.Reviews[0].Author = p.Author
	if got := reviewHandoffs(c, j, s, now); got[0].State == "reviewed" {
		t.Fatal("author's own review completed handoff")
	}
}

func TestTeamReviewHandoffPlannerCannotReviewOwnWork(t *testing.T) {
	c, s, j, now := teamHandoffFixture(t, "planner")
	s.PRs[0].Reviews = []Review{{ID: "1", Author: c.Self, State: "APPROVED", HeadSHA: s.PRs[0].HeadSHA, SubmittedAt: now.Add(time.Minute)}}
	h := reviewHandoffs(c, j, s, now)
	if len(h) != 1 || h[0].State != "blocked" || !strings.Contains(h[0].Reason, "assigned peer") || h[0].BlockerMarker == "" {
		t.Fatalf("planner status granted self review: %+v", h)
	}
}

func TestTeamReviewHandoffValidatesAssignmentBeforeReview(t *testing.T) {
	for _, name := range []string{"digest", "author", "scope", "metadata", "unapproved", "contributors", "partial"} {
		t.Run(name, func(t *testing.T) {
			c, s, j, now := teamHandoffFixture(t, "engineer")
			s.PRs[0].Reviews = []Review{{ID: "1", Author: c.Self, State: "APPROVED", HeadSHA: s.PRs[0].HeadSHA, SubmittedAt: now.Add(time.Minute)}}
			switch name {
			case "digest":
				s.PRs[0].AssignmentSHA = BodyHash("stale")
			case "author":
				s.PRs[0].Author = c.Self
			case "scope":
				s.Tasks[0].Body += " changed"
			case "metadata":
				s.PRs[0].AssignmentSHA = ""
			case "unapproved":
				s.Goals[0].Decisions = nil
			case "contributors":
				s.PRs[0].Contributors = append(s.PRs[0].Contributors, c.Self)
			case "partial":
				s.Complete = false
			}
			h := reviewHandoffs(c, j, s, now)
			if len(h) != 1 || h[0].State != "blocked" || h[0].BlockerMarker == "" {
				t.Fatalf("invalid evidence completed review handoff: %+v", h)
			}
		})
	}
}

func TestTeamDeliveryPlanRequiresAssignedDependentProgress(t *testing.T) {
	c, s, _, now := teamHandoffFixture(t, "planner")
	s.Events = nil
	s.Tasks[0].Completed, s.Tasks[0].Closed = true, true
	s.Tasks[0].ProjectID, s.Tasks[0].DoneID, s.Tasks[0].CurrentStatusID = "project", "done", "in-progress"
	s.PRs[0].Merged, s.PRs[0].Closed = true, true
	dependent := s.Tasks[0]
	dependent.URL = "https://github.com/code/repo/issues/6"
	dependent.Completed, dependent.Closed, dependent.Ready = false, false, false
	dependent.DependsOn = []string{s.Tasks[0].URL}
	a := *dependent.Assignment
	a.Author, a.Peer = "engineer", "planner"
	dependent.Assignment = &a
	s.Tasks = append(s.Tasks, dependent)
	events := planningEvents(c, s, now)
	if len(events) != 1 {
		t.Fatalf("missing completion handoff: %+v", events)
	}
	j := Job{Kind: "plan", GoalURL: events[0].URL, TaskURL: events[0].ID, InputRevision: events[0].Revision, PRURL: s.PRs[0].URL}
	var err error
	j, err = bindTeamJob(c, j, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if deliveryPlanProgress(c, j, s) {
		t.Fatal("completion handoff accepted board promise")
	}
	s.Tasks[0].CurrentStatusID = "done"
	if deliveryPlanProgress(c, j, s) {
		t.Fatal("completion handoff ignored blocked queue")
	}
	digest, err := ValidateTeamScope(c, dependent, s.Goals[0], now)
	if err != nil {
		t.Fatal(err)
	}
	s.PRs = append(s.PRs, PR{URL: "https://github.com/code/repo/pull/7", TaskURL: dependent.URL, GoalURL: dependent.GoalURL, BaseBranch: dependent.Assignment.BaseBranch, AssignmentSHA: digest, Author: c.Team.Members[0].Actor})
	if deliveryPlanProgress(c, j, s) {
		t.Fatal("wrong-author PR counted as dependent progress")
	}
	s.PRs[1].Author = c.Team.Members[1].Actor
	s.PRs[1].AssignmentSHA = BodyHash("stale")
	if deliveryPlanProgress(c, j, s) {
		t.Fatal("stale assignment PR counted as dependent progress")
	}
	s.PRs[1].AssignmentSHA = digest
	if !deliveryPlanProgress(c, j, s) {
		t.Fatal("assigned dependent author PR did not count as queue progress")
	}
}

func TestTeamCompletedDeliveryFromEitherAuthorWakesOnlyPlanner(t *testing.T) {
	for _, author := range []string{"engineer", "planner"} {
		t.Run(author, func(t *testing.T) {
			c, s, _, now := teamHandoffFixture(t, author)
			s.Events = nil
			s.Tasks[0].Completed, s.Tasks[0].Closed = true, true
			s.PRs[0].Merged, s.PRs[0].Closed = true, true
			events := planningEvents(c, s, now)
			if len(events) != 1 || events[0].Kind != "delivery_completed" || !sameActor(events[0].Author, s.PRs[0].Author) {
				t.Fatalf("missing planner followup for %s: %+v", author, events)
			}
			if !s.Tasks[0].Completed || !s.Tasks[0].Closed {
				t.Fatal("scope check mutated completed task")
			}
			c.Name, c.Self = c.Team.Members[1].Name, c.Team.Members[1].Actor
			if got := planningEvents(c, s, now); len(got) != 0 {
				t.Fatalf("non-planner received completion followup: %+v", got)
			}
		})
	}
}

func TestTeamCompletedDeliveryRejectsStaleOrForeignAssignment(t *testing.T) {
	for _, name := range []string{"digest", "author", "scope", "metadata", "base", "not-completed", "not-merged", "partial"} {
		t.Run(name, func(t *testing.T) {
			c, s, _, now := teamHandoffFixture(t, "planner")
			s.Events = nil
			s.Tasks[0].Completed, s.Tasks[0].Closed = true, true
			s.PRs[0].Merged, s.PRs[0].Closed = true, true
			switch name {
			case "digest":
				s.PRs[0].AssignmentSHA = BodyHash("stale")
			case "author":
				s.PRs[0].Author = c.Team.Members[1].Actor
			case "scope":
				s.Tasks[0].Body += " changed"
			case "metadata":
				s.Tasks[0].MetadataAuthor = c.Team.Members[1].Actor
			case "base":
				s.PRs[0].BaseBranch = "main"
			case "not-completed":
				s.Tasks[0].Completed = false
			case "not-merged":
				s.PRs[0].Merged = false
			case "partial":
				s.Complete = false
			}
			if got := planningEvents(c, s, now); len(got) != 0 {
				t.Fatalf("unverified delivery generated followup: %+v", got)
			}
		})
	}
}

func TestTeamMetadataRepairPreservesTrustedAssignmentAndScope(t *testing.T) {
	c, s, _, now := teamHandoffFixture(t, "planner")
	p := &s.PRs[0]
	p.TaskURL, p.GoalURL, p.AssignmentSHA = "", "", ""
	body, err := PRMetadataComment(c, s, p.URL, s.Tasks[0].URL, s.Goals[0].URL, now)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ValidateTeamScope(c, s.Tasks[0], s.Goals[0], now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTeamPRMetadata(body, s.Tasks[0].Repo, s.Tasks[0].URL, s.Goals[0].URL, digest); err != nil {
		t.Fatal(err)
	}
	metadata, err := TaskMetadataComment(c, s, s.Tasks[0].URL, s.Goals[0].URL, now)
	if err != nil {
		t.Fatal(err)
	}
	var parsed TeamTaskMetadata
	if err := teamMetadata(metadata, "task", &parsed); err != nil || parsed.Assignment != *s.Tasks[0].Assignment {
		t.Fatalf("repair changed assignment: %+v, %v", parsed, err)
	}
	if strings.Contains(metadata, "assignment_sha256") || strings.Contains(body, "coop-goal:") {
		t.Fatal("repair wrote digest to task or changed owner goal metadata")
	}
	p.AssignmentSHA = BodyHash("conflicting tracking")
	if _, err := PRMetadataComment(c, s, p.URL, s.Tasks[0].URL, s.Goals[0].URL, now); err == nil {
		t.Fatal("repair replaced conflicting assignment")
	}
	p.AssignmentSHA = ""
	s.Tasks[0].MetadataAuthor = c.Team.Members[1].Actor
	if _, err := PRMetadataComment(c, s, p.URL, s.Tasks[0].URL, s.Goals[0].URL, now); err == nil {
		t.Fatal("repair blessed untrusted assignment")
	}
}
