package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A zero goal on a Kata execution means that assignment comes from the current
// task, not a private approval. The binding is only an execution snapshot: it is
// never written to Kata, registered as an artifact, or used as PR authorship.
func kataExecution(b NativeTaskBinding) bool {
	return isKataTask(b.TaskURL) && b.Goal == (c.GoalRef{}) && b.ApprovalSequence == 0
}

func kataJob(j Job) bool {
	return isKataTask(j.TaskURL) && j.Master != nil && j.Master.Goal != nil && *j.Master.Goal == (c.GoalRef{})
}

func nativeQARequired(b NativeTaskBinding, a c.GoalAuthority) bool {
	return kataExecution(b) || a.Revision.Proposal.QA.Required
}

func nativeTaskAcceptanceSHA(b NativeTaskBinding, a c.GoalAuthority) string {
	if kataExecution(b) {
		return b.TaskScopeSHA
	}
	return NativeAcceptanceSHA(a)
}

func nativeStageActor(cfg Config, b NativeTaskBinding, a c.GoalAuthority, role string) bool {
	id, principal := b.AuthorID, a.Actors.Author.PrincipalID
	switch role {
	case "engineering-review":
		id, principal = b.PeerID, a.Actors.Peer.PrincipalID
	case "customer-qa":
		id, principal = b.QAID, a.Actors.QA.PrincipalID
	}
	if kataExecution(b) {
		m, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
		return ok && m.Name == cfg.Master.AgentID && cfg.Self.ID == id
	}
	return cfg.Self.ID == id && cfg.Master.AgentID == principal
}

func nativeCurrentExecutionAuthority(ctx context.Context, cfg Config, m NativeEngineeringMaster, x NativeExecution) (c.GoalAuthority, error) {
	if !kataJob(x.Job) {
		return nativeCurrentGoal(ctx, cfg, m, x.Goal)
	}
	self, err := nativeCurrentSelf(ctx, cfg, m)
	if err != nil {
		return c.GoalAuthority{}, err
	}
	capability := x.Job.Master.Role
	if capability == "implementer" {
		capability = "implement"
	} else if capability == "delivery" {
		capability = "deliver"
	}
	if !containsExact(self.Principal.Capabilities, capability) {
		return c.GoalAuthority{}, c.ErrForbidden
	}
	if x.Binding == nil || !kataExecution(*x.Binding) {
		return c.GoalAuthority{}, c.ErrForbidden
	}
	cl, err := TaskProviderClient(ctx, cfg)
	if err != nil {
		return c.GoalAuthority{}, err
	}
	project, issue, _, err := c.ParseKataTaskReference(x.Job.TaskURL)
	if err != nil || project != cfg.TaskSource.ProjectUID {
		return c.GoalAuthority{}, c.ErrForbidden
	}
	source, err := cl.TaskRead(ctx, issue)
	if err != nil {
		return c.GoalAuthority{}, err
	}
	if source.Reference != issue || source.IssueUID != issue || source.ProjectUID != project || source.Revision < 1 || source.State != "open" && source.State != "closed" {
		return c.GoalAuthority{}, c.ErrConflict
	}
	if kataTaskScope(source) != x.Job.ScopeSHA || source.State == "closed" && (x.Job.Kind != "deliver" || source.ClosedReason != "done") {
		return c.GoalAuthority{}, errNativeCandidateStale
	}
	expectedRevision := x.Binding.TaskRevision
	if source.State == "closed" && x.Job.Kind == "deliver" {
		expectedRevision++ // Only the acknowledged close may advance this snapshot.
	}
	if source.Revision != expectedRevision {
		return c.GoalAuthority{}, errNativeCandidateStale
	}
	author, _, qa, err := kataTaskRoster(cfg, source.Owner)
	if err != nil || author != x.Binding.AuthorID || qa != x.Binding.QAID || validateNativeRoster(cfg, *x.Binding) != nil {
		return c.GoalAuthority{}, c.ErrForbidden
	}
	// The same dispatcher/action guard reads the exact PR scope as well. Re-read
	// this small task snapshot immediately before effects, without rehydrating PRs.
	return c.GoalAuthority{}, nil
}

func kataTaskScope(source c.ProviderTask) string {
	return BodyHash(TaskScopeHash(source.Title, source.Body) + "\x00" + source.Owner)
}

// The final acknowledged task-link edit deliberately changes task scope. After
// a crash, confirm that exact edit read-only; never replay it or use the newer
// body as a grant for the old execution.
func confirmKataPRLink(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, x NativeExecution, r nativeActionRecord) error {
	if !kataJob(x.Job) || x.Binding == nil || r.Operation != "publish-pr" || r.State != "prepared" || r.Step != "task-pr-linked" || !qaHeadSHA.MatchString(r.PublishedHeadSHA) || !nativeStageActor(cfg, *x.Binding, c.GoalAuthority{}, "implementer") {
		return c.ErrForbidden
	}
	self, err := nativeCurrentSelf(ctx, cfg, m)
	if err != nil {
		return err
	}
	if !containsExact(self.Principal.Capabilities, "implement") {
		return c.ErrForbidden
	}
	cl, err := TaskProviderClient(ctx, cfg)
	if err != nil {
		return err
	}
	project, issue, repo, err := c.ParseKataTaskReference(x.Job.TaskURL)
	if err != nil || project != cfg.TaskSource.ProjectUID || !referenceInRepo(r.Result.URL, repo, "pull") {
		return c.ErrForbidden
	}
	source, err := cl.TaskRead(ctx, issue)
	if err != nil {
		return err
	}
	prior := source
	suffix := "\n\nPR: " + r.Result.URL
	if !strings.HasSuffix(source.Body, suffix) {
		return c.ErrConflict
	}
	prior.Body = strings.TrimSuffix(source.Body, suffix)
	if kataTaskScope(prior) != x.Job.ScopeSHA || source.ProjectUID != project || source.IssueUID != issue || source.Reference != issue || source.State != "open" || source.Revision != x.Binding.TaskRevision+1 {
		return c.ErrConflict
	}
	task, err := g.kataTask(ctx, cfg, source)
	if err != nil {
		return err
	}
	pr, err := g.ReadNativePR(ctx, cfg, task, c.GoalAuthority{}, r.Result.URL)
	if err != nil {
		return err
	}
	if pr.HeadSHA != r.PublishedHeadSHA {
		return c.ErrConflict
	}
	return nil
}

func kataIndependentPeer(cfg Config, preferred int64, pr PR) (int64, error) {
	if !nativeContributor(pr, preferred) {
		return preferred, nil
	}
	for _, m := range cfg.Team.Members {
		if memberCapability(m, "engineering-review") && !nativeContributor(pr, m.Actor.ID) {
			return m.Actor.ID, nil
		}
	}
	return 0, ghError("no_independent_engineering_reviewer")
}

var kataCodeURL = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/pull/[0-9]+)?`)

// Use ordinary, visible code links in the task body. No hidden coop metadata or
// private artifact registration is needed. Multiple PRs are delivered in their
// listed order; task completion requires every linked PR to have merged.
func kataCodeLinks(body string) (string, []string, error) {
	repo := ""
	prs := []string{}
	seen := map[string]bool{}
	for _, ref := range kataCodeURL.FindAllString(body, -1) {
		parts := strings.Split(strings.TrimPrefix(ref, "https://github.com/"), "/")
		r := strings.TrimSuffix(parts[0]+"/"+parts[1], ".git")
		if !validRepo(r) || repo != "" && repo != r {
			return "", nil, errors.New("Kata task must name one code repository")
		}
		repo = r
		if len(parts) == 4 && !seen[ref] {
			prs = append(prs, ref)
			seen[ref] = true
		}
	}
	if len(prs) > MaxConnection {
		return "", nil, c.ErrCapacity
	}
	return repo, prs, nil
}

func kataDeliveryHeld(body string) bool {
	body = strings.ToLower(body)
	for _, stop := range []string{"stop before merge", "do not merge", "don't merge", "delivery: hold"} {
		if strings.Contains(body, stop) {
			return true
		}
	}
	return false
}

func kataTaskRoster(cfg Config, owner string) (int64, int64, int64, error) {
	var assigned, peer, qa TeamMember
	if cfg.Team == nil {
		return 0, 0, 0, c.ErrForbidden
	}
	members := append([]TeamMember(nil), cfg.Team.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	for _, m := range members {
		if m.Name == owner {
			assigned = m
		}
		if memberCapability(m, "customer-qa") {
			if qa.Name != "" {
				return 0, 0, 0, errors.New("ambiguous Kata QA actor")
			}
			qa = m
		}
	}
	if assigned.Name == "" || !memberCapability(assigned, "implement") && !memberCapability(assigned, "deliver") {
		return 0, 0, 0, c.ErrForbidden
	}
	for _, m := range members {
		if memberCapability(m, "engineering-review") && m.Name != assigned.Name && (cfg.Team.EngineeringPeers[assigned.Name] == m.Name || !memberCapability(assigned, "implement")) {
			peer = m
			break
		}
	}
	if peer.Name == "" || qa.Name == "" || qa.Actor.ID == assigned.Actor.ID || qa.Actor.ID == peer.Actor.ID {
		return 0, 0, 0, c.ErrForbidden
	}
	return assigned.Actor.ID, peer.Actor.ID, qa.Actor.ID, nil
}

func (g *GitHubClient) kataTask(ctx context.Context, cfg Config, source c.ProviderTask) (NativeTask, error) {
	var out NativeTask
	if source.ProjectUID != cfg.TaskSource.ProjectUID || source.Revision < 1 || source.State != "open" && source.State != "closed" {
		return out, c.ErrConflict
	}
	repo, links, err := kataCodeLinks(source.Body)
	if err != nil {
		return out, err
	}
	if repo == "" || !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
		return out, c.ErrForbidden
	}
	ref, err := c.KataTaskReference(source.ProjectUID, source.IssueUID, repo)
	if err != nil {
		return out, err
	}
	author, peer, qa, err := kataTaskRoster(cfg, source.Owner)
	if err != nil {
		return out, err
	}
	reader := NewGitHubClient(g.runner)
	reader.config = cfg
	reader.observation = g.observation
	if err := reader.verifyOwner(ctx, strings.Split(repo, "/")[0]); err != nil {
		return out, err
	}
	base, selected := "", ""
	selectedMerged := true
	allMerged := len(links) > 0
	for _, link := range links {
		_, number, _ := parseRef(link)
		raw, _, e := reader.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/pulls/"+stringID(number), nil)
		if e != nil {
			return out, e
		}
		var pr struct {
			URL    string `json:"html_url"`
			Merged bool
			Base   struct{ Ref string }
		}
		if json.Unmarshal(raw, &pr) != nil || pr.URL != link || !validBaseBranch(pr.Base.Ref) {
			return out, c.ErrConflict
		}
		allMerged = allMerged && pr.Merged
		if selectedMerged {
			base, selected = pr.Base.Ref, link
			selectedMerged = pr.Merged
		}
	}
	if selected == "" {
		raw, _, e := reader.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo, nil)
		if e != nil {
			return out, e
		}
		var repository struct {
			DefaultBranch string `json:"default_branch"`
		}
		if json.Unmarshal(raw, &repository) != nil || !validBaseBranch(repository.DefaultBranch) {
			return out, c.ErrConflict
		}
		base = repository.DefaultBranch
	}
	// Assignment and task meaning are current Kata data. Comment-only revisions
	// do not invalidate reviews; changing owner, body or title does.
	scope := kataTaskScope(source)
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil {
		return out, err
	}
	out.Binding = NativeTaskBinding{Version: 3, TaskURL: ref, TaskScopeSHA: scope, PolicySHA: policy, BaseBranch: base, AuthorID: author, PeerID: peer, QAID: qa, TaskRevision: source.Revision}
	out.ProviderRevision = source.Revision
	out.Task = Task{URL: ref, Repo: repo, Title: source.Title, Body: source.Body, BodySHA: scope, MetadataScopeSHA: scope, Ready: source.State == "open", Closed: source.State == "closed", Completed: source.State == "closed" && source.ClosedReason == "done", TrustedMetadataPresent: true}
	out.LinkedPRs, out.AllPRsMerged = links, allMerged
	if selected != "" {
		pr, e := reader.ReadNativePR(ctx, cfg, out, c.GoalAuthority{}, selected)
		if e != nil {
			return NativeTask{}, e
		}
		independent, e := kataIndependentPeer(cfg, peer, pr)
		if e != nil {
			return NativeTask{}, e
		}
		if independent != peer {
			out.Binding.PeerID = independent
			pr, e = reader.ReadNativePR(ctx, cfg, out, c.GoalAuthority{}, selected)
			if e != nil || nativeContributor(pr, independent) {
				if e != nil {
					return NativeTask{}, e
				}
				return NativeTask{}, c.ErrConflict
			}
		}
		out.PRs = []PR{pr}
	}
	return out, nil
}

func (g *GitHubClient) kataSnapshot(ctx context.Context, cfg Config, taskRef string) (NativeCatalog, error) {
	out := NativeCatalog{TaskScope: taskRef}
	cl, err := TaskProviderClient(ctx, cfg)
	if err != nil {
		return out, err
	}
	if taskRef != "" {
		_, issue, _, e := c.ParseKataTaskReference(taskRef)
		if e != nil {
			return out, e
		}
		source, e := cl.TaskRead(ctx, issue)
		if e != nil {
			return out, e
		}
		task, e := g.kataTask(ctx, cfg, source)
		if e != nil {
			return out, e
		}
		if task.Task.URL != taskRef {
			return out, c.ErrConflict
		}
		out.Tasks = []NativeTask{task}
	} else {
		seen := map[string]bool{}
		for after := ""; ; {
			page, e := cl.TaskList(ctx, c.TaskListInput{Status: "open", After: after})
			if e != nil {
				return out, e
			}
			for _, source := range page.Tasks {
				if seen[source.IssueUID] || len(seen) >= MaxConnection {
					return out, c.ErrCapacity
				}
				seen[source.IssueUID] = true
				if source.State == "closed" {
					continue // Historical diagnostics do not hydrate or schedule.
				}
				if _, _, _, e := kataTaskRoster(cfg, source.Owner); e != nil {
					continue // Unassigned/nonengineering work is not a code job.
				}
				repo, _, e := kataCodeLinks(source.Body)
				if e != nil || repo == "" {
					continue
				}
				task, e := g.kataTask(ctx, cfg, source)
				if e != nil {
					var api *GitHubError
					if errors.As(e, &api) && (!api.RetryAt.IsZero() || api.StatusCode >= 500 || api.Category == "credentials_expired" || api.Category == "request_failed") {
						return out, e // Keep ordinary global transport/quota backoff.
					}
					// A conclusive task-local refusal must not strand unrelated work.
					// Targeted execution/action reads still return the original error.
					slog.Warn("Kata task admission waiting", "task", source.IssueUID, "category", errorCategory(e))
					continue
				}
				out.Tasks = append(out.Tasks, task)
			}
			if page.Next == "" {
				break
			}
			if page.Next == after {
				return out, c.ErrConflict
			}
			after = page.Next
		}
	}
	out.Complete, out.ReadAt = true, time.Now().UTC()
	return out, nil
}
