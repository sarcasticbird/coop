package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type TeamMember struct {
	Name         string   `json:"name"`
	Actor        Actor    `json:"actor"`
	Capabilities []string `json:"capabilities"`
}

type TeamPolicy struct {
	Version          int               `json:"version"`
	Members          []TeamMember      `json:"members"`
	Planner          string            `json:"planner"`
	EngineeringPeers map[string]string `json:"engineering_peers"`
}

type TaskAssignment struct {
	BaseBranch  string `json:"base_branch"`
	Version     int    `json:"version"`
	Author      string `json:"author"`
	Peer        string `json:"peer"`
	QA          string `json:"qa,omitempty"`
	GoalBodySHA string `json:"goal_body_sha256"`
	TaskBodySHA string `json:"task_body_sha256"`
	PolicySHA   string `json:"policy_sha256"`
}

type QAPolicy struct {
	Required            bool   `json:"required"`
	Reviewer            string `json:"reviewer,omitempty"`
	AcceptanceSHA       string `json:"acceptance_sha256,omitempty"`
	Environment         string `json:"environment,omitempty"`
	EnvironmentRevision string `json:"environment_revision,omitempty"`
}

func ValidateTeamPolicy(p TeamPolicy) error {
	if p.Version != 2 || len(p.Members) == 0 || len(p.Members) > 64 {
		return errors.New("invalid team policy version or members")
	}
	members := make(map[string]TeamMember, len(p.Members))
	ids := make(map[int64]bool, len(p.Members))
	for _, m := range p.Members {
		if !slug.MatchString(m.Name) || m.Name != strings.ToLower(m.Name) || !validActor(m.Actor, "Bot") {
			return errors.New("invalid team member name or bot actor")
		}
		if _, exists := members[m.Name]; exists || ids[m.Actor.ID] {
			return errors.New("duplicate team member name or actor ID")
		}
		seen := map[string]bool{}
		for _, capability := range m.Capabilities {
			switch capability {
			case "plan", "implement", "engineering-review", "customer-qa":
			default:
				return fmt.Errorf("unknown team capability %q", capability)
			}
			if seen[capability] {
				return fmt.Errorf("duplicate team capability %q", capability)
			}
			seen[capability] = true
		}
		members[m.Name] = m
		ids[m.Actor.ID] = true
	}
	if !slices.Contains(members[p.Planner].Capabilities, "plan") {
		return errors.New("team planner missing or unqualified")
	}
	for source, target := range p.EngineeringPeers {
		if !slices.Contains(members[source].Capabilities, "implement") || !slices.Contains(members[target].Capabilities, "engineering-review") || source == target {
			return errors.New("invalid engineering peer source or target")
		}
	}
	for _, m := range p.Members {
		if slices.Contains(m.Capabilities, "implement") && p.EngineeringPeers[m.Name] == "" {
			return fmt.Errorf("missing engineering peer for %s", m.Name)
		}
	}
	return nil
}

// TeamMemberForActor uses immutable identity, not the mutable GitHub login.
func TeamMemberForActor(p TeamPolicy, actor Actor) (TeamMember, bool) {
	for _, m := range p.Members {
		if sameActor(m.Actor, actor) {
			return m, true
		}
	}
	return TeamMember{}, false
}

// validateTaskAssignmentStructure does not infer QA requirements before the
// approved goal is available. A named QA actor must still be independent.
func validateTaskAssignmentStructure(p TeamPolicy, a TaskAssignment) error {
	policySHA, err := TeamPolicySHA(p)
	if err != nil {
		return err
	}
	if a.Version != 2 || !validBaseBranch(a.BaseBranch) || !hexSHA.MatchString(a.GoalBodySHA) || !hexSHA.MatchString(a.TaskBodySHA) || !hexSHA.MatchString(a.PolicySHA) || a.PolicySHA != policySHA {
		return errors.New("invalid assignment version or scope hashes")
	}
	if a.Author == "" || a.Peer == "" || p.EngineeringPeers[a.Author] != a.Peer {
		return errors.New("assignment author or peer does not match team policy")
	}
	if a.QA == "" {
		return nil
	}
	qualified := false
	for _, m := range p.Members {
		if m.Name == a.QA && slices.Contains(m.Capabilities, "customer-qa") {
			qualified = true
		}
	}
	if !qualified || a.QA == a.Author || a.QA == a.Peer {
		return errors.New("QA reviewer missing, unqualified or not independent")
	}
	return nil
}

func validateQAPolicy(q QAPolicy) error {
	if !q.Required {
		if q != (QAPolicy{}) {
			return errors.New("nonrequired QA must have no extra fields")
		}
		return nil
	}
	if !slug.MatchString(q.Reviewer) || q.Reviewer != strings.ToLower(q.Reviewer) || !hexSHA.MatchString(q.AcceptanceSHA) {
		return errors.New("invalid QA acceptance hash or environment")
	}
	for _, s := range []string{q.Environment, q.EnvironmentRevision} {
		if s == "" || strings.TrimSpace(s) != s || len(s) > 2<<10 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
			return errors.New("invalid QA environment or revision")
		}
	}
	return nil
}

func ValidateTaskAssignment(p TeamPolicy, a TaskAssignment, q QAPolicy) error {
	if err := validateTaskAssignmentStructure(p, a); err != nil {
		return err
	}
	if err := validateQAPolicy(q); err != nil {
		return err
	}
	if a.QA != q.Reviewer {
		return errors.New("assignment QA does not match goal policy")
	}
	return nil
}

// TeamPolicySHA sorts set-like fields on copies and excludes mutable logins.
// encoding/json sorts map keys, making peer mappings deterministic as well.
func TeamPolicySHA(p TeamPolicy) (string, error) {
	if err := ValidateTeamPolicy(p); err != nil {
		return "", err
	}
	type identity struct {
		ID   int64  `json:"id"`
		Kind string `json:"kind"`
	}
	type member struct {
		Name         string   `json:"name"`
		Actor        identity `json:"actor"`
		Capabilities []string `json:"capabilities"`
	}
	canonical := struct {
		Version          int               `json:"version"`
		Members          []member          `json:"members"`
		Planner          string            `json:"planner"`
		EngineeringPeers map[string]string `json:"engineering_peers"`
	}{Version: p.Version, Planner: p.Planner, EngineeringPeers: p.EngineeringPeers}
	for _, m := range p.Members {
		capabilities := append([]string{}, m.Capabilities...)
		slices.Sort(capabilities)
		canonical.Members = append(canonical.Members, member{m.Name, identity{m.Actor.ID, m.Actor.Kind}, capabilities})
	}
	slices.SortFunc(canonical.Members, func(a, b member) int { return strings.Compare(a.Name, b.Name) })
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return BodyHash(string(raw)), nil
}

// AssignmentSHA needs the policy to resolve stable member names to immutable
// actor IDs. Hashing names alone would not bind an assignment to its actors.
func AssignmentSHA(p TeamPolicy, repo, taskURL, goalURL string, a TaskAssignment, q QAPolicy) (string, error) {
	if err := ValidateTaskAssignment(p, a, q); err != nil {
		return "", err
	}
	repo, taskURL, goalURL = strings.ToLower(repo), strings.ToLower(taskURL), strings.ToLower(goalURL)
	if !validRepo(repo) || !referenceInRepo(taskURL, repo, "issues") || !validTeamGoalURL(goalURL) {
		return "", errors.New("invalid assignment repository or references")
	}
	var authorID, peerID, qaID int64
	for _, m := range p.Members {
		if m.Name == a.Author {
			authorID = m.Actor.ID
		}
		if m.Name == a.Peer {
			peerID = m.Actor.ID
		}
		if m.Name == a.QA {
			qaID = m.Actor.ID
		}
	}
	raw, err := json.Marshal(struct {
		Repo       string         `json:"repo"`
		Task       string         `json:"task"`
		Goal       string         `json:"goal"`
		Assignment TaskAssignment `json:"assignment"`
		AuthorID   int64          `json:"author_id"`
		PeerID     int64          `json:"peer_id"`
		QAID       int64          `json:"qa_id"`
		QA         QAPolicy       `json:"qa"`
	}{repo, taskURL, goalURL, a, authorID, peerID, qaID, q})
	if err != nil {
		return "", err
	}
	return BodyHash(string(raw)), nil
}
