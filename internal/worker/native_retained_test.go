package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Start with the production delivery transport and its ORIGINAL registered
// source binding/reviews, then change only the execution roster and owner grants.
func retainedProductionFixture(t *testing.T) (Config, *dispatchMaster, *GitHubClient, *fakeOpenCode, *nativeDeliveryTransport, NativeTaskBinding) {
	t.Helper()
	cfg, m, g, code, transport, source := nativeDeliveryActionFixture(t)
	cat, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	task, p := cat.Tasks[0], cat.Tasks[0].PRs[0]
	// Build the historical fixture as a real immutable source-bundle assessment,
	// not a caller-supplied workflow digest or a replacement task assignment.
	bundle, _, err := g.api(t.Context(), "code", "GET", "repos/code/repo/tarball/"+p.HeadSHA, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := sourceBundleSHA(bundle)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &QAArtifact{Kind: "source", HeadSHA: p.HeadSHA, SHA256: payload, URL: "https://api.github.com/repos/code/repo/tarball/" + p.HeadSHA, Revision: "source-bundle-v1"}
	transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: artifact})
	p, err = g.ReadNativePR(t.Context(), cfg, task, m.authority, p.URL)
	if err != nil {
		t.Fatal(err)
	}
	for i := range transport.reviews {
		body := transport.reviews[i]["body"].(string)
		if i == 0 {
			var review NativeReviewResult
			if err := nativeMetadata(body, "review", &review); err != nil {
				t.Fatal(err)
			}
			review.PRScopeRevision = p.ScopeRevision
			transport.reviews[i]["body"] = NativePeerMetadata(review)
		} else {
			var qa NativeQAResult
			if err := nativeMetadata(body, "qa", &qa); err != nil {
				t.Fatal(err)
			}
			qa.PRScopeRevision, qa.ArtifactSHA = p.ScopeRevision, payload
			transport.reviews[i]["body"] = NativeQAMetadata(qa)
		}
	}
	p, err = g.ReadNativePR(t.Context(), cfg, task, m.authority, p.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Team.Members = append(cfg.Team.Members,
		TeamMember{Name: "hawk", Actor: Actor{ID: 14, Kind: "Bot", Login: "hawk[bot]"}, Capabilities: []string{"engineering-review"}},
		TeamMember{Name: "wren", Actor: Actor{ID: 15, Kind: "Bot", Login: "wren[bot]"}, Capabilities: []string{"customer-qa"}})
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil {
		t.Fatal(err)
	}
	h := m.handoffs[0]
	h.Input.PRScopeRevision, h.Input.ArtifactSHA = p.ScopeRevision, payload
	h.ID, h.Input.Kind = "retained-gates", "retained-gates"
	h.Input.Retained = c.RetainedDeliveryAuthority{SourcePolicySHA: task.Binding.PolicySHA, ExecutionPolicySHA: policy, SourceTreeSHA: strings.Repeat("b", 40), PayloadSHA: p.QAArtifact.SHA256, SourcePeerReviewID: h.Input.PeerReviewID, SourcePeerBodySHA: BodyHash(p.Reviews[0].Body), SourceQAReviewID: h.Input.QAReviewID, SourceQABodySHA: BodyHash(p.Reviews[1].Body), SourceReportID: h.Input.QAEvidenceMessageID, SourceReportBodySHA: h.Input.QAEvidenceBodySHA, SourceEnvironment: h.Input.Environment, SourceEnvironmentRevision: h.Input.EnvironmentRevision, DeliveryAcceptanceSHA: h.Input.AcceptanceSHA, Override: c.RetainedDeliveryOverride}
	raw, _, err := g.api(t.Context(), "code", "GET", "repos/code/repo/tarball/"+p.HeadSHA, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Input.Retained.PayloadSHA, err = sourceBundleSHA(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateHandoff(h.Input); err != nil {
		t.Fatal(err)
	}
	m.handoffs = []c.Handoff{h}
	m.threads[0].Members = append(m.threads[0].Members, "owner", "hawk", "wren")
	transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo/git/commits/"+transport.head {
			return jsonResponse(map[string]any{"sha": transport.head, "tree": map[string]any{"sha": strings.Repeat("b", 40)}}), nil
		}
		return transport.response(owner, endpoint, method, body)
	}
	if validateNativeRoster(cfg, *source.Binding) == nil {
		t.Fatal("fixture did not change policy")
	}
	return cfg, m, g, code, transport, *source.Binding
}

func retainedAs(cfg *Config, m *dispatchMaster, transport *nativeDeliveryTransport, name string) {
	for _, member := range cfg.Team.Members {
		if member.Name == name {
			cfg.Name, cfg.Master.AgentID, cfg.Self = name, name, member.Actor
		}
	}
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = name, cfg.Self.ID, name
	transport.cfg = *cfg
}

func retainedExecution(t *testing.T, cfg Config, m *dispatchMaster, g *GitHubClient, binding NativeTaskBinding, kind string) NativeExecution {
	t.Helper()
	cat, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil {
		t.Fatal("production discovery", err)
	}
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != kind {
		t.Fatalf("jobs = %+v", jobs)
	}
	v := jobs[0]
	if NativeAssignmentSHA(v.Task.Binding) != NativeAssignmentSHA(binding) || v.Task.Binding.PolicySHA != binding.PolicySHA {
		t.Fatal("rewrote immutable binding")
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: binding.Goal, Binding: &binding, Job: v.Job, Phase: "active"}
	x.Job.SessionID, x.Job.MessageID = "ses_retained", "msg_retained"
	x.Job.Phase, x.Job.SubmissionState = "active", "accepted"
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	return x
}

func retainedReports(t *testing.T, cfg *Config, m *dispatchMaster, g *GitHubClient, transport *nativeDeliveryTransport, binding NativeTaskBinding) c.Handoff {
	t.Helper()
	results := []NativeActionResult{}
	sourceState := cfg.StateDir
	for _, name := range []string{"hawk", "wren"} {
		cfg.StateDir = sourceState + "-" + name
		retainedAs(cfg, m, transport, name)
		saveNativeFixtureConnection(t, *cfg)
		kind := "delivery-review"
		if name == "wren" {
			kind = "delivery-qa"
		}
		x := retainedExecution(t, *cfg, m, g, binding, kind)
		d := &NativeDispatcher{config: *cfg, master: m, github: g, clock: RealClock{}}
		candidate, err := d.freshCandidate(t.Context(), x)
		if err != nil {
			t.Fatal("prepare revalidation", err)
		}
		prompt, err := nativePrompt(*cfg, x, candidate)
		if err != nil || !strings.Contains(prompt, "ALL original assertions") {
			t.Fatal("missing retained instructions", err)
		}
		result, err := RunNativeAction(t.Context(), *cfg, m, g, x.Job.ID, NativeAction{Operation: "delivery-readiness", RequestID: "readiness", Verdict: "ready"})
		if err != nil {
			t.Fatal("private readiness", name, err)
		}
		var report c.RetainedGateReport
		if err := c.DecodeJSON([]byte(m.reports[result.EvidenceMessageID].Body), &report); err != nil || report.Binding != m.handoffs[0].Input || report.GateHandoffID != m.handoffs[0].ID || report.AssignmentSHA != NativeAssignmentSHA(binding) {
			t.Fatal("unbound private report", err)
		}
		results = append(results, result)
		x.Phase = "completed"
		if err := saveNativeExecution(*cfg, x); err != nil {
			t.Fatal(err)
		}
	}
	if len(transport.writes) != 0 {
		t.Fatal("gates wrote GitHub", transport.writes)
	}
	h := m.handoffs[0]
	h.ID, h.Sequence, h.Input.Kind, h.Input.RequestID = "retained-deliver", 2, "retained-deliver", "retained-deliver"
	h.Input.Retained.EngineeringReportID, h.Input.Retained.EngineeringReportBodySHA = results[0].EvidenceMessageID, results[0].EvidenceBodySHA
	h.Input.Retained.QAReportID, h.Input.Retained.QAReportBodySHA = results[1].EvidenceMessageID, results[1].EvidenceBodySHA
	if err := c.ValidateHandoff(h.Input); err != nil {
		t.Fatal(err)
	}
	m.handoffs = append(m.handoffs, h)
	cfg.StateDir = sourceState
	retainedAs(cfg, m, transport, "rook")
	return h
}

func TestRetainedProductionMergeJourney(t *testing.T) {
	cfg, m, g, _, transport, binding := retainedProductionFixture(t)
	metadata, prBody := transport.metadata, transport.prBody
	retainedReports(t, &cfg, m, g, transport, binding)
	x := retainedExecution(t, cfg, m, g, binding, "deliver")
	result, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "retained-merge"})
	if err != nil || result.Phase != "completed" {
		t.Fatal("retained merge", err)
	}
	if !transport.merged || !transport.closed || transport.metadata != metadata || transport.prBody != prBody {
		t.Fatal("lost original task/source", transport.writes)
	}
	if err := nativeConfirmDelivery(t.Context(), cfg, m, g, m.authority, x); err != nil {
		t.Fatal("confirmation", err)
	}
}

func TestRetainedProductionGateDispatch(t *testing.T) {
	for _, name := range []string{"hawk", "wren"} {
		t.Run(name, func(t *testing.T) {
			cfg, m, g, code, transport, binding := retainedProductionFixture(t)
			retainedAs(&cfg, m, transport, name)
			kind := "delivery-review"
			if name == "wren" {
				kind = "delivery-qa"
			}
			x := retainedExecution(t, cfg, m, g, binding, kind)
			x.Phase = "selected"
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			transport.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
			d, err := NewNativeDispatcher(cfg, m, g, code, NewCheckouts(cfg, transport), RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "active" {
				t.Fatal("gate preparation/submission", err, x.Phase)
			}
			if _, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "delivery-readiness", RequestID: "gate-result", Verdict: "ready"}); err != nil {
				t.Fatal(err)
			}
			code.running = false
			if complete, err := d.advance(t.Context(), &x); err != nil || !complete || x.Phase != "completed" {
				t.Fatal("gate result completion", complete, err, x.Phase)
			}
			if len(transport.writes) != 0 {
				t.Fatal("gate mutated source", transport.writes)
			}
		})
	}
}

func TestRetainedProductionFailsClosed(t *testing.T) {
	for _, change := range []string{"bridge", "policy", "source-policy", "mapping", "gate-revoked", "deliver-revoked", "assignment-drift", "head-drift", "tree-drift", "peer-body", "qa-body", "report-body", "gate-report", "gate-binding", "gate-blocked", "gate-author", "gate-imported", "gate-thread", "membership", "contributor", "incomplete", "checks", "hold", "gate-replaced"} {
		t.Run(change, func(t *testing.T) {
			cfg, m, g, _, transport, binding := retainedProductionFixture(t)
			retainedReports(t, &cfg, m, g, transport, binding)
			x := retainedExecution(t, cfg, m, g, binding, "deliver")
			switch change {
			case "bridge":
				m.handoffs = nil
			case "policy":
				cfg.Team.Members[0].Capabilities = append(cfg.Team.Members[0].Capabilities, "plan")
			case "source-policy":
				m.handoffs[1].Input.Retained.SourcePolicySHA = BodyHash("wrong")
			case "mapping":
				cfg.Team.EngineeringPeers["author"] = "hawk"
			case "gate-revoked":
				h := m.handoffs[0]
				h.ID, h.Sequence, h.Input.Revoked = "revoked-gates", 3, true
				m.handoffs = append(m.handoffs, h)
			case "deliver-revoked":
				h := m.handoffs[1]
				h.ID, h.Sequence, h.Input.Revoked = "revoked-deliver", 3, true
				m.handoffs = append(m.handoffs, h)
			case "assignment-drift":
				m.handoffs[1].Input.AssignmentSHA = BodyHash("wrong")
			case "head-drift":
				transport.head = strings.Repeat("f", 40)
			case "tree-drift":
				m.handoffs[1].Input.Retained.SourceTreeSHA = strings.Repeat("f", 40)
			case "peer-body":
				transport.reviews[0]["body"] = "changed"
			case "qa-body":
				transport.reviews[1]["body"] = "changed"
			case "report-body":
				report := m.reports["qa-evidence"]
				report.Body = "changed"
				m.reports[report.ID] = report
			case "gate-report":
				delete(m.reports, m.handoffs[1].Input.Retained.EngineeringReportID)
			case "gate-blocked", "gate-author", "gate-imported", "gate-thread":
				id := m.handoffs[1].Input.Retained.EngineeringReportID
				report := m.reports[id]
				switch change {
				case "gate-blocked":
					var body c.RetainedGateReport
					_ = json.Unmarshal([]byte(report.Body), &body)
					body.Result = "blocked"
					raw, _ := json.Marshal(body)
					report.Body = string(raw)
				case "gate-author":
					report.AuthorID = "rook"
				case "gate-imported":
					report.Imported = true
				case "gate-thread":
					report.ThreadID = "public"
				}
				m.reports[id] = report
				m.handoffs[1].Input.Retained.EngineeringReportBodySHA = BodyHash(report.Body)
			case "membership":
				m.threads[0].Members = []string{"author", "peer", "qa", "rook", "owner", "wren"}
			case "gate-binding":
				id := m.handoffs[1].Input.Retained.EngineeringReportID
				report := m.reports[id]
				var body c.RetainedGateReport
				_ = json.Unmarshal([]byte(report.Body), &body)
				body.Binding.Retained.SourcePolicySHA = BodyHash("wrong")
				raw, _ := json.Marshal(body)
				report.Body = string(raw)
				m.reports[id] = report
				m.handoffs[1].Input.Retained.EngineeringReportBodySHA = BodyHash(report.Body)
			case "contributor":
				cfg.Self.ID = binding.AuthorID
			case "incomplete":
				transport.reviews = nil
			case "checks":
				transport.checks = "FAILURE"
			case "hold":
				cfg.DeliveryHold = true
			case "gate-replaced":
				h := m.handoffs[0]
				h.ID, h.Sequence = "replacement-gates", 3
				m.handoffs = append(m.handoffs, h)
			}
			_, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "denied"})
			if err == nil || len(transport.writes) != 0 {
				t.Fatal("unsafe retained effect", err, transport.writes)
			}
		})
	}
}

func TestRetainedProductionRecovery(t *testing.T) {
	for _, mode := range []string{"unstarted", "merged", "revoked", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, code, transport, binding := retainedProductionFixture(t)
			retainedReports(t, &cfg, m, g, transport, binding)
			x := retainedExecution(t, cfg, m, g, binding, "deliver")
			x.Phase, x.Reason = "blocked", "native_completed_without_result"
			x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now().UTC()
			code.running = false
			in := NativeAction{Operation: "deliver", RequestID: "original-retained-delivery"}
			raw, _ := json.Marshal(in)
			r := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "prepared"}
			if mode == "merged" {
				r.Step = "merge"
				transport.merged, transport.closed, transport.mergeParentSHA = true, true, transport.baseSHA
			}
			if mode == "uncertain" {
				r.State = "uncertain"
			}
			if mode == "revoked" {
				m.handoffs[1].Input.Revoked = true
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			if err := saveNativeAction(cfg, r); err != nil {
				t.Fatal(err)
			}
			result, err := RecoverNativeDelivery(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, in)
			if mode == "revoked" || mode == "uncertain" {
				if err == nil || len(transport.writes) != 0 {
					t.Fatal("unsafe recovery", err, transport.writes)
				}
				return
			}
			if err != nil || result.Phase != "completed" {
				t.Fatal("retained recovery", err)
			}
			if mode == "merged" && len(transport.writes) != 0 {
				t.Fatal("replayed acknowledged merge", transport.writes)
			}
			if code.calls != 0 || code.aborts != 0 {
				t.Fatal("recovery submitted a model")
			}
		})
	}
}

func TestRetainedCachedProductionDiscovery(t *testing.T) {
	for _, actor := range []string{"hawk", "wren", "rook"} {
		for _, mode := range []string{"exact", "missing-bridge", "stale", "payload-drift", "revoked"} {
			t.Run(actor+"/"+mode, func(t *testing.T) {
				cfg, m, g, _, transport, binding := retainedProductionFixture(t)
				retainedReports(t, &cfg, m, g, transport, binding)
				retainedAs(&cfg, m, transport, actor)
				cat, err := g.NativeSnapshot(t.Context(), cfg, m)
				if err != nil {
					t.Fatal(err)
				}
				task, p := cat.Tasks[0], cat.Tasks[0].PRs[0]
				f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("retained-manifest"), Goals: []c.GoalAuthority{m.authority}}}
				addCachedTask(t, f, task)
				ref := c.GitHubObjectRef{RouteID: "route", Goal: binding.Goal, TaskURL: task.Task.URL, URL: p.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(binding)}
				raw, _ := json.Marshal(p)
				sha := c.GitHubDigest(raw)
				f.data[sha] = raw
				f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Minute), Header: c.GitHubHeader{Revision: sha, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, ScopeRevision: p.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: sha, HeaderRevision: sha, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Minute)}}})
				switch mode {
				case "missing-bridge":
					m.handoffs = nil
				case "stale":
					f.page.Objects[0].Dirty = true
				case "payload-drift":
					for i := range m.handoffs {
						m.handoffs[i].Input.Retained.PayloadSHA = BodyHash("different")
					}
				case "revoked":
					for i := range m.handoffs {
						m.handoffs[i].Input.Revoked = true
					}
				}
				out, err := NewCachedNativeReader(cfg, g, f, RealClock{}).NativeSnapshot(t.Context(), cfg, m)
				if err != nil {
					t.Fatal(err)
				}
				jobs := NativeCandidates(cfg, out, nil, time.Now())
				if mode != "exact" {
					if len(jobs) != 0 {
						t.Fatal("unsafe cached retained admission", jobs)
					}
					return
				}
				if len(jobs) != 1 || NativeAssignmentSHA(jobs[0].Task.Binding) != NativeAssignmentSHA(binding) {
					t.Fatal("cache lost original retained assignment", jobs)
				}
				if _, ok := ChooseTeamJob([]Job{jobs[0].Job}, ScheduleCounters{}); !ok {
					t.Fatal("retained stage was not schedulable")
				}
			})
		}
	}
}

type retainedEvidenceMaster struct {
	*dispatchMaster
	read []string
}

func (m *retainedEvidenceMaster) Message(ctx context.Context, id string) (c.Message, error) {
	m.read = append(m.read, id)
	return m.dispatchMaster.Message(ctx, id)
}

func TestRetainedRevokedEvidenceIsNotHydrated(t *testing.T) {
	cfg, master, g, _, transport, binding := retainedProductionFixture(t)
	retainedAs(&cfg, master, transport, "hawk")
	old := master.handoffs[0]
	old.Input.EvidenceMessageID, old.Input.Retained.SourceReportID = "inaccessible-owner", "inaccessible-report"
	newest := master.handoffs[0]
	newest.ID, newest.Sequence, newest.Input.Revoked = "revocation", 2, true
	master.handoffs = []c.Handoff{old, newest}
	m := &retainedEvidenceMaster{dispatchMaster: master}
	cat, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil || !cat.Complete || len(cat.Tasks) != 0 || len(NativeCandidates(cfg, cat, nil, time.Now())) != 0 {
		t.Fatal("revoked retained evidence contaminated discovery", err)
	}
	if len(m.read) != 0 {
		t.Fatal("hydrated revoked inaccessible evidence", m.read)
	}
	if _, err := g.ReadNativeTask(t.Context(), cfg, binding.TaskURL); err == nil {
		t.Fatal("ordinary engineering task read accepted changed policy")
	}
}

func TestRetainedHistoricalQAEnvironmentRequiresOriginalHandoff(t *testing.T) {
	for _, bridge := range []bool{true, false} {
		t.Run(map[bool]string{true: "historical-authority", false: "missing-historical-authority"}[bridge], func(t *testing.T) {
			cfg, m, g, _, transport, binding := retainedProductionFixture(t)
			gates := &m.handoffs[0]
			var qa NativeQAResult
			if err := nativeMetadata(transport.reviews[1]["body"].(string), "qa", &qa); err != nil {
				t.Fatal(err)
			}
			previous := qa
			previous.Verdict = "changes-needed"
			transport.reviews = append(transport.reviews, map[string]any{"fullDatabaseId": 3, "state": "CHANGES_REQUESTED", "body": NativeQAMetadata(previous), "submittedAt": time.Now().Add(-30 * time.Minute).UTC(), "commit": map[string]any{"oid": transport.head}, "author": map[string]any{"__typename": "Bot", "databaseId": binding.QAID, "login": "qa[bot]"}})
			historical := c.Handoff{ID: "historical-environment", ActorID: "owner", ActorKind: "owner", CreatedAt: time.Now().Add(-20 * time.Minute).UTC(), Input: c.HandoffInput{Kind: "reassess-qa", Goal: binding.Goal, TaskURL: binding.TaskURL, PRURL: gates.Input.PRURL, AssignmentSHA: NativeAssignmentSHA(binding), HeadSHA: qa.HeadSHA, ArtifactSHA: qa.ArtifactSHA, ReviewID: "3", Environment: "historical-device", EnvironmentRevision: "historical-device-v1", EvidenceMessageID: "environment-decision", RequestID: "environment-decision"}}
			qa.Assessment, qa.HandoffID = "reassessment", historical.ID
			qa.Environment, qa.EnvironmentRevision = historical.Input.Environment, historical.Input.EnvironmentRevision
			transport.reviews[1]["body"] = NativeQAMetadata(qa)
			gates.Input.Retained.SourceEnvironment, gates.Input.Retained.SourceEnvironmentRevision = qa.Environment, qa.EnvironmentRevision
			gates.Input.Retained.SourceQABodySHA = BodyHash(NativeQAMetadata(qa))
			if bridge {
				m.handoffs = append(m.handoffs, historical)
			}
			retainedAs(&cfg, m, transport, "wren")
			cat, err := g.NativeSnapshot(t.Context(), cfg, m)
			if err != nil {
				t.Fatal(err)
			}
			jobs := NativeCandidates(cfg, cat, nil, time.Now())
			if (len(jobs) == 1) != bridge {
				t.Fatal("historical environment was relabeled or authority lost", jobs)
			}
			if bridge && (jobs[0].Handoff.Input.Retained.SourceEnvironment != "historical-device" || jobs[0].Goal.Revision.Proposal.QA.Environment != "staging") {
				t.Fatal("rewrote historical environment")
			}
		})
	}
}
