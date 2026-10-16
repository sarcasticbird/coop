package worker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type teamEngineGitHub struct {
	*workflowGitHub
	actor  Actor
	reads  int
	change func(int, *Snapshot)
}

func (g *teamEngineGitHub) Revalidate(_ context.Context, _ Job, _ Snapshot) (Snapshot, error) {
	g.reads++
	if g.change != nil {
		g.change(g.reads, &g.snapshot)
	}
	return g.snapshot, nil
}

func (g *teamEngineGitHub) Claim(_ context.Context, j Job, instance string) (Claim, error) {
	g.claims++
	c := Claim{ID: "claim", Marker: marker(j, "claim"), InstanceID: instance, Author: g.actor, Binding: j.Binding}
	if j.Kind == "implement" {
		g.snapshot.Tasks[0].Claim = &c
	}
	return c, nil
}

func (g *teamEngineGitHub) Reconcile(_ context.Context, j Job) (Artifacts, error) {
	a := snapshotArtifacts(Config{}, j, g.snapshot)
	if task, ok := taskFor(g.snapshot, j.TaskURL); ok {
		a.Task = &task
	}
	return a, nil
}

func teamEngineFixture(t *testing.T, reverse bool, kind string) (*Engine, *teamEngineGitHub, *fakeOpenCode, Job) {
	t.Helper()
	c, s, now := engineeringFixture(t, reverse)
	switch kind {
	case "review":
		c.Self = s.PRs[0].Reviews[0].Author
		for _, m := range c.Team.Members {
			if sameActor(m.Actor, c.Self) {
				c.Name = m.Name
			}
		}
		s.PRs[0].Reviews = nil
	case "fix":
		s.PRs[0].Reviews[0].State = "CHANGES_REQUESTED"
	case "implement":
		s.PRs = nil
	}
	c = Defaults(c)
	state := newState()
	state.Version, state.SnapshotCache = 2, s
	state.Attempts = map[string]int{}
	jobs := Select(c, s, state, now)
	if len(jobs) != 1 || jobs[0].Kind != kind {
		t.Fatalf("selected %+v, want %s", jobs, kind)
	}
	g := &teamEngineGitHub{workflowGitHub: &workflowGitHub{snapshot: s}, actor: c.Self}
	code := &fakeOpenCode{}
	e := &Engine{Config: c, GitHub: g, Store: &memoryStore{}, Checkouts: &fakeCheckouts{}, OpenCode: code, Clock: &fakeClock{now: now}, state: state}
	return e, g, code, jobs[0]
}

func TestTeamEngineUnknownSubmissionArtifactsReciprocal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review", "fix", "deliver"} {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind, func(t *testing.T) {
				e, g, code, j := teamEngineFixture(t, reverse, kind)
				code.lost = true
				if err := e.dispatch(context.Background(), j); err != nil {
					t.Fatal(err)
				}
				j = e.state.Jobs[j.ID]
				if j.SubmissionState != "unknown" || code.calls != 1 {
					t.Fatal("submission intent was not retained")
				}
				b := e.state.Sessions[BindingKey(e.Config, j)]
				if !reflect.DeepEqual(b.Binding, j.Binding) || b.Role != j.Binding.Role {
					t.Fatalf("session lost ownership: %+v", b)
				}
				if kind == "review" && (g.claims != 1 || g.snapshot.Tasks[0].Claim != nil) {
					t.Fatal("review PR claim was skipped or replaced the author issue claim")
				}
				switch kind {
				case "implement":
					g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/2", TaskURL: j.TaskURL, GoalURL: j.GoalURL, Author: e.Config.Self, Branch: j.Branch, HeadSHA: BodyHash("new head"), AssignmentSHA: j.Binding.AssignmentSHA}}
				case "review":
					g.snapshot.PRs[0].Reviews = []Review{{ID: "20", HeadSHA: j.HeadSHA, Author: e.Config.Self, State: "CHANGES_REQUESTED", SubmittedAt: e.Clock.Now().Add(time.Second)}}
				case "fix":
					g.snapshot.PRs[0].HeadSHA = BodyHash("fixed head")
				case "deliver":
					g.snapshot.PRs[0].Merged, g.snapshot.PRs[0].Closed = true, true
					g.snapshot.Tasks[0].Completed, g.snapshot.Tasks[0].Closed = true, true
				}
				e.state.SnapshotCache = g.snapshot
				if ok, reason := e.continuationAllowed(j); !ok {
					t.Fatal("artifact continuation rejected:", reason)
				}
				if err := e.observe(context.Background(), j); err != nil {
					t.Fatal(err)
				}
				if e.state.Jobs[j.ID].Phase != "completed" || code.calls != 1 {
					t.Fatal("unknown submission replayed or artifact not completed")
				}
			})
		}
	}
}

func TestTeamEngineArtifactsRejectUnknownOrChangedEvidence(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, mutation := range []string{"task missing", "assignment", "author", "task", "goal", "executor", "role", "contributors", "reviews", "scope", "head", "self review", "wrong peer"} {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+mutation, func(t *testing.T) {
				e, g, _, j := teamEngineFixture(t, reverse, "review")
				g.snapshot.PRs[0].Reviews = []Review{{ID: "20", HeadSHA: j.HeadSHA, Author: e.Config.Self, State: "APPROVED", SubmittedAt: e.Clock.Now().Add(time.Second)}}
				a, _ := g.Reconcile(context.Background(), j)
				if !artifactCompletes(e.Config, j, a) {
					t.Fatal("valid review did not complete")
				}
				switch mutation {
				case "task missing":
					a.Task = nil
				case "assignment":
					a.PR.AssignmentSHA = BodyHash("different assignment")
				case "author":
					a.PR.Author = e.Config.Self
				case "task":
					a.PR.TaskURL += "0"
				case "goal":
					a.PR.GoalURL += "0"
				case "executor":
					j.Binding.ExecutorID++
				case "role":
					j.Binding.Role = "implementer"
				case "contributors":
					a.PR.ContributorsComplete = false
				case "reviews":
					a.PR.ReviewsComplete = false
				case "scope":
					a.PR.ScopeRevision = BodyHash("changed scope")
				case "head":
					a.PR.HeadSHA = BodyHash("changed head")
				case "self review":
					a.PR.Contributors = append(a.PR.Contributors, e.Config.Self)
				case "wrong peer":
					a.PR.Reviews[0].Author = a.PR.Author
				}
				if artifactCompletes(e.Config, j, a) {
					t.Fatal("unsafe evidence completed saved job")
				}
			})
		}
	}
}

func TestTeamEngineContinuationValidatesBindingBeforeArtifacts(t *testing.T) {
	for _, mutation := range []string{"executor", "role", "assignment", "scope", "missing binding", "incomplete"} {
		t.Run(mutation, func(t *testing.T) {
			e, _, _, j := teamEngineFixture(t, false, "deliver")
			e.state.SnapshotCache.PRs[0].Merged = true
			e.state.SnapshotCache.Tasks[0].Completed = true
			switch mutation {
			case "executor":
				j.Binding.ExecutorID++
			case "role":
				j.Binding.Role = "engineering-review"
			case "assignment":
				j.Binding.AssignmentSHA = BodyHash("old assignment")
			case "scope":
				j.PRScopeRevision = BodyHash("old scope")
			case "missing binding":
				j.Binding = nil
			case "incomplete":
				e.state.SnapshotCache.Complete = false
			}
			if ok, _ := e.continuationAllowed(j); ok {
				t.Fatal("completion shortcut bypassed saved ownership")
			}
		})
	}
}

func TestTeamEngineFixNewHeadPreservesBinding(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			e, g, _, j := teamEngineFixture(t, reverse, "fix")
			g.snapshot.PRs[0].HeadSHA = BodyHash("fixed head")
			e.state.SnapshotCache = g.snapshot
			if j.PRScopeRevision == "" || g.snapshot.PRs[0].ScopeRevision != j.PRScopeRevision {
				t.Fatal("head-only update changed durable PR scope")
			}
			if !e.savedBindingAllowed(j) {
				t.Fatal("head-only fix invalidated saved assignment binding")
			}
			if ok, reason := e.continuationAllowed(j); !ok {
				t.Fatal("head-only fix could not reconcile completion:", reason)
			}
			g.snapshot.PRs[0].ScopeRevision = BodyHash("changed durable scope")
			e.state.SnapshotCache = g.snapshot
			if e.savedBindingAllowed(j) {
				t.Fatal("new head bypassed changed durable scope")
			}
			if ok, _ := e.continuationAllowed(j); ok {
				t.Fatal("fix artifact shortcut bypassed changed durable scope")
			}
		})
	}
}

func TestTeamEngineRevalidatesBeforeClaimAndSubmission(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(map[int]string{1: "before claim", 2: "after checkout"}[at], func(t *testing.T) {
			e, g, code, j := teamEngineFixture(t, false, "implement")
			g.change = func(read int, s *Snapshot) {
				if read == at {
					s.Tasks[0].Assignment.Author, s.Tasks[0].Assignment.Peer = "planner", "engineer"
				}
			}
			if err := e.dispatch(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			if code.calls != 0 || g.claims != at-1 || g.reads != at {
				t.Fatalf("reads=%d claims=%d submits=%d", g.reads, g.claims, code.calls)
			}
		})
	}
}

func TestTeamEngineAuthorClaimInstanceRevalidatedBeforeSubmission(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"fix", "deliver"} {
			for _, foreign := range []bool{false, true} {
				name := map[bool]string{false: "forward/", true: "reverse/"}[reverse] + kind + map[bool]string{false: "/same-instance", true: "/foreign-instance"}[foreign]
				t.Run(name, func(t *testing.T) {
					e, g, code, j := teamEngineFixture(t, reverse, kind)
					claim := Claim{ID: "author-claim", Marker: marker(j, "claim"), InstanceID: e.state.InstanceID, Author: e.Config.Self, Binding: j.Binding}
					g.snapshot.Tasks[0].Claim = &claim
					if ok, reason := e.continuationAllowed(j); !ok {
						t.Fatal("same-instance author claim rejected before preparation:", reason)
					}
					g.change = func(_ int, s *Snapshot) {
						binding := e.state.Sessions[BindingKey(e.Config, j)]
						if binding.Checkout == "" || binding.SessionID == "" {
							t.Fatal("claim was not revalidated after checkout/session preparation")
						}
						freshClaim := claim
						if foreign {
							freshClaim.InstanceID = BodyHash("another worker instance")
						}
						s.Tasks[0].Claim = &freshClaim
					}
					if err := e.dispatch(context.Background(), j); err != nil {
						t.Fatal(err)
					}
					if g.reads != 1 || g.claims != 0 {
						t.Fatalf("reads=%d claims=%d, want one final revalidation and no replacement claim", g.reads, g.claims)
					}
					saved := e.Store.(*memoryStore).state.Jobs[j.ID]
					if foreign {
						if code.calls != 0 || saved.Phase != "paused" || saved.WaitReason != "claim" || saved.MessageID != "" || saved.SubmissionState != "" {
							t.Fatalf("foreign-instance claim did not prevent submission: calls=%d phase=%s reason=%s message=%s submission=%s", code.calls, saved.Phase, saved.WaitReason, saved.MessageID, saved.SubmissionState)
						}
					} else if code.calls != 1 || saved.Phase != "active" || saved.WaitReason != "" || saved.SubmissionState != "accepted" {
						t.Fatalf("same-instance author job did not submit: calls=%d phase=%s reason=%s submission=%s", code.calls, saved.Phase, saved.WaitReason, saved.SubmissionState)
					}
				})
			}
		}
	}
}

func TestTeamEnginePromptsUseAssignedJobRole(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review", "fix", "deliver"} {
			e, _, _, j := teamEngineFixture(t, reverse, kind)
			prompt, err := buildPrompt(e.Config, j, e.state.SnapshotCache, "", e.Clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"Role":"` + j.Binding.Role + `"`, `"Author":`, `"Peer":`, `"Binding":`, j.Binding.AssignmentSHA} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s prompt missing %s", kind, want)
				}
			}
			if strings.Contains(prompt, "Hawk") || strings.Contains(prompt, "Robin") || strings.Contains(prompt, `"Implementer":`) || strings.Contains(prompt, "coop-pr:v1") {
				t.Fatal("team prompt retained global-role assumptions")
			}
			if kind == "review" && (!strings.Contains(prompt, "read-only review branch") || !strings.Contains(prompt, j.PRScopeRevision) || strings.Contains(prompt, "You plan and independently review")) {
				t.Fatal("review received wrong instructions or scope")
			}
		}
	}
}

func TestTeamEnginePromptBudgetIncludesBinding(t *testing.T) {
	e, _, _, j := teamEngineFixture(t, true, "review")
	base, err := buildPrompt(e.Config, j, e.state.SnapshotCache, "", e.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	policy := strings.Repeat("x", 32768-len(base))
	prompt, err := buildPrompt(e.Config, j, e.state.SnapshotCache, policy, e.Clock.Now())
	if err != nil || len(prompt) != 32768 {
		t.Fatalf("exact boundary: bytes=%d err=%v", len(prompt), err)
	}
	prompt, err = buildPrompt(e.Config, j, e.state.SnapshotCache, policy+"!", e.Clock.Now())
	var size *PromptSizeError
	if prompt != "" || !errors.As(err, &size) || size.ActualBytes != 32769 {
		t.Fatalf("binding prompt escaped common budget: %v", err)
	}
}

func TestTeamEnginePlannerPromptDoesNotAssumeAuthorship(t *testing.T) {
	c, s, now := engineeringFixture(t, true)
	c.Self = teamPlanner(c)
	s.Events = []Event{{ID: "owner-planning", URL: s.Goals[0].URL, Kind: "planning", Revision: "owner-revision", Author: c.Owner}}
	j, err := bindTeamJob(c, Job{Kind: "plan", GoalURL: s.Goals[0].URL, TaskURL: s.Events[0].ID, InputRevision: s.Events[0].Revision}, s, now)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := buildPrompt(c, j, s, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"Role":"planner"`) || !strings.Contains(prompt, "Planner status does not determine code authorship") || strings.Contains(prompt, "coop-task:v1") || strings.Contains(prompt, "Robin") || strings.Contains(prompt, "Hawk") {
		t.Fatal("planner prompt assumed global engineering roles")
	}
}

func TestTeamEngineSyncBoardUsesValidatedAssignment(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			e, g, _, j := teamEngineFixture(t, reverse, "deliver")
			e.Config.Self = teamPlanner(e.Config)
			task := &e.state.SnapshotCache.Tasks[0]
			task.ProjectID, task.InProgressID, task.DoneID = "project", "progress", "done"
			task.Completed = true
			task.Claim = &Claim{Marker: "claim", InstanceID: e.state.InstanceID, Author: g.actor, Binding: j.Binding}
			e.state.SnapshotCache.PRs[0].Merged = true
			if stale {
				task.Body += " changed"
			}
			if err := e.syncBoard(context.Background()); err != nil {
				t.Fatal(err)
			}
			if stale && g.syncs != 0 || !stale && (g.syncs != 1 || e.state.SnapshotCache.Tasks[0].CurrentStatusID != "done") {
				t.Fatalf("reverse=%v stale=%v syncs=%d", reverse, stale, g.syncs)
			}
		}
	}
}

func TestTeamEngineReviewRevalidatesIndependenceBeforeSubmission(t *testing.T) {
	for _, at := range []int{1, 2} {
		e, g, code, j := teamEngineFixture(t, true, "review")
		g.change = func(read int, s *Snapshot) {
			if read == at {
				s.PRs[0].Contributors = append(s.PRs[0].Contributors, e.Config.Self)
			}
		}
		if err := e.dispatch(context.Background(), j); err != nil {
			t.Fatal(err)
		}
		if code.calls != 0 || g.claims != at-1 || g.reads != at {
			t.Fatalf("unsafe review dispatched: reads=%d claims=%d submits=%d", g.reads, g.claims, code.calls)
		}
	}
}
