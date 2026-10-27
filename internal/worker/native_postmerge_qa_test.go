package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativePostMergeQADiscoveryHydratesOnlyMatchingHandoffs(t *testing.T) {
	for _, source := range []string{"github", "kata", "legacy"} {
		for _, mode := range []string{"matching", "no-handoff", "wrong-head", "wrong-environment"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				cfg, cat := postMergeQAFixture(t)
				cfg.Projects, cfg.ProjectOwners = nil, nil
				task := cat.Tasks[0]
				if source != "github" {
					cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
					if source == "kata" {
						task.Task.URL, _ = c.KataTaskReference(providerProject, providerIssue, task.Task.Repo)
						task.Binding.TaskURL = task.Task.URL
					} else {
						cfg.TaskSource.RetainGitHubTasks = []string{task.Task.URL}
					}
					installTaskProviderHTTP(t, &cfg, func(path string) any {
						if path == "/api/v1/tasks/capabilities" {
							return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: task.Binding.PolicySHA}
						}
						if source == "kata" && path == "/api/v1/tasks/read" {
							return c.ProviderTask{Reference: task.Task.URL, ProjectUID: providerProject, IssueUID: providerIssue, Repository: task.Task.Repo, Title: task.Task.Title, Body: task.Task.Body, Revision: 7, Goal: task.Binding.Goal, Binding: &task.Binding, State: "closed", ClosedReason: "done"}
						}
						t.Fatalf("unexpected provider read %s", path)
						return nil
					})
				}
				transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: task.PRs[0].HeadSHA, merged: true, closed: true, taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding)}
				artifact := *task.PRs[0].QAArtifact
				artifact.Kind, artifact.URL, artifact.Revision = "workflow", "https://api.github.com/repos/code/repo/actions/artifacts/1", "1"
				transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: &artifact})
				artifactReads := 0
				transport.commands = &fakeCommands{respond: func(owner, path, method string, body []byte) (CommandResult, error) {
					if path == "repos/code/repo/actions/artifacts/1" {
						artifactReads++
					}
					return transport.response(owner, path, method, body)
				}}
				h := cat.Handoffs[0]
				h.Input.TaskURL, h.Input.AssignmentSHA = task.Task.URL, NativeAssignmentSHA(task.Binding)
				switch mode {
				case "wrong-head":
					h.Input.HeadSHA = strings.Repeat("b", 40)
				case "wrong-environment":
					h.Input.EnvironmentRevision = "other"
				}
				master := &releaseMasterFixture{dispatchMaster: &dispatchMaster{nativeMasterFixture: nativeMasterFixture{authority: cat.Goals[0], links: []c.ArtifactLink{{Kind: "task", Goal: task.Binding.Goal, URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}, {Kind: "pull_request", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: task.PRs[0].URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}}}, handoff: h}
				var m NativeMaster = master
				if mode == "no-handoff" {
					m = master.dispatchMaster
				}
				g := NewGitHubClient(transport)
				out, err := g.NativeSnapshot(t.Context(), cfg, m)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "matching" {
					if len(out.Tasks) != 1 || !out.Tasks[0].Task.Completed || !out.Tasks[0].Task.Closed || out.Tasks[0].PRs[0].QAArtifact == nil || artifactReads == 0 {
						t.Fatal("completed task artifact not hydrated", out, artifactReads)
					}
					job := Job{Kind: "qa", TaskURL: task.Task.URL, Master: &MasterJobBinding{Goal: &task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
					selected, err := g.NativeTaskSnapshot(t.Context(), cfg, m, job)
					if err != nil || len(selected.Tasks) != 1 || selected.Tasks[0].PRs[0].QAArtifact == nil || !selected.Tasks[0].Task.Completed {
						t.Fatal("fresh task snapshot lost merged artifact", selected, err)
					}
				} else if artifactReads != 0 {
					t.Fatal("unmatched handoff hydrated artifact", artifactReads)
				}
				if len(transport.writes) != 0 {
					t.Fatal("discovery wrote remotely", transport.writes)
				}
			})
		}
	}
}

func TestNativeCachedPostMergeQADiscovery(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmatched", true: "matched"}[matching], func(t *testing.T) {
			cfg, cat := postMergeQAFixture(t)
			task, p := cat.Tasks[0], cat.Tasks[0].PRs[0]
			f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("manifest"), Goals: cat.Goals}}
			addCachedTask(t, f, task)
			f.page.Objects[0].Header.State, f.page.Objects[0].Header.Completed = "closed", true
			p.QAArtifact = nil
			raw, _ := json.Marshal(p)
			hash := c.GitHubDigest(raw)
			f.data[hash] = raw
			ref := f.page.Objects[0].Object
			ref.Kind, ref.URL = "pull_request", p.URL
			f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Hour), Header: c.GitHubHeader{Revision: hash, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, ScopeRevision: p.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: hash, HeaderRevision: hash, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Hour)}}})
			transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: p.HeadSHA, merged: true}
			artifact := *task.PRs[0].QAArtifact
			artifact.Kind, artifact.URL, artifact.Revision = "workflow", "https://api.github.com/repos/code/repo/actions/artifacts/1", "1"
			transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: &artifact})
			transport.commands = &fakeCommands{respond: transport.response}
			h := cat.Handoffs[0]
			if !matching {
				h.Input.HeadSHA = strings.Repeat("b", 40)
			}
			master := &releaseMasterFixture{dispatchMaster: &dispatchMaster{nativeMasterFixture: nativeMasterFixture{authority: cat.Goals[0]}}, handoff: h}
			out, err := NewCachedNativeReader(cfg, NewGitHubClient(transport), f, RealClock{}).NativeSnapshot(t.Context(), cfg, master)
			if err != nil || len(out.Tasks) != 1 || !out.Tasks[0].Task.Completed {
				t.Fatal("cached completed task lost", out, err)
			}
			if len(out.Tasks[0].PRs) != 1 || (out.Tasks[0].PRs[0].QAArtifact != nil) != matching {
				t.Fatal("cached hydration did not follow exact handoff", out.Tasks[0].PRs)
			}
			if len(transport.writes) != 0 {
				t.Fatal("cache discovery wrote remotely", transport.writes)
			}
		})
	}
}

func postMergeQAFixture(t *testing.T) (Config, NativeCatalog) {
	t.Helper()
	cfg, cat, task := nativeWorkFixture(t)
	p := nativeReviewPRFixture(cfg, task)
	p.Merged, p.Closed = true, true
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: time.Now().UTC()}}
	task.Task.Closed, task.Task.Completed = true, true
	task.PRs = []PR{p}
	cat.Tasks[0] = task
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[2].Name, cfg.Team.Members[2].Actor, "qa"
	cat.Handoffs = []c.Handoff{{ID: "first", CreatedAt: time.Now().UTC(), Input: c.HandoffInput{Kind: "first-postmerge-qa", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, ArtifactSHA: p.QAArtifact.SHA256, Environment: cat.Goals[0].Revision.Proposal.QA.Environment, EnvironmentRevision: cat.Goals[0].Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: "owner-evidence", RequestID: "first"}}}
	return cfg, cat
}

func TestNativeFirstPostMergeQAAdmission(t *testing.T) {
	cfg, cat := postMergeQAFixture(t)
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	if len(jobs) != 1 || !nativeIsPostMergeQA(jobs[0].Job) || jobs[0].Handoff.ID != "first" {
		t.Fatalf("missing first postmerge QA: %+v", jobs)
	}
	job := jobs[0].Job
	prompt, err := nativePrompt(cfg, NativeExecution{Job: job, Binding: &cat.Tasks[0].Binding}, jobs[0])
	if err != nil || !strings.Contains(prompt, "first postmerge QA assessment") || !strings.Contains(prompt, "not a premerge clearance") || !strings.Contains(prompt, "owner-evidence") {
		t.Fatal("missing bounded postmerge prompt", err, prompt)
	}
	duplicate := cat.Handoffs[0]
	duplicate.ID, duplicate.CreatedAt = "later", duplicate.CreatedAt.Add(time.Second)
	cat.Handoffs = append([]c.Handoff{duplicate}, cat.Handoffs...)
	if got := NativeCandidates(cfg, cat, nil, time.Now()); len(got) != 1 || got[0].Job.ID != job.ID {
		t.Fatal("later request changed job identity", got)
	}
	if got := NativeCandidates(cfg, cat, map[string]NativeExecution{job.ID: {Job: job, Phase: "completed"}}, time.Now()); len(got) != 0 {
		t.Fatal("duplicate request replayed completed QA", got)
	}
	x := NativeExecution{Job: job, Binding: &cat.Tasks[0].Binding}
	if nativeScopeChanged(x, cat) {
		t.Fatal("completed task retired bounded QA")
	}
	retainedHandoffs := cat.Handoffs
	cat.Handoffs = nil
	if !nativeScopeChanged(x, cat) {
		t.Fatal("prefix alone bypassed the completed-task scope guard")
	}
	cat.Handoffs = retainedHandoffs
	x.Job.Kind = "review"
	if !nativeScopeChanged(x, cat) {
		t.Fatal("ordinary stage passed completed task guard")
	}
	if !cat.Tasks[0].Task.Closed || !cat.Tasks[0].Task.Completed {
		t.Fatal("task state changed")
	}
}

func TestNativeFirstPostMergeQARejectsMismatches(t *testing.T) {
	for _, change := range []string{"no-handoff", "goal", "task", "pr", "assignment", "head", "artifact", "environment", "environment-revision", "actor", "principal", "authority-actor", "contributor", "contributors-incomplete", "reviews-incomplete", "prior-review", "prior-dismissed-review", "unmerged", "checks", "peer", "no-artifact", "artifact-head", "qa-optional", "task-scope", "approval"} {
		t.Run(change, func(t *testing.T) {
			cfg, cat := postMergeQAFixture(t)
			p, h := &cat.Tasks[0].PRs[0], &cat.Handoffs[0].Input
			switch change {
			case "no-handoff":
				cat.Handoffs = nil
			case "goal":
				h.Goal.GoalID = "other"
			case "task":
				h.TaskURL += "0"
			case "pr":
				h.PRURL += "0"
			case "assignment":
				h.AssignmentSHA = BodyHash("other")
			case "head":
				h.HeadSHA = strings.Repeat("b", 40)
			case "artifact":
				h.ArtifactSHA = BodyHash("other")
			case "environment":
				h.Environment = "other"
			case "environment-revision":
				h.EnvironmentRevision = "other"
			case "actor":
				cfg.Self = cfg.Team.Members[0].Actor
			case "principal":
				cfg.Master.AgentID = "other"
			case "authority-actor":
				cat.Goals[0].Actors.QA.GitHubBotID++
			case "contributor":
				p.Contributors = append(p.Contributors, cfg.Self)
			case "contributors-incomplete":
				p.ContributorsComplete = false
			case "reviews-incomplete":
				p.ReviewsComplete = false
			case "prior-review":
				p.Reviews = append(p.Reviews, Review{ID: "old-qa", Author: cfg.Self, HeadSHA: "old-head", State: "APPROVED"})
			case "prior-dismissed-review":
				p.Reviews = append(p.Reviews, Review{ID: "old-qa", Author: cfg.Self, HeadSHA: "old-head", State: "DISMISSED"})
			case "unmerged":
				p.Merged = false
			case "checks":
				p.ChecksState = "pending"
			case "peer":
				p.Reviews = nil
			case "no-artifact":
				p.QAArtifact = nil
			case "artifact-head":
				p.QAArtifact.HeadSHA = "other"
			case "qa-optional":
				cat.Goals[0].Revision.Proposal.QA.Required = false
			case "task-scope":
				cat.Tasks[0].Task.BodySHA = BodyHash("other")
			case "approval":
				cat.Goals[0].ApprovalSequence++
			}
			if got := NativeCandidates(cfg, cat, nil, time.Now()); len(got) != 0 {
				t.Fatal("mismatched first assessment admitted", got)
			}
		})
	}
}

func TestNativePostMergeQASourceJournalsMustSettle(t *testing.T) {
	for _, state := range []string{"uncertain", "prepared", "completed", "no-effect-terminal", "active"} {
		t.Run(state, func(t *testing.T) {
			cfg, _, catalog, _ := dispatchFixture(t)
			task := catalog.catalog.Tasks[0]
			goal := task.Binding.Goal
			source := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: goal, Phase: "blocked", Reason: "native_completed_without_result", Job: Job{ID: BodyHash("source-delivery"), TaskURL: task.Task.URL, Kind: "deliver", Phase: "completed", SubmissionState: "completed", TerminalAt: time.Now(), SessionID: "ses_source", MessageID: "msg_source", Master: &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ExecutorID: cfg.Self.ID, Goal: &goal}}}
			if state == "active" {
				source.Phase = "active"
			}
			if err := saveNativeExecution(cfg, source); err != nil {
				t.Fatal(err)
			}
			if state != "no-effect-terminal" && state != "active" {
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: source.Job.ID, RequestID: "merge", Operation: "deliver", State: state}); err != nil {
					t.Fatal(err)
				}
			}
			allowed := state == "completed" || state == "no-effect-terminal"
			if err := releaseSourceSettled(cfg, task.Task.URL); (err == nil) != allowed {
				t.Fatal("pre-admission source guard", err)
			}
			own := source
			own.Phase, own.Reason = "selected", ""
			own.Job.ID, own.Job.Kind, own.Job.InputRevision = BodyHash("postmerge-qa"), "qa", nativePostMergeQAPrefix+"test"
			if err := saveNativeExecution(cfg, own); err != nil {
				t.Fatal(err)
			}
			if err := nativePostMergeQASourceSettled(cfg, own.Job); (err == nil) != allowed {
				t.Fatal("dispatch source guard skipped source or blocked own intent", err)
			}
		})
	}
}
