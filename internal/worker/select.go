package worker

import (
	"sort"
	"strings"
	"time"
)

func JobKey(j Job) string {
	return BodyHash(strings.Join([]string{j.Kind, j.GoalURL, j.TaskURL, j.PRURL, j.HeadSHA, j.InputRevision}, "\x00"))
}
func goalFor(s Snapshot, url string) (Goal, bool) {
	for _, g := range s.Goals {
		if g.URL == url {
			return g, true
		}
	}
	return Goal{}, false
}
func taskFor(s Snapshot, url string) (Task, bool) {
	for _, t := range s.Tasks {
		if t.URL == url {
			return t, true
		}
	}
	return Task{}, false
}
func prFor(s Snapshot, url string) (PR, bool) {
	for _, p := range s.PRs {
		if p.URL == url {
			return p, true
		}
	}
	return PR{}, false
}
func latestReview(c Config, p PR) Review {
	var latest Review
	for _, r := range p.Reviews {
		if !r.SubmittedAt.After(p.ScopeUpdatedAt) {
			continue
		}
		if r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" && r.State != "DISMISSED" {
			continue
		}
		if sameActor(r.Author, c.Reviewer) && r.HeadSHA == p.HeadSHA && (r.SubmittedAt.After(latest.SubmittedAt) || r.SubmittedAt.Equal(latest.SubmittedAt) && r.ID > latest.ID) {
			latest = r
		}
	}
	return latest
}
func taskAdmitted(c Config, t Task, g Goal, s Snapshot) bool {
	if !referenceInRepo(t.URL, t.Repo, "issues") {
		return false
	}
	if t.Completed || t.Closed || !validGitHubURL(t.URL) || !validRepo(t.Repo) || !containsFold(c.CodeOwners, strings.Split(t.Repo, "/")[0]) || !containsFold(g.Repositories, t.Repo) || t.GoalURL != g.URL || t.BodySHA != TaskScopeHash(t.Title, t.Body) || t.MetadataScopeSHA != t.BodySHA || (!sameActor(t.MetadataAuthor, c.Owner) && !sameActor(t.MetadataAuthor, c.Reviewer)) {
		return false
	}
	if t.Claim != nil && (!sameActor(t.Claim.Author, c.Implementer) || t.Claim.Marker == "" || t.Claim.InstanceID == "") {
		return false
	}
	for _, url := range t.DependsOn {
		dep, ok := taskFor(s, url)
		if !ok || !dep.Completed {
			return false
		}
	}
	return true
}
func Admit(c Config, j Job, s Snapshot, now time.Time) (bool, string) {
	if c.Version != 1 {
		return false, "v2 workflow not supported"
	}
	if !s.Complete {
		return false, "incomplete GitHub snapshot"
	}
	if j.Kind == "plan" {
		for _, e := range planningEvents(c, s, now) {
			if e.ID == j.TaskURL && e.Revision == j.InputRevision && e.URL == j.GoalURL && goalInScope(c, Goal{URL: e.URL}) && (sameActor(e.Author, c.Owner) || sameActor(e.Author, c.Implementer)) && c.Role == "planner" {
				if e.Kind == "delivery_completed" && j.PRURL != pullLink.FindString(e.Body) {
					return false, "delivery handoff PR changed"
				}
				return true, "trusted planning input"
			}
		}
		return false, "untrusted planning input"
	}
	g, ok := goalFor(s, j.GoalURL)
	if !ok {
		return false, "missing goal"
	}
	a := EvaluateGoal(c, g, now)
	if a.State != "approved" {
		return false, a.Reason
	}
	t, ok := taskFor(s, j.TaskURL)
	if !ok || !taskAdmitted(c, t, g, s) {
		return false, "task scope, metadata, claim or dependency blocked"
	}
	if j.ScopeSHA != "" && j.ScopeSHA != t.BodySHA {
		return false, "saved task scope changed"
	}
	if j.Kind == "implement" {
		if c.Role != "implementer" || !t.Ready {
			return false, "task not Ready"
		}
		for _, p := range s.PRs {
			if p.TaskURL == t.URL && (!p.Closed || p.Merged) {
				return false, "task has an existing PR"
			}
		}
		return true, "Ready task in approved goal"
	}
	p, ok := prFor(s, j.PRURL)
	if !ok || !referenceInRepo(p.URL, t.Repo, "pull") {
		return false, "PR repository does not match task repository"
	}
	if p.Merged && j.Kind == "deliver" && p.TaskURL == t.URL && p.GoalURL == g.URL && p.HeadSHA == j.HeadSHA && sameActor(p.Author, c.Implementer) {
		return c.Role == "implementer", "merged task requires issue completion"
	}
	if p.Closed || p.Merged || p.HeadSHA != j.HeadSHA || p.TaskURL != t.URL || p.GoalURL != g.URL || !sameActor(p.Author, c.Implementer) {
		return false, "PR identity or head changed"
	}
	r := latestReview(c, p)
	switch j.Kind {
	case "review":
		if j.InputRevision != "" && j.InputRevision != p.HeadSHA+":"+p.ScopeRevision {
			return false, "saved PR review scope changed"
		}
		return c.Role == "planner" && (r.ID == "" || r.State == "DISMISSED"), "current head requires review"
	case "fix":
		return c.Role == "implementer" && r.State == "CHANGES_REQUESTED", "current-head changes requested"
	case "deliver":
		return c.Role == "implementer" && r.State == "APPROVED" && (p.ChecksState == "success" || p.ChecksState == "none") && p.MergeState == "CLEAN", "current-head review and checks required"
	}
	return false, "unsupported work kind"
}
func Select(c Config, s Snapshot, state State, now time.Time) []Job {
	var candidates []Job
	for _, p := range s.PRs {
		r := latestReview(c, p)
		kind := "review"
		if c.Role == "implementer" {
			switch r.State {
			case "CHANGES_REQUESTED":
				kind = "fix"
			case "APPROVED":
				kind = "deliver"
			default:
				if !p.Merged {
					continue
				}
				kind = "deliver"
			}
		}
		revision := r.ID
		if p.Merged && c.Role == "implementer" {
			kind = "deliver"
			revision = "merged:" + p.HeadSHA
		}
		if kind == "review" {
			revision = p.HeadSHA + ":" + p.ScopeRevision
		}
		candidates = append(candidates, Job{Kind: kind, GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA, InputRevision: revision})
	}
	if c.Role == "implementer" {
		for _, t := range s.Tasks {
			candidates = append(candidates, Job{Kind: "implement", GoalURL: t.GoalURL, TaskURL: t.URL, InputRevision: t.BodySHA})
		}
	}
	if c.Role == "planner" {
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
	var result []Job
	for _, j := range candidates {
		j.ID = JobKey(j)
		if _, seen := state.Jobs[j.ID]; seen {
			continue
		}
		if pendingReviewHandoff(c, j, s, now) {
			continue // The dedicated review job owns its checkout and session.
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
