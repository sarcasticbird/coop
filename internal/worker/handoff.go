package worker

import (
	"regexp"
	"time"
)

// A planning reply is not evidence that a referenced PR has been reviewed.
// Keep handoffs bound to the trusted source event and the currently observed head.
type ReviewHandoff struct {
	PRURL, HeadSHA, State, Reason, BlockerMarker string
}

var pullLink = regexp.MustCompile(`https://github\.com/[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}/pull/[1-9][0-9]*`)

func missingPRMetadata(p PR) bool { return p.TaskURL == "" || p.GoalURL == "" }

func pendingReviewHandoff(c Config, j Job, s Snapshot, now time.Time) bool {
	for _, h := range reviewHandoffs(c, j, s, now) {
		if h.State == "needs_review" {
			return true
		}
	}
	return false
}

func reviewHandoffs(c Config, j Job, s Snapshot, now time.Time) []ReviewHandoff {
	if c.Role != "planner" || j.Kind != "plan" {
		return nil
	}
	var body string
	for _, e := range planningEvents(c, s, now) {
		if e.ID == j.TaskURL && e.Revision == j.InputRevision && e.URL == j.GoalURL && (sameActor(e.Author, c.Owner) || sameActor(e.Author, c.Implementer)) {
			body = e.Body
			break
		}
	}
	seen := map[string]bool{}
	var result []ReviewHandoff
	for _, ref := range pullLink.FindAllString(body, -1) {
		if seen[ref] || !validGitHubURL(ref) {
			continue
		}
		seen[ref] = true
		h := ReviewHandoff{PRURL: ref, State: "blocked", Reason: "PR unavailable in configured scope"}
		if p, ok := prFor(s, ref); ok {
			h.HeadSHA = p.HeadSHA
			switch {
			case !sameActor(p.Author, c.Implementer):
				h.Reason = "PR not authored by configured implementer"
			case p.Closed || p.Merged:
				h.State, h.Reason = "closed", "PR is already closed"
			case latestReview(c, p).State == "APPROVED" || latestReview(c, p).State == "CHANGES_REQUESTED":
				h.State, h.Reason = "reviewed", "verified current-head review"
			case missingPRMetadata(p):
				h.Reason = "PR missing task/goal metadata"
			case p.GoalURL != j.GoalURL:
				h.Reason = "PR belongs to a different goal"
			default:
				review := Job{Kind: "review", GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA}
				if allowed, reason := Admit(c, review, s, now); allowed {
					h.State, h.Reason = "needs_review", reason
				} else {
					h.Reason = reason
				}
			}
		}
		if h.State == "blocked" {
			h.BlockerMarker = encodeMetadata("handoff", struct {
				Job    string `json:"job"`
				PR     string `json:"pr"`
				Head   string `json:"head"`
				Reason string `json:"reason"`
			}{j.ID, ref, h.HeadSHA, h.Reason})
		}
		result = append(result, h)
	}
	return result
}
