package worker

import (
	"errors"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func verifiedCompletionRefusal(cfg Config, input c.TaskCompleteInput, err error) *c.TaskCompletionRefusal {
	var refused *c.TaskCompletionNotStartedError
	if !errors.As(err, &refused) || !errors.Is(err, c.ErrForbidden) || cfg.Master == nil {
		return nil
	}
	proof := refused.Refusal
	if proof.Version != 1 || proof.MasterID != cfg.Master.MasterID || proof.MasterID != input.Goal.MasterID || proof.RestoreEpoch < 1 || proof.RequestID != input.RequestID || proof.InputSHA256 != c.TaskCompletionInputSHA(input) || proof.Effect != "not_started" {
		return nil
	}
	var connection masterConnectionState
	raw, readErr := readPrivate(filepath.Join(masterDir(cfg), "connection.json"), 4096)
	if readErr != nil || strictTeamJSON(raw, &connection) != nil || connection.Version != 1 || connection.MasterID != cfg.Master.MasterID || connection.InstanceID != cfg.Master.InstanceID || connection.Generation != cfg.Master.Generation || connection.Epoch != proof.RestoreEpoch {
		return nil
	}
	return &proof
}
