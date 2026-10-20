package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A wait binds dependencies to one native turn and immutable registered tasks.
// Completion permits revalidation, never bypasses the saved job's authority.
type NativeDependency struct {
	TaskURL       string
	Goal          c.GoalRef
	AssignmentSHA string
}
type NativeDependencyWait struct {
	SessionID, MessageID, Reason string
	Dependencies                 []NativeDependency
}

func prepareNativeWait(ctx context.Context, cfg Config, m NativeEngineeringMaster, g NativeCatalogReader, x NativeExecution, in NativeAction) (*NativeDependencyWait, error) {
	if x.Job.Kind != "implement" && x.Job.Kind != "fix" || len(in.DependsOn) == 0 || len(in.DependsOn) > 20 || in.Body == "" || len(in.Body) > 1024 || !validSession(x.Job.SessionID) || !validMessage(x.Job.MessageID) {
		return nil, c.ErrInvalid
	}
	catalog, err := nativeValidationSnapshot(ctx, cfg, m, g, x.Job, in.DependsOn...)
	if err != nil {
		return nil, err
	}
	if !catalog.Complete {
		return nil, c.ErrUnavailable
	}
	wait := &NativeDependencyWait{SessionID: x.Job.SessionID, MessageID: x.Job.MessageID, Reason: in.Body}
	seen := map[string]bool{}
	pending := false
	for _, ref := range in.DependsOn {
		if !validGitHubURL(ref) || ref == x.Job.TaskURL || seen[ref] {
			return nil, c.ErrInvalid
		}
		seen[ref] = true
		found := false
		for _, task := range catalog.Tasks {
			if task.Task.URL != ref {
				continue
			}
			if task.Binding.TaskURL != ref || !nativeCatalogApproved(catalog, task.Binding.Goal, task.Binding.ApprovalSequence) || task.Task.Closed && !task.Task.Completed {
				return nil, c.ErrForbidden
			}
			wait.Dependencies = append(wait.Dependencies, NativeDependency{TaskURL: ref, Goal: task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
			pending = pending || !task.Task.Completed
			found = true
			break
		}
		if !found {
			return nil, c.ErrForbidden
		}
	}
	if !pending {
		return nil, c.ErrConflict
	}
	sort.Slice(wait.Dependencies, func(i, j int) bool { return wait.Dependencies[i].TaskURL < wait.Dependencies[j].TaskURL })
	return wait, nil
}
func nativeCatalogApproved(catalog NativeCatalog, goal c.GoalRef, approval int64) bool {
	for _, a := range catalog.Goals {
		if a.Valid && a.State == "approved" && a.Revision.Ref == goal && a.ActiveRevisionSHA256 == goal.RevisionSHA256 && a.ApprovalSequence == approval {
			return true
		}
	}
	return false
}
func nativeDependenciesReady(catalog NativeCatalog, wait *NativeDependencyWait) bool {
	if !catalog.Complete || wait == nil || len(wait.Dependencies) == 0 || len(wait.Dependencies) > 20 {
		return false
	}
	for _, dep := range wait.Dependencies {
		found := false
		for _, task := range catalog.Tasks {
			if task.Task.URL == dep.TaskURL && task.Binding.Goal == dep.Goal && NativeAssignmentSHA(task.Binding) == dep.AssignmentSHA && task.Task.Completed && nativeCatalogApproved(catalog, dep.Goal, task.Binding.ApprovalSequence) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func nativeTurnWait(cfg Config, x NativeExecution) (*NativeDependencyWait, error) {
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wait *NativeDependencyWait
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return nil, err
		}
		var record nativeActionRecord
		if strictTeamJSON(raw, &record) != nil || record.Version != 3 {
			return nil, c.ErrConflict
		}
		if record.JobID != x.Job.ID || record.Operation != "wait" || record.State != "completed" || record.Wait == nil || record.Wait.SessionID != x.Job.SessionID || record.Wait.MessageID != x.Job.MessageID {
			continue
		}
		if wait != nil {
			return nil, c.ErrConflict
		}
		wait = record.Wait
	}
	return wait, nil
}
func recordNativeWait(ctx context.Context, cfg Config, m NativeEngineeringMaster, g NativeCatalogReader, x NativeExecution, in NativeAction, record nativeActionRecord) (NativeActionResult, error) {
	var empty NativeActionResult
	existing, err := nativeTurnWait(cfg, x)
	if err != nil {
		return empty, err
	}
	if existing != nil {
		return empty, c.ErrConflict
	}
	wait, err := prepareNativeWait(ctx, cfg, m, g, x, in)
	if err != nil {
		return empty, err
	}
	record.Wait = wait
	record.State = "completed"
	record.Result.Phase = "waiting"
	if err = saveNativeAction(cfg, record); err != nil {
		return empty, err
	}
	return record.Result, nil
}
func nativeDependencyTransportRetry(x NativeExecution) bool {
	return x.Job.ReconcileError == "transport_failed"
}

func nativeDependencyBlocker(cfg Config, x NativeExecution) string {
	if x.Reason != "native_dependency_wait" {
		return x.Reason
	}
	if x.Job.ReconcileFailures > 0 && nativeDependencyTransportRetry(x) {
		return "native_dependency_retry"
	}
	if x.Job.ReconcileFailures >= Defaults(cfg).Limits.MaxAttempts {
		return "native_dependency_recovery_required"
	}
	return x.Reason
}

func nativeDependencyBackoff(x NativeExecution) time.Duration {
	delay := time.Minute
	if nativeDependencyTransportRetry(x) {
		for i := 1; i < x.Job.ReconcileFailures && delay < 15*time.Minute; i++ {
			delay *= 2
		}
	}
	return min(delay, 15*time.Minute)
}

func (d *NativeDispatcher) dependencyWaits() []NativeExecution {
	out := []NativeExecution{}
	if !d.catalog.Complete {
		return out
	}
	for _, x := range d.history {
		if x.Phase == "blocked" && x.Reason == "native_dependency_wait" && x.Wait != nil && !d.clock.Now().Before(x.Job.NextAttemptAt) && (x.Job.ReconcileFailures < Defaults(d.config).Limits.MaxAttempts || nativeDependencyTransportRetry(x)) && nativeDependenciesReady(d.catalog, x.Wait) {
			for _, a := range d.catalog.Goals {
				if a.Revision.Ref == x.Goal && nativeCatalogApproved(d.catalog, x.Goal, a.ApprovalSequence) {
					out = append(out, x)
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job.ID < out[j].Job.ID })
	return out
}

var errNativeDependencyWait = errors.New("native dependency not ready")

func (d *NativeDispatcher) parkDependencyContinuation(x *NativeExecution) (bool, error) {
	if x.Wait == nil || len(x.Turns) == 0 || x.Job.SubmissionState != "" && x.Job.SubmissionState != "prepared" {
		return false, c.ErrConflict
	}
	previous := x.Turns[len(x.Turns)-1]
	if previous.SessionID != x.Wait.SessionID || previous.MessageID != x.Wait.MessageID || previous.SubmissionState != "completed" || previous.TerminalAt.IsZero() {
		return false, c.ErrConflict
	}
	pending := x.Job
	x.PendingContinuation = &pending
	x.Job = previous
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	return false, saveNativeExecution(d.config, *x)
}
