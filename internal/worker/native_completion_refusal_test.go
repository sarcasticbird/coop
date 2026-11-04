package worker

import (
	"errors"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestCompletionRefusalBoundToCurrentConnectionAndInput(t *testing.T) {
	for _, fault := range []string{"valid", "epoch", "master", "input", "request", "effect", "bare", "cause"} {
		t.Run(fault, func(t *testing.T) {
			cfg, _, _, _ := dispatchFixture(t)
			in := c.TaskCompleteInput{Goal: c.GoalRef{MasterID: cfg.Master.MasterID}, Reference: "task", RequestID: "original"}
			proof := c.TaskCompletionRefusal{Version: 1, MasterID: cfg.Master.MasterID, RestoreEpoch: 1, RequestID: in.RequestID, InputSHA256: c.TaskCompletionInputSHA(in), Effect: "not_started"}
			cause := c.ErrForbidden
			switch fault {
			case "epoch":
				proof.RestoreEpoch++
			case "master":
				proof.MasterID = "foreign"
			case "input":
				proof.InputSHA256 = BodyHash("other")
			case "request":
				proof.RequestID = "foreign"
			case "effect":
				proof.Effect = "unknown"
			case "cause":
				cause = c.ErrUnavailable
			}
			var err error = &c.TaskCompletionNotStartedError{Refusal: proof, Cause: cause}
			if fault == "bare" {
				err = errors.New("not_started")
			}
			if (verifiedCompletionRefusal(cfg, in, err) != nil) != (fault == "valid") {
				t.Fatal("unbound no-effect evidence accepted", fault)
			}
		})
	}
}
