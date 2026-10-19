package worker

import (
	"context"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeChecksRecovered(p PR) bool {
	return !p.Closed && !p.Merged && (p.ChecksState == "success" || p.ChecksState == "none")
}

// A terminal repair may become unnecessary while its original checks rerun.
// Retire only exact, still-approved work with proof that no remote effect began.
// Preserve its checkout and journals, including rejected publication intents.
func (d *NativeDispatcher) retireRecoveredChecksRepair(ctx context.Context, x *NativeExecution) (bool, error) {
	if !nativeIsChecksRepair(x.Job) || x.Binding == nil {
		return false, nil
	}
	release, err := lockNativeProducer(d.config)
	if err != nil {
		return false, err
	}
	defer release()
	if _, err := nativeCurrentGoal(ctx, d.config, d.master, x.Goal); err != nil {
		return false, err
	}
	catalog, err := d.github.NativeSnapshot(ctx, d.config, d.master)
	if err != nil {
		return false, err
	}
	if !catalog.Complete {
		return false, c.ErrUnavailable
	}
	recovered := false
	for _, task := range catalog.Tasks {
		if task.Task.URL != x.Job.TaskURL || task.Task.Closed || task.Task.Completed || task.Binding.Goal != x.Goal || NativeAssignmentSHA(task.Binding) != NativeAssignmentSHA(*x.Binding) || !nativeCatalogApproved(catalog, x.Goal, task.Binding.ApprovalSequence) {
			continue
		}
		for _, pr := range task.PRs {
			if pr.URL == x.Job.PRURL && pr.HeadSHA == x.Job.HeadSHA && pr.ScopeRevision == x.Job.PRScopeRevision && pr.AssignmentSHA == NativeAssignmentSHA(*x.Binding) && pr.Author.ID == d.config.Self.ID && nativeChecksRecovered(pr) {
				recovered = true
			}
		}
	}
	if !recovered {
		return false, nil
	}
	entries, err := os.ReadDir(filepath.Join(masterDir(d.config), "actions"))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readPrivate(filepath.Join(masterDir(d.config), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return false, err
		}
		var record nativeActionRecord
		if strictTeamJSON(raw, &record) != nil || record.Version != 3 {
			return false, c.ErrConflict
		}
		if record.JobID == x.Job.ID && (record.State != "prepared" || record.Step != "" || record.Operation != "publish-pr" || record.Result.URL != "") {
			return false, nil
		}
	}
	x.Phase, x.Reason, x.Job.Phase = "cancelled", "native_checks_recovered", "cancelled"
	return true, saveNativeExecution(d.config, *x)
}
