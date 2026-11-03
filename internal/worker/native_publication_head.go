package worker

import (
	"fmt"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// The push is acknowledged before GitHub's PR read necessarily reflects it.
// Only the exact previously assigned head is a retryable observation. A third
// head is a conflict, never permission to overwrite another author's work.
func validatePublishedHead(current, previous, published string) error {
	if !qaHeadSHA.MatchString(published) {
		return fmt.Errorf("missing acknowledged publication head: %w", c.ErrConflict)
	}
	if current == published {
		return nil
	}
	if qaHeadSHA.MatchString(previous) && current == previous {
		return fmt.Errorf("GitHub PR still reports pre-push head %s; awaiting acknowledged head %s: %w", previous, published, c.ErrUnavailable)
	}
	return fmt.Errorf("post-push PR head changed to %s, expected %s: %w", current, published, c.ErrConflict)
}
