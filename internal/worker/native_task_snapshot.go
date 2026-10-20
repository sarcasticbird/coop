package worker

import (
	"context"
	"sort"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// taskSnapshotMaster narrows discovery inputs, but delegates every authority
// and evidence check to the real master. The existing catalog reader performs
// all task/PR/Project/contributor/QA validation on these inputs.
type taskSnapshotMaster struct {
	NativeMaster
	goals                []c.GoalAuthority
	tasks                map[string]bool
	linksRead, pagesRead int
}

func (m *taskSnapshotMaster) ApprovedGoals(context.Context, string, string) (c.GoalPage, error) {
	return c.GoalPage{Goals: m.goals}, nil
}
func (m *taskSnapshotMaster) Artifacts(ctx context.Context, in c.ArtifactQuery) (c.ArtifactPage, error) {
	page, err := m.NativeMaster.Artifacts(ctx, in)
	if err != nil {
		return c.ArtifactPage{}, err
	}
	m.linksRead += len(page.Links)
	m.pagesRead++
	if m.linksRead > MaxConnection || m.pagesRead > MaxConnection {
		return c.ArtifactPage{}, c.ErrCapacity
	}
	filtered := make([]c.ArtifactLink, 0, len(page.Links))
	for _, link := range page.Links {
		if link.Kind == "task" && m.tasks[link.URL] || link.Kind == "pull_request" && m.tasks[link.TaskURL] {
			filtered = append(filtered, link)
		}
	}
	page.Links = filtered
	return page, nil
}

// NativeTaskSnapshot returns a complete view of one engineering task and its
// dependency closure, never a complete discovery catalog. Extra dependencies
// come from a saved runtime wait or the current wait action, not authority.
func (g *GitHubClient) NativeTaskSnapshot(ctx context.Context, cfg Config, master NativeMaster, job Job, extra ...string) (NativeCatalog, error) {
	var empty NativeCatalog
	if cfg.Version != 3 || cfg.Master == nil || job.Master == nil || job.Master.Goal == nil || job.TaskURL == "" {
		return empty, c.ErrInvalid
	}
	reader := NewGitHubClient(g.runner)
	reader.config, reader.observation = cfg, g.observation
	allowed := map[c.GoalRef]bool{}
	seen := map[string]bool{}
	for after := ""; ; {
		if seen[after] || len(seen) >= MaxConnection {
			return empty, c.ErrCapacity
		}
		seen[after] = true
		page, err := master.ApprovedGoals(ctx, cfg.Master.Engagement, after)
		if err != nil {
			return empty, err
		}
		for _, a := range page.Goals {
			allowed[a.Revision.Ref] = true
			if len(allowed) > MaxConnection {
				return empty, c.ErrCapacity
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	scoped := &taskSnapshotMaster{NativeMaster: master, tasks: map[string]bool{}}
	goals := map[c.GoalRef]bool{}
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(ref string) error {
		if visiting[ref] {
			return ghError("native_dependency_cycle")
		}
		if scoped.tasks[ref] {
			return nil
		}
		if len(scoped.tasks) >= MaxConnection {
			return c.ErrCapacity
		}
		visiting[ref] = true
		t, err := reader.ReadNativeTask(ctx, cfg, ref)
		if err != nil {
			return err
		}
		if !allowed[t.Binding.Goal] {
			return c.ErrForbidden
		}
		if !goals[t.Binding.Goal] {
			a, err := master.ResolveGoal(ctx, t.Binding.Goal)
			if err != nil {
				return err
			}
			if !a.Valid || a.State != "approved" || a.Revision.Ref != t.Binding.Goal || a.ActiveRevisionSHA256 != t.Binding.Goal.RevisionSHA256 {
				return c.ErrForbidden
			}
			scoped.goals = append(scoped.goals, a)
			goals[t.Binding.Goal] = true
		}
		// A registered, currently approved root rebind is conclusive stale-work
		// evidence even if its new dependency graph cannot be traversed.
		if ref == job.TaskURL && (t.Binding.Goal != *job.Master.Goal || NativeAssignmentSHA(t.Binding) != job.Master.AssignmentSHA) {
			if _, err = ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: master}, t.Binding); err != nil {
				return err
			}
			if err = nativeRegisteredBinding(ctx, master, t.Binding); err != nil {
				return err
			}
			return errNativeCandidateStale
		}
		scoped.tasks[ref] = true
		for _, dep := range t.Binding.DependsOn {
			if err = visit(dep); err != nil {
				return err
			}
		}
		visiting[ref] = false
		return nil
	}
	if err := visit(job.TaskURL); err != nil {
		return empty, err
	}
	for _, ref := range extra {
		if err := visit(ref); err != nil {
			return empty, err
		}
	}
	sort.Slice(scoped.goals, func(i, j int) bool { return scoped.goals[i].Revision.Ref.GoalID < scoped.goals[j].Revision.Ref.GoalID })
	out, err := g.NativeSnapshot(ctx, cfg, scoped)
	if err != nil {
		return empty, err
	}
	found := map[string]bool{}
	for _, task := range out.Tasks {
		found[task.Task.URL] = true
	}
	for ref := range scoped.tasks {
		if !found[ref] {
			return empty, c.ErrForbidden
		}
	}
	for i := range out.Tasks {
		if out.Tasks[i].Task.URL == job.TaskURL {
			out.Tasks[i].Task.Ordinal = job.ScheduleOrdinal
		}
		for _, ref := range out.Tasks[i].Binding.DependsOn {
			if !found[ref] {
				return empty, c.ErrConflict
			}
		}
	}
	out.TaskScope = job.TaskURL
	return out, nil
}

func nativeRegisteredBinding(ctx context.Context, master NativeMaster, binding NativeTaskBinding) error {
	found, total := false, 0
	seen := map[string]bool{}
	for after := ""; ; {
		if seen[after] || len(seen) >= MaxConnection {
			return c.ErrCapacity
		}
		seen[after] = true
		page, err := master.Artifacts(ctx, c.ArtifactQuery{Goal: binding.Goal, After: after})
		if err != nil {
			return err
		}
		total += len(page.Links)
		if total > MaxConnection {
			return c.ErrCapacity
		}
		for _, link := range page.Links {
			if link.Kind == "task" && link.URL == binding.TaskURL {
				if found || link.Goal != binding.Goal || link.AssignmentSHA != NativeAssignmentSHA(binding) {
					return c.ErrConflict
				}
				found = true
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !found {
		return c.ErrForbidden
	}
	return nil
}

func nativeValidationSnapshot(ctx context.Context, cfg Config, master NativeMaster, reader NativeCatalogReader, job Job, extra ...string) (NativeCatalog, error) {
	switch job.Kind {
	case "implement", "fix", "review", "qa", "deliver":
		return reader.NativeTaskSnapshot(ctx, cfg, master, job, extra...)
	default:
		return reader.NativeSnapshot(ctx, cfg, master)
	}
}

func nativeWaitReferences(x NativeExecution) []string {
	var refs []string
	if x.Wait != nil {
		for _, dep := range x.Wait.Dependencies {
			refs = append(refs, dep.TaskURL)
		}
	}
	return refs
}
