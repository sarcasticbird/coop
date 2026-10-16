package hostcontrol

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func expiredOutput(t *testing.T, l *Ledger, c Caller) OperationRecord {
	t.Helper()
	past := time.Now().Add(-retryWindow - time.Hour)
	r, _, err := l.Accept(c, Mutation{Key: testKey(past), Workspace: "retention-fixture", Action: "exec"}, "fp", "vm", past)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SaveOutput(r.ID, "stdout", []byte("private output"), 14); err != nil {
		t.Fatal(err)
	}
	r, err = l.Lookup(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.State = "succeeded"
	r.CompletedAt = past
	if err = l.Update(r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestRetentionPrunesOnStartup(t *testing.T) {
	l := testLedger(t, Limits{})
	c := Caller{ID: "chief"}
	old := expiredOutput(t, l, c)
	reopened, err := OpenLedger(l.root, l.host, l.limits)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err = reopened.Lookup(c, old.ID); err == nil {
		t.Fatal("expired terminal record retained on startup")
	}
	if _, err = os.Stat(filepath.Join(l.root, old.ID+".stdout")); !os.IsNotExist(err) {
		t.Fatalf("expired output retained on startup: %v", err)
	}
}
func TestRetentionPrunesWhileEngineIdle(t *testing.T) {
	e, c, _ := fixtureEngine(t)
	e.retentionInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	old := expiredOutput(t, e.ledger, c)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := e.ledger.Lookup(c, old.ID); err != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("idle engine retained expired terminal output")
}
