package worker

// Read-only inspection of retained v1/v2 history. This has no execution path.
type legacyInspection struct {
	Config Config
	state  State
	Clock  Clock
}

func (e *legacyInspection) continuationAllowed(j Job) (bool, string) {
	s := e.state.SnapshotCache
	if !e.savedBindingAllowed(j) {
		return false, "saved assignment, executor or scope changed"
	}
	if j.Kind == "plan" {
		return Admit(e.Config, j, s, e.Clock.Now())
	}
	g, ok := goalFor(s, j.GoalURL)
	if !ok {
		return false, "scope"
	}
	if EvaluateGoal(e.Config, g, e.Clock.Now()).State != "approved" {
		return false, "scope"
	}
	t, ok := taskFor(s, j.TaskURL)
	if !ok {
		return false, "scope"
	}
	if t.Closed && !t.Completed {
		return false, "task_cancelled"
	}
	if j.ScopeSHA != "" && t.BodySHA != j.ScopeSHA {
		return false, "scope"
	}
	check := t
	check.Completed = false
	if t.Completed {
		if !artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, s)) {
			return false, "task_closed"
		}
		check.Closed = false
	}
	if !taskAdmitted(e.Config, check, g, s) {
		return false, "scope"
	}
	if e.Config.Version == 2 && j.Binding.Role == "implementer" && t.Claim != nil && t.Claim.InstanceID != e.state.InstanceID {
		return false, "claim"
	}
	if j.Kind == "implement" {
		if t.Claim == nil || t.Claim.InstanceID != e.state.InstanceID {
			return false, "claim"
		}
		return true, "owned implementation"
	}
	p, ok := prFor(s, j.PRURL)
	if !ok {
		return false, "PR unavailable"
	}
	if p.HeadSHA != j.HeadSHA && !artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, s)) {
		return false, "head changed"
	}
	if artifactCompletes(e.Config, j, snapshotArtifacts(e.Config, j, s)) {
		return true, "verified artifact"
	}
	return Admit(e.Config, j, s, e.Clock.Now())
}

func (e *legacyInspection) savedBindingAllowed(j Job) bool {
	if e.Config.Version != 2 {
		return true
	}
	if !e.state.SnapshotCache.Complete || e.Config.Team == nil || j.Binding == nil {
		return false
	}
	if j.Kind == "qa" {
		p, ok := prFor(e.state.SnapshotCache, j.PRURL)
		if !ok || !qaArtifactEqual(j.QAArtifact, p.QAArtifact) {
			return false
		}
	}
	bound, err := bindTeamJob(e.Config, j, e.state.SnapshotCache, e.Clock.Now())
	return err == nil && *bound.Binding == *j.Binding && bound.ID == j.ID &&
		j.Binding.ExecutorID == e.Config.Self.ID && bound.ScopeSHA == j.ScopeSHA &&
		bound.PRScopeRevision == j.PRScopeRevision
}

func artifactCompletes(c Config, j Job, a Artifacts) bool {
	if c.Version == 2 {
		if c.Team == nil || j.Binding == nil || j.Binding.Version != 2 || j.Binding.ExecutorID != c.Self.ID || j.ID != V2JobKey(j, j.Binding.AssignmentSHA, j.Binding.ExecutorID) {
			return false
		}
		if j.Kind == "plan" {
			sha, err := TeamPolicySHA(*c.Team)
			return err == nil && j.Binding.AssignmentSHA == sha && j.Binding.Role == "planner" && isPlanner(c) && a.PlanResponded
		}
		if a.Task == nil || a.PR == nil {
			return false
		}
		t, p := *a.Task, *a.PR
		if t.URL != j.TaskURL || t.GoalURL != j.GoalURL || t.Assignment == nil || t.BodySHA != j.ScopeSHA || t.BodySHA != TaskScopeHash(t.Title, t.Body) || t.MetadataScopeSHA != t.BodySHA || t.Assignment.TaskBodySHA != t.BodySHA || (!sameActor(t.MetadataAuthor, c.Owner) && !sameActor(t.MetadataAuthor, teamPlanner(c))) {
			return false
		}
		sha, err := AssignmentSHA(*c.Team, t.Repo, t.URL, t.GoalURL, *t.Assignment, t.QA)
		if err != nil || p.BaseBranch != t.Assignment.BaseBranch || sha != j.Binding.AssignmentSHA || p.AssignmentSHA != sha || !teamTaskAuthor(c, t, p.Author) || p.TaskURL != j.TaskURL || p.GoalURL != j.GoalURL || !referenceInRepo(p.URL, t.Repo, "pull") || (j.PRURL != "" && p.URL != j.PRURL) || (j.PRScopeRevision != "" && p.ScopeRevision != j.PRScopeRevision) {
			return false
		}
		if j.Kind == "review" {
			peer, ok := teamIndependentPeer(c, t, p)
			r := teamLatestReview(c, t, p)
			return ok && sameActor(c.Self, peer) && j.Binding.Role == "engineering-review" && p.HeadSHA == j.HeadSHA && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED")
		}
		if j.Kind == "qa" {
			if !qaArtifactEqual(j.QAArtifact, p.QAArtifact) || p.HeadSHA != j.HeadSHA || j.Binding.Role != "customer-qa" {
				return false
			}
			r, _, ok := latestTeamQA(c, t, p, t.QA, j.QAArtifact.SHA256)
			return ok && sameActor(r.Author, c.Self)
		}
		if j.Binding.Role != "implementer" || !teamTaskAuthor(c, t, c.Self) {
			return false
		}
		switch j.Kind {
		case "implement":
			return !p.Closed && p.Branch == j.Branch
		case "fix":
			return p.HeadSHA != j.HeadSHA && !p.Closed
		case "deliver":
			return p.HeadSHA == j.HeadSHA && p.Merged && a.IssueCompleted
		}
		return false
	}
	if j.Kind == "plan" {
		return a.PlanResponded
	}
	if a.PR == nil || !sameActor(a.PR.Author, c.Implementer) || a.PR.TaskURL != j.TaskURL || a.PR.GoalURL != j.GoalURL {
		return false
	}
	switch j.Kind {
	case "implement":
		return !a.PR.Closed && a.PR.Branch == j.Branch
	case "fix":
		return a.PR.HeadSHA != j.HeadSHA && !a.PR.Closed
	case "review":
		r := latestReview(c, *a.PR)
		return a.PR.HeadSHA == j.HeadSHA && (r.State == "APPROVED" || r.State == "CHANGES_REQUESTED")
	case "deliver":
		return a.PR.Merged && a.IssueCompleted
	}
	return false
}

func snapshotArtifacts(c Config, j Job, s Snapshot) Artifacts {
	a := Artifacts{PlanResponded: planningResponded(c, j, s)}
	if t, ok := taskFor(s, j.TaskURL); ok {
		a.Task = &t
		a.Claim = t.Claim
		a.IssueCompleted = t.Completed
	}
	for _, p := range s.PRs {
		match := p.URL == j.PRURL
		if j.PRURL == "" {
			match = p.TaskURL == j.TaskURL && !p.Closed && (j.Branch == "" || p.Branch == j.Branch)
		}
		if match {
			copy := p
			a.PR = &copy
			break
		}
	}
	return a
}
