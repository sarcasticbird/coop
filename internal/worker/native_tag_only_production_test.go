package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type tagOnlyProductionMaster struct {
	*dispatchMaster
	validations int
	deny        bool
}

func (m *tagOnlyProductionMaster) ValidateTagOnlyHandoff(_ context.Context, in c.TagOnlyValidation) (c.Handoff, error) {
	m.validations++
	if m.deny {
		return c.Handoff{}, c.ErrForbidden
	}
	for _, h := range m.handoffs {
		if h.ID == in.HandoffID && h.Input.Goal == in.Goal && !h.Input.Revoked && m.authority.State == "approved" {
			return h, nil
		}
	}
	return c.Handoff{}, c.ErrForbidden
}

// The original retained merge and completion run through the native action
// helper. Only GitHub/master transports are mocked; source metadata is retained.
func tagOnlyProductionFixture(t *testing.T, withVersion bool) (Config, *tagOnlyProductionMaster, *GitHubClient, *nativeDeliveryTransport, NativeExecution, *tagOnlyAPI) {
	t.Helper()
	return tagOnlyProductionEnvironmentFixture(t, withVersion, false)
}

func tagOnlyProductionEnvironmentFixture(t *testing.T, withVersion, renewedEnvironment bool) (Config, *tagOnlyProductionMaster, *GitHubClient, *nativeDeliveryTransport, NativeExecution, *tagOnlyAPI) {
	t.Helper()
	cfg, master, g, _, transport, binding := retainedProductionFixture(t)
	sourceBundle, versionBundle := tagOnlyVersionBundles(t, "v1.2.3")
	if withVersion {
		// Reassess the original immutable source bundle before its retained gates.
		// Nothing below rebinds the source task to the newer roster policy.
		sha, _ := sourceBundleSHA(sourceBundle)
		transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: binding.Goal, TaskURL: binding.TaskURL, AssignmentSHA: NativeAssignmentSHA(binding), Artifact: &QAArtifact{Kind: "source", HeadSHA: transport.head, SHA256: sha, URL: "https://api.github.com/repos/code/repo/tarball/" + transport.head, Revision: "source-bundle-v1"}})
		previous := transport.commands.respond
		transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
			if strings.Contains(endpoint, "/tarball/") {
				return response("200 OK", string(sourceBundle)), nil
			}
			return previous(owner, endpoint, method, body)
		}
		p, err := g.ReadNativePR(t.Context(), cfg, NativeTask{Task: Task{URL: binding.TaskURL, Repo: "code/repo"}, Binding: binding}, master.authority, "https://github.com/code/repo/pull/1")
		if err != nil {
			t.Fatal(err)
		}
		var peer NativeReviewResult
		var qa NativeQAResult
		if nativeMetadata(transport.reviews[0]["body"].(string), "review", &peer) != nil || nativeMetadata(transport.reviews[1]["body"].(string), "qa", &qa) != nil {
			t.Fatal("invalid historical reviews")
		}
		peer.PRScopeRevision, qa.PRScopeRevision, qa.ArtifactSHA = p.ScopeRevision, p.ScopeRevision, sha
		transport.reviews[0]["body"], transport.reviews[1]["body"] = NativePeerMetadata(peer), NativeQAMetadata(qa)
		h := &master.handoffs[0]
		h.Input.PRScopeRevision, h.Input.ArtifactSHA = p.ScopeRevision, sha
		h.Input.Retained.PayloadSHA, h.Input.Retained.SourcePeerBodySHA, h.Input.Retained.SourceQABodySHA = sha, BodyHash(NativePeerMetadata(peer)), BodyHash(NativeQAMetadata(qa))
	}
	if renewedEnvironment {
		tagOnlyRenewDeliveryEnvironment(t, cfg, master, g, transport, binding)
	}
	source := retainedReports(t, &cfg, master, g, transport, binding)
	x := retainedExecution(t, cfg, master, g, binding, "deliver")
	if _, err := RunNativeAction(t.Context(), cfg, master, g, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "source-complete"}); err != nil {
		t.Fatal("original retained delivery", err)
	}
	x.Phase = "completed"
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	h := source
	h.ID, h.Sequence, h.Input.Kind, h.Input.Tag, h.Input.RequestID = "tag-production", 3, "tag-only", "v1.2.3", "tag-production"
	h.CreatedAt = h.CreatedAt.Truncate(time.Second)
	r := source.Input.Retained
	h.Input.Retained = c.RetainedDeliveryAuthority{}
	h.Input.TagOnly = c.TagOnlyAuthority{ExecutionPolicySHA: r.ExecutionPolicySHA, SourcePolicySHA: r.SourcePolicySHA, SourceCommitSHA: h.Input.HeadSHA, SourceTreeSHA: r.SourceTreeSHA, IntegratedCommitSHA: strings.Repeat("9", 40), IntegratedTreeSHA: r.SourceTreeSHA, OwnerBodySHA: BodyHash(master.reports[h.Input.EvidenceMessageID].Body), PeerBodySHA: r.SourcePeerBodySHA, QABodySHA: r.SourceQABodySHA, SourceDeliveryHandoffID: source.ID, SourceDeliveryInputSHA: tagOnlyInputSHA(source.Input), SourceCompletionRequestID: "source-complete", TagAcceptanceSHA: BodyHash("tag acceptance"), TagEnvironment: "staging", TagEnvironmentRevision: "staging-v1"}
	if renewedEnvironment {
		h.Input.TagOnly.TagEnvironment, h.Input.TagOnly.TagEnvironmentRevision = "tag-device", "tag-device-v3"
	}
	cat := NativeCatalog{Goals: []c.GoalAuthority{master.authority}, Reports: master.reports, Handoffs: []c.Handoff{h}}
	renewTagOnlyReports(t, &cat, &h)
	master.handoffs = append(master.handoffs, h)
	api := &tagOnlyAPI{t: t, cfg: cfg}
	var versionTransport *nativeDeliveryTransport
	if withVersion {
		versionBinding := binding
		versionBinding.TaskURL = "https://github.com/code/repo/issues/10"
		versionBinding.PolicySHA = h.Input.TagOnly.ExecutionPolicySHA
		versionBinding.TaskScopeSHA = TaskScopeHash("Version-only update", "Set exact version")
		versionHead := strings.Repeat("c", 40)
		versionSHA, _ := sourceBundleSHA(versionBundle)
		versionTransport = &nativeDeliveryTransport{t: t, cfg: cfg, taskTitle: "Version-only update", taskBody: "Set exact version", metadata: NativeTaskMetadata(versionBinding), head: versionHead, baseSHA: h.Input.TagOnly.IntegratedCommitSHA, closed: true, merged: true}
		versionTransport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: binding.Goal, TaskURL: versionBinding.TaskURL, AssignmentSHA: NativeAssignmentSHA(versionBinding), Artifact: &QAArtifact{Kind: "source", HeadSHA: versionHead, SHA256: versionSHA, URL: "https://api.github.com/repos/code/repo/tarball/" + versionHead, Revision: "source-bundle-v1"}})
		master.links = append(master.links, c.ArtifactLink{Kind: "task", Goal: binding.Goal, URL: versionBinding.TaskURL, AssignmentSHA: NativeAssignmentSHA(versionBinding)}, c.ArtifactLink{Kind: "pull_request", Goal: binding.Goal, TaskURL: versionBinding.TaskURL, URL: "https://github.com/code/repo/pull/10", AssignmentSHA: NativeAssignmentSHA(versionBinding)})
		r := &h.Input.TagOnly
		r.VersionGoal, r.VersionTaskURL, r.VersionPRURL = binding.Goal, versionBinding.TaskURL, "https://github.com/code/repo/pull/10"
		r.VersionHeadSHA, r.VersionCommitSHA, r.VersionTreeSHA, r.VersionBaseSHA = versionHead, strings.Repeat("d", 40), strings.Repeat("e", 40), versionTransport.baseSHA
		r.PayloadSchema, r.SourceBundleSHA, r.VersionBundleSHA = c.TagOnlyPayloadV1, source.Input.Retained.PayloadSHA, versionSHA
		r.PayloadSHA, _ = tagOnlyPayloadSHA(sourceBundle, r.PayloadSchema, h.Input.Tag, false)
	}
	transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if strings.Contains(endpoint, "/git/tags") || strings.Contains(endpoint, "/git/ref") {
			raw, _, err := api.api(t.Context(), owner, method, endpoint, nil)
			if githubMissing(err) {
				return response("404 Not Found", `{}`), nil
			}
			if err != nil {
				return CommandResult{}, err
			}
			return response("200 OK", string(raw)), nil
		}
		if strings.Contains(endpoint, "/git/commits/") {
			sha := strings.TrimPrefix(endpoint, "repos/code/repo/git/commits/")
			tree, parent := r.SourceTreeSHA, h.Input.BaseSHA
			if withVersion && (sha == h.Input.TagOnly.VersionHeadSHA || sha == h.Input.TagOnly.VersionCommitSHA) {
				tree, parent = h.Input.TagOnly.VersionTreeSHA, h.Input.TagOnly.VersionBaseSHA
			}
			return jsonResponse(map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}, "parents": []map[string]string{{"sha": parent}}}), nil
		}
		if strings.Contains(endpoint, "/compare/") {
			return jsonResponse(map[string]any{"status": "ahead", "behind_by": 0}), nil
		}
		if withVersion && strings.Contains(endpoint, "/tarball/") {
			bundle := sourceBundle
			if strings.HasSuffix(endpoint, h.Input.TagOnly.VersionHeadSHA) || strings.HasSuffix(endpoint, h.Input.TagOnly.VersionCommitSHA) {
				bundle = versionBundle
			}
			return response("200 OK", string(bundle)), nil
		}
		var request struct {
			Variables struct{ Number int }
		}
		_ = json.Unmarshal(body, &request)
		if withVersion && (strings.Contains(endpoint, "/issues/10") || strings.Contains(endpoint, "/pulls/10") || request.Variables.Number == 10) {
			if endpoint == "repos/code/repo/pulls/10" {
				return jsonResponse(map[string]any{"html_url": h.Input.TagOnly.VersionPRURL, "merged": true, "merge_commit_sha": h.Input.TagOnly.VersionCommitSHA, "body": versionTransport.prBody, "user": map[string]any{"id": 12, "type": "Bot", "login": "implementer[bot]"}, "head": map[string]any{"sha": versionTransport.head, "repo": map[string]string{"full_name": "code/repo"}}, "base": map[string]any{"sha": versionTransport.baseSHA, "ref": "main", "repo": map[string]string{"full_name": "code/repo"}}}), nil
			}
			out, err := versionTransport.response(owner, strings.ReplaceAll(endpoint, "/10", "/1"), method, body)
			out.Stdout = []byte(strings.ReplaceAll(strings.ReplaceAll(string(out.Stdout), `/issues/1"`, `/issues/10"`), `/pull/1"`, `/pull/10"`))
			if endpoint == "repos/code/repo/issues/10" {
				out.Stdout = []byte(strings.ReplaceAll(string(out.Stdout), `"number":1`, `"number":10`))
			}
			return out, err
		}
		out, err := transport.response(owner, endpoint, method, body)
		if endpoint == "repos/code/repo/pulls/1" && method == "GET" {
			// The shared merge transport omits REST's merged boolean.
			_, payload, _ := strings.Cut(string(out.Stdout), "\r\n\r\n")
			var pr map[string]any
			if err := json.Unmarshal([]byte(payload), &pr); err != nil {
				t.Fatal(err)
			}
			pr["merged"] = true
			return jsonResponse(pr), nil
		}
		return out, err
	}
	if withVersion {
		b := binding
		b.TaskURL, b.PolicySHA, b.TaskScopeSHA = h.Input.TagOnly.VersionTaskURL, h.Input.TagOnly.ExecutionPolicySHA, TaskScopeHash(versionTransport.taskTitle, versionTransport.taskBody)
		task := NativeTask{Task: Task{URL: b.TaskURL, Repo: "code/repo"}, Binding: b}
		p, err := g.ReadNativeReleasePR(t.Context(), cfg, task, master.authority, h.Input.TagOnly.VersionPRURL)
		if err != nil {
			t.Fatal("version source", err)
		}
		peer := NativeReviewResult{Version: 3, Goal: b.Goal, TaskURL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
		qa := NativeQAResult{Version: 3, Goal: b.Goal, TaskURL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(master.authority), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "version-qa-report", EvidenceBodySHA: BodyHash("version QA passed"), Verdict: "approved"}
		for i, body := range []string{NativePeerMetadata(peer), NativeQAMetadata(qa)} {
			member := cfg.Team.Members[[]int{0, 2}[i]]
			versionTransport.reviews = append(versionTransport.reviews, map[string]any{"fullDatabaseId": i + 10, "state": "APPROVED", "body": body, "submittedAt": time.Now().UTC(), "commit": map[string]string{"oid": p.HeadSHA}, "author": map[string]any{"__typename": "Bot", "databaseId": member.Actor.ID, "login": member.Actor.Login}})
		}
		master.reports[qa.EvidenceMessageID] = c.Message{ID: qa.EvidenceMessageID, AuthorID: "qa", AuthorKind: "agent", ThreadID: "private", Body: "version QA passed"}
		v := source
		v.ID, v.Sequence, v.Input.Kind, v.Input.RequestID, v.Input.Retained = "version-delivery", 4, "deliver", "version-delivery", c.RetainedDeliveryAuthority{}
		v.Input.TaskURL, v.Input.PRURL, v.Input.AssignmentSHA, v.Input.TaskScopeSHA = b.TaskURL, p.URL, NativeAssignmentSHA(b), b.TaskScopeSHA
		v.Input.BaseSHA, v.Input.HeadSHA, v.Input.PRScopeRevision, v.Input.ArtifactSHA = h.Input.TagOnly.VersionBaseSHA, p.HeadSHA, p.ScopeRevision, p.QAArtifact.SHA256
		v.Input.PeerReviewID, v.Input.QAReviewID, v.Input.QAEvidenceMessageID, v.Input.QAEvidenceBodySHA = "10", "11", qa.EvidenceMessageID, qa.EvidenceBodySHA
		v.Input.Environment, v.Input.EnvironmentRevision = qa.Environment, qa.EnvironmentRevision
		h.Input.TagOnly.VersionDeliveryHandoffID, h.Input.TagOnly.VersionDeliveryInputSHA, h.Input.TagOnly.VersionCompletionRequestID = v.ID, tagOnlyInputSHA(v.Input), "version-complete"
		renewTagOnlyReports(t, &cat, &h)
		master.handoffs[len(master.handoffs)-1] = h
		master.handoffs = append(master.handoffs, v)
	}
	m := &tagOnlyProductionMaster{dispatchMaster: master}
	cat, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil {
		t.Fatal("tag discovery", err)
	}
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "tag-only" {
		t.Fatalf("expected one tag-only candidate, got %d (renewed environment: %t)", len(jobs), renewedEnvironment)
	}
	x = NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: binding.Goal, Binding: &binding, Job: jobs[0].Job, Phase: "active"}
	x.Job.SessionID, x.Job.MessageID = "ses_tag", "msg_tag"
	x.Job.Phase, x.Job.SubmissionState = "active", "accepted"
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	in := NativeAction{Operation: "tag-only", RequestID: "tag-request"}
	raw, _ := json.Marshal(in)
	api.intent = newTagOnlyIntent(cfg, h, releaseTarget{Repository: "code/repo", SHA: c.TagOnlyTargetSHA(h.Input)}, nativeActionRecord{JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw))})
	transport.writes = nil
	return cfg, m, g, transport, x, api
}

func TestTagOnlyProductionLifecycle(t *testing.T) {
	for _, lost := range []string{"", "object", "ref"} {
		t.Run("lost-"+lost, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, false)
			metadata, prBody, assignment := transport.metadata, transport.prBody, NativeAssignmentSHA(*x.Binding)
			d := &NativeDispatcher{config: cfg, master: m, github: g, clock: RealClock{}}
			candidate, err := d.freshCandidate(t.Context(), x)
			if err != nil {
				t.Fatal("fresh candidate", err)
			}
			prompt, err := nativePrompt(cfg, x, candidate)
			if err != nil || !strings.Contains(prompt, "observe-tag-only") || !strings.Contains(prompt, "Never merge") {
				t.Fatal("tag-only prompt", prompt, err)
			}
			transport.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
			if _, err := NewCheckouts(cfg, transport).Prepare(t.Context(), x.Job); err != nil {
				t.Fatal("checkout prepare", err)
			}
			if yes, err := nativeJobHasResult(cfg, x); err != nil || yes {
				t.Fatal("source delivery became tag result", yes, err)
			}
			api.lost = lost
			result, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"})
			if lost == "" && (err != nil || result.Phase != "completed") || lost != "" && err == nil {
				t.Fatal("tag action", result, err)
			}
			wantValidations := map[string]int{"": 5, "object": 2, "ref": 3}[lost]
			if m.validations != wantValidations {
				t.Fatal("authority not checked before each effect and completion", m.validations)
			}
			writes := len(api.writes)
			m.authority.State = "paused"
			m.handoffs[len(m.handoffs)-1].Input.Revoked = true
			cfg.DeliveryHold = true
			before := tagOnlyDiskSnapshot(t, cfg)
			result, err = ObserveNativeTagOnly(t.Context(), cfg, m, g, x.Goal, x.Job.Master.DeliveryHandoffID)
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			want := "observed"
			if lost == "object" {
				want = "checkpoint"
			}
			if err != nil || result.Phase != want || len(api.writes) != writes {
				t.Fatal("read-only observation after withdrawal", result, err, api.writes)
			}
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); (err == nil) != (lost == "") || len(api.writes) != writes {
				t.Fatal("withdrawn confirmation or completed receipt read", err)
			}
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "new-request"}); !errors.Is(err, c.ErrConflict) {
				t.Fatal("new action identity adopted old intent", err)
			}
			if yes, err := nativeJobHasResult(cfg, x); err != nil || yes != (lost == "") {
				t.Fatal("tag result", yes, err)
			}
			if transport.metadata != metadata || transport.prBody != prBody || NativeAssignmentSHA(*x.Binding) != assignment || len(transport.writes) != 0 {
				t.Fatal("tag lane changed source or invoked ordinary producer", transport.writes)
			}
		})
	}
}

func tagOnlyCachedCatalog(t *testing.T, cfg Config, cat NativeCatalog) *fakeCacheClient {
	t.Helper()
	f := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("tag-manifest"), Goals: cat.Goals}}
	for _, task := range cat.Tasks {
		addCachedTask(t, f, task)
		if task.Task.Closed {
			f.page.Objects[len(f.page.Objects)-1].Header.State = "closed"
		}
		f.page.Objects[len(f.page.Objects)-1].Header.Completed = task.Task.Completed
		for _, p := range task.PRs {
			ref := c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: p.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
			raw, _ := json.Marshal(p)
			sha := c.GitHubDigest(raw)
			f.data[sha] = raw
			f.page.Objects = append(f.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Minute), Header: c.GitHubHeader{Revision: sha, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, ScopeRevision: p.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: sha, HeaderRevision: sha, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Minute)}}})
		}
	}
	return f
}

func TestTagOnlyProductionCachedHydration(t *testing.T) {
	for _, mode := range []string{"exact", "dirty", "missing-report", "revoked", "foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, _, x, _ := tagOnlyProductionFixture(t, false)
			cat, err := g.NativeSnapshot(t.Context(), cfg, m)
			if err != nil {
				t.Fatal(err)
			}
			cache := tagOnlyCachedCatalog(t, cfg, cat)
			h := &m.handoffs[len(m.handoffs)-1]
			switch mode {
			case "dirty":
				cache.page.Objects[0].Dirty = true
			case "missing-report":
				delete(m.reports, h.Input.TagOnly.QAReportID)
			case "revoked":
				h.Input.Revoked = true
			case "foreign-owner":
				msg := m.reports[h.Input.EvidenceMessageID]
				msg.Imported = true
				m.reports[msg.ID] = msg
			}
			reader := NewCachedNativeReader(cfg, g, cache, RealClock{})
			out, err := reader.NativeSnapshot(t.Context(), cfg, m)
			if err != nil {
				t.Fatal(err)
			}
			jobs := NativeCandidates(cfg, out, nil, time.Now())
			if mode != "exact" {
				if len(jobs) != 0 {
					t.Fatal("unsafe cached admission", jobs)
				}
				return
			}
			if len(jobs) != 1 || jobs[0].Job.ID != x.Job.ID || NativeAssignmentSHA(jobs[0].Task.Binding) != NativeAssignmentSHA(*x.Binding) {
				t.Fatal("cache lost historical source", jobs)
			}
			if _, ok := ChooseTeamJob([]Job{jobs[0].Job}, ScheduleCounters{}); !ok {
				t.Fatal("tag candidate not schedulable")
			}
			if _, err := nativeValidationSnapshot(t.Context(), cfg, m, reader, x.Job); err != nil {
				t.Fatal("cached validation snapshot", err)
			}
		})
	}
}

func TestTagOnlyProductionVersionJourney(t *testing.T) {
	for _, mode := range []string{"exact", "version-report-missing", "version-registration-missing", "newer-tag-withdrawal", "source-unsettled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, true)
			metadata, prBody := transport.metadata, transport.prBody
			cat, err := g.NativeSnapshot(t.Context(), cfg, m)
			if err != nil || len(cat.Tasks) != 2 {
				t.Fatal("combined discovery lost source/version", len(cat.Tasks), err)
			}
			switch mode {
			case "version-report-missing":
				delete(m.reports, "version-qa-report")
			case "version-registration-missing":
				m.links = m.links[:len(m.links)-1]
			case "newer-tag-withdrawal":
				h := m.handoffs[len(m.handoffs)-2]
				h.ID, h.Sequence, h.Input.Revoked = "tag-withdrawal", 5, true
				m.handoffs = append(m.handoffs, h)
			case "source-unsettled":
				other := x
				other.Job.ID, other.Job.Kind, other.Phase = BodyHash("unfinished-source"), "deliver", "active"
				if err := saveNativeExecution(cfg, other); err != nil {
					t.Fatal(err)
				}
			}
			reader := NewCachedNativeReader(cfg, g, tagOnlyCachedCatalog(t, cfg, cat), RealClock{})
			d := &NativeDispatcher{config: cfg, master: m, github: reader, clock: RealClock{}}
			candidate, err := d.freshCandidate(t.Context(), x)
			if mode != "exact" {
				if err == nil {
					t.Fatal("unsafe version candidate", candidate)
				}
				if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil || len(api.writes) != 0 {
					t.Fatal("unsafe version tag effect", err, api.writes)
				}
				return
			}
			if err != nil {
				t.Fatal("version closure revalidation", err)
			}
			if _, err := nativePrompt(cfg, x, candidate); err != nil {
				t.Fatal(err)
			}
			cached, err := reader.NativeSnapshot(t.Context(), cfg, m)
			if err != nil || len(NativeCandidates(cfg, cached, nil, time.Now())) != 1 {
				t.Fatal("cached version hydration", err)
			}
			api.lost = "ref"
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil {
				t.Fatal("lost ref acknowledgment was not uncertain")
			}
			if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
				t.Fatal("premature tag result", yes, err)
			}
			m.authority.State = "paused"
			before := tagOnlyDiskSnapshot(t, cfg)
			result, err := ObserveNativeTagOnly(t.Context(), cfg, m, g, x.Goal, x.Job.Master.DeliveryHandoffID)
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if err != nil || result.Phase != "observed" || len(api.writes) != 2 {
				t.Fatal("version read-only recovery", result, err, api.writes)
			}
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil {
				t.Fatal("paused version confirmation admitted")
			}
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
				t.Fatal("observation created native completion", yes, err)
			}
			m.authority.State = "approved"
			result, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"})
			if err != nil || result.Phase != "completed" || len(api.writes) != 2 {
				t.Fatal("authorized version confirmation", result, err, api.writes)
			}
			j, err := readTagOnlyIntent(cfg, x.Job.Master.DeliveryHandoffID)
			if err != nil || j.Binding.Target.SHA != j.Binding.Handoff.Input.TagOnly.VersionCommitSHA || j.Binding.Target.SHA == j.Binding.Handoff.Input.TagOnly.IntegratedCommitSHA {
				t.Fatal("tag did not target version integration", err)
			}
			if yes, err := nativeJobHasResult(cfg, x); !yes || err != nil {
				t.Fatal("missing version tag result", yes, err)
			}
			if transport.metadata != metadata || transport.prBody != prBody || len(transport.writes) != 0 {
				t.Fatal("version followup rebound or merged original source", transport.writes)
			}
		})
	}
}

func TestTagOnlyProductionPrepareRejectsForeignStage(t *testing.T) {
	for _, change := range []string{"role", "handoff", "executor"} {
		t.Run(change, func(t *testing.T) {
			cfg, _, _, transport, x, _ := tagOnlyProductionFixture(t, false)
			switch change {
			case "role":
				x.Job.Master.Role = "implementer"
			case "handoff":
				x.Job.Master.DeliveryHandoffID = ""
			case "executor":
				x.Job.Master.ExecutorID++
			}
			transport.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
			if _, err := NewCheckouts(cfg, transport).Prepare(t.Context(), x.Job); err == nil {
				t.Fatal("checkout accepted tag-only job without exact assigned stage")
			}
		})
	}
}

func TestTagOnlyProductionActionGuards(t *testing.T) {
	for _, change := range []string{"hold", "master-refusal", "missing-source", "report-drift", "caller-target", "wrong-stage", "active-source"} {
		t.Run(change, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, false)
			in := NativeAction{Operation: "tag-only", RequestID: "tag-request"}
			switch change {
			case "hold":
				cfg.DeliveryHold = true
			case "master-refusal":
				m.deny = true
			case "missing-source":
				m.handoffs = m.handoffs[len(m.handoffs)-1:]
			case "report-drift":
				id := m.handoffs[len(m.handoffs)-1].Input.TagOnly.QAReportID
				msg := m.reports[id]
				msg.Body += "drift"
				m.reports[id] = msg
			case "caller-target":
				in.Body = "tag a different target"
			case "wrong-stage":
				x.Job.Master.Role = "implementer"
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
			case "active-source":
				other := x
				other.Job.ID, other.Job.Kind, other.Phase = BodyHash("active-original-delivery"), "deliver", "active"
				if err := saveNativeExecution(cfg, other); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil {
				t.Fatal("unsafe tag-only action admitted")
			}
			if len(api.writes) != 0 || len(transport.writes) != 0 {
				t.Fatal("guard refusal mutated GitHub", api.writes, transport.writes)
			}
			if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
				t.Fatal("refused action recorded a tag result", yes, err)
			}
		})
	}
}
