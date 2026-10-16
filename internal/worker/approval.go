package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func stringID(n int) string       { return strconv.Itoa(n) }
func BodyHash(body string) string { h := sha256.Sum256([]byte(body)); return hex.EncodeToString(h[:]) }
func TaskScopeHash(title, body string) string {
	raw, _ := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{title, body})
	return BodyHash(string(raw))
}
func validGitHubURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(p) != 4 || !numericID.MatchString(p[3]) {
		return false
	}
	if p[0] == "orgs" {
		return slug.MatchString(p[1]) && p[2] == "discussions"
	}
	return validRepo(p[0]+"/"+p[1]) && (p[2] == "issues" || p[2] == "pull" || p[2] == "discussions")
}
func goalInScope(c Config, g Goal) bool {
	if !validGitHubURL(g.URL) {
		return false
	}
	for _, b := range c.BootstrapGoals {
		if b.URL == g.URL && b.OwnerID == c.Owner.ID && b.Source == "owner-confirmed" {
			return true
		}
	}
	u, _ := url.Parse(g.URL)
	p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if p[0] == "orgs" {
		for _, r := range c.DiscussionRepositories {
			if strings.EqualFold(strings.Split(r, "/")[0], p[1]) {
				return true
			}
		}
		return false
	}
	return containsFold(c.CodeOwners, p[0]) || containsFold(c.DiscussionRepositories, p[0]+"/"+p[1])
}
func effectiveTime(d Decision) time.Time {
	if d.EditedAt.After(d.CreatedAt) {
		return d.EditedAt
	}
	return d.CreatedAt
}
func sameRepositories(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	for i := range aa {
		aa[i] = strings.ToLower(aa[i])
	}
	for i := range bb {
		bb[i] = strings.ToLower(bb[i])
	}
	sort.Strings(aa)
	sort.Strings(bb)
	return slices.Equal(aa, bb)
}

func EvaluateGoal(c Config, g Goal, now time.Time) Approval {
	result := Approval{State: "unapproved", BodySHA: BodyHash(g.Body), Reason: "owner approval required"}
	if c.Version == 2 {
		m, err := ParseTeamGoalMetadata(g.Body)
		if err != nil {
			return Approval{State: "blocked", BodySHA: result.BodySHA, Reason: err.Error()}
		}
		if !sameRepositories(m.Repositories, g.Repositories) {
			return Approval{State: "blocked", BodySHA: result.BodySHA, Reason: "v2 goal repository scope mismatch"}
		}
	}
	if !g.Readable || !goalInScope(c, g) || g.BodySHA != result.BodySHA {
		return Approval{State: "blocked", Reason: "goal unreadable or outside verified scope"}
	}
	if len(g.Repositories) == 0 {
		return Approval{State: "blocked", BodySHA: result.BodySHA, Reason: "goal repository scope metadata required"}
	}
	lastEdit := g.LastEditedAt
	if lastEdit.IsZero() {
		lastEdit = g.CreatedAt
	}
	approved := false
	for _, b := range c.BootstrapGoals {
		if b.URL == g.URL && b.BodySHA == result.BodySHA && b.OwnerID == c.Owner.ID && b.Source == "owner-confirmed" && sameRepositories(b.Repositories, g.Repositories) {
			approved = true
			result.State = "approved"
			result.Reason = "trusted host approval"
		}
	}
	decisions := append([]Decision(nil), g.Decisions...)
	sort.SliceStable(decisions, func(i, j int) bool {
		ti, tj := effectiveTime(decisions[i]), effectiveTime(decisions[j])
		if ti.Equal(tj) {
			return decisions[i].ID < decisions[j].ID
		}
		return ti.Before(tj)
	})
	for _, d := range decisions {
		t := effectiveTime(d)
		if !sameActor(d.Author, c.Owner) || t.After(now) || t.Before(lastEdit) {
			continue
		}
		switch ownerDecisionCommand(c, g, d.Command) {
		case "/coop approve":
			approved = true
			result.State = "approved"
			result.Reason = "verified owner approval"
		case "/coop pause":
			result.State = "paused"
			result.Reason = "owner paused goal"
		case "/coop resume":
			if approved && result.State != "rejected" && result.State != "complete" {
				result.State = "approved"
				result.Reason = "owner resumed goal"
			}
		case "/coop reject":
			approved = false
			result.State = "rejected"
			result.Reason = "owner rejected goal"
		case "/coop complete":
			approved = false
			result.State = "complete"
			result.Reason = "owner completed goal"
		}
	}
	return result
}
