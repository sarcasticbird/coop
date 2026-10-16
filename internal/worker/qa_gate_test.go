package worker

import (
	"reflect"
	"testing"
	"time"
)

// Unlike the parser fixture, this binds QA to the actual approved goal scope.
func qaGateFixture(t *testing.T) (Config, Snapshot, time.Time) {
	t.Helper()
	c, s, now := engineeringFixture(t, false)
	r, q := qaResultFixture()
	task, goal, p := &s.Tasks[0], &s.Goals[0], &s.PRs[0]
	task.QA = q
	goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{task.Repo: task.Assignment.BaseBranch}, QA: q})
	goal.BodySHA = BodyHash(goal.Body)
	task.Assignment.QA, task.Assignment.GoalBodySHA = q.Reviewer, goal.BodySHA
	sha, err := ValidateTeamScope(c, *task, *goal, now)
	if err != nil {
		t.Fatal(err)
	}
	p.AssignmentSHA = sha
	p.QAArtifact = &QAArtifact{Kind: "source", HeadSHA: p.HeadSHA, SHA256: r.ArtifactSHA256, URL: "https://api.github.com/repos/" + task.Repo + "/tarball/" + p.HeadSHA, Revision: "source-bundle-v1"}
	qaGateResult(t, &s, "pass", now)
	return c, s, now.Add(time.Minute)
}

func qaGateResult(t *testing.T, s *Snapshot, conclusion string, at time.Time) {
	t.Helper()
	task, p := s.Tasks[0], &s.PRs[0]
	r, _ := qaResultFixture()
	r.Conclusion = conclusion
	r.Repo, r.PRURL, r.TaskURL, r.GoalURL = task.Repo, p.URL, task.URL, task.GoalURL
	r.HeadSHA, r.AssignmentSHA, r.ArtifactSHA256 = p.HeadSHA, p.AssignmentSHA, p.QAArtifact.SHA256
	r.AcceptanceSHA, r.Environment, r.EnvironmentRevision = task.QA.AcceptanceSHA, task.QA.Environment, task.QA.EnvironmentRevision
	r.Evidence = []string{task.GoalURL}
	// The roster's third member is the independently assigned QA actor.
	p.Reviews = append(p.Reviews, Review{ID: "20", Author: teamPolicyFixture().Members[2].Actor, HeadSHA: p.HeadSHA, SubmittedAt: at, State: map[string]string{"pass": "APPROVED", "changes-needed": "CHANGES_REQUESTED", "blocked": "COMMENTED"}[conclusion], Body: qaMarker(r)})
}

func qaGateActor(c Config, name string) Config {
	for _, member := range c.Team.Members {
		if member.Name == name {
			c.Name, c.Self = member.Name, member.Actor
		}
	}
	return c
}

func qaGateJob(t *testing.T, c Config, s Snapshot, now time.Time, kind string) Job {
	t.Helper()
	p := s.PRs[0]
	revision := qaRevision(p)
	switch kind {
	case "deliver":
		revision = p.Reviews[0].ID + ":qa:20"
	case "fix":
		revision = "qa:20"
	}
	j, err := bindTeamJob(c, Job{Kind: kind, GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA, InputRevision: revision, QAArtifact: p.QAArtifact}, s, now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func qaGateSelected(t *testing.T, c Config, s Snapshot, now time.Time, kind string) Job {
	t.Helper()
	jobs := Select(c, s, newState(), now)
	if len(jobs) != 1 || jobs[0].Kind != kind {
		t.Fatalf("selected %+v, want exactly one %s job", jobs, kind)
	}
	if ok, reason := Admit(c, jobs[0], s, now); !ok {
		t.Fatalf("selected %s rejected: %s", kind, reason)
	}
	return jobs[0]
}

func TestQAGateDeliveryRequiresBothIndependentGates(t *testing.T) {
	for _, gates := range []string{"both", "engineering only", "QA only", "neither"} {
		t.Run(gates, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			j := qaGateJob(t, c, s, now, "deliver")
			switch gates {
			case "engineering only":
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			case "QA only":
				s.PRs[0].Reviews = s.PRs[0].Reviews[1:]
			case "neither":
				s.PRs[0].Reviews = nil
			}
			want := gates == "both"
			if ok, reason := Admit(c, j, s, now); ok != want {
				t.Fatalf("delivery admission=%v, want %v: %s", ok, want, reason)
			}
			jobs := Select(c, s, newState(), now)
			if want {
				qaGateSelected(t, c, s, now, "deliver")
			} else if len(jobs) != 0 {
				t.Fatalf("incomplete gates selected author work: %+v", jobs)
			}
		})
	}
	// Internal work retains its engineering-only path.
	c, s, now := engineeringFixture(t, false)
	j := qaGateSelected(t, c, s, now, "deliver")
	if j.QAArtifact != nil || j.InputRevision != s.PRs[0].Reviews[0].ID {
		t.Fatalf("internal delivery acquired a QA dependency: %+v", j)
	}
}

func TestQAGateUnsafeEvidenceRejectsSavedDelivery(t *testing.T) {
	mutations := map[string]func(*Config, *Snapshot){
		"malformed native result":    func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].Body = "not a result" },
		"edited":                     func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].LastEditedAt = s.PRs[0].Reviews[1].SubmittedAt },
		"dismissed":                  func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].State = "DISMISSED" },
		"wrong native state":         func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].State = "COMMENTED" },
		"wrong actor":                func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].Author.ID++ },
		"wrong actor kind":           func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].Author.Kind = "User" },
		"stale policy":               func(c *Config, s *Snapshot) { s.Tasks[0].QA.AcceptanceSHA = BodyHash("new acceptance") },
		"stale environment":          func(c *Config, s *Snapshot) { s.Tasks[0].QA.Environment = "new environment" },
		"stale environment revision": func(c *Config, s *Snapshot) { s.Tasks[0].QA.EnvironmentRevision = "new revision" },
		"stale artifact": func(c *Config, s *Snapshot) {
			a := *s.PRs[0].QAArtifact
			a.SHA256 = BodyHash("new artifact")
			s.PRs[0].QAArtifact = &a
		},
		"stale head":          func(c *Config, s *Snapshot) { s.PRs[0].HeadSHA = BodyHash("new head") },
		"stale native head":   func(c *Config, s *Snapshot) { s.PRs[0].Reviews[1].HeadSHA = BodyHash("old head") },
		"engineering revoked": func(c *Config, s *Snapshot) { s.PRs[0].Reviews[0].State = "CHANGES_REQUESTED" },
		"incomplete reviews":  func(c *Config, s *Snapshot) { s.PRs[0].ReviewsComplete = false },
		"QA contributed": func(c *Config, s *Snapshot) {
			s.PRs[0].Contributors = append(s.PRs[0].Contributors, s.PRs[0].Reviews[1].Author)
		},
		"missing reviewer": func(c *Config, s *Snapshot) { c.Team.Members = c.Team.Members[:2] },
		"paused goal": func(c *Config, s *Snapshot) {
			s.Goals[0].Decisions = append(s.Goals[0].Decisions, Decision{ID: "2", Author: c.Owner, Command: "/coop pause", CreatedAt: s.PRs[0].Reviews[1].SubmittedAt})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			j := qaGateSelected(t, c, s, now, "deliver")
			mutate(&c, &s)
			if ok, _ := Admit(c, j, s, now); ok {
				t.Fatal("unsafe evidence admitted saved delivery")
			}
			for _, candidate := range Select(c, s, newState(), now) {
				if candidate.Kind == "deliver" {
					t.Fatal("unsafe evidence selected fresh delivery")
				}
			}
		})
	}
}

func TestQAGateSelectionWaitsForEngineeringAndChecks(t *testing.T) {
	for _, checks := range []string{"success", "none", "pending", "failure", ""} {
		for _, engineering := range []bool{true, false} {
			t.Run(checks+"/engineering="+map[bool]string{true: "yes", false: "no"}[engineering], func(t *testing.T) {
				c, s, now := qaGateFixture(t)
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
				s.PRs[0].ChecksState = checks
				if !engineering {
					s.PRs[0].Reviews = nil
				}
				qa := qaGateActor(c, s.Tasks[0].Assignment.QA)
				j := qaGateJob(t, qa, s, now, "qa")
				want := engineering && (checks == "success" || checks == "none")
				if ok, reason := Admit(qa, j, s, now); ok != want {
					t.Fatalf("QA admission=%v, want %v: %s", ok, want, reason)
				}
				jobs := Select(qa, s, newState(), now)
				if want {
					selected := qaGateSelected(t, qa, s, now, "qa")
					if selected.Binding.Role != "customer-qa" || selected.Binding.ExecutorID != qa.Self.ID {
						t.Fatalf("wrong QA binding: %+v", selected.Binding)
					}
				} else if len(jobs) != 0 {
					t.Fatalf("premature QA selection: %+v", jobs)
				}
				for _, member := range c.Team.Members {
					if member.Name == qa.Name {
						continue
					}
					other := qaGateActor(c, member.Name)
					if ok, _ := Admit(other, j, s, now); ok {
						t.Fatalf("%s admitted QA job", member.Name)
					}
					for _, candidate := range Select(other, s, newState(), now) {
						if candidate.Kind == "qa" {
							t.Fatalf("%s selected QA job", member.Name)
						}
					}
				}
			})
		}
	}
	for _, merge := range []string{"BLOCKED", "UNKNOWN", ""} {
		c, s, now := qaGateFixture(t)
		s.PRs[0].Reviews, s.PRs[0].MergeState = s.PRs[0].Reviews[:1], merge
		qa := qaGateActor(c, "qa")
		// Required QA approval may itself be the outstanding branch protection.
		qaGateSelected(t, qa, s, now, "qa")
		qaGateResult(t, &s, "pass", now)
		if ok, _ := Admit(c, qaGateJob(t, c, s, now, "deliver"), s, now); ok {
			t.Fatalf("merge state %q admitted delivery", merge)
		}
	}
}

func TestQAGateNativeEvidenceCannotReviveOlderPass(t *testing.T) {
	for _, fault := range []string{"malformed", "edited", "dismissed", "wrong state", "wrong actor", "policy", "environment", "environment revision", "artifact", "head", "assignment"} {
		t.Run(fault, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			qa := qaGateActor(c, "qa")
			// Bind the running job before either native QA result exists.
			pass := s.PRs[0].Reviews[1]
			s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			j := qaGateSelected(t, qa, s, now, "qa")
			s.PRs[0].Reviews = append(s.PRs[0].Reviews, pass)
			latest := pass
			latest.ID, latest.SubmittedAt = "21", now
			r, err := ParseQAResult(latest.Body)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "malformed":
				latest.Body = "bad native result"
			case "edited":
				latest.LastEditedAt = now
			case "dismissed":
				latest.State = "DISMISSED"
			case "wrong state":
				latest.State = "COMMENTED"
			case "wrong actor":
				// An unassigned actor cannot supply evidence at all. Unlike a
				// bad assigned result, it must not revoke a real assigned pass.
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
				latest.Author.ID++
			case "policy":
				r.AcceptanceSHA = BodyHash("old acceptance")
			case "environment":
				r.Environment = "old environment"
			case "environment revision":
				r.EnvironmentRevision = "old revision"
			case "artifact":
				r.ArtifactSHA256 = BodyHash("old artifact")
			case "head":
				r.HeadSHA = BodyHash("old head")
			case "assignment":
				r.AssignmentSHA = BodyHash("old assignment")
			}
			if fault != "malformed" {
				latest.Body = qaMarker(r)
			}
			s.PRs[0].Reviews = append(s.PRs[0].Reviews, latest)
			if artifactCompletes(qa, j, Artifacts{Task: &s.Tasks[0], PR: &s.PRs[0]}) {
				t.Fatal("unsafe native result completed QA")
			}
			if ok, _ := Admit(c, qaGateJob(t, c, s, now, "deliver"), s, now); ok {
				t.Fatal("unsafe native result cleared delivery")
			}
			for _, candidate := range Select(c, s, newState(), now) {
				if candidate.Kind == "deliver" {
					t.Fatal("unsafe native result selected delivery")
				}
			}
		})
	}
}

func TestQAGateMissingAuthorityNeverWaivesQA(t *testing.T) {
	for _, fault := range []string{"missing reviewer", "missing assignment", "missing artifact", "paused goal"} {
		t.Run(fault, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			qa := qaGateActor(c, "qa")
			j := qaGateSelected(t, qa, s, now, "qa")
			switch fault {
			case "missing reviewer":
				c.Team.Members = c.Team.Members[:2]
			case "missing assignment":
				s.Tasks[0].Assignment.QA = ""
			case "missing artifact":
				s.PRs[0].QAArtifact = nil
			case "paused goal":
				s.Goals[0].Decisions = append(s.Goals[0].Decisions, Decision{ID: "2", Author: c.Owner, Command: "/coop pause", CreatedAt: now})
			}
			qa.Team = c.Team
			if ok, _ := Admit(qa, j, s, now); ok {
				t.Fatal("QA admitted without required authority or artifact")
			}
			for _, actor := range []Config{c, qa} {
				if jobs := Select(actor, s, newState(), now); len(jobs) != 0 {
					t.Fatalf("missing QA prerequisite selected work: %+v", jobs)
				}
			}
		})
	}
}

func TestQAGateDefectsRequireAuthorFixNewReviewThenQA(t *testing.T) {
	c, s, now := qaGateFixture(t)
	s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
	qaGateResult(t, &s, "changes-needed", now)
	fix := qaGateSelected(t, c, s, now, "fix")
	if fix.InputRevision != "qa:20" || !qaArtifactEqual(fix.QAArtifact, s.PRs[0].QAArtifact) {
		t.Fatalf("fix lost QA origin: %+v", fix)
	}
	qa := qaGateActor(c, "qa")
	if jobs := Select(qa, s, newState(), now); len(jobs) != 0 {
		t.Fatalf("terminal defects rescheduled QA: %+v", jobs)
	}
	p := &s.PRs[0]
	p.HeadSHA = BodyHash("fixed head")
	a := *p.QAArtifact
	a.HeadSHA, a.SHA256, a.URL = p.HeadSHA, BodyHash("fixed artifact"), "https://api.github.com/repos/code/repo/tarball/"+p.HeadSHA
	p.QAArtifact = &a
	if !artifactCompletes(c, fix, Artifacts{Task: &s.Tasks[0], PR: p}) {
		t.Fatal("new head did not complete author fix")
	}
	if jobs := Select(qa, s, newState(), now); len(jobs) != 0 {
		t.Fatalf("new head skipped engineering: %+v", jobs)
	}
	peer := qaGateActor(c, s.Tasks[0].Assignment.Peer)
	qaGateSelected(t, peer, s, now, "review")
	review := p.Reviews[0]
	review.ID, review.HeadSHA, review.SubmittedAt = "11", p.HeadSHA, now.Add(time.Minute)
	p.Reviews = append(p.Reviews, review)
	qaGateSelected(t, qa, s, now.Add(2*time.Minute), "qa")
	if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
		t.Fatalf("new engineering approval reused old QA: %+v", jobs)
	}
	qaGateResult(t, &s, "pass", now.Add(2*time.Minute))
	qaGateSelected(t, c, s, now.Add(3*time.Minute), "deliver")
}

func TestQAGateTerminalResultsCompleteQAWithoutWaivingDelivery(t *testing.T) {
	for _, conclusion := range []string{"pass", "changes-needed", "blocked"} {
		t.Run(conclusion, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			qa := qaGateActor(c, "qa")
			j := qaGateSelected(t, qa, s, now, "qa")
			if artifactCompletes(qa, j, Artifacts{Task: &s.Tasks[0], PR: &s.PRs[0]}) {
				t.Fatal("QA completed without native result")
			}
			qaGateResult(t, &s, conclusion, now)
			if !artifactCompletes(qa, j, Artifacts{Task: &s.Tasks[0], PR: &s.PRs[0]}) {
				t.Fatalf("valid %s did not complete QA", conclusion)
			}
			if jobs := Select(qa, s, newState(), now); len(jobs) != 0 {
				t.Fatalf("terminal %s retried: %+v", conclusion, jobs)
			}
			delivery := qaGateJob(t, c, s, now, "deliver")
			if ok, _ := Admit(c, delivery, s, now); ok != (conclusion == "pass") {
				t.Fatalf("%s delivery admission=%v", conclusion, ok)
			}
			if artifactCompletes(c, delivery, Artifacts{Task: &s.Tasks[0], PR: &s.PRs[0]}) {
				t.Fatal("QA result completed unmerged delivery")
			}
		})
	}
}

func TestQAGateSavedBindingRestartPreservesArtifactIdentity(t *testing.T) {
	c, s, now := qaGateFixture(t)
	s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
	qa := qaGateActor(c, "qa")
	j := qaGateSelected(t, qa, s, now, "qa")
	state := newState()
	state.Version = 2
	state.Jobs[j.ID] = j
	store := NewVersionedFileStore(storeFixture(t).Dir, 2)
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved := restarted.Jobs[j.ID]
	if !reflect.DeepEqual(saved, j) {
		t.Fatalf("restart changed bound QA job: got %+v, want %+v", saved, j)
	}
	if ok, reason := Admit(qa, saved, s, now); !ok {
		t.Fatal("restarted job rejected:", reason)
	}
	if jobs := Select(qa, s, restarted, now); len(jobs) != 0 {
		t.Fatalf("restart duplicated QA job: %+v", jobs)
	}
	for _, field := range []string{"digest", "head", "URL", "revision", "kind"} {
		t.Run(field, func(t *testing.T) {
			s := s
			s.PRs = append([]PR(nil), s.PRs...)
			s.PRs[0].Reviews = append([]Review(nil), s.PRs[0].Reviews...)
			changed := saved
			a := *saved.QAArtifact
			changed.QAArtifact = &a
			switch field {
			case "digest":
				a.SHA256 = BodyHash("different artifact")
			case "head":
				a.HeadSHA = BodyHash("different head")
			case "URL":
				a.URL += "0"
			case "revision":
				a.Revision += "-changed"
			case "kind":
				a.Kind = "workflow"
			}
			if ok, _ := Admit(qa, changed, s, now); ok {
				t.Fatal("changed saved artifact admitted")
			}
			qaGateResult(t, &s, "pass", now)
			if artifactCompletes(qa, changed, Artifacts{Task: &s.Tasks[0], PR: &s.PRs[0]}) {
				t.Fatal("changed saved artifact completed QA")
			}
		})
	}
}
