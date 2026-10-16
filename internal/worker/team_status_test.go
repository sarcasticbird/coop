package worker

import (
	"strings"
	"testing"
)

func TestTeamStatusHistoricalFailuresAreNotCurrentBlockers(t *testing.T) {
	for _, reason := range []string{"execution", "scope", "prompt_size"} {
		for _, phase := range []string{"blocked", "waiting", "completed"} {
			t.Run(reason+"/"+phase, func(t *testing.T) {
				e, _, _, j := teamEngineFixture(t, false, "implement")
				j.Phase, j.WaitReason = phase, reason
				e.state.Jobs[j.ID] = j
				e.state.SnapshotCache.Tasks[0].Completed = true
				e.state.SnapshotCache.Tasks[0].Closed = true
				e.status.State = "working"
				e.idleStatus()
				if e.status.State != "idle" || e.status.WaitReason != "" || e.status.Current != nil {
					t.Fatalf("historical failure became current: %+v", e.status)
				}
				inspection := InspectWork(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
				if len(inspection.History) != 1 || inspection.History[0].WaitReason != reason || len(inspection.Blocked) != 0 || len(inspection.Jobs) != 0 {
					t.Fatalf("history hidden or completed work reopened: %+v", inspection)
				}
			})
		}
	}
}

func TestTeamStatusCurrentBlockerAndStaleScope(t *testing.T) {
	e, _, _, j := teamEngineFixture(t, false, "implement")
	j.Phase, j.WaitReason = "blocked", "execution"
	e.state.Jobs[j.ID] = j
	e.idleStatus()
	if e.status.State != "blocked" || e.status.WaitReason != "execution" || e.status.Current == nil || e.status.Current.TaskURL != j.TaskURL || e.status.Current.Peer == nil || e.status.Current.Author.ID != e.Config.Self.ID {
		t.Fatalf("current blocker lost assignment: %+v", e.status)
	}
	e.state.SnapshotCache.Tasks[0].BodySHA = strings.Repeat("f", 64)
	e.status = Status{}
	e.idleStatus()
	if e.status.WaitReason == "execution" {
		t.Fatal("old scope is still the current execution blocker")
	}
	i := InspectWork(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
	if len(i.History) != 1 {
		t.Fatal("old scope failure disappeared from history")
	}
}

func TestTeamStatusActualActiveIdentityAndModel(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		e, g, _, j := teamEngineFixture(t, reverse, "review")
		j.Phase, j.SessionID, j.MessageID, j.SubmissionState = "active", "ses_status", "msg_status", "running"
		j.SubmittedModel = &ModelIdentity{ProviderID: "fixture", ModelID: "test", Variant: "high"}
		j.ExecutedModel = &ModelIdentity{ProviderID: "fixture", ModelID: "test", Variant: "medium"}
		e.state.Jobs[j.ID] = j
		e.initialized = true // Test status without enabling the fenced v2 startup.
		// Defer observation so this test only exercises current status publication.
		e.nextObserve = e.Clock.Now().Add(e.Config.Limits.TurnLimit)
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		w := e.status.Current
		if w == nil || w.Actor.ID != e.Config.Self.ID || w.Role != "engineering-review" || w.TaskURL != j.TaskURL || w.HeadSHA != j.HeadSHA || w.Peer == nil || w.Peer.ID != e.Config.Self.ID || w.Author.ID != g.snapshot.PRs[0].Author.ID || w.SubmittedModel.Variant != "high" || w.ExecutedModel.Variant != "medium" {
			t.Fatalf("current identity invented or omitted: %+v", w)
		}
	}
}

func TestTeamStatusClosedAuditNeverReturnsInProgress(t *testing.T) {
	e, g, _, _, _ := engineFixture(t)
	e.Config.Role, e.Config.Self = "planner", e.Config.Reviewer
	g.snapshot.Tasks[0].Completed, g.snapshot.Tasks[0].Closed = true, true
	g.snapshot.Tasks[0].Claim = &Claim{Author: e.Config.Implementer, InstanceID: "old", Marker: "old"}
	g.snapshot.Tasks[0].ProjectID, g.snapshot.Tasks[0].CurrentStatusID = "project", "done"
	g.snapshot.Tasks[0].DoneID, g.snapshot.Tasks[0].InProgressID = "done", "progress"
	e.state.SnapshotCache = g.snapshot
	if err := e.syncBoard(t.Context()); err != nil {
		t.Fatal(err)
	}
	if g.syncs != 0 {
		t.Fatal("completed audit card reopened by old claim")
	}
}

func TestTeamStatusStagesAndCurrentInspection(t *testing.T) {
	for _, kind := range []string{"implement", "fix", "deliver"} {
		for _, phase := range []string{"active", "waiting", "blocked", "recovery"} {
			e, _, _, j := teamEngineFixture(t, false, kind)
			j.Phase = phase
			e.state.Jobs[j.ID] = j
			w := currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
			if w.Stage != kind || w.Phase != phase || w.Role != "implementer" {
				t.Fatalf("lost stage: %+v", w)
			}
			if phase == "active" || phase == "recovery" {
				e.state.SnapshotCache.Complete = false
			}
			i := InspectWork(e.Config, e.state.SnapshotCache, e.state, e.Clock.Now())
			if len(i.Current) != 1 || len(i.History) != 0 || i.Current[0].ID != j.ID {
				t.Fatalf("current work hidden as history: %+v", i)
			}
		}
	}
}

func TestTeamStatusDoesNotMixAssignments(t *testing.T) {
	e, _, _, j := teamEngineFixture(t, false, "review")
	b := *j.Binding
	b.AssignmentSHA = strings.Repeat("f", 64)
	j.Binding = &b
	w := currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
	if w.Author != nil || w.Peer != nil || w.QA != nil {
		t.Fatalf("new assignment attached to old turn: %+v", w)
	}
}

func TestTeamStatusQAPassAdvancesDelivery(t *testing.T) {
	c, s, now := qaGateFixture(t)
	for _, change := range []string{"pass", "blocked", "changes-needed", "artifact", "checks"} {
		t.Run(change, func(t *testing.T) {
			c, s, now := c, s, now
			s.PRs = append([]PR{}, s.PRs...)
			s.PRs[0].Reviews = append([]Review{}, s.PRs[0].Reviews...)
			switch change {
			case "blocked", "changes-needed":
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
				qaGateResult(t, &s, change, now)
			case "artifact":
				s.PRs[0].QAArtifact = nil
			case "checks":
				s.PRs[0].ChecksState = "pending"
			}
			e := Engine{Config: c, state: State{SnapshotCache: s}, Clock: &fakeClock{now: now}}
			e.idleStatus()
			wantStage := map[string]string{"pass": "deliver", "blocked": "qa", "changes-needed": "fix", "artifact": "qa", "checks": "review"}[change]
			wantReason := map[string]string{"pass": "merge_eligibility", "blocked": "qa_blocked", "changes-needed": "qa_changes-needed", "artifact": "qa_artifact", "checks": "checks"}[change]
			if e.status.Current == nil || e.status.Current.QA == nil || e.status.Current.Stage != wantStage || e.status.WaitReason != wantReason {
				t.Fatalf("wrong QA status: %+v current=%+v", e.status, e.status.Current)
			}
		})
	}
}

func TestTeamStatusCancelledCardIsNotCompleted(t *testing.T) {
	e, g, _, _, _ := engineFixture(t)
	e.Config.Role, e.Config.Self = "planner", e.Config.Reviewer
	g.snapshot.Tasks[0].Completed, g.snapshot.Tasks[0].Closed = false, true
	g.snapshot.Tasks[0].Claim = &Claim{Author: e.Config.Implementer, InstanceID: "old", Marker: "old"}
	g.snapshot.Tasks[0].ProjectID, g.snapshot.Tasks[0].CurrentStatusID = "project", "cancelled"
	g.snapshot.Tasks[0].DoneID, g.snapshot.Tasks[0].InProgressID = "done", "progress"
	e.state.SnapshotCache = g.snapshot
	if err := e.syncBoard(t.Context()); err != nil {
		t.Fatal(err)
	}
	if g.syncs != 0 {
		t.Fatal("cancelled task was completed or reopened")
	}
}

func TestTeamStatusWaitingRoleIsLocalActorNotStageOwner(t *testing.T) {
	for _, name := range []string{"engineer", "planner", "qa"} {
		for _, stage := range []string{"qa", "deliver", "fix"} {
			c, s, now := qaGateFixture(t)
			c = qaGateActor(c, name)
			switch stage {
			case "qa":
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
			case "fix":
				s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
				s.PRs[0].Reviews[0].State = "CHANGES_REQUESTED"
			}
			e := Engine{Config: c, state: State{SnapshotCache: s}, Clock: &fakeClock{now: now}}
			e.idleStatus()
			want := map[string]string{"engineer": "implementer", "planner": "engineering-review", "qa": "customer-qa"}[name]
			if e.status.Current == nil || e.status.Current.Actor.ID != c.Self.ID || e.status.Current.Role != want || e.status.Current.Stage != stage {
				t.Fatalf("%s/%s: %+v", name, stage, e.status.Current)
			}
			if stage == "fix" && e.status.WaitReason != "changes_requested" {
				t.Fatal("changes request hidden as awaiting review")
			}
		}
	}
}
