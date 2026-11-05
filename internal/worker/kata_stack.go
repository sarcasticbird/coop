package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A previously merged link is not retroactive clearance. For earlier members
// of this task's stack, use the existing successful delivery receipts and exact
// saved PR scope. No registry, history import or new evidence ledger is needed.
func confirmKataStack(ctx context.Context, cfg Config, g *GitHubClient, current NativeExecution, task NativeTask) error {
	if len(task.LinkedPRs) < 2 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "engineering"))
	if err != nil {
		return err
	}
	for _, link := range task.LinkedPRs {
		if link == current.Job.PRURL {
			continue
		}
		confirmed := false
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			prior, err := loadNativeExecution(cfg, strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return err
			}
			if !kataJob(prior.Job) || prior.Phase != "completed" || prior.Binding == nil || prior.Job.Kind != "deliver" || prior.Job.TaskURL != current.Job.TaskURL || prior.Job.PRURL != link || prior.Job.ScopeSHA != current.Job.ScopeSHA || prior.Binding.TaskRevision != current.Binding.TaskRevision {
				continue
			}
			records, err := loadNativeJobActions(cfg, prior)
			if err != nil {
				return err
			}
			settled := false
			for _, r := range records {
				if r.State != "completed" && (r.State != "prepared" || r.Step != "") {
					return c.ErrConflict
				}
				settled = settled || r.State == "completed" && r.Operation == "deliver" && r.Result.URL == link && r.Result.Phase == "completed"
			}
			if !settled {
				continue
			}
			previousTask := task
			previousTask.Binding = *prior.Binding
			pr, err := g.ReadNativePR(ctx, cfg, previousTask, c.GoalAuthority{}, link)
			if err != nil {
				return err
			}
			if pr.Merged && pr.HeadSHA == prior.Job.HeadSHA && pr.ScopeRevision == prior.Job.PRScopeRevision && !nativeContributor(pr, cfg.Self.ID) {
				confirmed = true
				break
			}
		}
		if !confirmed {
			return ghError("linked_merged_PR_lacks_current_task_delivery_receipt")
		}
	}
	return nil
}
