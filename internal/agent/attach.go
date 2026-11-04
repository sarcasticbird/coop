package agent

import (
	"context"
	"fmt"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// AttachRunning connects to the installed generation, not the desired next
// image. It never starts a guest, refreshes grants, seeds files or applies shape.
func (s *Session) AttachRunning(ctx context.Context) error {
	if s.Controller.Store == nil {
		return fmt.Errorf("agent ownership store is not configured")
	}
	err := s.Controller.WithExclusive(ctx, s.Spec, func(g workspace.Locked) error {
		info, err := g.Inspect()
		if err != nil {
			return err
		}
		if info.State != runtime.StateRunning {
			return ErrAgentNotRunning
		}
		record, err := s.Controller.Store.Load(workspace.Agent, s.Spec.Identity)
		if err != nil {
			return err
		}
		if workspace.ValidateRecord(record) != nil || record.RuntimeName != s.Spec.RuntimeName || record.Fingerprint == "" || info.Fingerprint != record.Fingerprint {
			return fmt.Errorf("running agent does not match its installed ownership record")
		}
		// Portable execution bindings remain generation-fenced. The exception
		// for desired-image drift is only for existing legacy machine disks.
		if record.Applied.Runtime == "container" && record.Fingerprint != s.Spec.Fingerprint {
			return fmt.Errorf("portable agent configuration requires maintenance before attachment")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.Attach(ctx)
}
