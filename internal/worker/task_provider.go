package worker

import (
	"context"
	"errors"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

type TaskSourceConfig struct {
	Provider          string   `json:"provider"`
	ProjectUID        string   `json:"project_uid"`
	RetainGitHubTasks []string `json:"retain_github_tasks,omitempty"`
}

func kataTaskSource(cfg Config) bool {
	return cfg.TaskSource != nil && cfg.TaskSource.Provider == "kata"
}
func isKataTask(ref string) bool { return strings.HasPrefix(ref, "kata:") }
func nativeTaskReferenceInRepo(ref, repo string) bool {
	actual, err := nativeTaskRepo(ref)
	return err == nil && actual == repo
}
func validateTaskSource(cfg Config) error {
	if cfg.TaskSource == nil {
		return nil
	}
	if cfg.Version != 3 || cfg.Master == nil || !kataTaskSource(cfg) {
		return errors.New("kata task source requires pinned v3 master configuration")
	}
	if _, err := c.KataTaskReference(cfg.TaskSource.ProjectUID, cfg.TaskSource.ProjectUID, "scope/repo"); err != nil {
		return errors.New("invalid pinned Kata project UID")
	}
	seen := map[string]bool{}
	for _, ref := range cfg.TaskSource.RetainGitHubTasks {
		repo, err := nativeTaskRepo(ref)
		if err != nil || isKataTask(ref) || seen[ref] || !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
			return errors.New("invalid retained GitHub task scope")
		}
		seen[ref] = true
	}
	return nil
}

// TaskPlanningClient pins the task service for explicit planning/read helpers.
// The authenticated master supplies current policy and role bindings; this call
// grants no engineering authority and must not be used by the native engine.
func TaskPlanningClient(ctx context.Context, cfg Config) (*masterclient.Client, error) {
	cl, _, err := taskSourceClient(ctx, cfg)
	return cl, err
}

// TaskProviderClient also pins the local engineering roster policy before any
// native task admission, read or mutation.
func TaskProviderClient(ctx context.Context, cfg Config) (*masterclient.Client, error) {
	if cfg.Team == nil {
		return nil, c.ErrUnsupported
	}
	cl, caps, err := taskSourceClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sha, err := TeamPolicySHA(*cfg.Team)
	if err != nil || caps.PolicySHA != sha {
		return nil, errors.New("kata engineering policy mismatch")
	}
	return cl, nil
}

func taskSourceClient(ctx context.Context, cfg Config) (*masterclient.Client, c.TaskCapabilities, error) {
	var caps c.TaskCapabilities
	if cfg.Version != 3 || !kataTaskSource(cfg) || cfg.Master == nil {
		return nil, caps, c.ErrUnsupported
	}
	cl, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err != nil {
		return nil, caps, err
	}
	if cl.MasterID() != cfg.Master.MasterID {
		return nil, caps, c.ErrForbidden
	}
	caps, err = cl.TaskCapabilities(ctx)
	if err != nil {
		return nil, caps, err
	}
	if caps.Provider != "kata" || caps.ProjectUID != cfg.TaskSource.ProjectUID || !hexSHA.MatchString(caps.PolicySHA) {
		return nil, caps, errors.New("kata task source project or policy mismatch")
	}
	return cl, caps, nil
}
func (g *GitHubClient) readKataTask(ctx context.Context, cfg Config, ref string) (NativeTask, error) {
	var out NativeTask
	project, issue, repo, err := c.ParseKataTaskReference(ref)
	if err != nil || !kataTaskSource(cfg) || project != cfg.TaskSource.ProjectUID {
		return out, c.ErrForbidden
	}
	cl, err := TaskProviderClient(ctx, cfg)
	if err != nil {
		return out, err
	}
	task, err := cl.TaskRead(ctx, ref)
	if err != nil {
		return out, err
	}
	if task.Reference != ref || task.ProjectUID != project || task.IssueUID != issue || task.Repository != repo || task.Binding == nil || task.Revision < 1 {
		return out, c.ErrConflict
	}
	b := *task.Binding
	scope := TaskScopeHash(task.Title, task.Body)
	if b.TaskURL != ref || b.Goal != task.Goal || b.Goal.MasterID != cfg.Master.MasterID || b.TaskScopeSHA != scope {
		return out, c.ErrConflict
	}
	if err = validateNativeRoster(cfg, b); err != nil {
		return out, err
	}
	switch task.State {
	case "open", "closed":
	default:
		return out, c.ErrConflict
	}
	out.Binding = b
	out.ProviderRevision = task.Revision
	out.Task = Task{URL: ref, Repo: repo, Title: task.Title, Body: task.Body, BodySHA: scope, MetadataScopeSHA: scope, Ready: task.State == "open", Closed: task.State == "closed", Completed: task.State == "closed" && task.ClosedReason == "done", TrustedMetadataPresent: true, DependsOn: b.DependsOn}
	return out, nil
}
func nativeDiscoveryTask(cfg Config, ref string) bool {
	if !kataTaskSource(cfg) || isKataTask(ref) {
		return true
	}
	return containsExact(cfg.TaskSource.RetainGitHubTasks, ref)
}

func nativeKataTaskAction(cfg Config, in NativeAction) bool {
	return in.Operation == "create-task" && kataTaskSource(cfg) || in.Operation == "rebind-task" && isKataTask(in.TaskURL)
}

func nativeCanonicalTaskReference(ref string) bool { _, err := nativeTaskRepo(ref); return err == nil }

func nativeGitHubTasksPresent(tasks map[string]NativeTask) bool {
	for ref := range tasks {
		if !isKataTask(ref) {
			return true
		}
	}
	return false
}
