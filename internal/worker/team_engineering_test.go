package worker

import (
	"reflect"
	"testing"
	"time"
)

// Both directions use the same roster and policy; only the trusted task
// assignment changes. Global planner status must not decide PR ownership.
func engineeringFixture(t *testing.T, reverse bool) (Config, Snapshot, time.Time) {
	t.Helper()
	c, task, goal, comment, now := teamScopeFixture(t)
	c.Team.Members[0].Capabilities = append(c.Team.Members[0].Capabilities, "implement")
	c.Team.EngineeringPeers["planner"] = "engineer"
	a := assignmentFixture(t, *c.Team)
	a.GoalBodySHA, a.TaskBodySHA = goal.BodySHA, task.BodySHA
	if reverse {
		a.Author, a.Peer = "planner", "engineer"
	}
	comment.Body = teamMarker("task", TeamTaskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA, Assignment: a})
	if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	sha, err := ValidateTeamScope(c, task, goal, now)
	if err != nil {
		t.Fatal(err)
	}
	var author, peer Actor
	for _, m := range c.Team.Members {
		if m.Name == a.Author {
			c.Name, c.Self, author = m.Name, m.Actor, m.Actor
		}
		if m.Name == a.Peer {
			peer = m.Actor
		}
	}
	p := PR{
		BaseBranch: a.BaseBranch,
		URL:        "https://github.com/code/repo/pull/2", TaskURL: task.URL, GoalURL: goal.URL,
		HeadSHA: BodyHash("head"), Author: author, AssignmentSHA: sha,
		ScopeRevision: BodyHash("durable scope"), ScopeUpdatedAt: now,
		Contributors: []Actor{author}, ContributorsComplete: true, ReviewsComplete: true,
		ChecksState: "success", MergeState: "CLEAN",
		Reviews: []Review{{ID: "10", HeadSHA: BodyHash("head"), State: "APPROVED", Author: peer, SubmittedAt: now.Add(time.Minute)}},
	}
	return c, Snapshot{Complete: true, Goals: []Goal{goal}, Tasks: []Task{task}, PRs: []PR{p}}, now.Add(2 * time.Minute)
}

func TestTeamEngineeringClearanceReciprocal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "engineer-to-planner", true: "planner-to-engineer"}[reverse], func(t *testing.T) {
			c, s, _ := engineeringFixture(t, reverse)
			task, p := s.Tasks[0], s.PRs[0]
			got, ok := EngineeringReviewFor(c, task, p)
			if !ok {
				t.Fatal("assigned independent peer did not clear current head")
			}
			want := EngineeringClearance{
				ReviewID: p.Reviews[0].ID, ActorID: p.Reviews[0].Author.ID,
				Repo: task.Repo, PRURL: p.URL, TaskURL: task.URL, GoalURL: task.GoalURL,
				HeadSHA: p.HeadSHA, AssignmentSHA: p.AssignmentSHA,
				ScopeRevision: p.ScopeRevision, SubmittedAt: p.Reviews[0].SubmittedAt,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("clearance = %+v, want %+v", got, want)
			}
			// Immutable actor identity survives GitHub login changes.
			p.Reviews[0].Author.Login = "renamed-peer[bot]"
			if _, ok := EngineeringReviewFor(c, task, p); !ok {
				t.Fatal("mutable login invalidated immutable peer identity")
			}
		})
	}
}

func TestTeamEngineeringClearanceRejectsUnsafeEvidence(t *testing.T) {
	mutations := map[string]func(*Config, *Task, *PR){
		"self review":              func(c *Config, task *Task, p *PR) { p.Reviews[0].Author = p.Author },
		"outsider with peer login": func(c *Config, task *Task, p *PR) { p.Reviews[0].Author.ID = 999 },
		"wrong actor kind":         func(c *Config, task *Task, p *PR) { p.Reviews[0].Author.Kind = "User" },
		"wrong role":               func(c *Config, task *Task, p *PR) { p.Reviews[0].Author = c.Team.Members[2].Actor },
		"peer contributed":         func(c *Config, task *Task, p *PR) { p.Contributors = append(p.Contributors, p.Reviews[0].Author) },
		"renamed joint author": func(c *Config, task *Task, p *PR) {
			actor := p.Reviews[0].Author
			actor.Login = "renamed-contributor[bot]"
			p.Contributors = append(p.Contributors, actor)
		},
		"incomplete contributors":      func(c *Config, task *Task, p *PR) { p.ContributorsComplete = false },
		"incomplete reviews":           func(c *Config, task *Task, p *PR) { p.ReviewsComplete = false },
		"no native review":             func(c *Config, task *Task, p *PR) { p.Reviews = nil },
		"comment only":                 func(c *Config, task *Task, p *PR) { p.Reviews[0].State = "COMMENTED" },
		"changes requested":            func(c *Config, task *Task, p *PR) { p.Reviews[0].State = "CHANGES_REQUESTED" },
		"dismissed":                    func(c *Config, task *Task, p *PR) { p.Reviews[0].State = "DISMISSED" },
		"missing review ID":            func(c *Config, task *Task, p *PR) { p.Reviews[0].ID = "" },
		"missing submission time":      func(c *Config, task *Task, p *PR) { p.Reviews[0].SubmittedAt = time.Time{} },
		"stale head":                   func(c *Config, task *Task, p *PR) { p.HeadSHA = BodyHash("new head") },
		"review at scope boundary":     func(c *Config, task *Task, p *PR) { p.ScopeUpdatedAt = p.Reviews[0].SubmittedAt },
		"review before scope boundary": func(c *Config, task *Task, p *PR) { p.ScopeUpdatedAt = p.Reviews[0].SubmittedAt.Add(time.Second) },
		"missing scope revision":       func(c *Config, task *Task, p *PR) { p.ScopeRevision = "" },
		"stale PR assignment":          func(c *Config, task *Task, p *PR) { p.AssignmentSHA = BodyHash("old assignment") },
		"missing task assignment":      func(c *Config, task *Task, p *PR) { task.Assignment = nil },
		"self assignment":              func(c *Config, task *Task, p *PR) { task.Assignment.Peer = task.Assignment.Author },
		"changed policy":               func(c *Config, task *Task, p *PR) { c.Team.Members[2].Actor.ID++ },
		"changed task body":            func(c *Config, task *Task, p *PR) { task.Body += " changed" },
		"changed task title":           func(c *Config, task *Task, p *PR) { task.Title += " changed" },
		"stale metadata scope":         func(c *Config, task *Task, p *PR) { task.MetadataScopeSHA = BodyHash("old scope") },
		"untrusted metadata":           func(c *Config, task *Task, p *PR) { task.MetadataAuthor.ID = 999 },
		"foreign PR":                   func(c *Config, task *Task, p *PR) { p.URL = "https://github.com/code/foreign/pull/2" },
		"different task":               func(c *Config, task *Task, p *PR) { p.TaskURL += "0" },
		"different goal":               func(c *Config, task *Task, p *PR) { p.GoalURL += "0" },
		"wrong PR author":              func(c *Config, task *Task, p *PR) { p.Author = p.Reviews[0].Author },
	}
	for _, reverse := range []bool{false, true} {
		for name, mutate := range mutations {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+name, func(t *testing.T) {
				c, s, _ := engineeringFixture(t, reverse)
				task, p := s.Tasks[0], s.PRs[0]
				mutate(&c, &task, &p)
				if got, ok := EngineeringReviewFor(c, task, p); ok || !reflect.DeepEqual(got, EngineeringClearance{}) {
					t.Fatalf("unsafe evidence returned clearance: %+v, %v", got, ok)
				}
			})
		}
	}
}

func TestTeamEngineeringLatestPeerDecision(t *testing.T) {
	for _, state := range []string{"CHANGES_REQUESTED", "DISMISSED"} {
		t.Run(state, func(t *testing.T) {
			c, s, _ := engineeringFixture(t, false)
			p := s.PRs[0]
			newer := p.Reviews[0]
			newer.ID, newer.State, newer.SubmittedAt = "11", state, newer.SubmittedAt.Add(time.Second)
			// Slice order is not decision order.
			p.Reviews = []Review{newer, p.Reviews[0]}
			if _, ok := EngineeringReviewFor(c, s.Tasks[0], p); ok {
				t.Fatal("older approval survived a newer peer decision")
			}
			newer.ID, newer.State, newer.SubmittedAt = "12", "APPROVED", newer.SubmittedAt.Add(time.Second)
			p.Reviews = append(p.Reviews, newer)
			if got, ok := EngineeringReviewFor(c, s.Tasks[0], p); !ok || got.ReviewID != "12" {
				t.Fatalf("fresh reapproval not selected: %+v, %v", got, ok)
			}
		})
	}
}

func TestTeamEngineeringUnrelatedReviewsCannotClearOrRevoke(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		c, s, _ := engineeringFixture(t, reverse)
		p := s.PRs[0]
		approved := p.Reviews[0]
		for _, state := range []string{"APPROVED", "CHANGES_REQUESTED", "DISMISSED"} {
			unrelated := approved
			unrelated.ID, unrelated.State = "20", state
			unrelated.SubmittedAt = approved.SubmittedAt.Add(time.Second)
			unrelated.Author = c.Team.Members[2].Actor
			p.Reviews = []Review{unrelated, approved}
			if got, ok := EngineeringReviewFor(c, s.Tasks[0], p); !ok || got.ReviewID != approved.ID {
				t.Fatalf("unassigned actor changed peer decision: %+v, %v", got, ok)
			}
			p.Reviews = []Review{unrelated}
			if _, ok := EngineeringReviewFor(c, s.Tasks[0], p); ok {
				t.Fatal("unassigned actor supplied clearance")
			}
		}
		stale := approved
		stale.ID, stale.HeadSHA, stale.State = "30", BodyHash("previous head"), "CHANGES_REQUESTED"
		stale.SubmittedAt = approved.SubmittedAt.Add(time.Second)
		p.Reviews = []Review{stale, approved}
		if got, ok := EngineeringReviewFor(c, s.Tasks[0], p); !ok || got.ReviewID != approved.ID {
			t.Fatalf("another head changed current-head decision: %+v, %v", got, ok)
		}
	}
}

func TestTeamEngineeringSelectAndAdmitReciprocal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review", "fix", "deliver"} {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind, func(t *testing.T) {
				c, s, now := engineeringFixture(t, reverse)
				peer := s.PRs[0].Reviews[0].Author
				switch kind {
				case "implement":
					s.PRs = nil
				case "review":
					c.Self = peer
					for _, m := range c.Team.Members {
						if sameActor(m.Actor, peer) {
							c.Name = m.Name
						}
					}
					s.PRs[0].Reviews = nil
				case "fix":
					s.PRs[0].Reviews[0].State = "CHANGES_REQUESTED"
				}
				jobs := Select(c, s, newState(), now)
				if len(jobs) != 1 || jobs[0].Kind != kind {
					t.Fatalf("selected %+v, want one %s job", jobs, kind)
				}
				j := jobs[0]
				sha, err := ValidateTeamScope(c, s.Tasks[0], s.Goals[0], now)
				if err != nil {
					t.Fatal(err)
				}
				role := "implementer"
				if kind == "review" {
					role = "engineering-review"
				}
				want := &JobBinding{Version: 2, AssignmentSHA: sha, ExecutorID: c.Self.ID, Role: role}
				if !reflect.DeepEqual(j.Binding, want) || j.ID == "" {
					t.Fatalf("job binding = %+v, ID = %q; want %+v", j.Binding, j.ID, want)
				}
				if ok, reason := Admit(c, j, s, now); !ok {
					t.Fatal("selected job rejected:", reason)
				}
				if got := Select(c, s, State{Jobs: map[string]Job{j.ID: j}}, now); len(got) != 0 {
					t.Fatalf("saved job selected again: %+v", got)
				}
				for _, member := range c.Team.Members {
					if sameActor(member.Actor, c.Self) {
						continue
					}
					other := c
					other.Name, other.Self = member.Name, member.Actor
					if ok, _ := Admit(other, j, s, now); ok {
						t.Fatalf("%s admitted another executor's %s job", member.Name, kind)
					}
					for _, candidate := range Select(other, s, newState(), now) {
						if candidate.Kind == kind {
							t.Fatalf("%s selected unassigned %s job", member.Name, kind)
						}
					}
				}
			})
		}
	}
}

func TestTeamEngineeringBindingAndScopeRevalidation(t *testing.T) {
	for _, mutation := range []string{"head", "scope revision", "assignment", "task scope", "goal scope", "executor", "role", "missing binding", "incomplete snapshot"} {
		t.Run(mutation, func(t *testing.T) {
			c, s, now := engineeringFixture(t, false)
			p := s.PRs[0]
			j, err := bindTeamJob(c, Job{Kind: "deliver", GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA, InputRevision: p.Reviews[0].ID}, s, now)
			if err != nil {
				t.Fatal(err)
			}
			if ok, reason := Admit(c, j, s, now); !ok {
				t.Fatal("fresh bound delivery rejected:", reason)
			}
			switch mutation {
			case "head":
				s.PRs[0].HeadSHA = BodyHash("new head")
			case "scope revision":
				s.PRs[0].ScopeRevision = BodyHash("new durable scope")
				s.PRs[0].ScopeUpdatedAt = now
			case "assignment":
				s.Tasks[0].Assignment.Author, s.Tasks[0].Assignment.Peer = "planner", "engineer"
			case "task scope":
				s.Tasks[0].Body += " changed"
			case "goal scope":
				s.Goals[0].Body += " changed"
				s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
				s.Goals[0].LastEditedAt = now
			case "executor":
				j.Binding.ExecutorID++
			case "role":
				j.Binding.Role = "engineering-review"
			case "missing binding":
				j.Binding = nil
			case "incomplete snapshot":
				s.Complete = false
			}
			if ok, _ := Admit(c, j, s, now); ok {
				t.Fatal("saved delivery survived changed authority or evidence")
			}
		})
	}
}

func TestTeamEngineeringBoardCannotAssignWork(t *testing.T) {
	c, s, now := engineeringFixture(t, false)
	s.PRs = nil
	resolved, err := resolveTeamAssignment(c, s.Tasks[0], s.Goals[0], now)
	if err != nil || resolved.SHA == "" {
		t.Fatalf("resolve valid assignment: %+v, %v", resolved, err)
	}
	s.Tasks[0].ItemID, s.Tasks[0].CurrentStatusID = "different-card", "different-agent"
	changed, err := resolveTeamAssignment(c, s.Tasks[0], s.Goals[0], now)
	if err != nil || !reflect.DeepEqual(resolved, changed) {
		t.Fatalf("board fields changed assignment: %+v, %v", changed, err)
	}
	s.Tasks[0].Assignment = nil
	if _, err := resolveTeamAssignment(c, s.Tasks[0], s.Goals[0], now); err == nil {
		t.Fatal("board-only task resolved an assignment")
	}
	if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
		t.Fatalf("board-only task selected: %+v", jobs)
	}
	j := Job{Kind: "implement", GoalURL: s.Goals[0].URL, TaskURL: s.Tasks[0].URL}
	if _, err := bindTeamJob(c, j, s, now); err == nil {
		t.Fatal("board-only task bound a job")
	}
	if ok, _ := Admit(c, j, s, now); ok {
		t.Fatal("board-only task admitted")
	}
}

func TestTeamEngineeringBindingTracksAssignmentAndExecutor(t *testing.T) {
	c, s, now := engineeringFixture(t, false)
	s.PRs = nil
	j := Job{Kind: "implement", GoalURL: s.Goals[0].URL, TaskURL: s.Tasks[0].URL, InputRevision: s.Tasks[0].BodySHA}
	first, err := bindTeamJob(c, j, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Binding == nil {
		t.Fatalf("unbound job: %+v", first)
	}
	again, err := bindTeamJob(c, j, s, now)
	if err != nil || again.ID != first.ID || !reflect.DeepEqual(again.Binding, first.Binding) {
		t.Fatalf("same snapshot produced different binding: %+v, %v", again, err)
	}
	// This is a new trusted assignment, not a board reassignment. The old
	// author's saved job must not be adopted by the newly assigned author.
	s.Tasks[0].Assignment.Author, s.Tasks[0].Assignment.Peer = "planner", "engineer"
	c.Name, c.Self = c.Team.Members[0].Name, c.Team.Members[0].Actor
	second, err := bindTeamJob(c, j, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Binding.AssignmentSHA == first.Binding.AssignmentSHA || second.Binding.ExecutorID == first.Binding.ExecutorID {
		t.Fatalf("reassignment reused identity: first %+v, second %+v", first, second)
	}
	if ok, _ := Admit(c, first, s, now); ok {
		t.Fatal("new author adopted old author's saved job")
	}
	if ok, reason := Admit(c, second, s, now); !ok {
		t.Fatal("new assignment's own job rejected:", reason)
	}
}

func TestTeamEngineeringDeliveryStillRequiresChecks(t *testing.T) {
	for _, checks := range []string{"success", "none", "pending", "failure", ""} {
		for _, merge := range []string{"CLEAN", "BLOCKED", "UNKNOWN", ""} {
			t.Run(checks+"/"+merge, func(t *testing.T) {
				c, s, now := engineeringFixture(t, false)
				s.PRs[0].ChecksState, s.PRs[0].MergeState = checks, merge
				p := s.PRs[0]
				j, err := bindTeamJob(c, Job{Kind: "deliver", GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA, InputRevision: p.Reviews[0].ID}, s, now)
				if err != nil {
					t.Fatal(err)
				}
				want := (checks == "success" || checks == "none") && merge == "CLEAN"
				if ok, reason := Admit(c, j, s, now); ok != want {
					t.Fatalf("delivery admitted = %v, want %v: %s", ok, want, reason)
				}
			})
		}
	}
}
