package worker

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type ResolvedAssignment struct {
	SHA              string
	Author, Peer, QA Actor
}

func teamPlanner(c Config) Actor {
	if c.Team != nil {
		for _, m := range c.Team.Members {
			if m.Name == c.Team.Planner {
				return m.Actor
			}
		}
	}
	return Actor{}
}

func isPlanner(c Config) bool {
	if c.Version == 1 {
		return c.Role == "planner"
	}
	return c.Version == 2 && sameActor(c.Self, teamPlanner(c))
}

func teamTaskAuthor(c Config, t Task, actor Actor) bool {
	if c.Version == 1 {
		return sameActor(actor, c.Implementer)
	}
	if c.Team != nil && t.Assignment != nil {
		for _, m := range c.Team.Members {
			if m.Name == t.Assignment.Author {
				return sameActor(actor, m.Actor)
			}
		}
	}
	return false
}

func resolveTeamAssignment(c Config, t Task, g Goal, now time.Time) (ResolvedAssignment, error) {
	sha, err := ValidateTeamScope(c, t, g, now)
	if err != nil {
		return ResolvedAssignment{}, err
	}
	r := ResolvedAssignment{SHA: sha}
	for _, m := range c.Team.Members {
		if m.Name == t.Assignment.Author {
			r.Author = m.Actor
		}
		if m.Name == t.Assignment.Peer {
			r.Peer = m.Actor
		}
		if m.Name == t.Assignment.QA {
			r.QA = m.Actor
		}
	}
	return r, nil
}

type EngineeringClearance struct {
	ReviewID                              string
	ActorID                               int64
	Repo, PRURL, TaskURL, GoalURL         string
	HeadSHA, AssignmentSHA, ScopeRevision string
	SubmittedAt                           time.Time
}

// Native review identity alone is not enough: the assigned peer must be
// independent of every code author, including coauthors on later pages.
func teamIndependentPeer(c Config, t Task, p PR) (Actor, bool) {
	if c.Version != 2 || c.Team == nil || t.Assignment == nil || !p.ContributorsComplete || !p.ReviewsComplete || len(p.Contributors) == 0 || p.ScopeRevision == "" || p.ScopeUpdatedAt.IsZero() {
		return Actor{}, false
	}
	if p.BaseBranch != t.Assignment.BaseBranch || !referenceInRepo(p.URL, t.Repo, "pull") || p.TaskURL != t.URL || p.GoalURL != t.GoalURL || t.MetadataScopeSHA != t.BodySHA || t.BodySHA != TaskScopeHash(t.Title, t.Body) || t.Assignment.TaskBodySHA != t.BodySHA || (!sameActor(t.MetadataAuthor, c.Owner) && !sameActor(t.MetadataAuthor, teamPlanner(c))) {
		return Actor{}, false
	}
	sha, err := AssignmentSHA(*c.Team, t.Repo, t.URL, t.GoalURL, *t.Assignment, t.QA)
	if err != nil || sha != p.AssignmentSHA {
		return Actor{}, false
	}
	var author, peer Actor
	for _, m := range c.Team.Members {
		if m.Name == t.Assignment.Author {
			author = m.Actor
		}
		if m.Name == t.Assignment.Peer {
			peer = m.Actor
		}
	}
	if !sameActor(p.Author, author) || sameActor(peer, author) {
		return Actor{}, false
	}
	for _, a := range p.Contributors {
		if a.ID <= 0 || (a.Kind != "Bot" && a.Kind != "User") || sameActor(a, peer) {
			return Actor{}, false
		}
	}
	return peer, true
}

func teamLatestReview(c Config, t Task, p PR) Review {
	peer, ok := teamIndependentPeer(c, t, p)
	if !ok {
		return Review{}
	}
	var latest Review
	for _, r := range p.Reviews {
		if r.ID == "" || !sameActor(r.Author, peer) || r.HeadSHA != p.HeadSHA || !r.SubmittedAt.After(p.ScopeUpdatedAt) || (r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" && r.State != "DISMISSED") {
			continue
		}
		if r.SubmittedAt.After(latest.SubmittedAt) || r.SubmittedAt.Equal(latest.SubmittedAt) && reviewIDAfter(r.ID, latest.ID) {
			latest = r
		}
	}
	return latest
}

func reviewIDAfter(a, b string) bool {
	x, e1 := strconv.ParseUint(a, 10, 64)
	y, e2 := strconv.ParseUint(b, 10, 64)
	if e1 == nil && e2 == nil {
		return x > y
	}
	return a > b
}

func EngineeringReviewFor(c Config, t Task, p PR) (EngineeringClearance, bool) {
	r := teamLatestReview(c, t, p)
	if r.State != "APPROVED" {
		return EngineeringClearance{}, false
	}
	return EngineeringClearance{r.ID, r.Author.ID, t.Repo, p.URL, t.URL, t.GoalURL, p.HeadSHA, p.AssignmentSHA, p.ScopeRevision, r.SubmittedAt}, true
}

func V2JobKey(j Job, assignmentSHA string, executorID int64) string {
	role := ""
	if j.Binding != nil {
		role = j.Binding.Role
	}
	parts := []string{"v2", j.Kind, j.GoalURL, j.TaskURL, j.PRURL, j.HeadSHA, j.InputRevision, j.PRScopeRevision, assignmentSHA, strconv.FormatInt(executorID, 10), role}
	if j.QAArtifact != nil {
		raw, _ := json.Marshal(j.QAArtifact)
		parts = append(parts, string(raw))
	}
	return BodyHash(strings.Join(parts, "\x00"))
}

func bindTeamJob(c Config, j Job, s Snapshot, now time.Time) (Job, error) {
	if c.Team == nil {
		return j, errors.New("missing team policy")
	}
	var sha, role string
	var executor Actor
	if j.Kind == "plan" {
		policy, err := TeamPolicySHA(*c.Team)
		if err != nil {
			return j, err
		}
		sha, role, executor = policy, "planner", teamPlanner(c)
	} else {
		t, ok := taskFor(s, j.TaskURL)
		if !ok {
			return j, errors.New("missing assigned task")
		}
		g, ok := goalFor(s, j.GoalURL)
		if !ok {
			return j, errors.New("missing assigned goal")
		}
		a, err := resolveTeamAssignment(c, t, g, now)
		if err != nil {
			return j, err
		}
		sha, role, executor = a.SHA, "implementer", a.Author
		if j.Kind == "review" {
			role, executor = "engineering-review", a.Peer
		}
		if j.Kind == "qa" {
			if !t.QA.Required || a.QA.ID == 0 {
				return j, errors.New("missing required QA assignment")
			}
			role, executor = "customer-qa", a.QA
		}
		if j.PRURL != "" {
			p, ok := prFor(s, j.PRURL)
			if !ok {
				return j, errors.New("missing PR")
			}
			j.PRScopeRevision = p.ScopeRevision
		}
		j.ScopeSHA = t.BodySHA
	}
	j.Binding = &JobBinding{Version: 2, AssignmentSHA: sha, ExecutorID: executor.ID, Role: role}
	j.ID = V2JobKey(j, sha, executor.ID)
	return j, nil
}
