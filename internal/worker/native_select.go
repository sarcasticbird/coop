package worker

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type NativeExecution struct {
	PendingContinuation    *Job `json:",omitempty"`
	Wait                   *NativeDependencyWait
	SelectedAt             time.Time
	Version                int
	MasterID, InstanceID   string
	Generation             int64
	Goal                   c.GoalRef
	Binding                *NativeTaskBinding
	Job                    Job
	Phase, Reason          string
	Turns                  []Job
	PausedDecisionSequence int64
}
type NativeCandidate struct {
	Job         Job
	Goal        c.GoalAuthority
	Task        *NativeTask
	PR          *PR
	RebindTasks []string
}

const nativeBranchRefreshPrefix = "branch-refresh:"
const nativeChecksRepairPrefix = "checks-repair:"

func nativeNeedsChecksRepair(p PR) bool {
	return !p.Merged && !p.Closed && (p.ChecksState == "failure" || p.ChecksState == "error")
}

func nativeIsChecksRepair(j Job) bool {
	return j.Kind == "fix" && strings.HasPrefix(j.InputRevision, nativeChecksRepairPrefix)
}

func nativeNeedsBranchRefresh(p PR) bool {
	return !p.Merged && !p.Closed && (p.MergeState == "BEHIND" || p.MergeState == "DIRTY")
}

func nativeFixCycles(taskURL string, history map[string]NativeExecution) int {
	cycles := 0
	for _, v := range history {
		if v.Job.Kind == "fix" && v.Job.TaskURL == taskURL && v.Phase == "completed" {
			cycles++
		}
	}
	return cycles
}

func nativeIsBranchRefresh(j Job) bool {
	return j.Kind == "fix" && strings.HasPrefix(j.InputRevision, nativeBranchRefreshPrefix)
}

func NativeAcceptanceSHA(a c.GoalAuthority) string {
	raw, _ := json.Marshal(a.Revision.Proposal.Acceptance)
	return BodyHash(string(raw))
}

func nativeLatestReview(pr PR, actorID int64) Review {
	var out Review
	for _, r := range pr.Reviews {
		if r.Author.ID == actorID && (out.ID == "" || r.SubmittedAt.After(out.SubmittedAt) || r.SubmittedAt.Equal(out.SubmittedAt) && r.ID > out.ID) {
			out = r
		}
	}
	return out
}
func nativeContributor(pr PR, id int64) bool {
	if !pr.ContributorsComplete {
		return true
	}
	for _, a := range pr.Contributors {
		if a.ID == id {
			return true
		}
	}
	return false
}
func nativePeerCleared(b NativeTaskBinding, p PR) (Review, bool) {
	r := nativeLatestReview(p, b.PeerID)
	var meta NativeReviewResult
	valid := p.ReviewsComplete && !nativeContributor(p, b.PeerID) && r.ID != "" && r.Author.Kind == "Bot" && r.HeadSHA == p.HeadSHA && r.SubmittedAt.After(p.ScopeUpdatedAt) && nativeMetadata(r.Body, "review", &meta) == nil && meta.Version == 3 && meta.Goal == b.Goal && meta.TaskURL == b.TaskURL && meta.AssignmentSHA == NativeAssignmentSHA(b) && meta.HeadSHA == p.HeadSHA && meta.PRScopeRevision == p.ScopeRevision
	return r, valid && r.State == "APPROVED" && meta.Verdict == "approved"
}
func nativeQAState(b NativeTaskBinding, a c.GoalAuthority, p PR, reports map[string]c.Message) (Review, bool) {
	r := nativeLatestReview(p, b.QAID)
	if p.QAArtifact == nil || !p.ReviewsComplete || nativeContributor(p, b.QAID) || !r.SubmittedAt.After(p.ScopeUpdatedAt) {
		return r, false
	}
	var qa NativeQAResult
	if nativeMetadata(r.Body, "qa", &qa) != nil {
		return r, false
	}
	report, ok := reports[qa.EvidenceMessageID]
	if !ok {
		return r, false
	}
	if qa.PRScopeRevision != p.ScopeRevision || qa.HeadSHA != p.HeadSHA || qa.ArtifactSHA != p.QAArtifact.SHA256 || qa.AcceptanceSHA != NativeAcceptanceSHA(a) || qa.Verdict != "approved" {
		return r, false
	}
	return r, ValidateNativeQA(b, a, r, report, qa) == nil
}

func nativeChangesRequested(kind string, b NativeTaskBinding, a c.GoalAuthority, p PR, r Review) bool {
	if r.State != "CHANGES_REQUESTED" || r.HeadSHA != p.HeadSHA || !r.SubmittedAt.After(p.ScopeUpdatedAt) {
		return false
	}
	if kind == "review" {
		var meta NativeReviewResult
		return nativeMetadata(r.Body, "review", &meta) == nil && meta.Version == 3 && meta.Goal == b.Goal && meta.TaskURL == b.TaskURL && meta.AssignmentSHA == NativeAssignmentSHA(b) && meta.HeadSHA == p.HeadSHA && meta.PRScopeRevision == p.ScopeRevision && meta.Verdict == "changes-needed"
	}
	var meta NativeQAResult
	return p.QAArtifact != nil && nativeMetadata(r.Body, "qa", &meta) == nil && meta.Version == 3 && meta.Goal == b.Goal && meta.TaskURL == b.TaskURL && meta.AssignmentSHA == NativeAssignmentSHA(b) && meta.HeadSHA == p.HeadSHA && meta.PRScopeRevision == p.ScopeRevision && meta.ArtifactSHA == p.QAArtifact.SHA256 && meta.AcceptanceSHA == NativeAcceptanceSHA(a) && meta.Environment == a.Revision.Proposal.QA.Environment && meta.EnvironmentRevision == a.Revision.Proposal.QA.EnvironmentRevision && meta.Verdict == "changes-needed"
}

// NativeCandidates consumes only a complete catalog of v3 artifacts. It never
// feeds master goal references into GitHub-only v1/v2 admission functions.
func NativeCandidates(cfg Config, catalog NativeCatalog, history map[string]NativeExecution, now time.Time) []NativeCandidate {
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || !catalog.Complete {
		return nil
	}
	self, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
	if !ok {
		return nil
	}
	goals := map[c.GoalRef]c.GoalAuthority{}
	for _, a := range catalog.Goals {
		if a.Valid && a.State == "approved" && a.ActiveRevisionSHA256 == a.Revision.Ref.RevisionSHA256 && a.Revision.Ref.MasterID == cfg.Master.MasterID {
			goals[a.Revision.Ref] = a
		}
	}
	out := []NativeCandidate{}
	add := func(kind string, a c.GoalAuthority, t *NativeTask, p *PR, revision string) {
		ref := a.Revision.Ref
		policy, _ := TeamPolicySHA(*cfg.Team)
		role := "planner"
		sha, base := policy, ""
		taskURL, prURL, head, scope := "", "", "", ref.RevisionSHA256
		priority, ordinal := 0, 0
		if t != nil {
			sha = NativeAssignmentSHA(t.Binding)
			base = t.Binding.BaseBranch
			taskURL = t.Task.URL
			scope = t.Task.BodySHA
			priority, ordinal = t.Task.Priority, t.Task.Ordinal
		}
		switch kind {
		case "implement", "fix", "deliver":
			role = "implementer"
		case "review":
			role = "engineering-review"
		case "qa":
			role = "customer-qa"
		}
		if p != nil {
			prURL = p.URL
			head = p.HeadSHA
		}
		master := &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ThreadID: a.Revision.Proposal.ThreadID, Goal: &ref, AssignmentSHA: sha, ExecutorID: cfg.Self.ID, Role: role, BaseBranch: base}
		j := Job{Kind: kind, TaskURL: taskURL, PRURL: prURL, HeadSHA: head, ScopeSHA: scope, InputRevision: revision, Master: master, FirstEligibleAt: now, SchedulePriority: priority, ScheduleOrdinal: ordinal}
		if p != nil {
			j.PRScopeRevision = p.ScopeRevision
			j.QAArtifact = p.QAArtifact
		}
		raw, _ := json.Marshal(struct {
			Master                         *MasterJobBinding
			Kind, Task, PR, Head, Revision string
		}{master, kind, taskURL, prURL, head, revision})
		j.ID = BodyHash(string(raw))
		for _, retained := range history {
			if retained.Phase == "blocked" && BindingKey(cfg, retained.Job) == BindingKey(cfg, j) {
				return
			}
		}
		if _, seen := history[j.ID]; seen {
			return
		}
		candidate := NativeCandidate{Job: j, Goal: a, Task: t, PR: p}
		if kind == "plan" {
			for _, task := range catalog.Tasks {
				if task.Binding.Goal == ref && task.Binding.ApprovalSequence != a.ApprovalSequence && !task.Task.Completed {
					candidate.RebindTasks = append(candidate.RebindTasks, task.Task.URL)
				}
			}
		}
		out = append(out, candidate)
	}
	if cfg.Name == cfg.Team.Planner && memberCapability(self, "plan") {
		for ref, a := range goals {
			exists, needsRebind := false, false
			for _, t := range catalog.Tasks {
				if t.Binding.Goal == ref && t.Binding.ApprovalSequence == a.ApprovalSequence {
					exists = true
				}
				if t.Binding.Goal == ref && t.Binding.ApprovalSequence < a.ApprovalSequence && !t.Task.Completed {
					needsRebind = true
				}
			}
			if !exists || needsRebind {
				add("plan", a, nil, nil, ref.RevisionSHA256+":"+strconv.FormatInt(a.ApprovalSequence, 10))
			}
		}
	}
	for _, value := range catalog.Tasks {
		t := value
		a, ok := goals[t.Binding.Goal]
		if !ok || t.Binding.ApprovalSequence != a.ApprovalSequence || validateNativeRoster(cfg, t.Binding) != nil || t.Binding.TaskScopeSHA != t.Task.BodySHA || t.Task.Closed || t.Task.Completed {
			continue
		}
		deps := true
		for _, ref := range t.Binding.DependsOn {
			done := false
			for _, d := range catalog.Tasks {
				if d.Task.URL == ref && d.Binding.Goal == t.Binding.Goal && d.Task.Completed {
					done = true
				}
			}
			deps = deps && done
		}
		if !deps {
			continue
		}
		var live []PR
		for _, p := range t.PRs {
			if !p.Closed || p.Merged {
				live = append(live, p)
			}
		}
		if len(live) > 1 {
			continue
		}
		if len(live) == 0 {
			if cfg.Self.ID == t.Binding.AuthorID && cfg.Master.AgentID == a.Actors.Author.PrincipalID && memberCapability(self, "implement") && t.Task.Ready {
				add("implement", a, &t, nil, "initial")
			}
			continue
		}
		p := live[0]
		if p.AssignmentSHA != NativeAssignmentSHA(t.Binding) || p.MasterGoal == nil || *p.MasterGoal != t.Binding.Goal || p.Author.ID != t.Binding.AuthorID || p.BaseBranch != t.Binding.BaseBranch || !p.ReviewsComplete || !p.ContributorsComplete {
			continue
		}
		peer, cleared := nativePeerCleared(t.Binding, p)
		qa, qaCleared := Review{}, !a.Revision.Proposal.QA.Required
		if a.Revision.Proposal.QA.Required {
			qa, qaCleared = nativeQAState(t.Binding, a, p, catalog.Reports)
		}
		if cfg.Self.ID == t.Binding.PeerID && cfg.Master.AgentID == a.Actors.Peer.PrincipalID && memberCapability(self, "engineering-review") && !nativeContributor(p, cfg.Self.ID) && !cleared && !p.Merged && !nativeNeedsBranchRefresh(p) && !nativeNeedsChecksRepair(p) {
			if nativeChangesRequested("review", t.Binding, a, p, peer) {
				continue
			}
			revision := p.ScopeRevision
			if peer.State == "DISMISSED" && peer.HeadSHA == p.HeadSHA {
				revision += ":dismissed:" + peer.ID
			}
			add("review", a, &t, &p, revision)
			continue
		}
		if cfg.Self.ID == t.Binding.AuthorID && cfg.Master.AgentID == a.Actors.Author.PrincipalID && memberCapability(self, "implement") {
			if p.Merged {
				add("deliver", a, &t, &p, "merged:"+p.HeadSHA)
				continue
			}
			if nativeNeedsBranchRefresh(p) {
				if qaHeadSHA.MatchString(p.BaseSHA) && nativeFixCycles(t.Task.URL, history) < Defaults(cfg).Limits.MaxReviewCycles {
					add("fix", a, &t, &p, nativeBranchRefreshPrefix+p.BaseSHA+":"+p.ScopeRevision)
				}
				continue
			}
			if nativeNeedsChecksRepair(p) {
				if nativeFixCycles(t.Task.URL, history) < Defaults(cfg).Limits.MaxReviewCycles {
					add("fix", a, &t, &p, nativeChecksRepairPrefix+p.ScopeRevision)
				}
				continue
			}
			if peer.HeadSHA == p.HeadSHA && peer.State == "CHANGES_REQUESTED" || qa.HeadSHA == p.HeadSHA && qa.State == "CHANGES_REQUESTED" {
				if nativeFixCycles(t.Task.URL, history) < Defaults(cfg).Limits.MaxReviewCycles {
					add("fix", a, &t, &p, BodyHash(peer.ID+":"+qa.ID+":"+p.ScopeRevision))
				}
				continue
			}
			if cleared && qaCleared && (p.ChecksState == "success" || p.ChecksState == "none") && p.MergeState == "CLEAN" {
				add("deliver", a, &t, &p, BodyHash(peer.ID+":"+qa.ID+":"+p.ScopeRevision))
			}
		}
		if cfg.Self.ID == t.Binding.QAID && cfg.Master.AgentID == a.Actors.QA.PrincipalID && memberCapability(self, "customer-qa") && cleared && !qaCleared && !p.Merged && !nativeNeedsBranchRefresh(p) && p.QAArtifact != nil && !nativeContributor(p, cfg.Self.ID) && (p.ChecksState == "success" || p.ChecksState == "none") {
			if nativeChangesRequested("qa", t.Binding, a, p, qa) {
				continue
			}
			revision := p.ScopeRevision + ":" + p.QAArtifact.SHA256 + ":" + NativeAcceptanceSHA(a)
			if qa.State == "DISMISSED" && qa.HeadSHA == p.HeadSHA {
				revision += ":dismissed:" + qa.ID
			}
			add("qa", a, &t, &p, revision)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job.ID < out[j].Job.ID })
	return out
}
