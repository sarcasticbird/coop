package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestExclusiveGuardRejectsBeforeSideEffect(t *testing.T) {
	c := Controller{}
	blocked := errors.New("revoked")
	// The callback runs under the same lock used by normal Ensure/Down.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e := c.WithExclusive(context.Background(), Spec{RuntimeName: "coop-test-exclusive"}, func(_ Locked) error { return blocked })
	if !errors.Is(e, blocked) {
		t.Fatal("exclusive callback error lost")
	}
}
