package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type NativeTask struct {
	ProviderRevision int64 `json:"provider_revision,omitempty"`
	Task             Task
	Binding          NativeTaskBinding
	PRs              []PR
}
type NativePRMetadata struct {
	Version       int         `json:"version"`
	TaskURL       string      `json:"task_url"`
	Goal          c.GoalRef   `json:"goal"`
	AssignmentSHA string      `json:"assignment_sha256"`
	Artifact      *QAArtifact `json:"artifact,omitempty"`
}

func NativePRBody(in NativePRMetadata) string {
	raw, _ := json.Marshal(in)
	return "<!-- coop-pr:v3 " + string(raw) + " -->"
}
func ParseNativePRMetadata(body, repo, task string, goal c.GoalRef, assignmentSHA string) (NativePRMetadata, error) {
	var out NativePRMetadata
	if err := nativeMetadata(body, "pr", &out); err != nil {
		return out, err
	}
	if out.Version != 3 || out.Goal != goal || out.TaskURL != task || out.AssignmentSHA != assignmentSHA || !nativeTaskReferenceInRepo(task, repo) || !hexSHA.MatchString(assignmentSHA) {
		return out, ghError("invalid_native_PR_assignment")
	}
	return out, nil
}
func (g *GitHubClient) ReadNativeTask(ctx context.Context, cfg Config, ref string) (NativeTask, error) {
	var out NativeTask
	if cfg.Version != 3 || cfg.Team == nil {
		return out, ghError("native_task_requires_v3")
	}
	repo, err := nativeTaskRepo(ref)
	if err != nil || !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
		return out, ghError("native_task_outside_scope")
	}
	if isKataTask(ref) {
		return g.readKataTask(ctx, cfg, ref)
	}
	_, number, err := parseRef(ref)
	if err != nil {
		return out, err
	}
	raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/issues/"+stringID(number), nil)
	if err != nil {
		return out, err
	}
	issue, err := decode[restIssue](raw)
	if err != nil || issue.URL != ref || issue.Number != number || len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
		return out, ghError("invalid_native_task")
	}
	comments, err := g.issueComments(ctx, repo, number)
	if err != nil {
		return out, err
	}
	originalComments := append([]Comment(nil), comments...)
	sort.Slice(comments, func(i, j int) bool {
		a, b := commentTime(comments[i]), commentTime(comments[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return comments[i].ID < comments[j].ID
	})
	var latest *Comment
	for _, comment := range comments {
		if (sameActor(comment.Author, cfg.Owner) || sameActor(comment.Author, teamPlanner(cfg))) && strings.Contains(comment.Body, "<!-- coop-task:") {
			copy := comment
			latest = &copy
		}
	}
	if latest == nil {
		return out, ghError("native_task_metadata_missing")
	}
	b, err := ParseNativeTaskMetadata(latest.Body)
	if err != nil {
		return out, err
	}
	scope := TaskScopeHash(issue.Title, issue.Body)
	if b.TaskURL != ref || b.TaskScopeSHA != scope {
		return out, ghError("native_task_scope_changed")
	}
	if err = validateNativeRoster(cfg, b); err != nil {
		return out, err
	}
	if err = g.verifyTeamTaskSnapshot(ctx, repo, issue, originalComments); err != nil {
		return out, err
	}
	out.Binding = b
	out.Task = Task{URL: ref, Repo: repo, Title: issue.Title, Body: issue.Body, BodySHA: scope, MetadataScopeSHA: scope, MetadataAuthor: latest.Author, MetadataUpdatedAt: commentTime(*latest), Ready: issue.State == "open", Closed: issue.State == "closed", Completed: issue.State == "closed" && issue.StateReason == "completed", TrustedMetadataPresent: true, DependsOn: b.DependsOn}
	return out, nil
}
func validateNativeRoster(cfg Config, b NativeTaskBinding) error {
	if cfg.Team == nil {
		return errors.New("native team policy missing")
	}
	sha, err := TeamPolicySHA(*cfg.Team)
	if err != nil || sha != b.PolicySHA {
		return errors.New("native roster policy changed")
	}
	var author, peer, qa TeamMember
	for _, m := range cfg.Team.Members {
		switch m.Actor.ID {
		case b.AuthorID:
			author = m
		case b.PeerID:
			peer = m
		case b.QAID:
			qa = m
		}
	}
	if author.Name == "" || peer.Name == "" || cfg.Team.EngineeringPeers[author.Name] != peer.Name || !memberCapability(author, "implement") || !memberCapability(peer, "engineering-review") {
		return errors.New("native assignment violates peer policy")
	}
	if b.QAID > 0 && (qa.Name == "" || !memberCapability(qa, "customer-qa") || qa.Actor.ID == author.Actor.ID || qa.Actor.ID == peer.Actor.ID) {
		return errors.New("native assignment violates QA policy")
	}
	return nil
}
func memberCapability(m TeamMember, capability string) bool {
	for _, v := range m.Capabilities {
		if v == capability {
			return true
		}
	}
	return false
}

func (g *GitHubClient) ReadNativePR(ctx context.Context, cfg Config, t NativeTask, authority c.GoalAuthority, ref string) (PR, error) {
	repo, n, err := parseRef(ref)
	if err != nil || repo != t.Task.Repo || !referenceInRepo(ref, repo, "pull") {
		return PR{}, ghError("invalid_native_PR_target")
	}
	reader := NewGitHubClient(g.runner)
	reader.sourceArtifacts = g.sourceArtifacts
	reader.observation = g.observation
	reader.config = cfg
	parts := strings.Split(repo, "/")
	query := `query WorkerNativePR($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){url body createdAt lastEditedAt state merged headRefOid headRefName baseRefName baseRef{name target{oid}} updatedAt mergeStateStatus author{` + actorFields + `} commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}`
	data, err := reader.graphql(ctx, parts[0], query, map[string]any{"owner": parts[0], "repo": parts[1], "number": n})
	if err != nil {
		return PR{}, err
	}
	raw, err := at(data, "repository", "pullRequest")
	if err != nil {
		return PR{}, err
	}
	var item struct {
		prBodyVersion
		State, HeadRefName, MergeStateStatus string
		BaseRef                              *struct {
			Name   string
			Target *struct{ OID string }
		}
		Merged    bool
		UpdatedAt time.Time
		Author    graphActor
		Commits   struct {
			Nodes []struct {
				Commit struct{ StatusCheckRollup *struct{ State string } }
			}
		}
	}
	if json.Unmarshal(raw, &item) != nil || item.URL != ref || item.Author.ID != t.Binding.AuthorID || item.Author.Kind != "Bot" || !qaHeadSHA.MatchString(item.HeadRefOid) || item.BaseRefName != t.Binding.BaseBranch {
		return PR{}, ghError("native_PR_identity_or_scope_changed")
	}
	m, err := ParseNativePRMetadata(item.Body, repo, t.Task.URL, t.Binding.Goal, NativeAssignmentSHA(t.Binding))
	if err != nil {
		return PR{}, err
	}
	pr := PR{URL: ref, TaskURL: t.Task.URL, MasterGoal: &m.Goal, AssignmentSHA: m.AssignmentSHA, Author: item.Author.actor(), HeadSHA: item.HeadRefOid, Branch: item.HeadRefName, BaseBranch: item.BaseRefName, MergeState: item.MergeStateStatus, ChecksState: "unknown", Merged: item.Merged, Closed: item.State == "CLOSED" || item.Merged, Revision: item.UpdatedAt.Format(time.RFC3339Nano)}
	// PR baseRefOid and REST base.sha can lag the live base branch. Missing
	// refs remain readable so merged/deleted-base PRs can be reconciled.
	if item.BaseRef != nil && item.BaseRef.Name == pr.BaseBranch && item.BaseRef.Target != nil && qaHeadSHA.MatchString(item.BaseRef.Target.OID) {
		pr.BaseSHA = item.BaseRef.Target.OID
	}
	if len(item.Commits.Nodes) == 1 {
		pr.ChecksState = "none"
		if rollup := item.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
			pr.ChecksState = strings.ToLower(rollup.State)
		}
	}
	scopeHistory, err := reader.readPRScope(ctx, repo, n, item.prBodyVersion, &pr)
	if err != nil {
		return PR{}, err
	}
	if err = reader.readTeamEvidence(ctx, repo, n, &pr); err != nil {
		return PR{}, err
	}
	artifactTask := t.Task
	artifactTask.QA.Required = authority.Revision.Proposal.QA.Required
	if err = reader.bindQAArtifact(ctx, artifactTask, &pr, m.Artifact); err != nil {
		return PR{}, err
	}
	freshHistory, e := reader.readPRScope(ctx, repo, n, item.prBodyVersion, &pr)
	if e != nil {
		return PR{}, e
	}
	if freshHistory != scopeHistory {
		return PR{}, ghError("PR_scope_history_changed_during_read")
	}
	if err = reader.verifyPRVersion(ctx, repo, n, item.prBodyVersion); err != nil {
		return PR{}, err
	}
	raw, _, err = reader.api(ctx, parts[0], "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
	if err != nil {
		return PR{}, err
	}
	var fresh struct {
		URL  string    `json:"html_url"`
		Body string    `json:"body"`
		User restActor `json:"user"`
		Head struct {
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if json.Unmarshal(raw, &fresh) != nil || fresh.URL != ref || fresh.Body != item.Body || fresh.User.ID != pr.Author.ID || fresh.Head.SHA != pr.HeadSHA || fresh.Base.Ref != pr.BaseBranch || fresh.Head.Repo.FullName != repo || fresh.Base.Repo.FullName != repo {
		return PR{}, ghError("native_PR_changed_during_read")
	}
	if nativeNeedsBranchRefresh(pr) {
		base, err := reader.nativeBaseSHA(ctx, repo, pr.BaseBranch)
		if err != nil {
			return PR{}, err
		}
		if base != pr.BaseSHA {
			return PR{}, ghError("native_base_changed_during_read")
		}
	}
	return pr, nil
}

func (g *GitHubClient) nativeBaseSHA(ctx context.Context, repo, branch string) (string, error) {
	parts := strings.Split(repo, "/")
	query := `query WorkerNativeBase($owner:String!,$repo:String!,$ref:String!){repository(owner:$owner,name:$repo){ref(qualifiedName:$ref){name target{oid}}}}`
	data, err := g.graphql(ctx, parts[0], query, map[string]any{"owner": parts[0], "repo": parts[1], "ref": "refs/heads/" + branch})
	if err != nil {
		return "", err
	}
	raw, err := at(data, "repository", "ref")
	if err != nil {
		return "", err
	}
	ref, err := decode[struct {
		Name   string
		Target *struct{ OID string }
	}](raw)
	if err != nil || ref.Name != branch || ref.Target == nil || !qaHeadSHA.MatchString(ref.Target.OID) {
		return "", ghError("unresolved_native_base")
	}
	return ref.Target.OID, nil
}
