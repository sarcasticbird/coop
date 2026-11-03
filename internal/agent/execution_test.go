package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type executionFixture struct {
	self    c.SelfIdentity
	proof   c.PlacementProof
	offline bool
}

type portableCheckRunner struct {
	failingBackend
	output    c.ExecutionBinding
	malformed bool
	fail      bool
}

func (r *portableCheckRunner) Exec(_ context.Context, _ string, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
	if len(args) != 4 || args[1] != "portable-check" {
		return errors.New("unexpected portable check command")
	}
	if r.fail {
		return errors.New("worker unavailable")
	}
	if r.malformed {
		_, err := io.WriteString(out, `{"master_id":"master"}`)
		return err
	}
	return json.NewEncoder(out).Encode(r.output)
}

func TestPortableWorkerServicesRequireMatchingInstalledConfig(t *testing.T) {
	want := c.ExecutionBinding{MasterID: "master", Placement: c.Placement{AgentID: "rook", HostID: "local", InstanceID: strings.Repeat("a", 64), Generation: 2}}
	for _, fault := range []string{"none", "stale", "malformed", "unavailable"} {
		t.Run(fault, func(t *testing.T) {
			r := &portableCheckRunner{output: want}
			s := Session{Runner: r, Spec: workspace.Spec{RuntimeName: "coop-agent-rook", Profile: config.Profile{Runtime: "container", Execution: &config.AgentExecution{ExecutionBinding: want}, Services: map[string]config.Service{"worker": {Command: []string{"worker"}}}}}}
			switch fault {
			case "stale":
				r.output.Generation--
			case "malformed":
				r.malformed = true
			case "unavailable":
				r.fail = true
			}
			err := s.checkPortableWorker(t.Context())
			if fault == "none" && err != nil || fault != "none" && err == nil {
				t.Fatal(fault, err)
			}
		})
	}
}

func (f *executionFixture) MasterID() string { return "master" }
func (f *executionFixture) Self(context.Context) (c.SelfIdentity, error) {
	if f.offline {
		return f.self, c.ErrUnavailable
	}
	return f.self, nil
}
func (f *executionFixture) PlacementProof(context.Context) (c.PlacementProof, error) {
	return f.proof, nil
}

func TestPortableExecutionRequiresAuthenticatedNextPlacement(t *testing.T) {
	for _, fault := range []string{"none", "move-host", "generation", "same-instance", "identity", "kind", "bot", "placement", "master", "epoch", "digest", "proof-from", "proof-to", "offline", "missing-old"} {
		t.Run(fault, func(t *testing.T) {
			old := c.ExecutionBinding{MasterID: "master", Placement: c.Placement{AgentID: "robin", HostID: "local", InstanceID: strings.Repeat("a", 64), Generation: 1}}
			next := old
			next.Generation++
			next.InstanceID = strings.Repeat("b", 64)
			spec := workspace.Spec{Profile: config.Profile{Runtime: "container", Execution: &config.AgentExecution{ExecutionBinding: next}}}
			previous := workspace.Record{Applied: workspace.AppliedSpec{Runtime: "container", Execution: &old}}
			f := &executionFixture{self: c.SelfIdentity{Principal: c.Principal{ID: "robin", Kind: "agent", GitHubBotID: 12}, Placement: next.Placement, RestoreEpoch: 1}, proof: c.PlacementProof{MasterID: "master", RestoreEpoch: 1, From: old.Placement, To: next.Placement, ManifestSHA256: strings.Repeat("c", 64)}}
			switch fault {
			case "move-host":
				spec.Profile.Execution.HostID = "other-host"
				f.self.Placement.HostID = "other-host"
				f.proof.To.HostID = "other-host"
			case "generation":
				spec.Profile.Execution.Generation++
			case "same-instance":
				spec.Profile.Execution.InstanceID = old.InstanceID
				f.self.Placement.InstanceID = old.InstanceID
			case "identity":
				f.self.Principal.ID = "other"
			case "kind":
				f.self.Principal.Kind = "owner"
			case "bot":
				f.self.Principal.GitHubBotID = 0
			case "placement":
				f.self.Placement.HostID = "other"
			case "master":
				f.proof.MasterID = "other"
			case "epoch":
				f.proof.RestoreEpoch++
			case "digest":
				f.proof.ManifestSHA256 = strings.Repeat("g", 64)
			case "proof-from":
				f.proof.From.InstanceID = "other"
			case "proof-to":
				f.proof.To.InstanceID = "other"
			case "offline":
				f.offline = true
			case "missing-old":
				previous.Applied.Execution = nil
			}
			err := verifyExecution(t.Context(), &previous, spec, f)
			valid := fault == "none" || fault == "move-host"
			if valid && err != nil || !valid && err == nil {
				t.Fatal(fault, err)
			}
			if fault == "offline" && !errors.Is(err, c.ErrUnavailable) {
				t.Fatal("offline authority guessed", err)
			}
		})
	}
}

func TestPortableExecutionCreationAndRestartUseCurrentPin(t *testing.T) {
	binding := c.ExecutionBinding{MasterID: "master", Placement: c.Placement{AgentID: "rook", HostID: "local", InstanceID: strings.Repeat("a", 64), Generation: 1}}
	spec := workspace.Spec{Profile: config.Profile{Runtime: "container", Execution: &config.AgentExecution{ExecutionBinding: binding}}}
	f := &executionFixture{self: c.SelfIdentity{Principal: c.Principal{ID: "rook", Kind: "agent", GitHubBotID: 14}, Placement: binding.Placement, RestoreEpoch: 1}}
	if err := verifyExecution(t.Context(), nil, spec, f); err != nil {
		t.Fatal(err)
	}
	f.self.Placement.Generation++
	if err := verifyExecution(t.Context(), nil, spec, f); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("retired execution restarted", err)
	}
}
