package pilot

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"path/filepath"
	"testing"
)

type fixtureDeps struct {
	approved                                         bool
	claims, sends, birds, records                    int
	failSend, failBird                               bool
	failClaim, changeOnClaim, revokeOnClaim, changed bool
}

func (f *fixtureDeps) Authority(context.Context, c.GoalRef) (c.GoalAuthority, error) {
	return c.GoalAuthority{Valid: f.approved, ApprovalSequence: 1, Revision: c.GoalRevision{Proposal: c.GoalProposal{Scope: "kata-fixture-sha256:" + TaskDigest(Task{UID: "task-uid", Title: "Fixture audit", Body: "Only inspect public history"})}}, Actors: c.GoalActors{Author: c.GoalActor{PrincipalID: "fixture-bird"}}}, nil
}
func (f *fixtureDeps) ReadTask(context.Context, string) (Task, error) {
	body := "Only inspect public history"
	if f.changed {
		body = "Expanded task"
	}
	return Task{UID: "task-uid", Title: "Fixture audit", Body: body, Status: "open"}, nil
}
func (f *fixtureDeps) Claim(context.Context, string) error {
	f.claims++
	if f.failClaim {
		return c.ErrUnavailable
	}
	if f.changeOnClaim {
		f.changed = true
	}
	if f.revokeOnClaim {
		f.approved = false
	}
	return nil
}
func (f *fixtureDeps) Send(context.Context, c.GoalAuthority, Request) (c.MessageReceipt, error) {
	f.sends++
	if f.failSend {
		f.failSend = false
		return c.MessageReceipt{}, c.ErrUnavailable
	}
	return c.MessageReceipt{MessageID: "msg_fixture", Deliveries: []c.Delivery{{ID: "delivery_fixture", AgentID: "fixture-bird"}}}, nil
}
func (f *fixtureDeps) Execute(context.Context, c.MessageReceipt) (Evidence, error) {
	f.birds++
	if f.failBird {
		return Evidence{}, c.ErrUnavailable
	}
	return Evidence{SessionID: "opencode:fixture", SHA256: "fixture-evidence", ReplyID: "msg_reply", ExitCode: 7}, nil
}
func (f *fixtureDeps) Record(context.Context, Request, Evidence) error { f.records++; return nil }
func testPilot(t *testing.T, f *fixtureDeps) *Coordinator {
	t.Helper()
	return &Coordinator{StateDir: filepath.Join(t.TempDir(), "state"), ConfigSHA: "fixture-config", BirdID: "fixture-bird", Backend: f, Ledger: f, Bird: f}
}
func pilotRequest() Request {
	return Request{RequestID: "one", Task: "fixture#abcd", Goal: c.GoalRef{Source: "master", MasterID: "coop-maestro-pilot", GoalID: "goal_fixture", RevisionSHA256: "fixture-revision"}}
}
func TestPilotApprovalBeforeEffects(t *testing.T) {
	f := &fixtureDeps{}
	p := testPilot(t, f)
	_, e := p.Run(t.Context(), pilotRequest())
	if !errors.Is(e, c.ErrForbidden) || f.claims+f.sends+f.birds != 0 {
		t.Fatal("unapproved effect", e, f)
	}
}
func TestPilotReplayDoesNotDispatchTwice(t *testing.T) {
	f := &fixtureDeps{approved: true}
	p := testPilot(t, f)
	for range 2 {
		r, e := p.Run(t.Context(), pilotRequest())
		if e != nil || r.Phase != "completed" {
			t.Fatal(r, e)
		}
	}
	if f.claims != 1 || f.sends != 1 || f.birds != 1 || f.records != 1 {
		t.Fatal("duplicate effects", f)
	}
}
func TestPilotLostSendRetriesSameRequest(t *testing.T) {
	f := &fixtureDeps{approved: true, failSend: true}
	p := testPilot(t, f)
	if _, e := p.Run(t.Context(), pilotRequest()); e == nil {
		t.Fatal("missing failure")
	}
	r, e := p.Run(t.Context(), pilotRequest())
	if e != nil || r.Phase != "completed" || f.claims != 1 || f.birds != 1 {
		t.Fatal(r, e, f)
	}
}
func TestPilotUnknownBirdNeverResubmits(t *testing.T) {
	f := &fixtureDeps{approved: true, failBird: true}
	p := testPilot(t, f)
	_, _ = p.Run(t.Context(), pilotRequest())
	_, e := p.Run(t.Context(), pilotRequest())
	if !errors.Is(e, ErrHeld) || f.birds != 1 {
		t.Fatal("uncertain execution replayed", e, f.birds)
	}
}
func TestPilotChangedRequestCannotReuseReceipt(t *testing.T) {
	f := &fixtureDeps{approved: true}
	p := testPilot(t, f)
	r := pilotRequest()
	if _, e := p.Run(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	r.Task = "different#task"
	if _, e := p.Run(t.Context(), r); !errors.Is(e, c.ErrConflict) || f.birds != 1 {
		t.Fatal(e, f)
	}
}
func TestPilotRechecksTaskAndApprovalAfterClaim(t *testing.T) {
	for _, f := range []*fixtureDeps{{approved: true, changeOnClaim: true}, {approved: true, revokeOnClaim: true}} {
		p := testPilot(t, f)
		if _, e := p.Run(t.Context(), pilotRequest()); e == nil || f.claims != 1 || f.sends != 0 || f.birds != 0 {
			t.Fatal("changed authority dispatched", e, f)
		}
	}
}
func TestPilotUnknownClaimRequiresInspection(t *testing.T) {
	f := &fixtureDeps{approved: true, failClaim: true}
	p := testPilot(t, f)
	if _, e := p.Run(t.Context(), pilotRequest()); e == nil {
		t.Fatal("missing claim failure")
	}
	if _, e := p.Run(t.Context(), pilotRequest()); !errors.Is(e, ErrHeld) || f.claims != 1 || f.sends != 0 {
		t.Fatal("uncertain claim retried", e, f)
	}
}

type recoveredFixture struct {
	*fixtureDeps
	recoveries int
}

func (f *recoveredFixture) RecoverEvidence(context.Context, c.MessageReceipt) (Evidence, error) {
	f.recoveries++
	return Evidence{SessionID: "opencode:fixture", ReplyID: "reply", SHA256: "recovered"}, nil
}
func TestPilotCompletedNativeEvidenceRecoveryNeverResubmits(t *testing.T) {
	f := &recoveredFixture{fixtureDeps: &fixtureDeps{approved: true, failBird: true}}
	p := testPilot(t, f.fixtureDeps)
	p.Bird = f
	if _, e := p.Run(t.Context(), pilotRequest()); e == nil {
		t.Fatal("missing initial failure")
	}
	r, e := p.Run(t.Context(), pilotRequest())
	if e != nil || r.Phase != "completed" || f.recoveries != 1 || f.birds != 1 || f.sends != 1 || f.claims != 1 || f.records != 1 {
		t.Fatal(r, e, f)
	}
}
func TestPilotRecoveryRechecksApproval(t *testing.T) {
	f := &recoveredFixture{fixtureDeps: &fixtureDeps{approved: true, failBird: true}}
	p := testPilot(t, f.fixtureDeps)
	p.Bird = f
	_, _ = p.Run(t.Context(), pilotRequest())
	f.approved = false
	if _, e := p.Run(t.Context(), pilotRequest()); !errors.Is(e, c.ErrForbidden) || f.recoveries != 0 {
		t.Fatal("revoked recovery", e, f)
	}
}
