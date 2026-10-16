package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTeamEngineeringRejectsUnauthorizedBase(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		c, s, now := engineeringFixture(t, reverse)
		s.PRs[0].BaseBranch = "main"
		if _, ok := EngineeringReviewFor(c, s.Tasks[0], s.PRs[0]); ok {
			t.Fatal("main target cleared under feature-only scope")
		}
		if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
			t.Fatalf("unauthorized target selected: %+v", jobs)
		}
	}
}

func TestTeamBaseApprovedTargetReciprocal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			c, s, now := engineeringFixture(t, reverse)
			if s.PRs[0].BaseBranch != "feature/integration" {
				t.Fatal("fixture must target the approved feature branch")
			}
			if _, ok := EngineeringReviewFor(c, s.Tasks[0], s.PRs[0]); !ok {
				t.Fatal("approved feature target did not clear engineering")
			}
			if jobs := Select(c, s, newState(), now); len(jobs) != 1 || jobs[0].Kind != "deliver" {
				t.Fatalf("approved feature target did not select delivery: %+v", jobs)
			}
		})
	}
}

func TestTeamBaseGoalTargetPolicyFailsClosed(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, scenario := range []string{"no_policy", "missing_repository", "foreign_repository", "invalid_repository", "noncanonical_repository", "invalid_branch", "empty_branch"} {
			t.Run(fmt.Sprintf("reverse=%t/%s", reverse, scenario), func(t *testing.T) {
				c, s, now := engineeringFixture(t, reverse)
				goal, task := &s.Goals[0], &s.Tasks[0]
				m, err := ParseTeamGoalMetadata(goal.Body)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "no_policy":
					m.BaseBranches = nil
				case "missing_repository":
					m.Repositories = append(m.Repositories, "code/second")
				case "foreign_repository":
					m.BaseBranches = map[string]string{"code/foreign": "feature/integration"}
				case "invalid_repository":
					m.BaseBranches = map[string]string{"not a repository": "feature/integration"}
				case "noncanonical_repository":
					m.BaseBranches = map[string]string{"CODE/REPO": "feature/integration"}
				case "invalid_branch":
					m.BaseBranches[task.Repo] = "feature/../main"
				case "empty_branch":
					m.BaseBranches[task.Repo] = ""
				}
				goal.Body = teamMarker("goal", m)
				if scenario == "no_policy" {
					goal.Body = strings.Replace(goal.Body, `"base_branches":null,`, "", 1)
				}
				goal.BodySHA, goal.Repositories = BodyHash(goal.Body), m.Repositories
				goal.LastEditedAt = now.Add(time.Minute)
				goal.Decisions = []Decision{{ID: "renewed", Author: c.Owner, Command: "/coop approve", CreatedAt: now.Add(2 * time.Minute)}}
				task.Assignment.GoalBodySHA = goal.BodySHA
				now = now.Add(3 * time.Minute)
				if EvaluateGoal(c, *goal, now).State == "approved" {
					t.Fatal("owner approval bypassed invalid goal target policy")
				}
				if _, err := ParseTeamGoalMetadata(goal.Body); err == nil {
					t.Fatal("invalid target policy parsed")
				}
				if _, err := ValidateTeamScope(c, *task, *goal, now); err == nil {
					t.Fatal("invalid target policy authorized assignment")
				}
				if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
					t.Fatalf("invalid target policy selected work: %+v", jobs)
				}
			})
		}
	}
}

func TestTeamBasePlannerCannotOverrideGoal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			c, s, now := engineeringFixture(t, reverse)
			task := &s.Tasks[0]
			a := *task.Assignment
			a.BaseBranch = "main"
			comment := Comment{ID: "retarget", Author: teamPlanner(c), CreatedAt: now, Body: teamMarker("task", TeamTaskMetadata{Goal: task.GoalURL, ScopeSHA: task.BodySHA, Assignment: a})}
			if err := ApplyTaskMetadata(c, task, []Comment{comment}); err != nil {
				t.Fatal(err)
			}
			sha, err := AssignmentSHA(*c.Team, task.Repo, task.URL, task.GoalURL, a, QAPolicy{})
			if err != nil || sha == s.PRs[0].AssignmentSHA {
				t.Fatalf("target must participate in assignment digest: %s %v", sha, err)
			}
			s.PRs[0].BaseBranch, s.PRs[0].AssignmentSHA = "main", sha
			if _, err := ValidateTeamScope(c, *task, s.Goals[0], now); err == nil {
				t.Fatal("planner assignment overrode owner-approved feature target")
			}
			if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
				t.Fatalf("planner retarget selected work: %+v", jobs)
			}
		})
	}
}

func TestTeamBaseChangedApprovedGoalRequiresNewAssignment(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			c, s, now := engineeringFixture(t, reverse)
			goal, task, pr := &s.Goals[0], &s.Tasks[0], &s.PRs[0]
			oldSHA := pr.AssignmentSHA
			goal.Body = strings.ReplaceAll(goal.Body, "feature/integration", "main")
			goal.BodySHA, goal.LastEditedAt = BodyHash(goal.Body), now.Add(time.Minute)
			goal.Decisions = []Decision{{ID: "renewed", Author: c.Owner, Command: "/coop approve", CreatedAt: now.Add(2 * time.Minute)}}
			now = now.Add(3 * time.Minute)
			if EvaluateGoal(c, *goal, now).State != "approved" {
				t.Fatal("changed target was not owner approved")
			}
			if _, err := ValidateTeamScope(c, *task, *goal, now); err == nil {
				t.Fatal("old assignment survived an approved goal retarget")
			}
			if jobs := Select(c, s, newState(), now); len(jobs) != 0 {
				t.Fatalf("old assignment selected work: %+v", jobs)
			}
			task.Assignment.GoalBodySHA = goal.BodySHA
			if _, err := ValidateTeamScope(c, *task, *goal, now); err == nil {
				t.Fatal("updating only goal hash bypassed target mismatch")
			}
			task.Assignment.BaseBranch = "main"
			sha, err := ValidateTeamScope(c, *task, *goal, now)
			if err != nil || sha == oldSHA {
				t.Fatalf("fresh target assignment digest=%s old=%s err=%v", sha, oldSHA, err)
			}
			body := teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: oldSHA})
			if _, err := ParseTeamPRMetadata(body, task.Repo, task.URL, goal.URL, sha); err == nil {
				t.Fatal("old PR assignment digest survived goal retarget")
			}
			pr.BaseBranch = "main"
			if _, ok := EngineeringReviewFor(c, *task, *pr); ok {
				t.Fatal("old PR digest cleared new target assignment")
			}
		})
	}
}

func teamBaseReadFixture(t *testing.T, reverse bool) (*teamReadFixture, Snapshot, time.Time) {
	t.Helper()
	c, s, now := engineeringFixture(t, reverse)
	task, source := s.Tasks[0], s.PRs[0]
	f := newTeamReadFixture()
	f.c = c
	f.version["url"], f.version["baseRefName"] = source.URL, source.BaseBranch
	f.version["body"] = teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: task.GoalURL, AssignmentSHA: source.AssignmentSHA})
	f.version["createdAt"], f.version["headRefOid"] = task.MetadataUpdatedAt.Add(time.Second), source.HeadSHA
	f.version["author"] = graphActor{ID: source.Author.ID, Kind: source.Author.Kind, Login: source.Author.Login}
	f.version["mergeStateStatus"] = "CLEAN"
	f.version["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}}
	r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
	r["commit"], r["submittedAt"] = map[string]any{"oid": source.HeadSHA}, source.Reviews[0].SubmittedAt
	peer := source.Reviews[0].Author
	r["author"] = graphActor{ID: peer.ID, Kind: peer.Kind, Login: peer.Login}
	f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": source.HeadSHA, "committer": map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User"}}}}}
	f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User", Login: source.Author.Login}}}
	f.accounts[source.Author.ID] = restActor(source.Author)
	return f, s, now
}

func TestTeamBaseNativeRetargetDuringRead(t *testing.T) {
	f := newTeamReadFixture()
	f.hook = func(key string, _ map[string]any) {
		if key == "version" {
			f.version["baseRefName"] = "main"
		}
	}
	if _, err := f.client(t).readPR(context.Background(), f.version["url"].(string)); err == nil || !strings.Contains(err.Error(), "PR_body_changed_during_read") {
		t.Fatalf("native base retarget escaped final version check: %v", err)
	}
}

func TestTeamBaseTipAdvancementDoesNotChangeReviewScope(t *testing.T) {
	f, s, now := teamBaseReadFixture(t, false)
	f.version["baseRefOid"] = BodyHash("old base tip")
	before, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f.hook = func(key string, _ map[string]any) {
		if key == "version" {
			f.version["baseRefOid"] = BodyHash("advanced base tip")
		}
	}
	after, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatalf("branch tip advancement rejected an otherwise coherent read: %v", err)
	}
	bindReviewScope(&before, s.Tasks[0])
	bindReviewScope(&after, s.Tasks[0])
	if before.BaseScopeRevision != after.BaseScopeRevision || before.ScopeRevision != after.ScopeRevision || !before.ScopeUpdatedAt.Equal(after.ScopeUpdatedAt) {
		t.Fatal("branch tip advancement changed exact-branch review scope")
	}
	s.PRs = []PR{after}
	if jobs := Select(f.c, s, newState(), now); len(jobs) != 1 || jobs[0].Kind != "deliver" {
		t.Fatalf("branch tip advancement invalidated unchanged head/checks clearance: %+v", jobs)
	}
}

func TestTeamBaseRetargetHistoryRestartAndDelivery(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			f, s, now := teamBaseReadFixture(t, reverse)
			task := s.Tasks[0]
			originalBody := f.version["body"]
			read := func() PR {
				t.Helper()
				// A fresh client must derive scope from GitHub, not poll-local memory.
				pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
				if err != nil {
					t.Fatal(err)
				}
				bindReviewScope(&pr, task)
				return pr
			}
			original := read()
			if original.BaseScopeRevision == "" {
				t.Fatal("native read omitted base scope digest")
			}
			s.PRs = []PR{original}
			jobs := Select(f.c, s, newState(), now)
			if len(jobs) != 1 || jobs[0].Kind != "deliver" {
				t.Fatalf("initial approved target did not select delivery: %+v", jobs)
			}
			jobs[0].Phase, jobs[0].SessionID = "active", "saved-base-delivery"
			raw, err := json.Marshal(struct {
				Snapshot Snapshot
				Job      Job
			}{s, jobs[0]})
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Snapshot Snapshot
				Job      Job
			}
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			restored := saved.Snapshot.PRs[0]
			if restored.BaseBranch != original.BaseBranch || restored.BaseScopeRevision != original.BaseScopeRevision || restored.ScopeRevision != original.ScopeRevision || !restored.ScopeUpdatedAt.Equal(original.ScopeUpdatedAt) || saved.Job.PRScopeRevision != original.ScopeRevision {
				t.Fatal("JSON restart lost native base/review/job scope")
			}
			if ok, why := Admit(f.c, saved.Job, saved.Snapshot, now); !ok {
				t.Fatalf("unchanged saved delivery not admitted: %s", why)
			}
			merged := restored
			merged.Merged = true
			if !artifactCompletes(f.c, saved.Job, Artifacts{Task: &task, PR: &merged, IssueCompleted: true}) {
				t.Fatal("unchanged scope failed delivery completion control")
			}
			out, back := now.Add(time.Minute), now.Add(2*time.Minute)
			f.pages["timelineItems"] = []map[string]any{
				teamReadPage([]any{map[string]any{"__typename": "BaseRefChangedEvent", "id": "out", "createdAt": out, "previousRefName": "feature/integration", "currentRefName": "main"}}, 2, "base-page-2"),
				teamReadPage([]any{map[string]any{"__typename": "BaseRefChangedEvent", "id": "back", "createdAt": back, "previousRefName": "main", "currentRefName": "feature/integration"}}, 2, ""),
			}
			beforeCalls := f.calls["timelineItems"]
			retargeted := read()
			if f.calls["timelineItems"]-beforeCalls != 4 {
				t.Fatalf("paginated history not read and reverified: %+v", f.calls)
			}
			if retargeted.HeadSHA != original.HeadSHA || f.version["body"] != originalBody || retargeted.BaseBranch != original.BaseBranch || retargeted.AssignmentSHA != original.AssignmentSHA {
				t.Fatal("test must isolate feature -> main -> feature retarget from head/body/assignment changes")
			}
			if retargeted.BaseScopeRevision == original.BaseScopeRevision || retargeted.ScopeRevision == original.ScopeRevision || !retargeted.ScopeUpdatedAt.Equal(back) {
				t.Fatalf("round-trip retarget resurrected original scope: %+v", retargeted)
			}
			if _, ok := EngineeringReviewFor(f.c, task, retargeted); ok {
				t.Fatal("pre-retarget review cleared restored target")
			}
			s.PRs = []PR{retargeted}
			if jobs := Select(f.c, s, newState(), back.Add(time.Minute)); len(jobs) != 0 {
				t.Fatalf("old review selected work after retarget: %+v", jobs)
			}
			if ok, _ := Admit(f.c, saved.Job, s, back.Add(time.Minute)); ok {
				t.Fatal("saved delivery survived changed retarget scope")
			}
			merged = retargeted
			merged.Merged = true
			if artifactCompletes(f.c, saved.Job, Artifacts{Task: &task, PR: &merged, IssueCompleted: true}) {
				t.Fatal("changed retarget scope completed saved delivery")
			}
			r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
			r["fullDatabaseId"], r["submittedAt"] = "4294967298", back.Add(time.Minute)
			renewed := read()
			if _, ok := EngineeringReviewFor(f.c, task, renewed); !ok {
				t.Fatal("renewed post-retarget review did not clear")
			}
			s.PRs = []PR{renewed}
			fresh := Select(f.c, s, newState(), back.Add(2*time.Minute))
			if len(fresh) != 1 || fresh[0].Kind != "deliver" || fresh[0].ID == saved.Job.ID || fresh[0].PRScopeRevision == saved.Job.PRScopeRevision {
				t.Fatalf("renewed review did not create newly scoped delivery: %+v", fresh)
			}
			if ok, _ := Admit(f.c, saved.Job, s, back.Add(2*time.Minute)); ok {
				t.Fatal("renewed review resurrected old saved delivery")
			}
		})
	}
}

func TestTeamBaseBindReviewScopeIncludesBaseDigest(t *testing.T) {
	_, s, _ := engineeringFixture(t, false)
	first, second := s.PRs[0], s.PRs[0]
	first.BaseScopeRevision, second.BaseScopeRevision = BodyHash("first history"), BodyHash("second history")
	bindReviewScope(&first, s.Tasks[0])
	bindReviewScope(&second, s.Tasks[0])
	if first.ScopeRevision == second.ScopeRevision {
		t.Fatal("bindReviewScope discarded base digest at identical target/head/body/time")
	}
}

func TestTeamBaseTimelineFailsClosed(t *testing.T) {
	for _, fault := range []string{"incomplete", "stalled", "duplicate", "changed_during_evidence"} {
		t.Run(fault, func(t *testing.T) {
			f := newTeamReadFixture()
			at := f.version["createdAt"].(time.Time).Add(time.Minute)
			event := map[string]any{"__typename": "BaseRefChangedEvent", "id": "retarget", "createdAt": at, "previousRefName": "main", "currentRefName": "feature/integration"}
			f.pages["timelineItems"] = []map[string]any{teamReadPage([]any{event}, 1, "")}
			wantError := "incomplete_team_connection"
			switch fault {
			case "incomplete":
				delete(f.pages["timelineItems"][0], "pageInfo")
			case "stalled":
				wantError = "stalled_team_cursor"
				f.pages["timelineItems"] = []map[string]any{teamReadPage([]any{event}, 3, "same"), teamReadPage([]any{event}, 3, "same")}
			case "duplicate":
				wantError = "incoherent_PR_base_history"
				out := map[string]any{"__typename": "BaseRefChangedEvent", "id": "retarget", "createdAt": at.Add(-time.Second), "previousRefName": "feature/integration", "currentRefName": "main"}
				f.pages["timelineItems"] = []map[string]any{teamReadPage([]any{out}, 2, "second"), teamReadPage([]any{event}, 2, "")}
			case "changed_during_evidence":
				wantError = "PR_scope_history_changed_during_read"
				f.hook = func(key string, _ map[string]any) {
					if key == "reviews" {
						event["id"] = "replacement"
					}
				}
			}
			if _, err := f.client(t).readPR(context.Background(), f.version["url"].(string)); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("base timeline fault %s: got %v, want %s", fault, err, wantError)
			}
		})
	}
}

func TestTeamBaseTimelineVisibleEntriesAndUnrelatedEvents(t *testing.T) {
	f := newTeamReadFixture()
	// Actual GitHub timeline shape: totalCount can exceed visible nodes even
	// without filtering. Explicit terminal pageInfo determines completion.
	nodes := []any{
		map[string]any{"__typename": "PullRequestCommit"},
		map[string]any{"__typename": "IssueComment"},
		map[string]any{"__typename": "CrossReferencedEvent"},
		map[string]any{"__typename": "PullRequestReview"},
	}
	f.pages["timelineItems"] = []map[string]any{teamReadPage(nodes, 5, "")}
	before, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f.hook = func(key string, _ map[string]any) {
		if key == "reviews" {
			f.pages["timelineItems"] = []map[string]any{teamReadPage(append(nodes, map[string]any{"__typename": "IssueComment"}), 6, "")}
		}
	}
	after, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if before.ScopeRevision != after.ScopeRevision || before.BaseScopeRevision != after.BaseScopeRevision {
		t.Fatal("unrelated timeline events changed reviewable base scope")
	}
}
