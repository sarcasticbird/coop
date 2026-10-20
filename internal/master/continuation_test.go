package master

import (
	"errors"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func continuationFixture(t *testing.T) (*Store, c.Principal, c.Principal, c.Thread, c.SendMessage) {
	t.Helper()
	s, o, b, th := storeFixture(t)
	src, e := s.Send(t.Context(), o, c.SendMessage{ThreadID: th.ID, Body: "Review findings are now available", RequestID: "source"})
	if e != nil {
		t.Fatal(e)
	}
	in := c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "Continue approved work", RequestID: "continue", Continuation: &c.TaskContinuation{SourceMessageID: src.MessageID, Target: c.TaskContinuationTarget{JobID: strings.Repeat("a", 64), TaskURL: "https://github.com/code/repo/issues/1", SessionID: "ses_saved", MessageID: "msg_saved", InstanceID: "instance", Generation: 1}}}
	return s, o, b, th, in
}
func TestContinuationAuthorizationIdempotencyAndRevocation(t *testing.T) {
	s, o, b, _, in := continuationFixture(t)
	if _, e := s.Send(t.Context(), b, in); !errors.Is(e, c.ErrForbidden) {
		t.Fatalf("ordinary bird gained task control: %v", e)
	}
	b.Capabilities = append(b.Capabilities, "task_continue")
	if e := s.Configure(t.Context(), []c.Principal{b}, nil); e != nil {
		t.Fatal(e)
	}
	in.Recipients = []string{"agent-two"}
	in.Continuation.Target.InstanceID = "instance-two"
	a, e := s.Send(t.Context(), b, in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Send(t.Context(), b, in)
	if e != nil || again.MessageID != a.MessageID {
		t.Fatal("duplicate control", e)
	}
	receiver, e := s.Principal(t.Context(), "agent-two")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Continuation(t.Context(), receiver, a.MessageID); e != nil {
		t.Fatal(e)
	}
	b.Capabilities = []string{"read", "send"}
	if e = s.Configure(t.Context(), []c.Principal{b}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Continuation(t.Context(), receiver, a.MessageID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked coordinator retained admission", e)
	}
	if _, e = s.Send(t.Context(), o, in); e != nil {
		t.Fatal("owner continuation refused", e)
	}
}
func TestContinuationRefusesInvalidTargetAndSource(t *testing.T) {
	for _, kind := range []string{"placement", "source", "recipient", "task", "identity"} {
		t.Run(kind, func(t *testing.T) {
			s, o, _, _, in := continuationFixture(t)
			switch kind {
			case "placement":
				in.Continuation.Target.Generation++
			case "source":
				in.Continuation.SourceMessageID = "msg_missing"
			case "recipient":
				in.Recipients = append(in.Recipients, "agent-two")
			case "task":
				in.Continuation.Target.TaskURL = "https://example.com/code/repo/issues/1"
			case "identity":
				in.Continuation.Target.MessageID = "bad"
			}
			if _, e := s.Send(t.Context(), o, in); e == nil {
				t.Fatal("invalid continuation admitted")
			}
		})
	}
}
func TestContinuationReceiptsNeverReportResumeAtAdmission(t *testing.T) {
	s, o, b, _, in := continuationFixture(t)
	r, e := s.Send(t.Context(), o, in)
	if e != nil {
		t.Fatal(e)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: r.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if e = s.SetDelivery(t.Context(), b, u); e != nil {
		t.Fatal(e)
	}
	u.State = "resumed"
	u.Intent = &c.NativeIntent{SessionID: "ses_saved", MessageID: "msg_new"}
	if e = s.SetDelivery(t.Context(), b, u); !errors.Is(e, c.ErrConflict) {
		t.Fatal("resume skipped admission", e)
	}
	u.State = "admitted"
	u.Intent = nil
	if e = s.SetDelivery(t.Context(), b, u); e != nil {
		t.Fatal(e)
	}
	p, e := s.Pending(t.Context(), b, c.PendingQuery{})
	if e != nil || len(p.Deliveries) != 1 || p.Deliveries[0].State != "admitted" {
		t.Fatal("admitted control disappeared", e)
	}
	u.State = "resumed"
	u.Intent = &c.NativeIntent{SessionID: "ses_saved", MessageID: "msg_saved"}
	if e = s.SetDelivery(t.Context(), b, u); e == nil {
		t.Fatal("old message reported resumed")
	}
	u.Intent.MessageID = "msg_new"
	if e = s.SetDelivery(t.Context(), b, u); e != nil {
		t.Fatal(e)
	}
	if e = s.SetDelivery(t.Context(), b, u); e != nil {
		t.Fatal("receipt retry failed", e)
	}
	status, e := s.MessageStatus(t.Context(), o, r.MessageID)
	if e != nil || status.Deliveries[0].State != "resumed" {
		t.Fatal("missing actual resumed receipt", e)
	}
}

func TestAdmittedContinuationCancelledWhenRecipientLeavesThread(t *testing.T) {
	s, o, b, th, in := continuationFixture(t)
	receipt, err := s.Send(t.Context(), o, in)
	if err != nil {
		t.Fatal(err)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: receipt.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(t.Context(), b, u); err != nil {
		t.Fatal(err)
	}
	u.State = "admitted"
	if err = s.SetDelivery(t.Context(), b, u); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetMembers(t.Context(), o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: []string{o.ID, "agent-two"}, RequestID: "remove-recipient"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(t.Context(), o, receipt.MessageID)
	if err != nil || status.Deliveries[0].State != "cancelled" {
		t.Fatal("removed recipient retained admitted control", status, err)
	}
}
