package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func tagOnlyFixture(t *testing.T) (Config, NativeCatalog, c.Handoff) {
	t.Helper()
	cfg, cat := nativeDeliveryReadyFixture(t)
	cfg.StateDir = t.TempDir()
	h := cat.Handoffs[0]
	cfg.Team.Members = append(cfg.Team.Members, TeamMember{Name: "hawk", Actor: Actor{ID: 14, Kind: "Bot", Login: "hawk[bot]"}, Capabilities: []string{"engineering-review"}}, TeamMember{Name: "wren", Actor: Actor{ID: 15, Kind: "Bot", Login: "wren[bot]"}, Capabilities: []string{"customer-qa"}})
	policy, _ := TeamPolicySHA(*cfg.Team)
	sourceDelivery := h
	sourceDelivery.Input.Kind = "retained-deliver"
	sourceDelivery.Input.Retained = c.RetainedDeliveryAuthority{SourcePolicySHA: cat.Tasks[0].Binding.PolicySHA, ExecutionPolicySHA: policy, SourceTreeSHA: strings.Repeat("a", 40), PayloadSHA: h.Input.ArtifactSHA, SourcePeerReviewID: h.Input.PeerReviewID, SourcePeerBodySHA: BodyHash(nativeLatestReview(cat.Tasks[0].PRs[0], cat.Tasks[0].Binding.PeerID).Body), SourceQAReviewID: h.Input.QAReviewID, SourceQABodySHA: BodyHash(nativeLatestReview(cat.Tasks[0].PRs[0], cat.Tasks[0].Binding.QAID).Body), SourceReportID: h.Input.QAEvidenceMessageID, SourceReportBodySHA: h.Input.QAEvidenceBodySHA, SourceEnvironment: h.Input.Environment, SourceEnvironmentRevision: h.Input.EnvironmentRevision, DeliveryAcceptanceSHA: h.Input.AcceptanceSHA, Override: c.RetainedDeliveryOverride, EngineeringReportID: "original-hawk", EngineeringReportBodySHA: BodyHash("original source readiness"), QAReportID: "original-wren", QAReportBodySHA: BodyHash("original source readiness")}
	for _, actor := range []string{"hawk", "wren"} {
		cat.Reports["original-"+actor] = c.Message{ID: "original-" + actor, AuthorKind: "agent", AuthorID: actor, ThreadID: cat.Goals[0].Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: "original source readiness"}
	}
	h.ID, h.Input.Kind, h.Input.Tag = "tag-grant", "tag-only", "v1.2.3"
	h.CreatedAt = h.CreatedAt.Truncate(time.Second)
	task := &cat.Tasks[0]
	task.Task.Completed, task.Task.Closed = true, true
	p := &task.PRs[0]
	p.Merged, p.Closed = true, true
	h.Input.TagOnly = c.TagOnlyAuthority{ExecutionPolicySHA: policy, SourcePolicySHA: task.Binding.PolicySHA, SourceCommitSHA: p.HeadSHA, SourceTreeSHA: strings.Repeat("a", 40), IntegratedCommitSHA: strings.Repeat("b", 40), IntegratedTreeSHA: strings.Repeat("a", 40), OwnerBodySHA: BodyHash(cat.Reports[h.Input.EvidenceMessageID].Body), PeerBodySHA: BodyHash(nativeLatestReview(*p, task.Binding.PeerID).Body), QABodySHA: BodyHash(nativeLatestReview(*p, task.Binding.QAID).Body)}
	h.Input.TagOnly.SourceDeliveryHandoffID, h.Input.TagOnly.SourceDeliveryInputSHA = sourceDelivery.ID, tagOnlyInputSHA(sourceDelivery.Input)
	h.Input.TagOnly.SourceCompletionRequestID = "source-complete"
	h.Input.TagOnly.TagAcceptanceSHA, h.Input.TagOnly.TagEnvironment, h.Input.TagOnly.TagEnvironmentRevision = BodyHash("exact tag and version source"), "staging", "staging-v1"
	for _, actor := range []string{"hawk", "wren"} {
		raw, _ := json.Marshal(c.TagOnlyReport{Binding: c.TagOnlyReportBinding(h.Input), Result: "ready"})
		id := "tag-report-" + actor
		cat.Reports[id] = c.Message{ID: id, AuthorID: actor, AuthorKind: "agent", ThreadID: cat.Goals[0].Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: string(raw)}
		if actor == "hawk" {
			h.Input.TagOnly.EngineeringReportID, h.Input.TagOnly.EngineeringReportBodySHA = id, BodyHash(string(raw))
		} else {
			h.Input.TagOnly.QAReportID, h.Input.TagOnly.QAReportBodySHA = id, BodyHash(string(raw))
		}
	}
	cat.Handoffs = []c.Handoff{h, sourceDelivery}
	if err := c.ValidateHandoff(h.Input); err != nil {
		t.Fatal(err)
	}
	return cfg, cat, h
}

func TestTagOnlyCandidatesExactAuthority(t *testing.T) {
	for _, change := range []string{"none", "author", "peer", "qa", "name", "agent", "bot", "capability", "hold", "incomplete", "paused", "open-task", "unmerged", "assignment", "policy", "source-policy", "tree", "version", "owner-body", "peer-body", "qa-body", "report", "imported", "head", "acceptance", "artifact", "revoked", "newer-drift", "tie"} {
		t.Run(change, func(t *testing.T) {
			cfg, cat, _ := tagOnlyFixture(t)
			h := &cat.Handoffs[0]
			switch change {
			case "author", "peer", "qa":
				i := map[string]int{"author": 1, "peer": 0, "qa": 2}[change]
				cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[i].Name, cfg.Team.Members[i].Actor, change
			case "name":
				cfg.Name = "hawk"
			case "agent":
				cfg.Master.AgentID = "hawk"
			case "bot":
				cfg.Self.ID++
			case "capability":
				cfg.Team.Members[3].Capabilities = nil
			case "hold":
				cfg.DeliveryHold = true
			case "incomplete":
				cat.Complete = false
			case "paused":
				cat.Goals[0].State = "paused"
			case "open-task":
				cat.Tasks[0].Task.Completed = false
			case "unmerged":
				cat.Tasks[0].PRs[0].Merged = false
			case "assignment":
				h.Input.AssignmentSHA = BodyHash("drift")
			case "policy":
				h.Input.TagOnly.ExecutionPolicySHA = BodyHash("drift")
			case "source-policy":
				h.Input.TagOnly.SourcePolicySHA = BodyHash("drift")
			case "tree":
				h.Input.TagOnly.IntegratedTreeSHA = strings.Repeat("f", 40)
			case "version":
				h.Input.TagOnly.VersionPRURL = "https://github.com/code/repo/pull/2"
			case "owner-body":
				h.Input.TagOnly.OwnerBodySHA = BodyHash("drift")
			case "peer-body":
				h.Input.TagOnly.PeerBodySHA = BodyHash("drift")
			case "qa-body":
				h.Input.TagOnly.QABodySHA = BodyHash("drift")
			case "report":
				delete(cat.Reports, h.Input.TagOnly.EngineeringReportID)
			case "imported":
				m := cat.Reports[h.Input.TagOnly.QAReportID]
				m.Imported = true
				cat.Reports[m.ID] = m
			case "head":
				h.Input.HeadSHA = strings.Repeat("f", 40)
			case "acceptance":
				h.Input.AcceptanceSHA = BodyHash("drift")
			case "artifact":
				h.Input.ArtifactSHA = BodyHash("drift")
			case "revoked":
				h.Input.Revoked = true
			case "newer-drift", "tie":
				next := *h
				next.ID = "new"
				if change != "tie" {
					next.Sequence++
					next.Input.AssignmentSHA = BodyHash("drift")
				}
				cat.Handoffs = append(cat.Handoffs, next)
			}
			got := NativeTagOnlyCandidates(cfg, cat, nil, time.Now())
			if change != "none" {
				if len(got) != 0 {
					t.Fatal("drift admitted", got)
				}
				return
			}
			if len(got) != 1 || !nativeTagOnlyJob(cfg, got[0].Job) || got[0].Job.Master.DeliveryHandoffID != h.ID {
				t.Fatalf("candidate: %+v", got)
			}
			if len(NativeTagOnlyCandidates(cfg, cat, map[string]NativeExecution{got[0].Job.ID: {Phase: "completed"}}, time.Now())) != 0 {
				t.Fatal("duplicate job")
			}
		})
	}
}

func TestTagOnlySchemaAndAssignedStage(t *testing.T) {
	cfg, cat, h := tagOnlyFixture(t)
	raw, err := json.Marshal(h.Input)
	var decoded c.HandoffInput
	if err != nil || c.DecodeJSON(raw, &decoded) != nil || decoded != h.Input {
		t.Fatal("comparable value round trip", err)
	}
	bad := h.Input
	bad.Workflow = "release.yml"
	if c.ValidateHandoff(bad) == nil {
		t.Fatal("workflow accepted for tag-only")
	}
	bad = h.Input
	bad.Kind, bad.TagOnly = "release", c.TagOnlyAuthority{}
	if c.ValidateHandoff(bad) == nil {
		t.Fatal("release relaxed without workflow")
	}
	job := NativeTagOnlyCandidates(cfg, cat, nil, time.Now())[0].Job
	for _, change := range []string{"kind", "role", "executor", "principal", "handoff"} {
		j, mb := job, *job.Master
		j.Master = &mb
		switch change {
		case "kind":
			j.Kind = "deliver"
		case "role":
			mb.Role = "implementer"
		case "executor":
			mb.ExecutorID++
		case "principal":
			mb.AgentID = "hawk"
		case "handoff":
			mb.DeliveryHandoffID = ""
		}
		if nativeTagOnlyJob(cfg, j) {
			t.Fatal("wrong assigned stage accepted", change)
		}
	}
	if _, err = runNativeTagOnlyActionLocked(t.Context(), cfg, nil, nil, "", NativeAction{Operation: "tag-only", RequestID: "request", Body: "caller authority"}); err == nil {
		t.Fatal("caller authority accepted")
	}
	for _, member := range cfg.Team.Members {
		if member.Name == "rook" {
			continue
		}
		other := cfg
		master := *cfg.Master
		other.Master = &master
		other.Name, other.Self, master.AgentID = member.Name, member.Actor, member.Name
		if _, err = runNativeTagOnlyActionLocked(t.Context(), other, nil, nil, "", NativeAction{Operation: "tag-only", RequestID: "request"}); err == nil {
			t.Fatal("non-Rook action accepted", member.Name)
		}
	}
}

type tagOnlyAPI struct {
	t           *testing.T
	cfg         Config
	intent      tagOnlyIntent
	object, ref bool
	lost, drift string
	writes      []string
}

func (a *tagOnlyAPI) api(_ context.Context, _, method, endpoint string, _ any) ([]byte, http.Header, error) {
	missing := &GitHubError{StatusCode: 404}
	if method == "POST" {
		j, err := readTagOnlyIntent(a.cfg, a.intent.Binding.Handoff.ID)
		if err != nil {
			a.t.Fatal("effect preceded durable intent", err)
		}
		a.writes = append(a.writes, endpoint)
		if strings.HasSuffix(endpoint, "/git/tags") {
			if j.Phase != "object-intent" {
				a.t.Fatal(j.Phase)
			}
			a.object = true
			if a.lost == "object" {
				return nil, nil, errors.New("lost object response")
			}
			return []byte(`{"sha":"` + a.intent.Object + `"}`), nil, nil
		}
		if strings.HasSuffix(endpoint, "/git/refs") {
			if j.Phase != "ref-intent" {
				a.t.Fatal(j.Phase)
			}
			a.ref = true
			if a.lost == "ref" {
				return nil, nil, errors.New("lost ref response")
			}
			if a.lost == "rejected" {
				return nil, nil, &GitHubError{StatusCode: 422}
			}
			return []byte(`{}`), nil, nil
		}
		a.t.Fatal("unexpected mutation", endpoint)
	}
	if strings.Contains(endpoint, "/git/ref/tags/") {
		if !a.ref {
			return nil, nil, missing
		}
		sha := a.intent.Object
		if a.drift == "ref" {
			sha = strings.Repeat("f", 40)
		}
		raw, _ := json.Marshal(map[string]any{"ref": a.intent.Ref, "object": map[string]string{"sha": sha, "type": "tag"}})
		return raw, nil, nil
	}
	if strings.Contains(endpoint, "/git/tags/") {
		if !a.object {
			return nil, nil, missing
		}
		message := a.intent.Message
		if a.drift == "object" {
			message = "foreign"
		}
		raw, _ := json.Marshal(map[string]any{"sha": a.intent.Object, "tag": a.intent.Binding.Handoff.Input.Tag, "message": message, "object": map[string]string{"sha": a.intent.Binding.Target.SHA, "type": "commit"}, "tagger": map[string]string{"name": a.intent.Name, "email": a.intent.Email, "date": a.intent.Date}})
		return raw, nil, nil
	}
	if strings.Contains(endpoint, "/git/commits/") {
		tree := a.intent.Binding.Handoff.Input.TagOnly.IntegratedTreeSHA
		if a.intent.Binding.Handoff.Input.TagOnly.VersionPRURL != "" {
			tree = a.intent.Binding.Handoff.Input.TagOnly.VersionTreeSHA
		}
		if a.drift == "tree" {
			tree = strings.Repeat("f", 40)
		}
		raw, _ := json.Marshal(map[string]any{"sha": a.intent.Binding.Target.SHA, "tree": map[string]string{"sha": tree}})
		return raw, nil, nil
	}
	a.t.Fatal("unexpected endpoint", endpoint)
	return nil, nil, c.ErrForbidden
}

func TestTagOnlyEffectsAndReadOnlyRecovery(t *testing.T) {
	for _, scenario := range []string{"success", "lost-object", "lost-ref", "rejected-ref", "foreign-ref", "foreign-object", "revoked-before-object", "revoked-before-ref", "ref-drift", "object-drift"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, cat, h := tagOnlyFixture(t)
			target := releaseTarget{Repository: cat.Tasks[0].Task.Repo, SHA: h.Input.TagOnly.IntegratedCommitSHA}
			r := nativeActionRecord{Version: 3, JobID: "job", RequestID: "request", InputSHA: BodyHash("input"), Operation: "tag-only"}
			api := &tagOnlyAPI{t: t, cfg: cfg, intent: newTagOnlyIntent(cfg, h, target, r)}
			if scenario == "lost-object" {
				api.lost = "object"
			}
			if scenario == "lost-ref" {
				api.lost = "ref"
			}
			if scenario == "rejected-ref" {
				api.lost = "rejected"
			}
			if scenario == "foreign-ref" {
				api.ref = true
			}
			if scenario == "foreign-object" {
				api.object = true
			}
			guards := 0
			guard := func() error {
				guards++
				if scenario == "revoked-before-object" || scenario == "revoked-before-ref" && guards == 2 {
					return c.ErrForbidden
				}
				return nil
			}
			result, err := publishNativeTagOnly(t.Context(), cfg, api, h, target, r, guard)
			want := 2
			if scenario == "lost-object" || scenario == "revoked-before-ref" {
				want = 1
			}
			if strings.HasPrefix(scenario, "foreign") || scenario == "revoked-before-object" {
				want = 0
			}
			if len(api.writes) != want {
				t.Fatalf("writes=%v want=%d", api.writes, want)
			}
			if scenario == "success" || strings.HasSuffix(scenario, "drift") {
				if err != nil || result.Phase != "completed" {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("expected refusal or uncertainty")
			}
			if want == 0 {
				return
			}
			if scenario == "ref-drift" {
				api.drift = "ref"
			}
			if scenario == "object-drift" {
				api.drift = "object"
			}
			j, err := readTagOnlyIntent(cfg, h.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := tagOnlyDiskSnapshot(t, cfg)
			result, err = observeTagOnlyIntent(t.Context(), api, j)
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if len(api.writes) != want {
				t.Fatal("replayed mutation", api.writes)
			}
			if scenario == "rejected-ref" {
				if err == nil {
					t.Fatal("foreign equal object ref adopted after conclusive refusal")
				}
				return
			}
			if strings.HasSuffix(scenario, "drift") {
				if err == nil {
					t.Fatal("foreign evidence accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "lost-object" || scenario == "revoked-before-ref" {
				if result.Phase != "checkpoint" {
					t.Fatal(result)
				}
				raw, e := readPrivate(nativeActionPath(cfg, "job", "request"), c.MaxJSON)
				var receipt nativeActionRecord
				if e != nil || json.Unmarshal(raw, &receipt) != nil || receipt.State != "uncertain" {
					t.Fatal("premature completed receipt")
				}
				return
			}
			if result.Phase != "observed" {
				t.Fatal(result)
			}
			result, err = publishNativeTagOnly(t.Context(), cfg, api, h, target, r, guard)
			if err != nil || result.Phase != "completed" || len(api.writes) != want {
				t.Fatal("authorized confirmation", result, err, api.writes)
			}
			entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("one receipt: %v %v", entries, err)
			}
			other := r
			other.RequestID = "second"
			if _, err = publishNativeTagOnly(t.Context(), cfg, api, h, target, other, guard); err == nil {
				t.Fatal("second receipt identity admitted")
			}
		})
	}
}
