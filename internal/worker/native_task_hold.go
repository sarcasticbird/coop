package worker

import (
	"errors"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A verified terminal turn without its expected result holds that task, not
// unrelated work. A retained acknowledged action can recover automatically.
func nativeExecutionRecoveryHold(cfg Config, x NativeExecution) (bool, error) {
	if x.Phase != "blocked" {
		return false, nil
	}
	if (x.Reason != "native_completed_without_result" && x.Reason != "native_dependency_wait") || x.Job.Phase != "completed" || x.Job.SubmissionState != "completed" || x.Job.TerminalAt.IsZero() || !validSession(x.Job.SessionID) || !validMessage(x.Job.MessageID) {
		return true, nil
	}
	release, err := lockNativeProducer(cfg)
	if errors.Is(err, errNativeProducerBusy) {
		return true, nil // A live producer cannot be classified as a safe task wait.
	}
	if err != nil {
		return true, err
	}
	defer release()
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if err != nil {
		return true, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return true, err
		}
		var record nativeActionRecord
		if strictTeamJSON(raw, &record) != nil || record.Version != 3 {
			return true, c.ErrConflict
		}
		if record.JobID == x.Job.ID && record.State != "completed" {
			automatic, err := nativeAutomaticRecovery(cfg, x)
			return !automatic, err
		}
	}
	return false, nil
}

func (d *NativeDispatcher) updateRecoveryHold() error {
	d.held = true // Classification errors must retain the recovery hold.
	d.recoveryJob = ""
	held := false
	for _, x := range d.history {
		blocked, err := nativeExecutionRecoveryHold(d.config, x)
		if err != nil {
			d.recoveryJob = x.Job.ID
			return err
		}
		if blocked && d.recoveryJob == "" {
			d.recoveryJob = x.Job.ID
		}
		held = held || blocked
	}
	d.held = held
	return nil
}
