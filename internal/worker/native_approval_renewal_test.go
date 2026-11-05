package worker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativeRenewalRetainsStageIdentityAndSettledHistory(t *testing.T) {
	for _, kind := range []string{"implement", "review", "fix", "qa", "deliver"} {
		t.Run(kind, func(t *testing.T) {
			cfg, catalog, task := nativeWorkFixture(t)
			now := time.Now().UTC()
			p := nativeReviewPRFixture(cfg, task)
			peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
			if kind == "fix" {
				peer.Verdict = "changes-needed"
			}
			peerState := "APPROVED"
			if kind == "fix" {
				peerState = "CHANGES_REQUESTED"
			}
			if kind == "qa" || kind == "deliver" || kind == "fix" {
				p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: peerState, Body: NativePeerMetadata(peer), SubmittedAt: now}}
			}
			if kind == "deliver" {
				qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(catalog.Goals[0]), Environment: "staging", EnvironmentRevision: "staging-v1", Verdict: "approved"}
				p.Reviews = append(p.Reviews, Review{ID: "qa", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[2].Actor, State: "APPROVED", Body: NativeQAMetadata(qa), SubmittedAt: now})
				selectNativeRook(&cfg, nil)
			} else if kind == "review" || kind == "qa" {
				index, agent := 0, "peer"
				if kind == "qa" {
					index, agent = 2, "qa"
				}
				cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[index].Name, cfg.Team.Members[index].Actor, agent
			}
			if kind != "implement" {
				task.PRs = []PR{p}
			}
			catalog.Tasks[0] = task
			before := NativeCandidates(cfg, catalog, nil, now)
			if len(before) != 1 || before[0].Job.Kind != kind {
				t.Fatal("missing original stage", before)
			}
			original, _ := json.Marshal(catalog.Tasks)
			catalog.Goals[0].ApprovalSequence += 4
			catalog.Goals[0].DecisionSequence += 4
			after := NativeCandidates(cfg, catalog, nil, now)
			if len(after) != 1 || !reflect.DeepEqual(before[0].Job, after[0].Job) {
				t.Fatal("renewal replaced the job or requested planning", before, after)
			}
			for _, phase := range []string{"completed", "blocked"} {
				history := map[string]NativeExecution{before[0].Job.ID: {Job: before[0].Job, Phase: phase}}
				if jobs := NativeCandidates(cfg, catalog, history, now); len(jobs) != 0 {
					t.Fatal("renewal replayed a settled or held job", phase, jobs)
				}
			}
			unchanged, _ := json.Marshal(catalog.Tasks)
			if string(original) != string(unchanged) {
				t.Fatal("renewal rewrote task, binding or PR evidence")
			}
		})
	}
}

func TestNativeRenewalKeepsCurrentAuthorityBoundaries(t *testing.T) {
	for _, fault := range []string{"none", "paused", "rejected", "scope", "author", "peer", "qa", "base", "future", "invalid"} {
		t.Run(fault, func(t *testing.T) {
			b, source := nativeBindingFixture()
			source.authority.ApprovalSequence += 4
			switch fault {
			case "paused", "rejected":
				source.authority.State, source.authority.Valid = fault, false
			case "scope":
				source.authority.ActiveRevisionSHA256 = BodyHash("changed scope")
			case "author":
				source.authority.Actors.Author.GitHubBotID++
			case "peer":
				source.authority.Actors.Peer.GitHubBotID++
			case "qa":
				source.authority.Actors.QA.GitHubBotID++
			case "base":
				b.BaseBranch = "outside"
			case "future":
				b.ApprovalSequence = source.authority.ApprovalSequence + 1
			case "invalid":
				b.ApprovalSequence = 0
			}
			_, err := ValidateNativeAuthority(t.Context(), source, b)
			if (err == nil) != (fault == "none") {
				t.Fatal("incorrect current-authority decision", fault, err)
			}
		})
	}
}

func TestNativeCachedRenewalKeepsRegisteredPR(t *testing.T) {
	cfg, cache, master, live := cachedFixture(t)
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[0].Name, cfg.Team.Members[0].Actor, "peer"
	task := live.catalog.Tasks[0]
	p := nativeReviewPRFixture(cfg, task)
	live.catalog.Tasks[0].PRs = []PR{p}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	hash := c.GitHubDigest(raw)
	cache.data[hash] = raw
	ref := c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: p.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
	cache.page.Objects = append(cache.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Hour), Header: c.GitHubHeader{Revision: hash, ScopeRevision: p.ScopeRevision, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, State: "open"}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: hash, HeaderRevision: hash, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Hour)}}})
	live.catalog.Goals[0].ApprovalSequence += 4
	cache.page.Goals = live.catalog.Goals
	master.authority = live.catalog.Goals[0]
	catalog, err := NewCachedNativeReader(cfg, live, cache, RealClock{}).NativeSnapshot(t.Context(), cfg, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tasks) != 1 || len(catalog.Tasks[0].PRs) != 1 {
		t.Fatal("renewal hid the registered PR", catalog)
	}
	now := time.Now().UTC()
	a, b := NativeCandidates(cfg, catalog, nil, now), NativeCandidates(cfg, live.catalog, nil, now)
	if len(a) != 1 || len(b) != 1 || a[0].Job.Kind != "review" || !reflect.DeepEqual(a[0].Job, b[0].Job) {
		t.Fatal("cached/live renewal decisions differ", a, b)
	}
}

func TestNativeRenewalDoesNotStrandCompletedDependencies(t *testing.T) {
	_, catalog, task := nativeWorkFixture(t)
	catalog.Tasks[0].Task.Completed = true
	catalog.Tasks[0].Task.Closed = true
	wait := &NativeDependencyWait{Dependencies: []NativeDependency{{TaskURL: task.Task.URL, Goal: task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}}
	catalog.Goals[0].ApprovalSequence += 4
	if !nativeDependenciesReady(catalog, wait) {
		t.Fatal("unchanged completed dependency required rebinding")
	}
	catalog.Goals[0].State, catalog.Goals[0].Valid = "paused", false
	if nativeDependenciesReady(catalog, wait) {
		t.Fatal("paused authority passed the dependency guard")
	}
}
