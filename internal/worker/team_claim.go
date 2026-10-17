package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func trustedTeamClaimActor(c Config, a Actor) bool {
	if sameActor(a, c.Owner) {
		return true
	}
	if c.Team == nil {
		return false
	}
	_, ok := TeamMemberForActor(*c.Team, a)
	return ok
}

// A claim is a durable ownership assertion, not compare-and-swap. Deployment
// still requires exactly one live instance per actor; conflicts never transfer.
func (g *GitHubClient) claimTeam(ctx context.Context, j Job, instance string) (Claim, error) {
	s, err := g.Revalidate(ctx, j, g.snapshot)
	if err != nil {
		return Claim{}, err
	}
	if ok, _ := Admit(g.config, j, s, time.Now()); !ok {
		return Claim{}, ghError("claim_scope_changed")
	}
	if j.Kind != "implement" && j.Kind != "review" && j.Kind != "qa" {
		return Claim{}, ghError("invalid_claim_role")
	}
	ref := j.TaskURL
	if j.Kind == "review" || j.Kind == "qa" {
		ref = j.PRURL
	}
	repo, n, err := parseRef(ref)
	if err != nil {
		return Claim{}, err
	}
	comments, err := g.issueComments(ctx, repo, n)
	if err != nil {
		return Claim{}, err
	}
	var saved *Claim
	for _, c := range comments {
		if !trustedTeamClaimActor(g.config, c.Author) {
			continue
		}
		claim := claimFromComment(c)
		if claim == nil {
			if strings.Contains(c.Body, marker(j, "claim")) {
				return Claim{}, ghError("invalid_existing_claim")
			}
			continue
		}
		if (j.Kind == "review" || j.Kind == "qa") && claim.Marker != marker(j, "claim") {
			continue
		}
		if claim.Binding == nil || *claim.Binding != *j.Binding || !sameActor(claim.Author, g.config.Self) || claim.InstanceID != instance {
			return Claim{}, ghError("conflicting_claim")
		}
		saved = claim
	}
	if saved != nil {
		return *saved, nil
	}
	raw, _ := json.Marshal(struct {
		Instance string      `json:"instance"`
		Binding  *JobBinding `json:"binding"`
	}{instance, j.Binding})
	c, err := g.postMarked(ctx, j, "claim", "Picked up this approved task.\n<!-- coop-claim:v2 "+string(raw)+" -->")
	if err != nil {
		return Claim{}, err
	}
	claim := claimFromComment(c)
	if claim == nil || claim.Binding == nil || *claim.Binding != *j.Binding || claim.InstanceID != instance || !sameActor(claim.Author, g.config.Self) {
		return Claim{}, ghError("invalid_claim_response")
	}
	return *claim, nil
}
