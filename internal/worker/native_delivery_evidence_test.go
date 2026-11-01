package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type deliveryEvidenceMaster struct {
	*dispatchMaster
	goals     []c.GoalAuthority
	forbidden map[string]bool
	reads     map[string]int
}

func (m *deliveryEvidenceMaster) ApprovedGoals(context.Context, string, string) (c.GoalPage, error) {
	return c.GoalPage{Goals: m.goals}, nil
}

func (m *deliveryEvidenceMaster) ResolveGoal(_ context.Context, ref c.GoalRef) (c.GoalAuthority, error) {
	for _, a := range m.goals {
		if a.Revision.Ref == ref {
			return a, nil
		}
	}
	return c.GoalAuthority{}, c.ErrNotFound
}

func (m *deliveryEvidenceMaster) Handoffs(_ context.Context, q c.HandoffQuery) (c.HandoffPage, error) {
	var page c.HandoffPage
	for _, h := range m.handoffs {
		if h.Input.Goal == q.Goal {
			page.Handoffs = append(page.Handoffs, h)
		}
	}
	return page, nil
}

func (m *deliveryEvidenceMaster) Message(ctx context.Context, id string) (c.Message, error) {
	m.reads[id]++
	if m.forbidden[id] {
		return c.Message{}, c.ErrForbidden
	}
	return m.dispatchMaster.Message(ctx, id)
}

// Both goals remain approved with readable source and independent assessments.
// Only their separate private message memberships can become inaccessible.
func deliveryEvidenceFixture(t *testing.T) (Config, *deliveryEvidenceMaster, *GitHubClient, *fakeCacheClient, []NativeTask) {
	t.Helper()
	cfg, base, initial, _ := dispatchFixture(t)
	cfg.Projects, cfg.ProjectOwners = nil, nil
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[3].Name, cfg.Team.Members[3].Actor, "rook"
	base.reports = map[string]c.Message{}
	m := &deliveryEvidenceMaster{dispatchMaster: base, forbidden: map[string]bool{}, reads: map[string]int{}}
	sources := map[string]*nativeDeliveryTransport{}
	runner := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		repo := "code/repo"
		var req struct{ Variables struct{ Repo string } }
		if strings.HasPrefix(endpoint, "repos/code/other/") || endpoint == "graphql" && json.Unmarshal(body, &req) == nil && req.Variables.Repo == "other" {
			repo = "code/other"
		}
		s := sources[repo]
		if s == nil {
			t.Fatalf("missing source for %s", repo)
		}
		result, err := s.response(owner, strings.ReplaceAll(endpoint, repo, "code/repo"), method, body)
		// The shared transport's structural URLs are fixed to code/repo; metadata
		// and review bodies already carry the actual goal/task/assignment binding.
		if repo != "code/repo" {
			result.Stdout = bytes.ReplaceAll(result.Stdout, []byte("code/repo"), []byte(repo))
		}
		return result, err
	}}
	g := NewGitHubClient(runner)
	var tasks []NativeTask
	for i, repo := range []string{"code/repo", "code/other"} {
		suffix := []string{"a", "b"}[i]
		a := initial.catalog.Goals[0]
		a.Revision.Ref.GoalID = "goal-" + suffix
		a.Revision.Proposal.ThreadID = "owner-" + suffix
		a.Revision.Proposal.QA.EvidenceDestination.ThreadID = "qa-" + suffix
		a.Revision.Proposal.Repositories = append([]c.RepositoryScope(nil), a.Revision.Proposal.Repositories...)
		a.Revision.Proposal.Repositories[0].Name = repo
		task := initial.catalog.Tasks[0]
		task.Binding.Goal = a.Revision.Ref
		task.Task.Repo, task.Task.URL = repo, "https://github.com/"+repo+"/issues/1"
		task.Binding.TaskURL = task.Task.URL
		artifact := &QAArtifact{Kind: "workflow", HeadSHA: strings.Repeat("a", 40), SHA256: strings.Repeat("e", 64), URL: "https://api.github.com/repos/" + repo + "/actions/artifacts/1", Revision: "1"}
		s := &nativeDeliveryTransport{t: t, cfg: cfg, head: artifact.HeadSHA, baseSHA: strings.Repeat("d", 40), taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding)}
		s.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: artifact})
		sources[repo] = s
		prURL := "https://github.com/" + repo + "/pull/1"
		p, err := g.ReadNativePR(t.Context(), cfg, task, a, prURL)
		if err != nil {
			t.Fatal(err)
		}
		peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
		qaID := "qa-evidence-" + suffix
		qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(a), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: qaID, EvidenceBodySHA: BodyHash("QA passed"), Verdict: "approved"}
		for j, body := range []string{NativePeerMetadata(peer), NativeQAMetadata(qa)} {
			member := cfg.Team.Members[[]int{0, 2}[j]]
			s.reviews = append(s.reviews, map[string]any{"fullDatabaseId": j + 1, "state": "APPROVED", "body": body, "submittedAt": time.Now().UTC(), "commit": map[string]any{"oid": p.HeadSHA}, "author": map[string]any{"__typename": "Bot", "databaseId": member.Actor.ID, "login": member.Actor.Login}})
		}
		p, err = g.ReadNativePR(t.Context(), cfg, task, a, prURL)
		if err != nil {
			t.Fatal(err)
		}
		task.PRs = []PR{p}
		base.reports[qaID] = c.Message{ID: qaID, ThreadID: "qa-" + suffix, AuthorID: "qa", AuthorKind: "agent", Body: "QA passed"}
		cat := NativeCatalog{Complete: true, Goals: []c.GoalAuthority{a}, Tasks: []NativeTask{task}, Reports: base.reports}
		h := nativeAuthorizeDelivery(t, &cfg, &cat)
		ownerID := "owner-evidence-" + suffix
		report := base.reports[h.Input.EvidenceMessageID]
		delete(base.reports, h.Input.EvidenceMessageID)
		report.ID = ownerID
		base.reports[ownerID] = report
		h.ID, h.Input.EvidenceMessageID = "grant-"+suffix, ownerID
		base.handoffs = append(base.handoffs, h)
		base.links = append(base.links, c.ArtifactLink{Kind: "task", Goal: a.Revision.Ref, URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}, c.ArtifactLink{Kind: "pull_request", Goal: a.Revision.Ref, TaskURL: task.Task.URL, URL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
		m.goals = append(m.goals, a)
		tasks = append(tasks, task)
	}
	f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("two-goal-manifest"), Goals: m.goals}}
	for _, task := range tasks {
		addCachedTask(t, f, task)
		p := task.PRs[0]
		ref := c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: p.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		hash := c.GitHubDigest(raw)
		f.data[hash] = raw
		f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Hour), Header: c.GitHubHeader{Revision: hash, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, ScopeRevision: p.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: hash, HeaderRevision: hash, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Hour)}}})
	}
	return cfg, m, g, f, tasks
}

func TestNativeDeliveryEvidenceIsolationAcrossGoals(t *testing.T) {
	for _, path := range []string{"live", "cached"} {
		for _, mode := range []string{"accessible", "revoked", "superseded-revoked", "superseded-accessible", "superseded-inaccessible", "active-owner-inaccessible", "active-qa-inaccessible", "active-both-inaccessible", "stale-private-report"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				cfg, m, g, cache, tasks := deliveryEvidenceFixture(t)
				var reader NativeCatalogReader = g
				if path == "cached" {
					reader = NewCachedNativeReader(cfg, g, cache, RealClock{})
				}
				baseline, err := reader.NativeSnapshot(t.Context(), cfg, m)
				if err != nil || len(NativeCandidates(cfg, baseline, nil, time.Now())) != 2 {
					t.Fatal("fixture requires two independently deliverable goals", err)
				}
				m.reads = map[string]int{}
				original := m.handoffs[0]
				switch mode {
				case "revoked":
					m.handoffs[0].Input.Revoked = true
				case "superseded-revoked", "superseded-accessible", "superseded-inaccessible":
					h := original
					h.ID, h.Sequence, h.CreatedAt = "new-a-decision", h.Sequence+1, h.CreatedAt.Add(-time.Hour)
					h.Input.EvidenceMessageID = "replacement-owner-evidence-a"
					h.Input.Revoked = mode == "superseded-revoked"
					m.handoffs = append(m.handoffs, h)
					m.forbidden[h.Input.EvidenceMessageID] = mode != "superseded-accessible"
					report := m.reports[original.Input.EvidenceMessageID]
					report.ID = h.Input.EvidenceMessageID
					m.reports[report.ID] = report
				case "stale-private-report":
					r := m.reports["qa-evidence-a"]
					r.Body = "Evidence changed after clearance"
					m.reports[r.ID] = r
				}
				if mode == "revoked" || mode == "superseded-revoked" || mode == "active-both-inaccessible" {
					m.forbidden["owner-evidence-a"], m.forbidden["qa-evidence-a"] = true, true
				}
				if mode == "active-owner-inaccessible" || mode == "superseded-inaccessible" || mode == "superseded-accessible" {
					m.forbidden["owner-evidence-a"] = true
				}
				if mode == "active-qa-inaccessible" {
					m.forbidden["qa-evidence-a"] = true
				}
				// Repeat on the same reader: cached source payloads must not retain
				// private evidence or authorize an older grant on the next poll.
				for range 2 {
					ownerReadsB, qaReadsB := m.reads["owner-evidence-b"], m.reads["qa-evidence-b"]
					cat, err := reader.NativeSnapshot(t.Context(), cfg, m)
					if err != nil || !cat.Complete {
						t.Fatal("A evidence access poisoned catalog", err)
					}
					if len(cat.Handoffs) != len(m.handoffs) {
						t.Fatal("decision history was discarded", cat.Handoffs)
					}
					decisions := map[string]c.Handoff{}
					for _, h := range cat.Handoffs {
						decisions[h.ID] = h
					}
					for _, h := range m.handoffs {
						if decisions[h.ID] != h {
							t.Fatal("decision history changed", h.ID)
						}
					}
					if mode == "revoked" || mode == "superseded-revoked" {
						if len(cat.Tasks) != 2 {
							t.Fatal("inaccessible history removed readable source", cat.Tasks)
						}
						for _, task := range cat.Tasks {
							if len(task.PRs) != 1 || task.Task.Closed || task.Task.Completed {
								t.Fatal("fixture lost historical source PR", task)
							}
						}
					}
					jobs := NativeCandidates(cfg, cat, nil, time.Now())
					want := 1
					if mode == "accessible" || mode == "superseded-accessible" {
						want = 2
					}
					if len(jobs) != want {
						t.Fatal("incorrect isolated delivery candidates", jobs)
					}
					foundB := false
					for _, job := range jobs {
						if job.Job.Kind != "deliver" {
							t.Fatal("unexpected stage", job.Job.Kind)
						}
						if job.Job.TaskURL == tasks[1].Task.URL && job.Job.Master.DeliveryHandoffID == "grant-b" {
							foundB = true
						}
						if job.Job.TaskURL == tasks[0].Task.URL {
							if mode != "accessible" && mode != "superseded-accessible" {
								t.Fatal("A fell back to inaccessible or superseded grant")
							}
							if mode == "superseded-accessible" && job.Job.Master.DeliveryHandoffID != "new-a-decision" {
								t.Fatal("A selected superseded grant")
							}
						}
					}
					if !foundB {
						t.Fatal("unrelated B delivery disappeared")
					}
					if mode == "revoked" || mode == "superseded-revoked" {
						if m.reads["owner-evidence-a"] != 0 || m.reads["qa-evidence-a"] != 0 || m.reads["replacement-owner-evidence-a"] != 0 {
							t.Fatal("revoked history fetched private evidence", m.reads)
						}
					}
					if (mode == "superseded-inaccessible" || mode == "superseded-accessible") && (m.reads["owner-evidence-a"] != 0 || m.reads["replacement-owner-evidence-a"] == 0) {
						t.Fatal("superseded evidence was read or current evidence bypassed", m.reads)
					}
					if mode == "active-owner-inaccessible" && m.reads["owner-evidence-a"] == 0 {
						t.Fatal("active owner evidence was not checked")
					}
					if mode == "active-qa-inaccessible" && m.reads["qa-evidence-a"] == 0 {
						t.Fatal("active QA evidence was not checked")
					}
					if m.reads["owner-evidence-b"] <= ownerReadsB || m.reads["qa-evidence-b"] <= qaReadsB {
						t.Fatal("B was selected without fresh evidence", m.reads)
					}
					if path == "cached" && cache.dataReads != 4 {
						t.Fatal("cache projection did not reuse the two task/PR datasets", cache.dataReads)
					}
				}
			})
		}
	}
}
