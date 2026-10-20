package worker

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type slowDiscoveryCatalog struct{ *dispatchCatalog }

func (g slowDiscoveryCatalog) NativeSnapshot(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	// The live multi-goal catalog needs more than a minute. Model that work
	// without sleeping, and require a bounded deadline rather than no deadline.
	if !ok || remaining < 90*time.Second || remaining > 5*time.Minute {
		return NativeCatalog{}, fmt.Errorf("discovery cannot finish within %v", remaining)
	}
	return g.dispatchCatalog.NativeSnapshot(ctx, cfg, m)
}

func TestNativeDiscoveryHasBudgetForMultiGoalCatalog(t *testing.T) {
	cfg, master, catalog, code := dispatchFixture(t)
	d, err := NewNativeDispatcher(cfg, master, slowDiscoveryCatalog{catalog}, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.poll()
	select {
	case result := <-d.refresh:
		if result.err != nil || !result.catalog.Complete {
			t.Fatalf("discovery discarded the expanded catalog: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background discovery did not return")
	}
}
