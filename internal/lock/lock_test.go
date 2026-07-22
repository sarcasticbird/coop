package lock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireExcludesAndReleases(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rel1, err := Acquire("coop-x", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// second acquisition must time out while held
	if _, err := Acquire("coop-x", 300*time.Millisecond); err == nil {
		t.Fatal("concurrent acquisition succeeded")
	}
	// a different coop is unaffected
	rel2, err := Acquire("coop-y", time.Second)
	if err != nil {
		t.Fatalf("independent lock blocked: %v", err)
	}
	rel2()

	rel1()
	rel1() // idempotent
	rel3, err := Acquire("coop-x", time.Second)
	if err != nil {
		t.Fatalf("reacquire after release failed: %v", err)
	}
	rel3()
}

func TestAcquireContextStopsWaitingWhenCanceled(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	release, err := Acquire("coop-x", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cause := errors.New("entry interrupted")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	started := time.Now()
	if _, err := AcquireContext(ctx, "coop-x", 30*time.Second); !errors.Is(err, cause) {
		t.Fatalf("lock cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled lock acquisition took %v", elapsed)
	}
}
