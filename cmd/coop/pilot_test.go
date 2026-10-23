package main

import (
	"io"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

func TestPilotRoutesNeverLaunchAContainer(t *testing.T) {
	oldRuntime, oldSession := newRuntime, runSession
	t.Cleanup(func() { newRuntime, runSession = oldRuntime, oldSession })
	newRuntime = func() runtime.Runtime { t.Fatal("pilot constructed VM runtime"); return nil }
	runSession = func(*session.Session, string, []string, []string) error {
		t.Fatal("pilot launched session")
		return nil
	}
	for _, args := range [][]string{{"pilot"}, {"pilot", "unknown"}, {"pilot", "run"}, {"pilot", "status", "x"}, {"pilot", "mcp"}} {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		e := cmd.Execute()
		if len(args) == 1 {
			if e != nil {
				t.Fatal(e)
			}
		} else if e == nil {
			t.Fatal("expected safe rejection", args)
		}
	}
}
