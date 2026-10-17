package worker

import (
	"errors"
	"regexp"
	"strings"
)

type ownerApproval struct {
	BodySHA      string   `json:"body_sha256"`
	Repositories []string `json:"repositories"`
}

func validApprovalRepositories(c Config, repos []string) bool {
	if len(repos) == 0 || len(repos) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, repo := range repos {
		if !validRepo(repo) || seen[strings.ToLower(repo)] || !containsFold(c.CodeOwners, strings.Split(repo, "/")[0]) || (len(c.Repositories) > 0 && !containsFold(c.Repositories, repo)) {
			return false
		}
		seen[strings.ToLower(repo)] = true
	}
	return true
}

// GoalApprovalComment binds host approval to the exact goal body and repository scope.
// The host publishes it as the verified human owner, never through an agent token.
func GoalApprovalComment(c Config, g Goal, repos []string) (string, error) {
	if !g.Readable || !goalInScope(c, g) || g.BodySHA != BodyHash(g.Body) || !validApprovalRepositories(c, repos) || (len(g.Repositories) > 0 && !sameRepositories(g.Repositories, repos)) {
		return "", errors.New("approval requires the current readable goal and explicit in-scope repositories")
	}
	return "/coop approve\n\n" + encodeMetadata("approval", ownerApproval{g.BodySHA, repos}), nil
}

func approvalRecord(c Config, g Goal, body string) (ownerApproval, bool) {
	var a ownerApproval
	if metadata(body, "approval", &a) != nil || strings.TrimSpace(body) != "/coop approve\n\n"+encodeMetadata("approval", a) || a.BodySHA != BodyHash(g.Body) || !validApprovalRepositories(c, a.Repositories) {
		return a, false
	}
	return a, true
}

var clearApproval = regexp.MustCompile(`^i approve (this (goal|work|plan|scope)|continuation of this work)(, including (the )?merge of prs( for [a-z0-9_.-]+(/[a-z0-9_.-]+)?)?)?$`)

func ownerDecisionCommand(c Config, g Goal, body string) string {
	text := strings.TrimSpace(body)
	switch text {
	case "/coop approve", "/coop pause", "/coop resume", "/coop reject", "/coop complete":
		return text
	}
	if record, ok := approvalRecord(c, g, body); ok && sameRepositories(record.Repositories, g.Repositories) {
		return "/coop approve"
	}
	// Only a complete, unconditional statement is a decision. Quoted commands,
	// unknown mentions, questions and trailing qualifications never grant authority.
	if strings.ContainsAny(text, "\r\n") {
		return ""
	}
	tokens := strings.Fields(strings.ToLower(text))
	for len(tokens) > 0 && strings.HasPrefix(tokens[0], "@") {
		name := strings.TrimSuffix(strings.TrimPrefix(tokens[0], "@"), "[bot]")
		known := false
		for _, a := range []Actor{c.Implementer, c.Reviewer} {
			if strings.EqualFold(name, strings.TrimSuffix(a.Login, "[bot]")) {
				known = true
			}
		}
		if !known {
			return ""
		}
		tokens = tokens[1:]
		if len(tokens) > 0 && tokens[0] == "&" {
			tokens = tokens[1:]
		}
	}
	if len(tokens) > 0 && tokens[0] == "-" {
		tokens = tokens[1:]
	}
	text = strings.TrimSuffix(strings.Join(tokens, " "), ".")
	switch text {
	case "approved", "i approve", "this looks good to me", "looks good to me":
		return "/coop approve"
	}
	if clearApproval.MatchString(text) {
		if index := strings.LastIndex(text, " for "); index >= 0 {
			// A repository-qualified sentence is not authority for other
			// repositories in the goal. Partial goal approval is unsupported.
			if len(g.Repositories) != 1 {
				return ""
			}
			named := text[index+len(" for "):]
			_, repoName, _ := strings.Cut(strings.ToLower(g.Repositories[0]), "/")
			if !strings.EqualFold(named, g.Repositories[0]) && named != repoName {
				return ""
			}
		}
		return "/coop approve"
	}
	return ""
}
