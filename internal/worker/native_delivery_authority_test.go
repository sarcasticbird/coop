package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Grant delivery without changing the source author, assignment, or reviewers.
func nativeAuthorizeDelivery(t *testing.T, cfg *Config, cat *NativeCatalog) c.Handoff {
	t.Helper()
	cfg.Name, cfg.Master.AgentID = "rook", "rook"
	for _, member := range cfg.Team.Members {
		if member.Name == "rook" {
			cfg.Self = member.Actor
		}
	}
	task, a := cat.Tasks[0], cat.Goals[0]
	p := task.PRs[0]
	peer, ok := nativePeerCleared(task.Binding, p)
	if !ok {
		t.Fatal("delivery fixture requires independent peer clearance")
	}
	h := c.Handoff{ID: "delivery-grant", Sequence: 1, ActorID: "owner", ActorKind: "owner", CreatedAt: time.Now().UTC(), Input: c.HandoffInput{
		Kind: "deliver", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL,
		AssignmentSHA: NativeAssignmentSHA(task.Binding), ApprovalSequence: a.ApprovalSequence,
		TaskScopeSHA: task.Binding.TaskScopeSHA, BaseBranch: p.BaseBranch, BaseSHA: p.BaseSHA,
		HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256,
		AcceptanceSHA: NativeAcceptanceSHA(a), PeerReviewID: peer.ID,
		EvidenceMessageID: "delivery-evidence", RequestID: "delivery-request",
	}}
	if a.Revision.Proposal.QA.Required {
		r, cleared := nativeQAState(task.Binding, a, p, cat.Reports, cat.Handoffs...)
		var qa NativeQAResult
		if !cleared || nativeMetadata(r.Body, "qa", &qa) != nil {
			t.Fatal("delivery fixture requires independent QA clearance")
		}
		h.Input.QAReviewID, h.Input.QAEvidenceMessageID, h.Input.QAEvidenceBodySHA = r.ID, qa.EvidenceMessageID, qa.EvidenceBodySHA
		h.Input.Environment, h.Input.EnvironmentRevision = qa.Environment, qa.EnvironmentRevision
	}
	if err := c.ValidateHandoff(h.Input); err != nil {
		t.Fatal("invalid delivery fixture", err)
	}
	cat.Handoffs = append(cat.Handoffs, h)
	cat.Reports[h.Input.EvidenceMessageID] = c.Message{ID: h.Input.EvidenceMessageID, ThreadID: a.Revision.Proposal.ThreadID, AuthorID: "owner", AuthorKind: "owner", Body: "Deliver this exact artifact."}
	return h
}

func nativeDeliveryReadyFixture(t *testing.T) (Config, NativeCatalog) {
	t.Helper()
	cfg, cat, task := nativeWorkFixture(t)
	p := nativeReviewPRFixture(cfg, task)
	now := time.Now().UTC()
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(cat.Goals[0]), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "qa-evidence", EvidenceBodySHA: BodyHash("QA passed"), Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer-review", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: now}, {ID: "qa-review", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[2].Actor, State: "APPROVED", Body: NativeQAMetadata(qa), SubmittedAt: now}}
	task.PRs = []PR{p}
	cat.Tasks[0] = task
	cat.Reports[qa.EvidenceMessageID] = c.Message{ID: qa.EvidenceMessageID, ThreadID: "private", AuthorID: "qa", AuthorKind: "agent", Body: "QA passed"}
	nativeAuthorizeDelivery(t, &cfg, &cat)
	return cfg, cat
}

func TestNativeDeliveryRequiresExactOwnerGrant(t *testing.T) {
	for _, change := range []string{"none", "author", "peer", "qa", "name", "agent", "bot", "capability", "missing", "revoked", "agent-grant", "head", "base", "scope", "artifact", "acceptance", "peer-review", "qa-review", "qa-evidence", "environment", "owner-evidence", "imported", "thread", "dismissed-peer", "dismissed-qa", "contributor", "missing-artifact", "incomplete", "goal-paused", "scope-drift", "zero-sequence", "negative-sequence", "pending-checks", "failed-checks", "hold", "unknown-contributors", "unknown-reviews", "stale-approval", "stale-assignment", "stale-task-scope", "stale-policy", "missing-owner-evidence", "missing-private-evidence", "stale-peer", "stale-qa", "qa-contributor"} {
		t.Run(change, func(t *testing.T) {
			cfg, cat := nativeDeliveryReadyFixture(t)
			h := &cat.Handoffs[0]
			p := &cat.Tasks[0].PRs[0]
			switch change {
			case "author", "peer", "qa":
				index := map[string]int{"author": 1, "peer": 0, "qa": 2}[change]
				cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[index].Name, cfg.Team.Members[index].Actor, change
			case "name":
				cfg.Name = "author"
			case "agent":
				cfg.Master.AgentID = "author"
			case "bot":
				cfg.Self.ID = 12
			case "capability":
				cfg.Team.Members[3].Capabilities = nil
			case "missing":
				cat.Handoffs = nil
			case "revoked":
				h.Input.Revoked = true
			case "agent-grant":
				h.ActorKind = "agent"
			case "head":
				h.Input.HeadSHA = strings.Repeat("f", 40)
			case "base":
				h.Input.BaseSHA = strings.Repeat("f", 40)
			case "scope":
				h.Input.PRScopeRevision = BodyHash("other")
			case "artifact":
				h.Input.ArtifactSHA = BodyHash("other")
			case "acceptance":
				h.Input.AcceptanceSHA = BodyHash("other")
			case "peer-review":
				h.Input.PeerReviewID = "other"
			case "qa-review":
				h.Input.QAReviewID = "other"
			case "qa-evidence":
				h.Input.QAEvidenceBodySHA = BodyHash("other")
			case "environment":
				h.Input.EnvironmentRevision = "other"
			case "owner-evidence", "imported", "thread":
				e := cat.Reports[h.Input.EvidenceMessageID]
				if change == "owner-evidence" {
					e.AuthorKind = "agent"
				}
				if change == "imported" {
					e.Imported = true
				}
				if change == "thread" {
					e.ThreadID = "other"
				}
				cat.Reports[e.ID] = e
			case "dismissed-peer":
				p.Reviews[0].State = "DISMISSED"
			case "dismissed-qa":
				p.Reviews[1].State = "DISMISSED"
			case "contributor":
				p.Contributors = append(p.Contributors, cfg.Team.Members[0].Actor)
			case "missing-artifact":
				p.QAArtifact = nil
			case "incomplete":
				cat.Complete = false
			case "goal-paused":
				cat.Goals[0].Valid = false
			case "scope-drift":
				cat.Tasks[0].Task.BodySHA = BodyHash("other")
			case "zero-sequence":
				h.Sequence = 0
			case "negative-sequence":
				h.Sequence = -1
			case "pending-checks":
				p.ChecksState = "pending"
			case "failed-checks":
				p.ChecksState = "failure"
			case "hold":
				cfg.DeliveryHold = true
			case "unknown-contributors":
				p.ContributorsComplete = false
			case "unknown-reviews":
				p.ReviewsComplete = false
			case "stale-approval":
				cat.Goals[0].ApprovalSequence++
			case "stale-assignment":
				p.AssignmentSHA = BodyHash("other assignment")
			case "stale-task-scope":
				h.Input.TaskScopeSHA = BodyHash("other task scope")
			case "stale-policy":
				cat.Tasks[0].Binding.PolicySHA = BodyHash("other policy")
			case "missing-owner-evidence":
				delete(cat.Reports, h.Input.EvidenceMessageID)
			case "missing-private-evidence":
				delete(cat.Reports, h.Input.QAEvidenceMessageID)
			case "stale-peer":
				p.Reviews[0].SubmittedAt = p.ScopeUpdatedAt
			case "stale-qa":
				p.Reviews[1].SubmittedAt = p.ScopeUpdatedAt
			case "qa-contributor":
				p.Contributors = append(p.Contributors, cfg.Team.Members[2].Actor)
			}
			jobs := NativeCandidates(cfg, cat, nil, time.Now())
			var delivery []NativeCandidate
			for _, job := range jobs {
				if job.Job.Kind == "deliver" {
					delivery = append(delivery, job)
				}
			}
			if change != "none" {
				if len(delivery) != 0 {
					t.Fatal("unsafe delivery admitted", delivery)
				}
				return
			}
			if len(delivery) != 1 {
				t.Fatal("missing delivery", jobs)
			}
			j := delivery[0].Job
			if j.Master.Role != "delivery" || j.Master.AgentID != "rook" || j.Master.ExecutorID != c.DeliveryGitHubBotID || j.Master.DeliveryHandoffID != h.ID || j.InputRevision != "delivery:"+h.ID || cat.Tasks[0].Binding.AuthorID != 12 || p.Author.ID != 12 {
				t.Fatal("delivery lost executor/source binding", j)
			}
		})
	}
}

func TestNativeDeliveryLatestDecisionWins(t *testing.T) {
	for _, mode := range []string{"newer", "tie", "revoked", "drift", "unrelated"} {
		for _, clock := range []string{"forward", "same", "rollback"} {
			for _, id := range []string{"039cf57d-21ee-48db-a28c-c4cc969d4201", "f634bfd7-c5e9-40b7-a285-036ad2cb0dd2"} {
				for _, order := range []string{"first", "last"} {
					t.Run(mode+"/"+clock+"/"+id+"/"+order, func(t *testing.T) {
						cfg, cat := nativeDeliveryReadyFixture(t)
						first := NativeCandidates(cfg, cat, nil, time.Now())[0].Job
						h := cat.Handoffs[0]
						h.ID = id
						if mode != "tie" {
							h.Sequence++
						}
						if clock == "forward" {
							h.CreatedAt = h.CreatedAt.Add(time.Hour)
						}
						if clock == "rollback" {
							h.CreatedAt = h.CreatedAt.Add(-time.Hour)
						}
						if mode == "revoked" {
							h.Input.Revoked = true
						}
						if mode == "drift" {
							h.Input.HeadSHA = strings.Repeat("f", 40)
						}
						if mode == "unrelated" {
							h.Input.PRURL += "9"
						}
						if order == "first" {
							cat.Handoffs = append([]c.Handoff{h}, cat.Handoffs...)
						} else {
							cat.Handoffs = append(cat.Handoffs, h)
						}
						jobs := NativeCandidates(cfg, cat, nil, time.Now())
						if mode == "revoked" || mode == "drift" || mode == "tie" {
							if len(jobs) != 0 {
								t.Fatal("fell back to superseded grant", jobs)
							}
							return
						}
						if len(jobs) != 1 {
							t.Fatal("missing current grant", jobs)
						}
						if mode == "unrelated" {
							if jobs[0].Job.ID != first.ID {
								t.Fatal("unrelated grant changed delivery identity")
							}
						} else if jobs[0].Job.ID == first.ID || jobs[0].Job.Master.DeliveryHandoffID != h.ID || jobs[0].Job.InputRevision != "delivery:"+h.ID {
							t.Fatal("replacement grant reused old delivery identity")
						}
					})
				}
			}
		}
	}
}

func nativeDeliveryActionFixture(t *testing.T) (Config, *dispatchMaster, *GitHubClient, *fakeOpenCode, *nativeDeliveryTransport, NativeExecution) {
	t.Helper()
	cfg, m, catalog, code := dispatchFixture(t)
	cfg.Projects, cfg.ProjectOwners = nil, nil
	task := catalog.catalog.Tasks[0]
	s := &nativeDeliveryTransport{t: t, cfg: cfg, head: strings.Repeat("a", 40), baseSHA: strings.Repeat("d", 40), taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding)}
	s.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: &QAArtifact{Kind: "workflow", HeadSHA: s.head, SHA256: strings.Repeat("e", 64), URL: "https://api.github.com/repos/code/repo/actions/artifacts/1", Revision: "1"}})
	s.commands = &fakeCommands{respond: s.response}
	g := NewGitHubClient(s)
	m.links = []c.ArtifactLink{{Kind: "task", Goal: task.Binding.Goal, URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}, {Kind: "pull_request", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: "https://github.com/code/repo/pull/1", AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
	p, err := g.ReadNativePR(t.Context(), cfg, task, m.authority, "https://github.com/code/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(m.authority), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "qa-evidence", EvidenceBodySHA: BodyHash("QA passed"), Verdict: "approved"}
	for i, body := range []string{NativePeerMetadata(peer), NativeQAMetadata(qa)} {
		member := cfg.Team.Members[[]int{0, 2}[i]]
		s.reviews = append(s.reviews, map[string]any{"fullDatabaseId": i + 1, "state": "APPROVED", "body": body, "submittedAt": time.Now().UTC(), "commit": map[string]any{"oid": p.HeadSHA}, "author": map[string]any{"__typename": "Bot", "databaseId": member.Actor.ID, "login": member.Actor.Login}})
	}
	m.reports = map[string]c.Message{"qa-evidence": {ID: "qa-evidence", ThreadID: "private", AuthorID: "qa", AuthorKind: "agent", Body: "QA passed"}}
	cat, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	h := nativeAuthorizeDelivery(t, &cfg, &cat)
	m.handoffs = []c.Handoff{h}
	m.reports[h.Input.EvidenceMessageID] = cat.Reports[h.Input.EvidenceMessageID]
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "rook", cfg.Self.ID, "rook"
	s.cfg = cfg
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" {
		t.Fatal("missing delivery fixture", jobs)
	}
	v := jobs[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	x.Job.SessionID, x.Job.MessageID = "ses_delivery", "msg_delivery"
	x.Job.Phase, x.Job.SubmissionState = "active", "accepted"
	if err := nativeValidateDelivery(t.Context(), cfg, m, g, x); err != nil {
		t.Fatal("live delivery fixture is not authorized", err)
	}
	return cfg, m, g, code, s, x
}

func TestNativeDeliveryLiveGuards(t *testing.T) {
	for _, path := range []string{"action", "confirmation", "recovery", "merged-recovery"} {
		for _, change := range []string{"author", "peer", "qa", "revoked", "replacement", "head", "base", "scope", "peer-dismissed", "qa-dismissed", "qa-evidence", "grant-evidence", "role", "handoff-id"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				cfg, m, g, code, s, x := nativeDeliveryActionFixture(t)
				if path == "confirmation" || path == "merged-recovery" {
					s.merged, s.closed, s.mergeParentSHA = true, true, s.baseSHA
				}
				switch change {
				case "author", "peer", "qa":
					i := map[string]int{"author": 1, "peer": 0, "qa": 2}[change]
					cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[i].Name, cfg.Team.Members[i].Actor, change
					m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = change, cfg.Self.ID, change
					x.Job.Master.AgentID, x.Job.Master.ExecutorID = change, cfg.Self.ID
					s.cfg = cfg
				case "revoked":
					m.handoffs[0].Input.Revoked = true
					m.handoffs[0].Sequence++
				case "replacement":
					m.handoffs[0].ID = "replacement-grant"
					m.handoffs[0].Sequence++
				case "head":
					s.head = strings.Repeat("f", 40)
				case "base":
					s.baseSHA, s.mergeParentSHA = strings.Repeat("f", 40), strings.Repeat("f", 40)
				case "scope":
					m.handoffs[0].Input.PRScopeRevision = BodyHash("changed scope")
				case "peer-dismissed":
					s.reviews[0]["state"] = "DISMISSED"
				case "qa-dismissed":
					s.reviews[1]["state"] = "DISMISSED"
				case "qa-evidence":
					e := m.reports["qa-evidence"]
					e.Body = "changed"
					m.reports[e.ID] = e
				case "grant-evidence":
					e := m.reports["delivery-evidence"]
					e.AuthorKind = "agent"
					m.reports[e.ID] = e
				case "role":
					x.Job.Master.Role = "implementer"
				case "handoff-id":
					x.Job.Master.DeliveryHandoffID = "other"
				}
				in := NativeAction{Operation: "deliver", RequestID: "delivery"}
				if path == "recovery" || path == "merged-recovery" {
					x.Phase, x.Reason = "blocked", "native_completed_without_result"
					x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now().UTC()
					code.running = false
					raw, _ := json.Marshal(in)
					step := ""
					if path == "merged-recovery" {
						step = "merge"
					}
					if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: "deliver", State: "prepared", Step: step}); err != nil {
						t.Fatal(err)
					}
				}
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
				var err error
				switch path {
				case "action":
					_, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
				case "confirmation":
					err = nativeConfirmDelivery(t.Context(), cfg, m, g, m.authority, x)
				case "recovery", "merged-recovery":
					_, err = RecoverNativeDelivery(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, in)
				}
				if err == nil || len(s.writes) != 0 || code.calls != 0 || code.aborts != 0 {
					t.Fatal("unsafe delivery crossed effect boundary", err, s.writes)
				}
			})
		}
	}
}

func TestNativeDeliveryMergedConfirmation(t *testing.T) {
	for _, mode := range []string{"exact", "base-advanced", "wrong-parent", "two-parents", "no-parents", "missing-merge-sha", "missing-artifact"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, _, s, x := nativeDeliveryActionFixture(t)
			s.merged, s.closed, s.mergeParentSHA = true, true, s.baseSHA
			if mode == "base-advanced" {
				s.baseSHA = strings.Repeat("f", 40)
			}
			if mode == "wrong-parent" {
				s.mergeParentSHA = strings.Repeat("f", 40)
			}
			if mode == "missing-artifact" {
				s.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: x.Goal, TaskURL: x.Job.TaskURL, AssignmentSHA: x.Job.Master.AssignmentSHA})
			}
			s.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if endpoint == "repos/code/repo/pulls/1" && mode == "missing-merge-sha" {
					return jsonResponse(map[string]any{}), nil
				}
				if strings.Contains(endpoint, "/git/commits/") {
					if mode == "two-parents" {
						return jsonResponse(map[string]any{"parents": []any{map[string]any{"sha": s.mergeParentSHA}, map[string]any{"sha": s.head}}}), nil
					}
					if mode == "no-parents" {
						return jsonResponse(map[string]any{"parents": []any{}}), nil
					}
				}
				return s.response(owner, endpoint, method, body)
			}
			err := nativeConfirmDelivery(t.Context(), cfg, m, g, m.authority, x)
			want := mode == "exact" || mode == "base-advanced"
			if (err == nil) != want || len(s.writes) != 0 {
				t.Fatal("incorrect read-only merge confirmation", err, s.writes)
			}
		})
	}
}
