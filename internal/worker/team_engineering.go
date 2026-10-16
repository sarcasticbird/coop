package worker

import (
	"sort"
	"time"
)

func admitTeam(c Config, j Job, s Snapshot, now time.Time) (bool, string) {
	if !s.Complete || c.Team == nil || j.Binding == nil {
		return false, "incomplete snapshot or missing assignment binding"
	}
	if _, ok := TeamMemberForActor(*c.Team, c.Self); !ok {
		return false, "executor is not a configured team actor"
	}
	bound, err := bindTeamJob(c, j, s, now)
	if err != nil {
		return false, err.Error()
	}
	if *bound.Binding != *j.Binding || j.ID != bound.ID || j.Binding.ExecutorID != c.Self.ID || j.PRScopeRevision != bound.PRScopeRevision {
		return false, "saved assignment, executor or scope changed"
	}
	if j.Kind == "plan" {
		for _, e := range planningEvents(c, s, now) {
			if isPlanner(c) && e.ID == j.TaskURL && e.Revision == j.InputRevision && e.URL == j.GoalURL && goalInScope(c, Goal{URL: e.URL}) && (sameActor(e.Author, c.Owner) || configuredImplementer(c, e.Author)) {
				return e.Kind != "delivery_completed" || j.PRURL == pullLink.FindString(e.Body), "trusted planning input"
			}
		}
		return false, "untrusted planning input"
	}
	t, _ := taskFor(s, j.TaskURL)
	g, _ := goalFor(s, j.GoalURL)
	if t.Closed || t.Completed || !taskAdmitted(c, t, g, s) {
		return false, "task scope, claim or dependencies blocked"
	}
	if j.ScopeSHA != t.BodySHA {
		return false, "saved task scope changed"
	}
	if j.Kind == "implement" {
		if !t.Ready {
			return false, "task not Ready"
		}
		for _, p := range s.PRs {
			if p.TaskURL == t.URL && (!p.Closed || p.Merged) {
				return false, "task has an existing PR"
			}
		}
		return true, "assigned Ready task in approved goal"
	}
	p, ok := prFor(s, j.PRURL)
	if !ok || p.BaseBranch != t.Assignment.BaseBranch || !referenceInRepo(p.URL, t.Repo, "pull") || p.TaskURL != t.URL || p.GoalURL != g.URL || p.HeadSHA != j.HeadSHA || p.AssignmentSHA != j.Binding.AssignmentSHA || !teamTaskAuthor(c, t, p.Author) {
		return false, "PR identity, head or assignment changed"
	}
	if p.Merged && j.Kind == "deliver" {
		return true, "merged task requires issue completion"
	}
	if p.Closed || p.Merged {
		return false, "PR closed"
	}
	if _, ok := teamIndependentPeer(c, t, p); !ok {
		return false, "independent engineering evidence unavailable"
	}
	r := teamLatestReview(c, t, p)
	switch j.Kind {
	case "review":
		return j.InputRevision == p.HeadSHA+":"+p.ScopeRevision && (r.ID == "" || r.State == "DISMISSED"), "current assigned head requires review"
	case "fix":
		return r.State == "CHANGES_REQUESTED" && j.InputRevision == r.ID, "current-head changes requested"
	case "deliver":
		_, cleared := EngineeringReviewFor(c, t, p)
		return cleared && j.InputRevision == r.ID && (p.ChecksState == "success" || p.ChecksState == "none") && p.MergeState == "CLEAN", "current engineering clearance and checks required"
	}
	return false, "unsupported team work kind"
}

func selectTeam(c Config, s Snapshot, state State, now time.Time) []Job {
	var candidates, result []Job
	for _, p := range s.PRs {
		t, ok := taskFor(s, p.TaskURL)
		if !ok {
			continue
		}
		r := teamLatestReview(c, t, p)
		kind, revision := "review", p.HeadSHA+":"+p.ScopeRevision
		if teamTaskAuthor(c, t, c.Self) {
			switch {
			case p.Merged:
				kind, revision = "deliver", "merged:"+p.HeadSHA
			case r.State == "CHANGES_REQUESTED":
				kind, revision = "fix", r.ID
			case r.State == "APPROVED":
				kind, revision = "deliver", r.ID
			default:
				continue
			}
		}
		candidates = append(candidates, Job{Kind: kind, GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA, InputRevision: revision})
	}
	for _, t := range s.Tasks {
		candidates = append(candidates, Job{Kind: "implement", GoalURL: t.GoalURL, TaskURL: t.URL, InputRevision: t.BodySHA})
	}
	if isPlanner(c) {
		for _, e := range planningEvents(c, s, now) {
			if e.Kind == "planning" || e.Kind == "delivery_completed" {
				j := Job{Kind: "plan", GoalURL: e.URL, TaskURL: e.ID, InputRevision: e.Revision}
				if e.Kind == "delivery_completed" {
					j.PRURL = pullLink.FindString(e.Body)
				}
				candidates = append(candidates, j)
			}
		}
	}
	for _, j := range candidates {
		j, err := bindTeamJob(c, j, s, now)
		if err != nil {
			continue
		}
		if _, seen := state.Jobs[j.ID]; seen {
			continue
		}
		if pendingReviewHandoff(c, j, s, now) {
			continue
		}
		if ok, _ := Admit(c, j, s, now); ok {
			result = append(result, j)
		}
	}
	priority := map[string]int{"fix": 0, "review": 1, "deliver": 2, "implement": 3, "plan": 4}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if priority[a.Kind] != priority[b.Kind] {
			return priority[a.Kind] < priority[b.Kind]
		}
		ta, _ := taskFor(s, a.TaskURL)
		tb, _ := taskFor(s, b.TaskURL)
		if ta.Priority != tb.Priority {
			return ta.Priority < tb.Priority
		}
		if ta.Ordinal != tb.Ordinal {
			return ta.Ordinal < tb.Ordinal
		}
		return a.ID < b.ID
	})
	return result
}
