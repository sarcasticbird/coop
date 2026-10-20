package worker

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/githubapp"
	"strings"
	"time"
)

type GitHubObservationSource interface {
	Observe(context.Context, Config, c.GitHubLease, []c.GitHubDatasetChunk) (GitHubObservation, error)
}
type GitHubObservation struct {
	Commit           c.GitHubCommit
	Payloads         map[string][]byte
	ProviderRequests int
}
type githubObservationSource struct {
	projectOrder map[string]cachedProjectOrder
	clock        Clock
	github       *GitHubClient
	identity     func(context.Context) (githubapp.Status, error)
	yield        func(context.Context) error
}

func NewGitHubObservationSource(g *GitHubClient) GitHubObservationSource {
	return &githubObservationSource{github: g, clock: RealClock{}, identity: func(ctx context.Context) (githubapp.Status, error) {
		var out githubapp.Status
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		r, e := g.runner.Run(ctx, []string{githubapp.GuestHelper, "status"}, nil, nil)
		if e != nil || r.ExitCode != 0 || len(r.Stdout) > MaxAPIPage || json.Unmarshal(r.Stdout, &out) != nil {
			return out, ghError("cache_credential_identity_unavailable")
		}
		return out, nil
	}}
}
func (s *githubObservationSource) Observe(ctx context.Context, cfg Config, l c.GitHubLease, previous []c.GitHubDatasetChunk) (GitHubObservation, error) {
	out := GitHubObservation{Commit: c.GitHubCommit{LeaseID: l.ID}, Payloads: map[string][]byte{}}
	if err := c.ValidateGitHubObject(l.Object); err != nil {
		return out, err
	}
	repo, _, err := parseRef(l.Object.URL)
	if err != nil {
		return out, err
	}
	owner := strings.Split(repo, "/")[0]
	if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
		return out, c.ErrForbidden
	}
	status, err := s.identity(ctx)
	if err != nil {
		return out, err
	}
	inst := status.Installations[strings.ToLower(owner)]
	if status.AppID != l.AppID || inst.ID != l.InstallationID || !inst.ExpiresAt.After(s.clock.Now()) || status.BotLogin != cfg.Self.Login {
		return out, ghError("cache_credential_identity_changed")
	}
	old := providerState{}
	var oldPR PR
	for _, d := range previous {
		if d.Ref.Kind == "provider" {
			if err = json.Unmarshal(d.Data, &old); err != nil {
				return out, c.ErrInvalid
			}
		}
		if d.Ref.Kind == "pr" {
			if err = json.Unmarshal(d.Data, &oldPR); err != nil {
				return out, c.ErrInvalid
			}
		}
	}
	if old.AppID != status.AppID || old.InstallationID != inst.ID {
		old = providerState{}
	}
	runner := newObservationRunner(s.github.runner, s.clock.Now().UTC(), old)
	runner.yield = s.yield
	runner.clock = s.clock
	g := NewGitHubClient(runner)
	g.config = cfg
	g.observation = s.github.observation
	g.cacheRegisteredOnly = true
	fresh := func(endpoint string) ([]byte, error) {
		runner.force = true
		raw, _, e := g.api(ctx, owner, "GET", endpoint, nil)
		runner.force = false
		return raw, e
	}
	raw, err := fresh("repos/" + repo)
	if err != nil {
		return out, err
	}
	var repository struct {
		ID       int64
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(raw, &repository) != nil || repository.ID < 1 || repository.FullName != repo {
		return out, ghError("cache_repository_identity_changed")
	}
	path := strings.TrimPrefix(l.Object.URL, "https://github.com/")
	path = "repos/" + strings.Replace(path, "/pull/", "/pulls/", 1)
	first, err := fresh(path)
	if err != nil {
		return out, err
	}
	var header struct {
		ID          int64
		NodeID      string `json:"node_id"`
		State       string
		StateReason string `json:"state_reason"`
	}
	if json.Unmarshal(first, &header) != nil || header.ID < 1 || header.NodeID == "" || (header.State != "open" && header.State != "closed") {
		return out, ghError("cache_invalid_header")
	}
	fingerprint := BodyHash(string(first))
	if old.Fingerprint != fingerprint {
		runner.state.Entries = map[string]providerEntry{}
	}
	runner.state.AppID, runner.state.InstallationID, runner.state.Fingerprint = status.AppID, inst.ID, fingerprint
	out.Commit.Header = c.GitHubHeader{Revision: fingerprint, RepositoryID: repository.ID, ObjectID: header.NodeID, State: header.State, Completed: header.State == "closed" && header.StateReason == "completed"}
	var primaryObserved time.Time
	finish := func() (GitHubObservation, error) {
		last, err := fresh(path)
		if err != nil {
			return out, err
		}
		if BodyHash(string(last)) != fingerprint {
			return out, ghError("cache_header_changed_during_read")
		}
		out.Commit.HeaderObservedAt = s.clock.Now().UTC()
		out.Payloads["provider"], err = json.Marshal(runner.state)
		if err != nil {
			return out, err
		}
		total := 0
		for k, p := range out.Payloads {
			total += len(p)
			if total > MaxSnapshot {
				return out, c.ErrCapacity
			}
			observed := primaryObserved
			if k == "provider" || observed.IsZero() {
				observed = s.clock.Now().UTC()
			}
			out.Commit.Datasets = append(out.Commit.Datasets, c.GitHubDatasetRef{Kind: k, SHA256: c.GitHubDigest(p), Total: len(p), Version: 1, ObservedAt: observed})
		}
		out.ProviderRequests = runner.requests
		return out, nil
	}
	if l.Object.Kind == "pull_request" && header.State == "closed" && old.Fingerprint == fingerprint && oldPR.URL == l.Object.URL && oldPR.MasterGoal != nil && *oldPR.MasterGoal == l.Object.Goal && oldPR.AssignmentSHA == l.Object.AssignmentSHA {
		out.Commit.Header.HeadSHA, out.Commit.Header.BaseSHA, out.Commit.Header.ScopeRevision = oldPR.HeadSHA, oldPR.BaseSHA, oldPR.ScopeRevision
		out.Payloads["pr"], err = json.Marshal(oldPR)
		if err != nil {
			return out, err
		}
		return finish()
	}
	runner.reused = map[string]time.Time{}
	task, err := g.ReadNativeTask(ctx, cfg, l.Object.TaskURL)
	if err != nil {
		return out, err
	}
	if task.Binding.Goal != l.Object.Goal || NativeAssignmentSHA(task.Binding) != l.Object.AssignmentSHA {
		return out, ghError("native_task_link_changed")
	}
	kind := "task"
	if l.Object.Kind == "task" {
		out.Commit.Header.DependsOn = task.Binding.DependsOn
		out.Commit.Header.ScopeRevision = task.Task.BodySHA
		if !task.Task.Closed && (len(cfg.Projects) > 0 || len(cfg.ProjectOwners) > 0) {
			task.Task.Ready = false
			tasks := map[string]Task{task.Task.URL: task.Task}
			projects, e := g.projectRefs(ctx)
			if e != nil {
				return out, e
			}
			var tracking Snapshot
			for _, p := range projects {
				if e = g.readProjectView(ctx, p, tasks, &tracking, true, true); e != nil {
					return out, e
				}
			}
			task.Task = tasks[task.Task.URL]
			if task.Task.ProjectID != "" {
				task.Task.Ordinal, err = s.projectOrdinal(ctx, g, l, task.Task)
				if err != nil {
					return out, err
				}
			}
		}
		out.Payloads[kind], err = json.Marshal(task)
	} else {
		kind = "pr"
		runner.reused = map[string]time.Time{}
		// The normalized PR is a one-minute discovery dataset. Reused input
		// cannot outlive that bound; immutable source digests remain reusable.
		runner.maxTTL = time.Minute
		if oldPR.HeadSHA != "" && oldPR.QAArtifact != nil && oldPR.QAArtifact.Kind == "source" && oldPR.QAArtifact.Revision == "source-bundle-v1" && oldPR.URL == l.Object.URL {
			g.sourceArtifacts = map[string]QAArtifact{repo + "\x00" + oldPR.HeadSHA: *oldPR.QAArtifact}
		}
		var pr PR
		pr, err = g.ReadNativePR(ctx, cfg, task, l.Authority, l.Object.URL)
		if err == nil {
			out.Commit.Header.HeadSHA, out.Commit.Header.BaseSHA, out.Commit.Header.ScopeRevision = pr.HeadSHA, pr.BaseSHA, pr.ScopeRevision
			out.Payloads[kind], err = json.Marshal(pr)
		}
	}
	if err != nil {
		return out, err
	}
	primaryObserved = runner.observedAt(s.clock.Now())
	return finish()
}
func sourceArtifactReuse(g *GitHubClient, repo string, p *PR, candidate *QAArtifact) (bool, error) {
	cached, ok := g.sourceArtifacts[repo+"\x00"+p.HeadSHA]
	if !ok {
		return false, nil
	}
	if cached.Kind != "source" || cached.HeadSHA != p.HeadSHA || cached.Revision != "source-bundle-v1" || cached.URL != "https://api.github.com/repos/"+repo+"/tarball/"+p.HeadSHA || !hexSHA.MatchString(cached.SHA256) {
		return false, c.ErrInvalid
	}
	if candidate != nil && *candidate != cached {
		return false, errors.New("cached artifact no longer matches metadata")
	}
	p.QAArtifact = &cached
	return true, nil
}
