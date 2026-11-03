package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type executionAuthority interface {
	MasterID() string
	Self(context.Context) (c.SelfIdentity, error)
	PlacementProof(context.Context) (c.PlacementProof, error)
}

// Reuse the master's existing accepted recovery-inventory/placement protocol;
// do not create a second generation counter or trust a diagnostic heartbeat.
func verifyExecution(ctx context.Context, previous *workspace.Record, spec workspace.Spec, cl executionAuthority) error {
	want := spec.Profile.Execution
	if want == nil || cl.MasterID() != want.MasterID {
		return c.ErrForbidden
	}
	self, err := cl.Self(ctx)
	if err != nil {
		return err
	}
	if self.Principal.Kind != "agent" || self.Principal.ID != want.AgentID || self.Principal.GitHubBotID <= 0 || self.Placement != want.Placement || self.RestoreEpoch < 1 {
		return c.ErrForbidden
	}
	if previous == nil {
		return nil
	}
	old := previous.Applied.Execution
	if old == nil || old.MasterID != want.MasterID || old.AgentID != want.AgentID || want.Generation != old.Generation+1 || want.InstanceID == old.InstanceID {
		return fmt.Errorf("replacement requires the next fenced worker placement")
	}
	proof, err := cl.PlacementProof(ctx)
	if err != nil {
		return err
	}
	_, digestErr := hex.DecodeString(proof.ManifestSHA256)
	if proof.MasterID != want.MasterID || proof.RestoreEpoch != self.RestoreEpoch || proof.From != old.Placement || proof.To != want.Placement || len(proof.ManifestSHA256) != 64 || digestErr != nil {
		return c.ErrForbidden
	}
	return nil
}

func executionGuard(ctx context.Context, previous *workspace.Record, spec workspace.Spec) error {
	if spec.Profile.Execution == nil {
		if _, worker := spec.Profile.Services["worker"]; worker {
			return c.ErrForbidden
		}
		if previous != nil && previous.Applied.Execution != nil {
			return c.ErrForbidden
		}
		return nil
	}
	cl, err := masterclient.LoadConfig(spec.Profile.Execution.ClientConfig)
	if err != nil {
		return err
	}
	return verifyExecution(ctx, previous, spec, cl)
}

func (s *Session) CheckExecutionReplacement(ctx context.Context, previous workspace.Record) error {
	return executionGuard(ctx, &previous, s.Spec)
}

func (s *Session) checkPortableWorker(ctx context.Context) error {
	if s.Spec.Profile.Runtime != "container" {
		return nil
	}
	if _, worker := s.Spec.Profile.Services["worker"]; !worker {
		return nil
	}
	if s.Spec.Profile.Execution == nil {
		return c.ErrForbidden
	}
	out := runtime.NewBoundedCapture(4096)
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", []string{"/usr/local/libexec/coop-worker", "portable-check", "--config", "/var/lib/coop/worker/config.json"}, nil, out, io.Discard); err != nil {
		return fmt.Errorf("portable worker configuration is unavailable; stage matching binaries/configuration before starting services: %w", err)
	}
	var binding c.ExecutionBinding
	if out.Dropped() > 0 || json.Unmarshal(out.Bytes(), &binding) != nil || binding != s.Spec.Profile.Execution.ExecutionBinding {
		return fmt.Errorf("installed worker does not match the portable execution pin")
	}
	return nil
}
