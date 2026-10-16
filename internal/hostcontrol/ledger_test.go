package hostcontrol

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testKey(now time.Time) RequestKey {
	return RequestKey{ID: fmt.Sprintf("r1.%013d.%s", now.UnixMilli(), strings.Repeat("a", 32)), CreatedAt: time.UnixMilli(now.UnixMilli()).UTC()}
}
func testLedger(t *testing.T, limits Limits) *Ledger {
	t.Helper()
	limits.defaults()
	l, e := OpenLedger(privateTemp(t), Host{ID: "h1_" + strings.Repeat("b", 32), Label: "test"}, limits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}
func TestLedgerDurableIdempotencyAndTimestamp(t *testing.T) {
	l := testLedger(t, Limits{})
	now := time.Now().UTC()
	in := Mutation{Key: testKey(now), Workspace: "w1.test", Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	caller := Caller{ID: "chief"}
	first, dup, e := l.Accept(caller, in, "fp", "vm", now)
	if e != nil || dup {
		t.Fatal(e)
	}
	again, dup, e := l.Accept(caller, in, "new-fp", "vm2", now.Add(time.Minute))
	if e != nil || !dup || again.ID != first.ID {
		t.Fatal("retry did not resolve original operation")
	}
	changed := in
	changed.Argv = []string{"false"}
	if _, _, e = l.Accept(caller, changed, "fp", "vm", now); e == nil {
		t.Fatal("different args accepted under same request")
	}
	changed = in
	changed.Key.CreatedAt = changed.Key.CreatedAt.Add(time.Hour)
	if _, _, e = l.Accept(caller, changed, "fp", "vm", now); e == nil {
		t.Fatal("old ID accepted under new creation time")
	}
	reopened, e := OpenLedger(l.root, l.host, l.limits)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = reopened.Close() }()
	again, dup, e = reopened.Accept(caller, in, "fp", "vm", now)
	if e != nil || !dup || again.ID != first.ID {
		t.Fatal("reopen lost idempotency")
	}
	if _, _, e = l.Accept(caller, in, "fp", "vm", now.Add(8*24*time.Hour)); e == nil {
		t.Fatal("expired request accepted")
	}
	if _, e = l.Lookup(Caller{ID: "observer"}, first.ID); e == nil {
		t.Fatal("other caller obtained operation")
	}
}
func TestLedgerRevisionAndCapacity(t *testing.T) {
	l := testLedger(t, Limits{Unresolved: 1, Records: 2, Terminal: 1})
	now := time.Now().UTC()
	in := Mutation{Key: testKey(now), Workspace: "w1.test", Action: "exec"}
	r, _, e := l.Accept(Caller{ID: "chief"}, in, "fp", "vm", now)
	if e != nil {
		t.Fatal(e)
	}
	next := in
	next.Key = testKey(now.Add(time.Millisecond))
	if _, _, e = l.Accept(Caller{ID: "chief"}, next, "fp", "vm", now); e == nil {
		t.Fatal("unresolved capacity exceeded")
	}
	stale := r
	r.State = "running"
	if e = l.Update(r); e != nil {
		t.Fatal(e)
	}
	stale.State = "cancelled"
	if e = l.Update(stale); e == nil {
		t.Fatal("stale revision overwrote state")
	}
	if _, dup, e := l.Accept(Caller{ID: "chief"}, in, "fp", "vm", now); e != nil || !dup {
		t.Fatal("duplicate rejected at capacity")
	}
}
func TestOutputPagingAndTruncation(t *testing.T) {
	l := testLedger(t, Limits{OutputBytes: 8})
	now := time.Now().UTC()
	r, _, e := l.Accept(Caller{ID: "chief"}, Mutation{Key: testKey(now), Workspace: "w1.test", Action: "exec"}, "fp", "vm", now)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.SaveOutput(r.ID, "stdout", []byte("A😀BC"), 7); e != nil {
		t.Fatal(e)
	}
	page, e := l.Page(Caller{ID: "chief"}, r.ID, "stdout", 0, 4)
	if e != nil || page.Data != "A" || page.NextOffset != 1 {
		t.Fatalf("UTF8 page boundary: %+v %v", page, e)
	}
	if _, e = l.Page(Caller{ID: "chief"}, r.ID, "stdout", 2, 4); e == nil {
		t.Fatal("middle-codepoint offset accepted")
	}
	if e = l.SaveOutput(r.ID, "stderr", bytes.Repeat([]byte{0xff}, 12), 12); e != nil {
		t.Fatal(e)
	}
	page, e = l.Page(Caller{ID: "chief"}, r.ID, "stderr", 0, 8)
	if e != nil || page.Encoding != "base64" || page.DroppedBytes != 4 || !page.Truncated {
		t.Fatalf("binary/truncation: %+v %v", page, e)
	}
	if _, e = l.Page(Caller{ID: "observer"}, r.ID, "stderr", 0, 8); e == nil {
		t.Fatal("cross-caller output exposed")
	}
}

func TestLedgerPrunedOutputRetainsRetryEvidence(t *testing.T) {
	l := testLedger(t, Limits{Terminal: 1})
	now := time.Now().UTC()
	caller := Caller{ID: "chief"}
	in := Mutation{Key: testKey(now), Workspace: "w1.test", Action: "exec", Argv: []string{"sensitive"}}
	first, _, e := l.Accept(caller, in, "fp", "vm", now)
	if e != nil {
		t.Fatal(e)
	}
	first.State = "succeeded"
	if e = l.Update(first); e != nil {
		t.Fatal(e)
	}
	next := in
	next.Key = testKey(now.Add(time.Millisecond))
	second, _, e := l.Accept(caller, next, "fp", "vm", now)
	if e != nil {
		t.Fatal(e)
	}
	second.State = "succeeded"
	if e = l.Update(second); e != nil {
		t.Fatal(e)
	}
	if e = l.Prune(now); e != nil {
		t.Fatal(e)
	}
	again, dup, e := l.Accept(caller, in, "fp", "vm", now)
	if e != nil || !dup || again.ID != first.ID {
		t.Fatal("output pruning destroyed idempotency")
	}
	if _, e = l.Page(caller, first.ID, "stdout", 0, 64); e == nil {
		t.Fatal("pruned output looked like empty output")
	}
}
func TestLedgerFailedPersistenceDoesNotAccept(t *testing.T) {
	l := testLedger(t, Limits{})
	if e := l.dir.Close(); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if _, _, e := l.Accept(Caller{ID: "chief"}, Mutation{Key: testKey(now), Action: "exec"}, "fp", "vm", now); e == nil || len(l.records) != 0 {
		t.Fatal("failed persistence admitted an operation")
	}
}
