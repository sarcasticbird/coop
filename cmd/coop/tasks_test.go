package main

import (
	"io"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

func TestTaskCommandsNeverStartVMForInvalidInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldRuntime, oldSession := newRuntime, runSession
	t.Cleanup(func() { newRuntime, runSession = oldRuntime, oldSession })
	newRuntime = func() runtime.Runtime { t.Fatal("task command constructed VM runtime"); return nil }
	runSession = func(*session.Session, string, []string, []string) error {
		t.Fatal("task command launched session")
		return nil
	}
	for _, args := range [][]string{{"task"}, {"task", "unknown"}, {"task", "create"}, {"task", "read"}, {"task", "list"}, {"task", "capabilities"}} {
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
			t.Fatal("missing safe rejection", args)
		}
	}
}
func TestTaskCommandRegistered(t *testing.T) {
	cmd := root()
	if command, _, e := cmd.Find([]string{"task", "read"}); e != nil || command.Name() != "read" {
		t.Fatal("missing task read", command, e)
	}
}

func TestTaskAdoptCommandRegistered(t *testing.T) {
	cmd := root()
	command, _, err := cmd.Find([]string{"task", "adopt"})
	if err != nil || command.Name() != "adopt" {
		t.Fatal("missing authenticated task adoption command", command.Name(), err)
	}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"task", "adopt"})
	if err = cmd.Execute(); err == nil {
		t.Fatal("adoption accepted missing explicit input")
	}
}
