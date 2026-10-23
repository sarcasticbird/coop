package worker

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type NativeMaster interface {
	ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error)
	ApprovedGoals(context.Context, string, string) (c.GoalPage, error)
	Artifacts(context.Context, c.ArtifactQuery) (c.ArtifactPage, error)
	RegisterArtifact(context.Context, c.ArtifactLink) (c.ArtifactLink, error)
	Message(context.Context, string) (c.Message, error)
	Send(context.Context, c.SendMessage) (c.MessageReceipt, error)
}

// NativeCatalogTimeout bounds full discovery across approved goals and their
// artifacts. Per-action authority revalidation keeps its separate, shorter limit.
const NativeCatalogTimeout = 5 * time.Minute

type NativeCatalog struct {
	Cache *NativeCacheCoverage
	// TaskScope is nonempty for a validation-only view; never cache it as discovery.
	TaskScope string
	Goals     []c.GoalAuthority
	Tasks     []NativeTask
	Reports   map[string]c.Message
	Complete  bool
	ReadAt    time.Time
}

func (g *GitHubClient) NativeSnapshot(ctx context.Context, cfg Config, master NativeMaster) (NativeCatalog, error) {
	out := NativeCatalog{Reports: map[string]c.Message{}}
	if cfg.Version != 3 || cfg.Master == nil {
		return out, errors.New("native catalog requires v3 configuration")
	}
	if kataTaskSource(cfg) {
		if _, err := TaskProviderClient(ctx, cfg); err != nil {
			return out, err
		}
	}
	_, selectedOnly := master.(*taskSnapshotMaster)
	reader := NewGitHubClient(g.runner)
	reader.observation = g.observation
	reader.config = cfg
	links := []c.ArtifactLink{}
	byGoal := map[c.GoalRef]c.GoalAuthority{}
	owners := map[string]bool{}
	for after := ""; ; {
		page, err := master.ApprovedGoals(ctx, cfg.Master.Engagement, after)
		if err != nil {
			return out, err
		}
		for _, a := range page.Goals {
			if !a.Valid || a.Revision.Ref.MasterID != cfg.Master.MasterID {
				return out, errors.New("native catalog authority mismatch")
			}
			for _, repo := range a.Revision.Proposal.Repositories {
				owner := strings.Split(repo.Name, "/")[0]
				if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo.Name) {
					return out, ghError("native_goal_outside_worker_scope")
				}
				if !owners[owner] {
					if err = reader.verifyOwner(ctx, owner); err != nil {
						return out, err
					}
					owners[owner] = true
				}
			}
			out.Goals = append(out.Goals, a)
			byGoal[a.Revision.Ref] = a
			for cursor := ""; ; {
				p, e := master.Artifacts(ctx, c.ArtifactQuery{Goal: a.Revision.Ref, After: cursor})
				if e != nil {
					return out, e
				}
				links = append(links, p.Links...)
				if len(links) > MaxConnection {
					return out, ghError("native_artifact_capacity")
				}
				if p.Next == "" {
					break
				}
				if p.Next == cursor {
					return out, ghError("native_artifact_cursor_stalled")
				}
				cursor = p.Next
			}
		}
		if page.Next == "" {
			break
		}
		if page.Next == after {
			return out, ghError("native_goal_cursor_stalled")
		}
		after = page.Next
	}
	tasks := map[string]NativeTask{}
	for _, link := range links {
		if link.Kind != "task" || !selectedOnly && !nativeDiscoveryTask(cfg, link.URL) {
			continue
		}
		if _, duplicate := tasks[link.URL]; duplicate {
			return out, ghError("native_task_link_ambiguous")
		}
		t, err := reader.ReadNativeTask(ctx, cfg, link.URL)
		if err != nil {
			return out, err
		}
		if t.Binding.Goal != link.Goal || NativeAssignmentSHA(t.Binding) != link.AssignmentSHA {
			return out, ghError("native_task_link_changed")
		}
		if a := byGoal[link.Goal]; t.Binding.ApprovalSequence < a.ApprovalSequence {
			tasks[link.URL] = t
			continue
		}
		if _, err = ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: master}, t.Binding); err != nil {
			return out, err
		}
		tasks[link.URL] = t
	}
	for _, link := range links {
		if link.Kind != "pull_request" || !selectedOnly && !nativeDiscoveryTask(cfg, link.TaskURL) {
			continue
		}
		task, ok := tasks[link.TaskURL]
		if ok && task.Binding.Goal == link.Goal && (task.Binding.ApprovalSequence < byGoal[link.Goal].ApprovalSequence || NativeAssignmentSHA(task.Binding) != link.AssignmentSHA) {
			continue
		}
		if !ok || task.Binding.Goal != link.Goal || NativeAssignmentSHA(task.Binding) != link.AssignmentSHA {
			return out, ghError("native_PR_link_changed")
		}
		pr, err := reader.ReadNativePR(ctx, cfg, task, byGoal[link.Goal], link.URL)
		if err != nil {
			return out, err
		}
		task.PRs = append(task.PRs, pr)
		tasks[link.TaskURL] = task
	}
	// Existing Project field readers supply ordering/status only. Native authority
	// always comes from the structured master goal and v3 metadata above.
	if (len(cfg.Projects) > 0 || len(cfg.ProjectOwners) > 0) && nativeGitHubTasksPresent(tasks) {
		projectTasks := map[string]Task{}
		for ref, t := range tasks {
			if isKataTask(ref) {
				continue
			}
			t.Task.Ready = false
			projectTasks[ref] = t.Task
		}
		refs, err := reader.projectRefs(ctx)
		if err != nil {
			return out, err
		}
		var tracking Snapshot
		_, selectedOnly := master.(*taskSnapshotMaster)
		for _, p := range refs {
			if err = reader.readProjectView(ctx, p, projectTasks, &tracking, true, selectedOnly); err != nil {
				return out, err
			}
		}
		for _, t := range projectTasks {
			if native, ok := tasks[t.URL]; ok {
				native.Task = t
				tasks[t.URL] = native
			}
		}
	}
	for _, t := range tasks {
		for _, p := range t.PRs {
			for _, r := range p.Reviews {
				if r.Author.ID != t.Binding.QAID || r.HeadSHA != p.HeadSHA {
					continue
				}
				var qa NativeQAResult
				if nativeMetadata(r.Body, "qa", &qa) != nil || qa.Goal != t.Binding.Goal || qa.AssignmentSHA != NativeAssignmentSHA(t.Binding) {
					continue
				}
				report, err := master.Message(ctx, qa.EvidenceMessageID)
				if err != nil {
					return out, err
				}
				out.Reports[report.ID] = report
			}
		}
		out.Tasks = append(out.Tasks, t)
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Task.URL < out.Tasks[j].Task.URL })
	sort.Slice(out.Goals, func(i, j int) bool { return out.Goals[i].Revision.Ref.GoalID < out.Goals[j].Revision.Ref.GoalID })
	out.ReadAt = time.Now().UTC()
	out.Complete = true
	return out, nil
}
