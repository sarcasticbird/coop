package worker

import (
	"fmt"
	"strings"
	"time"
)

// planningEvents adds a bounded workflow handoff for verified completed delivery.
// This is task evidence, not a peer message or new goal approval. Stable event
// identity lets the ordinary job ledger suppress repeat model turns across polls.
func planningEvents(c Config, s Snapshot, now time.Time) []Event {
	var events []Event
	for _, event := range s.Events {
		if event.Kind != "delivery_completed" {
			events = append(events, event)
		}
	}
	if !isPlanner(c) || !s.Complete {
		return events
	}
	for _, p := range s.PRs {
		if !p.Merged || !configuredImplementer(c, p.Author) || p.HeadSHA == "" {
			continue
		}
		t, ok := taskFor(s, p.TaskURL)
		if !ok || !t.Completed || t.GoalURL != p.GoalURL || !referenceInRepo(p.URL, t.Repo, "pull") {
			continue
		}
		g, ok := goalFor(s, p.GoalURL)
		if !ok || EvaluateGoal(c, g, now).State != "approved" {
			continue
		}
		check := t
		check.Completed = false
		check.Closed = false
		if !taskAdmitted(c, check, g, s) {
			continue
		}
		if c.Version == 2 {
			a, err := resolveTeamAssignment(c, check, g, now)
			if err != nil || p.AssignmentSHA != a.SHA || !sameActor(p.Author, a.Author) {
				continue
			}
		}
		revision := BodyHash(p.URL + ":" + p.TaskURL + ":" + p.GoalURL + ":" + p.HeadSHA + ":" + t.BodySHA + ":" + g.BodySHA)
		if c.Version == 2 {
			revision = BodyHash(revision + ":" + p.AssignmentSHA)
		}
		events = append(events, Event{ID: "delivery:" + revision, Revision: revision, URL: g.URL, Kind: "delivery_completed", Author: p.Author, OccurredAt: now,
			Body: fmt.Sprintf("Verified completed delivery: task %s, merged PR %s, head %s. Reconcile its Project item as Done and advance dependent tasks that serve this approved goal when their actual dependencies and acceptance conditions permit. Preserve scope and dependencies. Do not propose a new goal or request repeated owner approval. Report the concrete next queue state in this goal thread.", t.URL, p.URL, p.HeadSHA)})
	}
	return events
}

func deliveryPlanProgress(c Config, j Job, s Snapshot) bool {
	if !strings.HasPrefix(j.TaskURL, "delivery:") {
		return true
	}
	if allowed, _ := Admit(c, j, s, time.Now()); !allowed {
		return false
	}
	p, ok := prFor(s, j.PRURL)
	if !ok {
		return false
	}
	t, ok := taskFor(s, p.TaskURL)
	if !ok || !t.Completed {
		return false
	}
	if t.ProjectID != "" && (t.DoneID == "" || t.CurrentStatusID != t.DoneID) {
		return false
	}
	g, ok := goalFor(s, j.GoalURL)
	if !ok {
		return false
	}
	for _, dependent := range s.Tasks {
		if dependent.Completed || dependent.Closed || dependent.GoalURL != g.URL || !containsFold(dependent.DependsOn, t.URL) || !taskAdmitted(c, dependent, g, s) {
			continue
		}
		if dependent.Ready {
			continue
		}
		inFlight := dependent.Claim != nil && teamTaskAuthor(c, dependent, dependent.Claim.Author)
		for _, pr := range s.PRs {
			if pr.TaskURL == dependent.URL && pr.GoalURL == g.URL && teamTaskAuthor(c, dependent, pr.Author) && (!pr.Closed || pr.Merged) {
				if c.Version == 2 {
					a, err := resolveTeamAssignment(c, dependent, g, time.Now())
					if err != nil || pr.AssignmentSHA != a.SHA || !referenceInRepo(pr.URL, dependent.Repo, "pull") {
						continue
					}
				}
				inFlight = true
			}
		}
		if !inFlight {
			return false
		}
	}
	return true
}
