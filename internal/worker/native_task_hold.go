package worker

import (
	"errors"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeModelFinished(j Job) bool {
	return j.Phase == "completed" && j.SubmissionState == "completed" && !j.TerminalAt.IsZero() && validSession(j.SessionID) && validMessage(j.MessageID)
}

// A finished model's blocked work holds its task, not the whole coworker.
// External-action uncertainty is enforced by the task's existing journal.
func nativeExecutionRecoveryHold(cfg Config, x NativeExecution) (bool, error) {
	held, err := nativeExecutionRecoveryCheck(cfg, x)
	if errors.Is(err, errNativeProducerBusy) {
		return true, nil
	}
	return held, err
}

// Admission needs the underlying busy error to retry; general hold
// classification remains conservative while another producer is active.
func nativeExecutionRecoveryCheck(cfg Config, x NativeExecution) (bool, error) {
	if x.Phase == "completed" || x.Phase == "cancelled" {
		return false, nil
	}
	if x.Phase != "blocked" && !x.unresolvedHistoricalRecovery() {
		return false, nil
	}
	if !nativeModelFinished(x.Job) {
		return true, nil
	}
	release, err := lockNativeProducer(cfg)
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
