package worker

import (
	"errors"
	"strings"
)

// discussionIntakeAllows bounds new work independently of historical goal
// authority. Issue goals keep their existing scope and approval rules.
func discussionIntakeAllows(c Config, ref string) bool {
	if len(c.DiscussionURLs) == 0 || !strings.Contains(ref, "/discussions/") {
		return true
	}
	return containsFold(c.DiscussionURLs, ref)
}

func validateDiscussionIntake(c Config) error {
	if len(c.DiscussionURLs) > 64 {
		return errors.New("too many selected Discussions")
	}
	seen := map[string]bool{}
	for _, ref := range c.DiscussionURLs {
		repo, number, err := parseRef(ref)
		key := strings.ToLower(ref)
		if err != nil || number <= 0 || !referenceInRepo(ref, repo, "discussions") || strings.ContainsAny(ref, "?#") || !containsFold(c.DiscussionRepositories, repo) || seen[key] {
			return errors.New("invalid, duplicate or out-of-scope Discussion selection")
		}
		seen[key] = true
	}
	return nil
}

func selectedDiscussions(c Config, repo string) []string {
	var refs []string
	for _, ref := range c.DiscussionURLs {
		selectedRepo, _, err := parseRef(ref)
		if err == nil && strings.EqualFold(selectedRepo, repo) {
			refs = append(refs, ref)
		}
	}
	return refs
}
