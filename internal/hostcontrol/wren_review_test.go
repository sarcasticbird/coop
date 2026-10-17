package hostcontrol

import (
	"context"
	"github.com/sarcasticbird/coop/internal/workspace"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestKeysAtSameTimestampAreUnique(t *testing.T) {
	now := time.Now()
	first := testKey(now)
	second := testKey(now)
	if first.ID == second.ID {
		t.Fatal("independent requests at the same millisecond collide")
	}
}
func TestLifecycleCachedIdleWorkerCannotProveQuiescence(t *testing.T) {
	e, c, b := fixtureWorkerEngine(t)
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(c, a.OperationID)
	e.execute(context.Background(), r)
	r, _ = e.ledger.Lookup(c, r.ID)
	if b.stops.Load() != 0 || r.State != "failed" || r.Outcome.Category != "activity_unknown" {
		t.Fatalf("cached idle authorized stop: stops=%d state=%s category=%s", b.stops.Load(), r.State, r.Outcome.Category)
	}
}
func TestSaturatedUnknownExecCanBeStoppedWithoutReplay(t *testing.T) {
	e, c, b := fixtureEngine(t)
	e.ledger.limits = Limits{Unresolved: 1, Records: 4, Terminal: 1, OutputBytes: 8}
	b.broken = true
	now := time.Now()
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(c, a.OperationID)
	e.execute(context.Background(), r)
	r, _ = e.ledger.Lookup(c, r.ID)
	if r.State != "indeterminate" {
		t.Fatal("fixture did not create uncertain exec")
	}
	if _, err = e.CancelRequest(context.Background(), c, r.ID, testKey(now.Add(time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	in := Mutation{Key: testKey(now.Add(2 * time.Millisecond)), Workspace: r.Input.Workspace, Action: "stop", InterruptActive: true}
	stop, err := e.Submit(context.Background(), c, in)
	if err != nil {
		t.Fatalf("uncertain execution saturation prevented recovery: %v", err)
	}
	sr, _ := e.ledger.Lookup(c, stop.OperationID)
	e.execute(context.Background(), sr)
	sr, _ = e.ledger.Lookup(c, sr.ID)
	r, _ = e.ledger.Lookup(c, r.ID)
	if sr.State != "succeeded" || !r.HoldResolved || !r.Outcome.TerminationProven || b.calls.Load() != 1 {
		t.Fatal("recovery replayed command or failed to resolve execution hold")
	}
	again, err := e.Submit(context.Background(), c, in)
	if err != nil || again.OperationID != stop.OperationID {
		t.Fatal("recovery lost retry identity")
	}
	if len(e.ledger.all()) > 4 {
		t.Fatal("recovery exceeded total record limit")
	}
}
func TestScopedInventoryIgnoresMalformedUngrantableRecords(t *testing.T) {
	e, _, _ := fixtureEngine(t)
	root := privateTemp(t)
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.Mkdir(filepath.Join(root, "coop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coop/coop.toml"), []byte("[profiles.robin]\ncommand=['true']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := e.control.store.(workspace.FileStore)
	allow := func(selector string) bool { return selector == "agent:robin" }
	for _, mode := range []string{"agent", "interactive"} {
		dir := filepath.Join(store.Root, mode)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hawk.json"), []byte("invalid record"), 0600); err != nil {
			t.Fatal(err)
		}
		rows, err := e.control.List(context.Background(), allow)
		if err != nil || len(rows) != 1 || rows[0].Name != "robin" {
			t.Fatalf("malformed ungranted %s record broke authorized inventory: %v", mode, err)
		}
	}
	if _, err := e.control.List(context.Background(), func(string) bool { return true }); err == nil {
		t.Fatal("authorized malformed record did not fail closed")
	}
}
func TestReceiptUsesExternalExecutableAndHandlesBuiltinNames(t *testing.T) {
	e, c, _ := fixtureEngine(t)
	bin := privateTemp(t)
	for _, name := range []string{"printf", "-c", "NAME=value"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf 'external:%s' \"$1\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for _, test := range []struct {
		argv   []string
		state  string
		code   int
		output string
	}{{[]string{"printf", "$HOME; two words"}, "succeeded", 0, "external:$HOME; two words"}, {[]string{"-c", "literal"}, "succeeded", 0, "external:literal"}, {[]string{"NAME=value", "literal"}, "succeeded", 0, "external:literal"}, {[]string{"exit", "0"}, "failed", 127, ""}} {
		a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: test.argv, Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		r, _ := e.ledger.Lookup(c, a.OperationID)
		e.execute(context.Background(), r)
		op, err := e.Status(context.Background(), c, a.OperationID, 0, 0, 65536)
		if err != nil || op.State != test.state || op.ExitCode == nil || *op.ExitCode != test.code || op.Stdout.Data != test.output {
			t.Fatalf("argv %v: %+v %v", test.argv, op, err)
		}
	}
}

func TestRecoveryAdmissionCannotBypassOrdinaryQuotaOrTargetChecks(t *testing.T) {
	l := testLedger(t, Limits{Unresolved: 1, Records: 8})
	c := Caller{ID: "chief"}
	now := time.Now()
	target, _, err := l.Accept(c, Mutation{Key: testKey(now), Workspace: "fixture", Action: "exec"}, "fp", "vm", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = l.Accept(c, Mutation{Key: testKey(now), Workspace: "other", Action: "start"}, "fp", "vm", now); err == nil {
		t.Fatal("ordinary work bypassed saturated quota")
	}
	for _, tc := range []struct {
		action, workspace, incarnation string
		interrupt                      bool
	}{{"start", "fixture", "vm", true}, {"stop", "fixture", "vm", false}, {"stop", "other", "vm", true}, {"stop", "fixture", "replacement", true}} {
		if _, _, err = l.AcceptRecovery(c, Mutation{Key: testKey(now), Workspace: tc.workspace, Action: tc.action, InterruptActive: tc.interrupt}, "fp", tc.incarnation, now, "", target.ID); err == nil {
			t.Fatalf("invalid recovery accepted: %+v", tc)
		}
	}
}
func TestResolvedUnknownExecReleasesWorkQuotaButKeepsEvidence(t *testing.T) {
	l := testLedger(t, Limits{Unresolved: 1, Records: 8})
	c := Caller{ID: "chief"}
	now := time.Now()
	r, _, err := l.Accept(c, Mutation{Key: testKey(now), Workspace: "fixture", Action: "exec"}, "fp", "vm", now)
	if err != nil {
		t.Fatal(err)
	}
	r.State = "indeterminate"
	r.HoldResolved = true
	r.Outcome.TerminationProven = true
	if err = l.Update(r); err != nil {
		t.Fatal(err)
	}
	if _, _, err = l.Accept(c, Mutation{Key: testKey(now), Workspace: "fixture", Action: "exec"}, "fp", "vm", now); err != nil {
		t.Fatalf("resolved execution still consumed active quota: %v", err)
	}
	if saved, err := l.Lookup(c, r.ID); err != nil || saved.State != "indeterminate" {
		t.Fatal("resolution discarded or rewrote unknown outcome")
	}
}

func TestCancelledRecoveryPreservesOriginalCancellationReservation(t *testing.T) {
	e, c, b := fixtureEngine(t)
	e.ledger.limits = Limits{Unresolved: 1, Records: 4, Terminal: 1, OutputBytes: 8}
	b.broken = true
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(c, a.OperationID)
	e.execute(context.Background(), r)
	resolver, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: r.Input.Workspace, Action: "stop", InterruptActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.CancelRequest(context.Background(), c, resolver.OperationID, testKey(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err = e.CancelRequest(context.Background(), c, a.OperationID, testKey(time.Now())); err != nil {
		t.Fatalf("cancelled resolver consumed original cancellation reservation: %v", err)
	}
	saved, _ := e.ledger.Lookup(c, a.OperationID)
	if !saved.CancelIntent || saved.State != "indeterminate" || len(e.ledger.all()) > 4 || b.calls.Load() != 1 || b.stops.Load() != 0 {
		t.Fatal("cancellation changed uncertain outcome or exceeded bounds")
	}
}
