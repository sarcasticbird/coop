package main

import (
	"context"
	"github.com/sarcasticbird/coop/internal/runtime"
	"testing"
)

type recoveryRuntime struct {
	*runtime.Mock
	starts int
}

func (r *recoveryRuntime) Start(name string) error { r.starts++; return r.Mock.Start(name) }

func TestRecoveryInitializesInteractiveDiskAndLeavesTargetCold(t *testing.T) {
	rt := &recoveryRuntime{Mock: runtime.NewMock()}
	spec := runtime.RunSpec{Name: "coop-uat", Image: "test"}
	called := false
	err := prepareInteractiveTarget(context.Background(), rt, func(got runtime.RunSpec) error {
		called = true
		if got.Name != spec.Name {
			t.Fatal("wrong identity")
		}
		rt.Existing[got.Name] = true
		return nil
	}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !called || rt.starts != 1 || len(rt.Stopped) != 1 {
		t.Fatalf("target disk was not initialized: starts=%v stops=%v", rt.starts, rt.Stopped)
	}
}
