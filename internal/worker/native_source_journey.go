package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type sourceJourneyMaster interface {
	SourceDecisions(context.Context, c.SourceDecisionQuery) (c.SourceDecisionPage, error)
	ValidateSourceDecision(context.Context, c.SourceDecisionValidation) (c.SourceDecision, error)
	SourceEffects(context.Context, c.SourceDecisionValidation) ([]c.SourceEffectReceipt, error)
	RecordSourceEffect(context.Context, c.SourceEffectReceipt) (c.SourceEffectReceipt, error)
	CompleteSourceTask(context.Context, c.SourceCompletionInput) (c.ProviderTask, error)
	TaskRead(context.Context, string) (c.ProviderTask, error)
	SourceTaskState(context.Context, c.SourceDecisionValidation) (c.SourceTaskState, error)
}

func nativeSourceKind(kind string) bool {
	return kind == "source-review" || kind == "source-qa" || kind == "source-retarget" || kind == "source-deliver"
}

func nativeSourceJob(cfg Config, j Job) bool {
	if !nativeSourceKind(j.Kind) || j.Master == nil || cfg.Master == nil || cfg.Team == nil || j.Master.SourceDecisionID == "" || j.Master.AgentID != cfg.Master.AgentID || j.Master.ExecutorID != cfg.Self.ID || cfg.Name != cfg.Master.AgentID || j.Master.MasterID != cfg.Master.MasterID || cfg.Self.Kind != "Bot" {
		return false
	}
	m, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
	if !ok || m.Name != cfg.Name {
		return false
	}
	capability, role := "engineering-review", "engineering-review"
	if j.Kind == "source-qa" {
		capability, role = "customer-qa", "customer-qa"
	}
	if j.Kind == "source-retarget" || j.Kind == "source-deliver" {
		capability, role = "deliver", "delivery"
		if cfg.Name != c.DeliveryPrincipalID || cfg.Self.ID != c.DeliveryGitHubBotID || cfg.DeliveryHold {
			return false
		}
	}
	return memberCapability(m, capability) && j.Master.Role == role && j.InputRevision == "source:"+j.Master.SourceDecisionID
}

func loadNativeSourceDecisions(ctx context.Context, cfg Config, m NativeMaster, cat *NativeCatalog) error {
	sm, ok := m.(sourceJourneyMaster)
	if !ok {
		return nil
	}
	cat.SourceTasks = map[string]bool{}
	if cat.RegisteredGoals == nil {
		cat.RegisteredGoals = map[c.GoalRef]bool{}
	}
	for _, a := range cat.Goals {
		for cursor := int64(0); ; {
			page, err := sm.SourceDecisions(ctx, c.SourceDecisionQuery{Goal: a.Revision.Ref, After: cursor})
			if err != nil {
				return err
			}
			for _, d := range page.Decisions {
				if c.ValidateSourceDecision(d.Input) != nil || d.Sequence < 1 {
					return c.ErrConflict
				}
				cat.SourceDecisions = append(cat.SourceDecisions, d)
				cat.RegisteredGoals[d.Input.Goal] = true
				if d.Input.GateTaskURL != "" {
					cat.SourceTasks[d.Input.GateTaskURL] = true
				}
			}
			if len(cat.SourceDecisions) > MaxConnection {
				return c.ErrCapacity
			}
			if page.Next == 0 {
				break
			}
			if page.Next <= cursor {
				return c.ErrConflict
			}
			cursor = page.Next
		}
	}
	return nil
}

// Source candidates are never projected from cached source assertions. Every
// decision, contributor, payload and target is read through the live boundary.
func (g *GitHubClient) hydrateNativeSources(ctx context.Context, cfg Config, m NativeMaster, cat *NativeCatalog) error {
	sm, ok := m.(sourceJourneyMaster)
	if !ok {
		return nil
	}
	for _, d := range cat.SourceDecisions {
		if d.Input.Kind == "revoke" {
			continue
		}
		latest := true
		for _, other := range cat.SourceDecisions {
			if other.Input.Kind == "revoke" && other.Input.RevokeID == d.ID {
				latest = false
			}
			gates := d.Input.Kind == "intake" || d.Input.Kind == "transition"
			if other.Input.Source.PRURL == d.Input.Source.PRURL && other.Sequence > d.Sequence && (other.Input.Kind == d.Input.Kind || gates && (other.Input.Kind == "intake" || other.Input.Kind == "transition")) {
				latest = false
			}
		}
		if !latest {
			continue
		}
		current, err := sm.ValidateSourceDecision(ctx, c.SourceDecisionValidation{Goal: d.Input.Goal, ID: d.ID})
		if errors.Is(err, c.ErrForbidden) || errors.Is(err, c.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if c.SourceDigest(current) != c.SourceDigest(d) {
			return c.ErrConflict
		}
		effects, err := sm.SourceEffects(ctx, c.SourceDecisionValidation{Goal: d.Input.Goal, ID: d.ID})
		if err != nil {
			return err
		}
		if cat.SourceEffects == nil {
			cat.SourceEffects = map[string][]c.SourceEffectReceipt{}
		}
		cat.SourceEffects[d.ID] = effects
		for _, e := range effects {
			if e.ReportID != "" {
				msg, err := m.Message(ctx, e.ReportID)
				if err != nil {
					return err
				}
				cat.Reports[msg.ID] = msg
			}
		}
		for _, id := range []string{d.Input.EngineeringReportID, d.Input.QAReportID} {
			if id != "" {
				msg, err := m.Message(ctx, id)
				if err != nil {
					return err
				}
				cat.Reports[msg.ID] = msg
			}
		}
		finished := false
		for _, e := range effects {
			if cfg.Name == d.Input.ReviewerID && e.Kind == "source-review" || cfg.Name == d.Input.QAID && e.Kind == "source-qa" {
				finished = true
			}
		}
		if finished {
			continue
		}
		p, err := g.readSourceJourneyPR(ctx, cfg, d, sm)
		if err != nil {
			if cat.SourceRefusals == nil {
				cat.SourceRefusals = map[string]string{}
			}
			cat.SourceRefusals[d.ID] = err.Error()
			continue
		}
		t, err := sm.TaskRead(ctx, d.Input.GateTaskURL)
		if err != nil {
			return err
		}
		if t.Binding == nil || NativeAssignmentSHA(*t.Binding) != d.Input.GateAssignmentSHA {
			return c.ErrConflict
		}
		nt := NativeTask{Binding: *t.Binding, Task: Task{URL: t.Reference, Repo: t.Repository, Title: t.Title, Body: t.Body, BodySHA: t.Binding.TaskScopeSHA, Ready: true, Closed: t.State == "closed", Completed: t.State == "closed"}}
		if d.Input.Source.OriginalTaskURL != "" {
			original, e := sm.SourceTaskState(ctx, c.SourceDecisionValidation{Goal: d.Input.Goal, ID: d.ID})
			if e != nil {
				return e
			}
			nt.Task.Completed = original.State == "closed"
		}
		if cat.SourceEffects == nil {
			cat.SourceEffects = map[string][]c.SourceEffectReceipt{}
		}
		cat.SourceEffects[d.ID] = effects
		for _, e := range effects {
			if e.ReportID != "" {
				msg, err := m.Message(ctx, e.ReportID)
				if err != nil {
					return err
				}
				cat.Reports[msg.ID] = msg
			}
		}
		cat.SourceEntries = append(cat.SourceEntries, nativeSourceEntry{Decision: d, Task: nt, PR: p})
	}
	return nil
}

type nativeSourceEntry struct {
	Decision c.SourceDecision
	Task     NativeTask
	PR       PR
}
type sourceRetargetAcknowledgedKey struct{}

func nativeSourceCandidates(cfg Config, cat NativeCatalog, history map[string]NativeExecution, now time.Time) []NativeCandidate {
	var out []NativeCandidate
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil {
		return nil
	}
	for _, entry := range cat.SourceEntries {
		d, t, p := entry.Decision, entry.Task, entry.PR
		in := d.Input
		if policy != in.PolicySHA || !p.ContributorsComplete || !p.ReviewsComplete || slices.Contains(in.Source.Contributors, cfg.Self.ID) {
			continue
		}
		var a c.GoalAuthority
		for _, goal := range cat.Goals {
			if goal.Revision.Ref == in.Goal {
				a = goal
			}
		}
		if !a.Valid || a.State != "approved" || a.ApprovalSequence != in.ApprovalSequence || NativeAcceptanceSHA(a) != in.AcceptanceSHA {
			continue
		}
		if cfg.Name == in.ReviewerID && cfg.Self.ID != a.Actors.Peer.GitHubBotID || cfg.Name == in.QAID && cfg.Self.ID != a.Actors.QA.GitHubBotID {
			continue
		}
		kind, role := "", ""
		reviewed, qa := false, false
		for _, e := range cat.SourceEffects[d.ID] {
			m, ok := cat.Reports[e.ReportID]
			var report c.SourceGateReport
			if !ok || BodyHash(m.Body) != e.BodySHA || m.Imported || m.AuthorKind != "agent" || c.DecodeJSON([]byte(m.Body), &report) != nil || report.DecisionID != d.ID || report.ScopeSHA != c.SourceDigest(in) || report.Verdict != "approved" {
				continue
			}
			if e.Kind == "source-review" && m.AuthorID == in.ReviewerID {
				reviewed = true
			}
			if e.Kind == "source-qa" && m.AuthorID == in.QAID {
				qa = true
			}
		}
		switch in.Kind {
		case "intake", "transition":
			if cfg.Name == in.ReviewerID && !reviewed {
				kind, role = "source-review", "engineering-review"
			}
			if cfg.Name == in.QAID && reviewed && !qa {
				kind, role = "source-qa", "customer-qa"
			}
		case "retarget":
			if cfg.Name == in.ActorID && len(cat.SourceEffects[d.ID]) == 0 && p.BaseBranch == in.Source.BaseBranch && sourceExecutionGates(cat, d, p) {
				kind, role = "source-retarget", "delivery"
			}
		case "deliver":
			if cfg.Name == in.ActorID && p.BaseBranch == "main" && sourceExecutionGates(cat, d, p) && (!t.Task.Completed || len(cat.SourceEffects[d.ID]) == 0) {
				kind, role = "source-deliver", "delivery"
			}
		}
		if kind == "" || p.Closed && !p.Merged || kind != "source-deliver" && p.Merged {
			continue
		}
		ref := in.Goal
		j := Job{Kind: kind, TaskURL: in.GateTaskURL, PRURL: in.Source.PRURL, HeadSHA: in.Source.HeadSHA, ScopeSHA: c.SourceDigest(in), InputRevision: "source:" + d.ID, QAArtifact: p.QAArtifact, FirstEligibleAt: now, Master: &MasterJobBinding{SourceDecisionID: d.ID, SourceTargetSHA: in.Target.SHA, SourceResultTreeSHA: in.Target.ResultTreeSHA, SourceResultBundleSHA: in.Target.ResultBundleSHA, AssignmentSHA: in.GateAssignmentSHA, ExecutorID: cfg.Self.ID, Role: role, BaseBranch: in.Source.BaseBranch, MasterID: cfg.Master.MasterID, ThreadID: a.Revision.Proposal.ThreadID, AgentID: cfg.Name, Goal: &ref}}
		if !nativeSourceJob(cfg, j) {
			continue
		}
		j.ID = c.SourceDigest(struct {
			Master      *MasterJobBinding
			Kind, Scope string
		}{j.Master, j.Kind, j.ScopeSHA})
		if _, seen := history[j.ID]; seen {
			continue
		}
		out = append(out, NativeCandidate{Job: j, Goal: a, Task: &t, PR: &p, SourceDecision: &d})
	}
	return out
}

func sourceExecutionGates(cat NativeCatalog, d c.SourceDecision, p PR) bool {
	var parent c.SourceDecision
	for _, v := range cat.SourceDecisions {
		if v.ID == d.Input.ParentID {
			parent = v
		}
	}
	if parent.ID == "" {
		return false
	}
	for _, gate := range []struct {
		id, sha, author, stage string
		bot                    int64
	}{{d.Input.EngineeringReportID, d.Input.EngineeringBodySHA, d.Input.ReviewerID, "source-review", 0}, {d.Input.QAReportID, d.Input.QABodySHA, d.Input.QAID, "source-qa", 0}} {
		msg, ok := cat.Reports[gate.id]
		var report c.SourceGateReport
		if !ok || msg.AuthorID != gate.author || msg.AuthorKind != "agent" || msg.Imported || BodyHash(msg.Body) != gate.sha || c.DecodeJSON([]byte(msg.Body), &report) != nil || report.DecisionID != parent.ID || report.ScopeSHA != c.SourceDigest(parent.Input) || report.Stage != gate.stage || report.Verdict != "approved" {
			return false
		}
		for _, a := range cat.Goals {
			if a.Revision.Ref == d.Input.Goal {
				if gate.stage == "source-review" {
					gate.bot = a.Actors.Peer.GitHubBotID
				} else {
					gate.bot = a.Actors.QA.GitHubBotID
				}
			}
		}
		r := nativeLatestReview(p, gate.bot)
		if r.ID != report.ReviewID || r.State != "APPROVED" || r.HeadSHA != d.Input.Source.HeadSHA || r.Author.Kind != "Bot" || !r.SubmittedAt.After(parent.CreatedAt) || !strings.Contains(r.Body, "decision "+parent.ID+" scope "+c.SourceDigest(parent.Input)) {
			return false
		}
	}
	return true
}

func (g *GitHubClient) readSourceJourneyPR(ctx context.Context, cfg Config, d c.SourceDecision, sm sourceJourneyMaster) (PR, error) {
	in, s := d.Input, d.Input.Source
	repo, n, err := parseRef(s.PRURL)
	if err != nil || repo != s.Repository || !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
		return PR{}, c.ErrForbidden
	}
	reader := NewGitHubClient(g.runner)
	reader.config = cfg
	reader.observation = g.observation
	owner := strings.Split(repo, "/")[0]
	if err = reader.verifyOwner(ctx, owner); err != nil {
		return PR{}, err
	}
	raw, _, err := reader.api(ctx, owner, "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
	if err != nil {
		return PR{}, err
	}
	var remote struct {
		ID             int64     `json:"id"`
		NodeID         string    `json:"node_id"`
		URL            string    `json:"html_url"`
		Body           string    `json:"body"`
		State          string    `json:"state"`
		Merged         bool      `json:"merged"`
		Mergeable      *bool     `json:"mergeable"`
		MergeableState string    `json:"mergeable_state"`
		User           restActor `json:"user"`
		Head           struct {
			SHA, Ref string
			Repo     struct {
				ID       int64
				FullName string `json:"full_name"`
				Owner    restActor
			}
		}
		Base struct {
			SHA, Ref string
			Repo     struct {
				ID       int64
				FullName string `json:"full_name"`
			}
		}
	}
	if json.Unmarshal(raw, &remote) != nil || remote.URL != s.PRURL || remote.NodeID != s.PRID || remote.User.ID != s.AuthorID || remote.User.Kind != s.AuthorKind || remote.Head.SHA != s.HeadSHA || remote.Head.Ref != s.Branch || remote.Head.Repo.ID != s.SourceRepoID || remote.Head.Repo.FullName != s.SourceRepo || remote.Head.Repo.Owner.ID != s.OwnerID || remote.Base.Repo.ID != s.RepositoryID || remote.Base.Repo.FullName != s.Repository {
		return PR{}, ghError("source_identity_changed")
	}
	p := PR{URL: s.PRURL, HeadSHA: s.HeadSHA, Branch: s.Branch, BaseBranch: remote.Base.Ref, Author: remote.User.actor(), Merged: remote.Merged, Closed: remote.State == "closed", ScopeRevision: c.SourceDigest(in), ChecksState: "unknown", MergeState: strings.ToUpper(remote.MergeableState)}
	if s.AuthorKind == "Bot" {
		if _, err := ParseNativePRMetadata(remote.Body, repo, s.OriginalTaskURL, *s.OriginalGoal, s.OriginalAssignmentSHA); err != nil {
			return p, err
		}
	}
	boundBody := false
	for _, e := range s.Evidence {
		if e.Kind == "pr-body" && e.ID == s.PRURL+"#body" && e.BodySHA == BodyHash(remote.Body) {
			boundBody = true
		}
	}
	if !boundBody {
		return p, ghError("source_original_body_evidence_missing_or_changed")
	}
	base, err := reader.nativeBaseSHA(ctx, repo, in.Target.Branch)
	if err != nil {
		return p, err
	}
	if !p.Merged && base != in.Target.SHA {
		return p, ghError("source_target_changed")
	}
	p.BaseSHA = base
	if remote.Base.Ref != s.BaseBranch && remote.Base.Ref != "main" {
		return p, ghError("source_base_changed")
	}
	if remote.Base.Ref == "main" && s.BaseBranch != "main" {
		ack := false
		page, err := sm.SourceDecisions(ctx, c.SourceDecisionQuery{Goal: in.Goal})
		if err != nil {
			return p, err
		}
		for _, prior := range page.Decisions {
			if prior.Input.Kind != "retarget" || prior.Input.ParentID != in.ParentID || c.SourceDigest(prior.Input.Source) != c.SourceDigest(s) || c.SourceDigest(prior.Input.Target) != c.SourceDigest(in.Target) {
				continue
			}
			effects, e := sm.SourceEffects(ctx, c.SourceDecisionValidation{Goal: in.Goal, ID: prior.ID})
			if e != nil {
				return p, e
			}
			for _, effect := range effects {
				if effect.Kind == "retarget" {
					ack = true
				}
			}
		}
		if !ack && ctx.Value(sourceRetargetAcknowledgedKey{}) != d.ID {
			return p, ghError("source_foreign_or_unacknowledged_retarget")
		}
	}
	if remote.Base.Ref == s.BaseBranch && s.BaseBranch != "main" && remote.Base.SHA != s.BaseSHA {
		return p, ghError("source_original_base_changed")
	}
	if err = reader.readTeamEvidence(ctx, repo, n, &p); err != nil {
		return p, err
	}
	for _, a := range p.Contributors {
		if !slices.Contains(s.Contributors, a.ID) {
			return p, ghError("source_contributors_changed")
		}
	}
	if remote.Base.Ref == s.BaseBranch && len(p.Contributors) != len(s.Contributors) {
		observed := map[int64]bool{}
		for _, a := range p.Contributors {
			observed[a.ID] = true
		}
		if r := in.Target.Predecessor; r != nil {
			found := false
			for cursor := int64(0); ; {
				page, e := sm.SourceDecisions(ctx, c.SourceDecisionQuery{Goal: r.Goal, After: cursor})
				if e != nil {
					return p, e
				}
				for _, prior := range page.Decisions {
					if prior.ID == r.DecisionID && c.SourceDigest(prior.Input.Source) == r.SourceSHA {
						found = true
						for _, id := range prior.Input.Source.Contributors {
							observed[id] = true
						}
					}
				}
				if page.Next == 0 {
					break
				}
				if page.Next <= cursor {
					return p, c.ErrConflict
				}
				cursor = page.Next
			}
			if !found {
				return p, ghError("source_predecessor_contributors_unknown")
			}
		}
		if len(observed) != len(s.Contributors) {
			return p, ghError("source_contributors_incomplete")
		}
		for _, id := range s.Contributors {
			if !observed[id] {
				return p, ghError("source_contributors_incomplete")
			}
		}
	}
	commitRaw, _, err := reader.api(ctx, owner, "GET", "repos/"+s.SourceRepo+"/git/commits/"+s.HeadSHA, nil)
	if err != nil {
		return p, err
	}
	var commit struct {
		SHA  string
		Tree struct{ SHA string }
	}
	if json.Unmarshal(commitRaw, &commit) != nil || commit.SHA != s.HeadSHA || commit.Tree.SHA != s.TreeSHA {
		return p, ghError("source_tree_changed")
	}
	archive, _, err := reader.api(ctx, owner, "GET", "repos/"+s.SourceRepo+"/tarball/"+s.HeadSHA, nil)
	if err != nil {
		return p, err
	}
	bundle, err := sourceBundleSHA(archive)
	if err != nil || bundle != s.BundleSHA || s.PayloadSHA != bundle {
		return p, ghError("source_payload_changed")
	}
	p.QAArtifact = &QAArtifact{Kind: "source", HeadSHA: s.HeadSHA, SHA256: bundle, URL: "https://api.github.com/repos/" + s.SourceRepo + "/tarball/" + s.HeadSHA, Revision: "source-bundle-v1"}
	if predecessor := in.Target.Predecessor; predecessor != nil {
		raw, _, err := reader.api(ctx, owner, "GET", "repos/"+repo+"/git/commits/"+predecessor.CommitSHA, nil)
		if err != nil {
			return p, err
		}
		var integrated struct {
			SHA     string
			Tree    struct{ SHA string }
			Parents []struct{ SHA string }
		}
		if json.Unmarshal(raw, &integrated) != nil || integrated.SHA != predecessor.CommitSHA || integrated.Tree.SHA != predecessor.TreeSHA || len(integrated.Parents) != 1 || integrated.Parents[0].SHA != predecessor.BeforeSHA {
			return p, ghError("source_predecessor_receipt_changed")
		}
		raw, _, err = reader.api(ctx, owner, "GET", "repos/"+repo+"/compare/"+predecessor.CommitSHA+"..."+in.Target.SHA, nil)
		if err != nil {
			return p, err
		}
		var comparison struct {
			Status    string
			MergeBase struct{ SHA string } `json:"merge_base_commit"`
		}
		if json.Unmarshal(raw, &comparison) != nil || (comparison.Status != "ahead" && comparison.Status != "identical") || comparison.MergeBase.SHA != predecessor.CommitSHA {
			return p, ghError("source_predecessor_not_in_main")
		}
	}
	checks, _, err := reader.api(ctx, owner, "GET", "repos/"+repo+"/commits/"+s.HeadSHA+"/status", nil)
	if err != nil {
		return p, err
	}
	var status struct {
		State string
		Total int `json:"total_count"`
	}
	if json.Unmarshal(checks, &status) != nil {
		return p, c.ErrConflict
	}
	runs, _, err := reader.api(ctx, owner, "GET", "repos/"+repo+"/commits/"+s.HeadSHA+"/check-runs?per_page=100", nil)
	if err != nil {
		return p, err
	}
	var checkRuns struct {
		Total int                                   `json:"total_count"`
		Runs  []struct{ Status, Conclusion string } `json:"check_runs"`
	}
	if json.Unmarshal(runs, &checkRuns) != nil || checkRuns.Total != len(checkRuns.Runs) {
		return p, ghError("source_checks_incomplete")
	}
	if status.Total > 0 && status.State != "success" {
		return p, ghError("source_checks_not_green")
	}
	for _, check := range checkRuns.Runs {
		if check.Status != "completed" || check.Conclusion != "success" && check.Conclusion != "skipped" && check.Conclusion != "neutral" {
			return p, ghError("source_checks_not_green")
		}
	}
	if status.Total == 0 && checkRuns.Total == 0 {
		return p, ghError("source_checks_missing")
	}
	p.ChecksState = "success"
	if !p.Merged && (remote.Mergeable == nil || !*remote.Mergeable || remote.MergeableState != "clean") {
		return p, ghError("source_requires_explicit_author_integration_and_renewed_gates")
	}
	confirm, _, err := reader.api(ctx, owner, "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
	if err != nil {
		return p, err
	}
	{
		// Reparse and compare the identity fields; unrelated updated_at/review
		// counts are not source authority. The original raw is reused below.
		var fresh struct {
			Body string
			Head struct{ SHA, Ref string }
			Base struct{ SHA, Ref string }
			User restActor
		}
		if json.Unmarshal(confirm, &fresh) != nil || fresh.Body != remote.Body || fresh.Head.SHA != s.HeadSHA || fresh.Head.Ref != s.Branch || fresh.Base.Ref != remote.Base.Ref || fresh.Base.SHA != remote.Base.SHA || fresh.User.ID != s.AuthorID {
			return p, ghError("source_changed_during_read")
		}
	}
	return p, nil
}

// A source-job snapshot bypasses no ordinary native guard: it is a separate
// exact owner-decision lane, with no retrospective PR/task metadata injection.
func (g *GitHubClient) sourceJobSnapshot(ctx context.Context, cfg Config, m NativeMaster, j Job) (NativeCatalog, error) {
	cat := NativeCatalog{Complete: true, Reports: map[string]c.Message{}}
	if !nativeSourceJob(cfg, j) || j.Master.Goal == nil {
		return cat, c.ErrForbidden
	}
	em, ok := m.(NativeEngineeringMaster)
	if !ok {
		return cat, c.ErrUnsupported
	}
	a, err := nativeCurrentGoal(ctx, cfg, em, *j.Master.Goal)
	if err != nil {
		return cat, err
	}
	cat.Goals = []c.GoalAuthority{a}
	if err = loadNativeSourceDecisions(ctx, cfg, m, &cat); err != nil {
		return cat, err
	}
	if err = g.hydrateNativeSources(ctx, cfg, m, &cat); err != nil {
		return cat, err
	}
	if reason := cat.SourceRefusals[j.Master.SourceDecisionID]; reason != "" {
		return cat, errors.New(reason)
	}
	return cat, nil
}

var errSourceUncertain = errors.New("source effect uncertain; observe original request read-only and obtain supported reconciliation; never replay or change request ID")

func verifySourceIntegration(ctx context.Context, runner CommandRunner, j Job) error {
	if j.Checkout == "" || j.Master == nil {
		return c.ErrConflict
	}
	if err := safePath(j.Checkout); err != nil {
		return err
	}
	checkout := &TaskCheckouts{runner: runner}
	tree, err := checkout.run(ctx, j.Checkout, "git", "merge-tree", "--write-tree", j.Master.SourceTargetSHA, j.HeadSHA)
	if err != nil || strings.TrimSpace(string(tree)) != j.Master.SourceResultTreeSHA {
		return c.ErrConflict
	}
	archive, err := checkout.run(ctx, j.Checkout, "git", "archive", "--format=tar.gz", "--prefix=source/", j.Master.SourceResultTreeSHA)
	if err != nil {
		return err
	}
	bundle, err := sourceBundleSHA(archive)
	if err != nil || bundle != j.Master.SourceResultBundleSHA {
		return c.ErrConflict
	}
	return nil
}

// The existing action producer owns this function. Persist intent before every
// service boundary; only an acknowledged response can advance the write state.
func runNativeSourceAction(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, x NativeExecution, in NativeAction) (NativeActionResult, error) {
	var empty NativeActionResult
	if !nativeSourceJob(cfg, x.Job) || x.Phase != "active" || in.Operation != x.Job.Kind || in.RequestID == "" || len(in.RequestID) > 100 {
		return empty, c.ErrForbidden
	}
	sm, ok := m.(sourceJourneyMaster)
	if !ok {
		return empty, c.ErrUnsupported
	}
	record := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: c.SourceDigest(in), Operation: in.Operation, State: "prepared"}
	raw, err := readPrivate(nativeActionPath(cfg, x.Job.ID, in.RequestID), c.MaxJSON)
	if err == nil {
		if strictTeamJSON(raw, &record) != nil || record.InputSHA != c.SourceDigest(in) {
			return empty, c.ErrConflict
		}
		if record.State == "completed" {
			return record.Result, nil
		}
		if record.State == "uncertain" {
			return empty, errSourceUncertain
		}
	} else if !os.IsNotExist(err) {
		return empty, err
	}
	if in.Operation == "source-retarget" && record.Step != "" {
		ctx = context.WithValue(ctx, sourceRetargetAcknowledgedKey{}, x.Job.Master.SourceDecisionID)
	}
	if uncertain, err := nativeJobActionUncertain(cfg, x.Job.ID); err != nil {
		return empty, err
	} else if uncertain {
		return empty, errSourceUncertain
	}
	var candidate NativeCandidate
	guard := func() error {
		for _, paused := range []func() (bool, error){NewVersionedFileStore(cfg.StateDir, 3).Paused, NewVersionedFileStore(cfg.StateDir, 3).AdmissionPaused} {
			if yes, err := paused(); err != nil {
				return err
			} else if yes {
				return c.ErrForbidden
			}
		}
		if err := verifyNativePlacement(ctx, cfg, m); err != nil {
			return err
		}
		saved, err := loadNativeExecution(cfg, x.Job.ID)
		if err != nil || saved.Phase != "active" {
			return c.ErrForbidden
		}
		cat, err := g.sourceJobSnapshot(ctx, cfg, m, x.Job)
		if err != nil {
			return err
		}
		for _, v := range nativeSourceCandidates(cfg, cat, nil, time.Now()) {
			if v.Job.ID == x.Job.ID {
				candidate = v
				return nil
			}
		}
		// An acknowledged merge can leave a completed task; confirmation must
		// retain the same decision and effect lineage, never re-enter selection.
		if record.Step == "effect-acknowledged" || record.Step == "receipt-acknowledged" || record.Step == "completion-acknowledged" {
			for _, entry := range cat.SourceEntries {
				if entry.Decision.ID == x.Job.Master.SourceDecisionID && c.SourceDigest(entry.Decision.Input) == x.Job.ScopeSHA {
					d := entry.Decision
					candidate = NativeCandidate{Goal: cat.Goals[0], PR: &entry.PR, Task: &entry.Task, SourceDecision: &d}
					return nil
				}
			}
		}
		return c.ErrForbidden
	}
	if err = guard(); err != nil {
		return empty, err
	}
	d := *candidate.SourceDecision
	if err = persistNativeActionIntent(cfg, x, in); err != nil {
		return empty, err
	}
	if err = saveNativeAction(cfg, record); err != nil {
		return empty, err
	}
	effect := c.SourceEffectReceipt{Goal: d.Input.Goal, DecisionID: d.ID, RequestID: in.RequestID + ":receipt", Kind: strings.TrimPrefix(in.Operation, "source-")}
	if in.Operation == "source-review" || in.Operation == "source-qa" {
		effect.Kind = in.Operation
	}
	if record.Step == "" {
		if in.Operation == "source-review" || in.Operation == "source-qa" {
			if in.Verdict != "approved" && in.Verdict != "changes-needed" {
				return empty, c.ErrInvalid
			}
			body := in.Body
			if in.Operation == "source-qa" {
				body = in.ReportBody
			}
			if strings.TrimSpace(body) == "" {
				return empty, c.ErrInvalid
			}
		}
		if err = guard(); err != nil {
			return empty, err
		}
		if err = verifySourceIntegration(ctx, g.runner, x.Job); err != nil {
			return empty, err
		}
		if err = guard(); err != nil {
			return empty, err
		}
		record.State = "uncertain"
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
		repo, n, _ := parseRef(d.Input.Source.PRURL)
		owner := strings.Split(repo, "/")[0]
		switch in.Operation {
		case "source-review", "source-qa":
			if in.Verdict != "approved" && in.Verdict != "changes-needed" {
				return empty, c.ErrInvalid
			}
			body := in.Body
			if in.Operation == "source-qa" {
				body = in.ReportBody
			}
			if strings.TrimSpace(body) == "" {
				return empty, c.ErrInvalid
			}
			event := "APPROVE"
			if in.Verdict == "changes-needed" {
				event = "REQUEST_CHANGES"
			}
			reviewBody := "Supplemental native " + in.Operation + " decision " + d.ID + " scope " + c.SourceDigest(d.Input) + ". Original source and historical evidence are unchanged."
			if in.Operation == "source-review" {
				reviewBody += "\n\n" + body
			}
			raw, _, err = g.api(ctx, owner, "POST", "repos/"+repo+"/pulls/"+stringID(n)+"/reviews", map[string]any{"commit_id": d.Input.Source.HeadSHA, "event": event, "body": reviewBody})
			if err != nil {
				return empty, err
			}
			var review struct{ ID int64 }
			if json.Unmarshal(raw, &review) != nil || review.ID <= 0 {
				return empty, c.ErrConflict
			}
			report := c.SourceGateReport{DecisionID: d.ID, ScopeSHA: c.SourceDigest(d.Input), Stage: in.Operation, Verdict: in.Verdict, ReviewID: stringID(int(review.ID)), ReportBody: body}
			encoded, _ := json.Marshal(report)
			record.PublishedHeadSHA = string(encoded)
		case "source-retarget":
			if candidate.PR.BaseBranch != d.Input.Source.BaseBranch || d.Input.Source.BaseBranch == "main" {
				return empty, c.ErrConflict
			}
			_, _, err = g.api(ctx, owner, "PATCH", "repos/"+repo+"/pulls/"+stringID(n), map[string]any{"base": "main"})
			if err != nil {
				return empty, err
			}
		case "source-deliver":
			if candidate.PR.BaseBranch != "main" || candidate.PR.Merged {
				return empty, c.ErrConflict
			}
			raw, _, err = g.api(ctx, owner, "PUT", "repos/"+repo+"/pulls/"+stringID(n)+"/merge", map[string]any{"sha": d.Input.Source.HeadSHA, "merge_method": "squash"})
			if err != nil {
				return empty, err
			}
			var merged struct {
				Merged bool
				SHA    string
			}
			if json.Unmarshal(raw, &merged) != nil || !merged.Merged || !qaHeadSHA.MatchString(merged.SHA) {
				return empty, c.ErrConflict
			}
			record.PublishedHeadSHA = merged.SHA
		}
		record.State, record.Step = "prepared", "effect-acknowledged"
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
	}
	if record.Step == "effect-acknowledged" {
		// Revalidation after the effect must not turn revocation into another
		// write. An acknowledged fact can remain locally held for observation.
		if _, err = sm.ValidateSourceDecision(ctx, c.SourceDecisionValidation{Goal: d.Input.Goal, ID: d.ID}); err != nil {
			return empty, err
		}
		switch in.Operation {
		case "source-review", "source-qa":
			msg, err := m.Send(ctx, c.SendMessage{ThreadID: candidate.Goal.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: record.PublishedHeadSHA, RequestID: in.RequestID + ":report"})
			if err != nil {
				return empty, err
			}
			record.Result.EvidenceMessageID = msg.MessageID
			record.Result.EvidenceBodySHA = BodyHash(record.PublishedHeadSHA)
		case "source-deliver":
			repo, _, _ := parseRef(d.Input.Source.PRURL)
			if err = nativeConfirmMergeBase(ctx, g, repo, d.Input.Source.PRURL, d.Input.Target.SHA); err != nil {
				return empty, err
			}
			raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/git/commits/"+record.PublishedHeadSHA, nil)
			if err != nil {
				return empty, err
			}
			var commit struct {
				SHA  string
				Tree struct{ SHA string }
			}
			if json.Unmarshal(raw, &commit) != nil || commit.SHA != record.PublishedHeadSHA || commit.Tree.SHA != d.Input.Target.ResultTreeSHA {
				return empty, c.ErrConflict
			}
			effect.CommitSHA, effect.TreeSHA = commit.SHA, commit.Tree.SHA
		default:
			repo, n, _ := parseRef(d.Input.Source.PRURL)
			raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
			if err != nil {
				return empty, err
			}
			var post struct {
				Head struct{ SHA string }
				Base struct{ Ref string }
			}
			if json.Unmarshal(raw, &post) != nil || post.Head.SHA != d.Input.Source.HeadSHA || post.Base.Ref != "main" {
				return empty, c.ErrConflict
			}
		}
		effect.ReportID, effect.BodySHA = record.Result.EvidenceMessageID, record.Result.EvidenceBodySHA
		if _, err = sm.RecordSourceEffect(ctx, effect); err != nil {
			return empty, err
		}
		record.Step = "receipt-acknowledged"
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
	}
	if record.Step == "receipt-acknowledged" && in.Operation == "source-deliver" {
		if err = guard(); err != nil {
			return empty, err
		}
		if _, err = sm.CompleteSourceTask(ctx, c.SourceCompletionInput{Goal: d.Input.Goal, DecisionID: d.ID, RequestID: in.RequestID + ":complete"}); err != nil {
			return empty, err
		}
		record.Step = "completion-acknowledged"
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
	}
	record.State = "completed"
	record.Result.Phase = "completed"
	record.Result.URL = d.Input.Source.PRURL
	if err = saveNativeAction(cfg, record); err != nil {
		return empty, err
	}
	return record.Result, nil
}
