package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

type sourceStackFixture struct {
	Tree                 string
	PR, Reviews, Commits json.RawMessage
	Binding              *c.NativeTaskBinding
	Registration         *c.ArtifactLink
	archive              []byte
	digest               string
}

type sourceStackProvider struct {
	mu                        sync.Mutex
	tasks                     map[string]c.ProviderTask
	results                   map[string]c.ProviderTask
	policy                    string
	creates, binds, completes int
}

func (p *sourceStackProvider) Capabilities() c.TaskCapabilities {
	return c.TaskCapabilities{Provider: "kata", ProjectUID: "01M69M973PWDTSMG3GYRQM6R74", PolicySHA: p.policy}
}
func (p *sourceStackProvider) Read(_ context.Context, ref string) (c.ProviderTask, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	task, ok := p.tasks[ref]
	if !ok {
		return task, c.ErrNotFound
	}
	return task, nil
}
func (p *sourceStackProvider) Create(_ context.Context, in c.TaskCreateInput, _ c.Principal) (c.ProviderTask, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	ref, err := c.KataTaskReference(p.Capabilities().ProjectUID, fmt.Sprintf("%026d", p.creates), in.Repository)
	if err != nil {
		return c.ProviderTask{}, err
	}
	task := c.ProviderTask{Reference: ref, ProjectUID: p.Capabilities().ProjectUID, Repository: in.Repository, Title: in.Title, Body: in.Body, State: "open", Goal: in.Goal, Revision: 1}
	p.tasks[ref] = task
	return task, nil
}
func (p *sourceStackProvider) Bind(_ context.Context, in c.TaskBindInput, actor c.Principal) (c.ProviderTask, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	task := p.tasks[in.Binding.TaskURL]
	if task.Binding != nil && NativeAssignmentSHA(*task.Binding) == NativeAssignmentSHA(in.Binding) {
		return task, nil
	}
	p.binds++
	task.Binding = &in.Binding
	task.BindingActor = actor
	task.Revision++
	p.tasks[task.Reference] = task
	return task, nil
}
func (p *sourceStackProvider) Complete(_ context.Context, in c.TaskCompleteInput, actor c.Principal) (c.ProviderTask, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if actor.ID != "rook" || actor.GitHubBotID != c.DeliveryGitHubBotID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if task, ok := p.results[in.RequestID]; ok {
		return task, nil
	}
	task := p.tasks[in.Reference]
	if task.Binding == nil || NativeAssignmentSHA(*task.Binding) != in.AssignmentSHA {
		return task, c.ErrConflict
	}
	if task.State != "closed" {
		p.completes++
	}
	task.State, task.ClosedReason = "closed", "done"
	p.tasks[task.Reference] = task
	p.results[in.RequestID] = task
	return task, nil
}

type sourceStackReplay struct {
	t                   *testing.T
	data                map[int]*sourceStackFixture
	commands            *fakeCommands
	self                Actor
	active              int
	branch              string
	main                string
	retargeted, merged  map[int]bool
	reviews             map[int][]Review
	integrations        map[string]map[string]any
	writes              []string
	lost                string
	checks              string
	integrationRequired bool
}

func (r *sourceStackReplay) Run(ctx context.Context, args, env []string, in io.Reader) (CommandResult, error) {
	return r.commands.Run(ctx, args, env, in)
}
func (r *sourceStackReplay) RunIn(_ context.Context, _ string, args, _ []string, _ io.Reader) (CommandResult, error) {
	if len(args) < 2 || args[0] != "git" {
		r.t.Fatal("unexpected local source command", args)
	}
	var out []byte
	switch args[1] {
	case "clone", "switch", "status", "fetch", "reset":
	case "config":
		out = []byte("https://github.com/sarcasticbird/coop.git")
		if strings.Contains(strings.Join(args, " "), "pushurl") {
			out = []byte("/dev/null/coop-qa-push-disabled")
		}
	case "remote":
		out = []byte("/dev/null/coop-qa-push-disabled")
	case "branch":
		out = []byte(r.branch)
	case "rev-parse":
		out = []byte(r.head(r.active))
	case "merge-tree":
		if r.active == 160 && r.integrationRequired {
			return CommandResult{ExitCode: 1}, nil
		}
		out = []byte(r.data[r.active].Tree)
	case "archive":
		out = r.data[r.active].archive
	default:
		r.t.Fatal("unapproved source checkout effect", args)
	}
	return CommandResult{Stdout: out}, nil
}
func (r *sourceStackReplay) head(n int) string {
	var p struct{ Head struct{ SHA string } }
	_ = json.Unmarshal(r.data[n].PR, &p)
	return p.Head.SHA
}
func (r *sourceStackReplay) response(_ string, endpoint, method string, input []byte) (CommandResult, error) {
	if endpoint == "graphql" {
		var req struct {
			Query     string
			Variables map[string]any
		}
		if json.Unmarshal(input, &req) != nil {
			r.t.Fatal("bad source graphql")
		}
		if strings.Contains(req.Query, "WorkerIdentity") {
			return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "User", "databaseId": r.self.ID, "login": strings.TrimSuffix(r.self.Login, "[bot]")}}}), nil
		}
		if strings.Contains(req.Query, "WorkerNativeBase") {
			return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"ref": map[string]any{"name": "main", "target": map[string]any{"oid": r.main}}}}}), nil
		}
		n := r.active
		if number, ok := req.Variables["number"].(float64); ok {
			n = int(number)
		}
		if strings.Contains(req.Query, "WorkerTeamCommits") {
			var data map[string]any
			_ = json.Unmarshal(r.data[n].Commits, &data)
			connection := data["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["commits"].(map[string]any)
			nodes := connection["nodes"].([]any)
			if r.retargeted[n] {
				prior := []int{158, 159}
				if n == 159 {
					prior = []int{158}
				}
				if n == 162 {
					prior = append(prior, 161)
				}
				for _, number := range prior {
					var earlier map[string]any
					_ = json.Unmarshal(r.data[number].Commits, &earlier)
					priorNodes := earlier["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["commits"].(map[string]any)["nodes"].([]any)
					nodes = append(nodes, priorNodes...)
				}
				connection["nodes"], connection["totalCount"] = nodes, len(nodes)
			}
			for _, node := range nodes {
				delete(node.(map[string]any)["commit"].(map[string]any), "authors")
			}
			return jsonResponse(data), nil
		}
		if strings.Contains(req.Query, "WorkerTeamAuthors") {
			for _, f := range r.data {
				var data map[string]any
				_ = json.Unmarshal(f.Commits, &data)
				nodes := data["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["commits"].(map[string]any)["nodes"].([]any)
				for _, node := range nodes {
					commit := node.(map[string]any)["commit"].(map[string]any)
					if commit["id"] == req.Variables["id"] {
						return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"authors": commit["authors"]}}}), nil
					}
				}
			}
			r.t.Fatal("unknown fixture commit identity")
		}
		if strings.Contains(req.Query, "WorkerTeamReviews") {
			var original []struct {
				ID          int64
				CommitID    string `json:"commit_id"`
				Body, State string
				SubmittedAt time.Time `json:"submitted_at"`
				User        restActor
			}
			_ = json.Unmarshal(r.data[n].Reviews, &original)
			nodes := []any{}
			for _, rev := range original {
				nodes = append(nodes, map[string]any{"fullDatabaseId": fmt.Sprint(rev.ID), "commit": map[string]any{"oid": rev.CommitID}, "body": rev.Body, "state": rev.State, "submittedAt": rev.SubmittedAt, "author": map[string]any{"__typename": rev.User.Kind, "databaseId": rev.User.ID, "login": rev.User.Login}})
			}
			for _, rev := range r.reviews[n] {
				nodes = append(nodes, map[string]any{"fullDatabaseId": rev.ID, "commit": map[string]any{"oid": rev.HeadSHA}, "body": rev.Body, "state": rev.State, "submittedAt": rev.SubmittedAt, "author": map[string]any{"__typename": rev.Author.Kind, "databaseId": rev.Author.ID, "login": rev.Author.Login}})
			}
			return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviews": map[string]any{"totalCount": len(nodes), "nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}}), nil
		}
		r.t.Fatal("unexpected source GraphQL read", req.Query)
	}
	if strings.HasPrefix(endpoint, "user/") {
		id := strings.TrimPrefix(endpoint, "user/")
		kind, login := "Bot", "sarcastic-bird-hawk[bot]"
		if id == "6818256" {
			kind, login = "User", "sarcasticbird"
		}
		if id == "342161151" {
			login = "sarcastic-bird-robin[bot]"
		}
		if id == "343005406" {
			login = "winged-keys-wren[bot]"
		}
		var number int64
		_, _ = fmt.Sscan(id, &number)
		return jsonResponse(map[string]any{"id": number, "type": kind, "login": login}), nil
	}
	prefix := "repos/sarcasticbird/coop/"
	if strings.HasPrefix(endpoint, prefix+"tarball/") {
		for n, f := range r.data {
			if strings.TrimPrefix(endpoint, prefix+"tarball/") == r.head(n) {
				return CommandResult{Stdout: append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n"), f.archive...)}, nil
			}
		}
	}
	if strings.HasPrefix(endpoint, prefix+"git/commits/") {
		sha := strings.TrimPrefix(endpoint, prefix+"git/commits/")
		if commit, ok := r.integrations[sha]; ok {
			return jsonResponse(commit), nil
		}
		for n, f := range r.data {
			if sha == r.head(n) {
				return jsonResponse(map[string]any{"sha": sha, "tree": map[string]any{"sha": f.Tree}}), nil
			}
		}
	}
	if strings.HasPrefix(endpoint, prefix+"compare/") {
		parts := strings.Split(strings.TrimPrefix(endpoint, prefix+"compare/"), "...")
		status := "diverged"
		if _, ok := r.integrations[parts[0]]; ok {
			status = "ahead"
			if parts[0] == parts[1] {
				status = "identical"
			}
		}
		return jsonResponse(map[string]any{"status": status, "merge_base_commit": map[string]any{"sha": parts[0]}}), nil
	}
	if strings.HasSuffix(endpoint, "/status") {
		state := r.checks
		if state == "" {
			state = "success"
		}
		return jsonResponse(map[string]any{"state": state, "total_count": 1}), nil
	}
	if strings.Contains(endpoint, "/check-runs?") {
		return jsonResponse(map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"status": "completed", "conclusion": "success"}}}), nil
	}
	var n int
	if _, err := fmt.Sscanf(endpoint, prefix+"pulls/%d", &n); err == nil && r.data[n] != nil {
		if method != "GET" {
			r.writes = append(r.writes, method+" "+endpoint)
			var intent map[string]any
			_ = json.Unmarshal(input, &intent)
			switch {
			case method == "POST" && strings.HasSuffix(endpoint, "/reviews"):
				if intent["commit_id"] != r.head(n) {
					r.t.Fatal("review not exact-head")
				}
				id := int64(900000 + len(r.writes))
				state := "APPROVED"
				if intent["event"] == "REQUEST_CHANGES" {
					state = "CHANGES_REQUESTED"
				}
				r.reviews[n] = append(r.reviews[n], Review{ID: fmt.Sprint(id), HeadSHA: r.head(n), Body: intent["body"].(string), State: state, Author: r.self, SubmittedAt: time.Now().UTC()})
				if r.lost == "review" {
					return CommandResult{}, c.ErrUnavailable
				}
				return jsonResponse(map[string]any{"id": id}), nil
			case method == "PATCH":
				if intent["base"] != "main" || r.retargeted[n] {
					r.t.Fatal("wrong/repeated retarget")
				}
				r.retargeted[n] = true
				if r.lost == "retarget" {
					return CommandResult{}, c.ErrUnavailable
				}
				return jsonResponse(map[string]any{}), nil
			case method == "PUT" && strings.HasSuffix(endpoint, "/merge"):
				if intent["sha"] != r.head(n) || intent["merge_method"] != "squash" || r.merged[n] || r.self.ID != c.DeliveryGitHubBotID {
					r.t.Fatal("wrong/repeated/unassigned merge")
				}
				before := r.main
				r.main = fmt.Sprintf("%040x", n+10000)
				r.merged[n] = true
				r.integrations[r.main] = map[string]any{"sha": r.main, "tree": map[string]any{"sha": r.data[n].Tree}, "parents": []any{map[string]any{"sha": before}}}
				if r.lost == "merge" {
					return CommandResult{}, c.ErrUnavailable
				}
				return jsonResponse(map[string]any{"merged": true, "sha": r.main}), nil
			default:
				r.t.Fatal("unapproved external write", method, endpoint)
			}
		}
		var pr map[string]any
		_ = json.Unmarshal(r.data[n].PR, &pr)
		if r.retargeted[n] {
			pr["base"].(map[string]any)["ref"] = "main"
			pr["base"].(map[string]any)["sha"] = r.main
		}
		if r.merged[n] {
			pr["merged"], pr["state"], pr["merge_commit_sha"] = true, "closed", fmt.Sprintf("%040x", n+10000)
		}
		pr["mergeable"], pr["mergeable_state"] = true, "clean"
		return jsonResponse(pr), nil
	}
	r.t.Fatal("unexpected source fixture boundary", method, endpoint)
	return CommandResult{}, c.ErrNotFound
}

type sourceStackJourney struct {
	t                *testing.T
	replay           *sourceStackReplay
	g                *GitHubClient
	provider         *sourceStackProvider
	service          *master.Service
	clients          map[string]*masterclient.Client
	configs          map[string]Config
	goal             c.GoalAuthority
	ownerConfig, cli string
	decisions        map[string]c.SourceDecision
}

func newSourceStackJourney(t *testing.T) *sourceStackJourney {
	t.Helper()
	root := t.TempDir()
	masterID := "coop-master-20261031"
	team := &TeamPolicy{Version: 2, Planner: "rook", EngineeringPeers: map[string]string{"hawk": "robin", "robin": "hawk"}, Members: []TeamMember{{Name: "hawk", Actor: Actor{ID: 342159661, Kind: "Bot", Login: "sarcastic-bird-hawk[bot]"}, Capabilities: []string{"implement", "engineering-review"}}, {Name: "robin", Actor: Actor{ID: 342161151, Kind: "Bot", Login: "sarcastic-bird-robin[bot]"}, Capabilities: []string{"implement", "engineering-review"}}, {Name: "wren", Actor: Actor{ID: 343005406, Kind: "Bot", Login: "winged-keys-wren[bot]"}, Capabilities: []string{"customer-qa"}}, {Name: "rook", Actor: Actor{ID: c.DeliveryGitHubBotID, Kind: "Bot", Login: "winged-keys-rook[bot]"}, Capabilities: []string{"plan", "deliver"}}}}
	policy, err := TeamPolicySHA(*team)
	if err != nil {
		t.Fatal(err)
	}
	provider := &sourceStackProvider{policy: policy, tasks: map[string]c.ProviderTask{}, results: map[string]c.ProviderTask{}}
	replay := &sourceStackReplay{t: t, data: map[int]*sourceStackFixture{}, retargeted: map[int]bool{}, merged: map[int]bool{}, reviews: map[int][]Review{}, integrations: map[string]map[string]any{}, main: "306bfd0a3e7ae76fdd00635553e399941cec6394", active: 158}
	for n := 158; n <= 162; n++ {
		raw, err := os.ReadFile(fmt.Sprintf("testdata/source_stack/pr%d.json", n))
		if err != nil {
			t.Fatal(err)
		}
		var f sourceStackFixture
		if err = json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		f.archive, err = os.ReadFile(fmt.Sprintf("testdata/source_stack/pr%d.tar.gz", n))
		if err != nil {
			t.Fatal(err)
		}
		f.digest, err = sourceBundleSHA(f.archive)
		if err != nil {
			t.Fatal(err)
		}
		if n == 161 && f.digest != "8c206e9e3fe590dd6a1e861fcd122b453ac4aa00b6b8e16934c2680e9d1c5f6e" || n == 162 && f.digest != "28b88ca31594d9283b8d5e19ee88ac71d602a4358acaf61378f349371eb2d408" {
			t.Fatal("original published bundle changed", n, f.digest)
		}
		replay.data[n] = &f
		if f.Binding != nil {
			provider.tasks[f.Binding.TaskURL] = c.ProviderTask{Reference: f.Binding.TaskURL, Repository: "sarcasticbird/coop", Goal: f.Binding.Goal, Binding: f.Binding, State: "open"}
		}
		t.Logf("PR%d original source %s tree %s source-bundle-v1 %s", n, replay.head(n), f.Tree, f.digest)
	}
	dbPath := filepath.Join(root, "master.sqlite")
	store, err := master.OpenStore(t.Context(), master.StoreConfig{Path: dbPath, MasterID: masterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principals := []c.Principal{{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"uat"}, Capabilities: []string{"read", "send", "manage", "decide"}}}
	tokens := []master.Token{{PrincipalID: "owner", SHA256: BodyHash("stack-owner-token")}}
	var placements []c.Placement
	for _, m := range team.Members {
		caps := []string{"read", "send"}
		if m.Name == "rook" {
			caps = append(caps, "plan")
		}
		principals = append(principals, c.Principal{ID: m.Name, Alias: m.Name, Kind: "agent", GitHubBotID: m.Actor.ID, Engagements: []string{"uat"}, Capabilities: caps})
		instance := BodyHash("stack-" + m.Name)
		placements = append(placements, c.Placement{AgentID: m.Name, HostID: "fixture-host", InstanceID: instance, Generation: 1})
		tokens = append(tokens, master.Token{PrincipalID: m.Name, InstanceID: instance, Generation: 1, SHA256: BodyHash("stack-" + m.Name + "-token")})
	}
	if err = store.Configure(t.Context(), principals, placements); err != nil {
		t.Fatal(err)
	}
	svc := &master.Service{Store: store, Tasks: provider}
	server := httptest.NewServer(master.NewHandler(svc, &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: tokens}))
	t.Cleanup(server.Close)
	f := &sourceStackJourney{t: t, replay: replay, g: NewGitHubClient(replay), provider: provider, service: svc, clients: map[string]*masterclient.Client{}, configs: map[string]Config{}, decisions: map[string]c.SourceDecision{}}
	for _, p := range principals {
		client, err := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: masterID, Token: "stack-" + p.ID + "-token", AllowHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		f.clients[p.ID] = client
		tokenPath := filepath.Join(root, p.ID+".token")
		if err = os.WriteFile(tokenPath, []byte("stack-"+p.ID+"-token"), 0600); err != nil {
			t.Fatal(err)
		}
		clientPath := filepath.Join(root, p.ID+".toml")
		if err = os.WriteFile(clientPath, fmt.Appendf(nil, "url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", server.URL, masterID, tokenPath), 0600); err != nil {
			t.Fatal(err)
		}
		if p.ID == "owner" {
			f.ownerConfig = clientPath
			continue
		}
		cfg, _, _, _ := dispatchFixture(t)
		cfg.Name, cfg.Team, cfg.CodeOwners, cfg.Repositories = p.ID, team, []string{"sarcasticbird"}, []string{"sarcasticbird/coop"}
		cfg.Projects, cfg.ProjectOwners = nil, nil
		cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: provider.Capabilities().ProjectUID}
		cfg.Master = &MasterConfig{Backend: "master", MasterID: masterID, AgentID: p.ID, Engagement: "uat", ClientConfig: clientPath, HostID: "fixture-host", InstanceID: BodyHash("stack-" + p.ID), Generation: 1}
		cfg.StateDir, cfg.WorkRoot = filepath.Join(root, p.ID, "state"), filepath.Join(root, p.ID, "work")
		cfg.DeliveryHold = false
		for _, m := range team.Members {
			if m.Name == p.ID {
				cfg.Self = m.Actor
			}
		}
		saveNativeFixtureConnection(t, cfg)
		f.configs[p.ID] = cfg
	}
	thread, err := f.clients["owner"].CreateThread(t.Context(), c.CreateThread{Title: "Hermetic source stack", Engagement: "uat", Members: []string{"owner", "hawk", "robin", "wren", "rook"}, RequestID: "stack-thread"})
	if err != nil {
		t.Fatal(err)
	}
	proposal := c.GoalProposal{ThreadID: thread.ID, Purpose: "Hermetic explicit source intake and immutable main integration", Scope: "Only mocked external effects for exact retained source stack; no historical approvals invented", Repositories: []c.RepositoryScope{{Name: "sarcasticbird/coop", BaseBranches: []string{"main", "simplify/kata-first", "simplify/operational-cleanup", "agent/hawk/e0642638a17acc68"}}}, Acceptance: []string{"Exact source, independent renewed native gates, main target and Rook-only mocked delivery"}, AuthorID: "hawk", PeerID: "robin", QA: c.QAPolicy{Required: true, ActorID: "wren", Environment: "hermetic-stack", EnvironmentRevision: "v1", EvidenceDestination: c.EvidenceDestination{MasterID: masterID, ThreadID: thread.ID, Engagement: "uat"}}, RequestID: "stack-goal"}
	revision, err := f.clients["owner"].ProposeGoal(t.Context(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.clients["owner"].DecideGoal(t.Context(), c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "stack-approve"}); err != nil {
		t.Fatal(err)
	}
	f.goal, err = f.clients["owner"].ResolveGoal(t.Context(), revision.Ref)
	if err != nil {
		t.Fatal(err)
	}
	// Restore only OBSERVED native source registrations in this disposable DB.
	// These are historical identities, not new registrations or fabricated v3.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, fixture := range replay.data {
		if fixture.Registration != nil {
			link := fixture.Registration
			raw, _ := json.Marshal(link)
			if _, err = db.Exec("INSERT INTO artifacts(goal_id,revision,url,kind,assignment_sha,body) VALUES(?,?,?,?,?,?)", link.Goal.GoalID, link.Goal.RevisionSHA256, link.URL, link.Kind, link.AssignmentSHA, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	replay.commands = &fakeCommands{respond: replay.response}
	return f
}

func (f *sourceStackJourney) decision(n int, predecessor *c.SourceIntegrationReceipt) c.SourceDecisionInput {
	f.t.Helper()
	fixture := f.replay.data[n]
	var p struct {
		NodeID string `json:"node_id"`
		URL    string `json:"html_url"`
		Body   string
		User   restActor
		Head   struct {
			SHA, Ref string
			Repo     struct {
				ID       int64
				FullName string `json:"full_name"`
				Owner    restActor
			}
		}
		Base struct {
			SHA, Ref string
			Repo     struct{ ID int64 }
		}
	}
	if err := json.Unmarshal(fixture.PR, &p); err != nil {
		f.t.Fatal(err)
	}
	task, err := f.clients["rook"].TaskCreate(f.t.Context(), c.TaskCreateInput{Purpose: "source-gates", Goal: f.goal.Revision.Ref, ExpectedApprovalSequence: f.goal.ApprovalSequence, Repository: "sarcasticbird/coop", BaseBranch: p.Base.Ref, Title: fmt.Sprintf("Supplemental source gates PR%d", n), Body: "Gates only; existing product source is not reimplemented", RequestID: fmt.Sprintf("gates-pr%d", n)})
	if err != nil {
		f.t.Fatal(err)
	}
	s := c.SourceIdentity{Repository: "sarcasticbird/coop", RepositoryID: p.Base.Repo.ID, PRURL: p.URL, PRID: p.NodeID, AuthorID: p.User.ID, AuthorKind: p.User.Kind, OwnerID: p.Head.Repo.Owner.ID, SourceRepo: p.Head.Repo.FullName, SourceRepoID: p.Head.Repo.ID, Branch: p.Head.Ref, BaseBranch: p.Base.Ref, BaseSHA: p.Base.SHA, HeadSHA: p.Head.SHA, TreeSHA: fixture.Tree, BundleKind: "source-bundle-v1", BundleRevision: 1, BundleSHA: fixture.digest, PayloadSHA: fixture.digest, Contributors: []int64{p.User.ID}, ContributorsComplete: true, Evidence: []c.SourceEvidence{{ID: p.URL + "#body", Kind: "pr-body", BodySHA: BodyHash(p.Body)}}}
	kind := "intake"
	if predecessor != nil {
		for _, id := range f.decisions[predecessor.DecisionID].Input.Source.Contributors {
			if !slices.Contains(s.Contributors, id) {
				s.Contributors = append(s.Contributors, id)
			}
		}
		slices.Sort(s.Contributors)
	}
	if fixture.Binding != nil {
		b := fixture.Binding
		g := b.Goal
		s.OriginalGoal = &g
		s.OriginalTaskURL = b.TaskURL
		s.OriginalAssignmentSHA = NativeAssignmentSHA(*b)
		s.OriginalPolicySHA = b.PolicySHA
		s.OriginalRegistration = c.SourceDigest(fixture.Registration)
		kind = "transition"
	}
	evidence, err := f.clients["owner"].Send(f.t.Context(), c.SendMessage{ThreadID: f.goal.Revision.Proposal.ThreadID, Body: fmt.Sprintf("Explicit mocked source decision PR%d at %s to main %s", n, s.HeadSHA, f.replay.main), RequestID: fmt.Sprintf("owner-evidence-pr%d", n)})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.SourceDecisionInput{Kind: kind, Goal: f.goal.Revision.Ref, ApprovalSequence: f.goal.ApprovalSequence, GateTaskURL: task.Reference, GateAssignmentSHA: NativeAssignmentSHA(*task.Binding), PolicySHA: f.provider.policy, Source: s, Target: c.SourceTarget{Repository: s.Repository, RepositoryID: s.RepositoryID, Branch: "main", SHA: f.replay.main, ResultTreeSHA: fixture.Tree, ResultBundleSHA: fixture.digest, Predecessor: predecessor}, AcceptanceSHA: NativeAcceptanceSHA(f.goal), Environment: f.goal.Revision.Proposal.QA.Environment, EnvironmentRevision: f.goal.Revision.Proposal.QA.EnvironmentRevision, ReviewerID: "robin", QAID: "wren", EvidenceMessageID: evidence.MessageID, RequestID: fmt.Sprintf("source-pr%d", n)}
}

func (f *sourceStackJourney) ownerDecision(in c.SourceDecisionInput, cli bool) c.SourceDecision {
	f.t.Helper()
	if !cli {
		d, err := f.clients["owner"].CreateSourceDecision(f.t.Context(), in)
		if err != nil {
			f.t.Fatal("owner source decision", in.Kind, err)
		}
		f.decisions[d.ID] = d
		return d
	}
	if f.cli == "" {
		f.cli = filepath.Join(f.t.TempDir(), "coop")
		cmd := exec.Command("go", "build", "-o", f.cli, "./cmd/coop")
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatal("build CLI", err, string(out))
		}
	}
	raw, _ := json.Marshal(in)
	path := filepath.Join(f.t.TempDir(), "source.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(f.cli, "--master-config", f.ownerConfig, "task", "source-decision", "--file", path)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatal("owner CLI", err, string(raw))
	}
	var d c.SourceDecision
	if err = json.Unmarshal(raw, &d); err != nil {
		f.t.Fatal(err)
	}
	f.decisions[d.ID] = d
	return d
}

func (f *sourceStackJourney) stage(n int, actor, kind string, d c.SourceDecision) (NativeActionResult, error) {
	f.t.Helper()
	cfg := f.configs[actor]
	f.replay.self, f.replay.active = cfg.Self, n
	cat, err := f.g.NativeSnapshot(f.t.Context(), cfg, f.clients[actor])
	if err != nil {
		return NativeActionResult{}, err
	}
	var v NativeCandidate
	for _, candidate := range NativeCandidates(cfg, cat, nil, time.Now()) {
		if candidate.Job.Kind == kind && candidate.SourceDecision != nil && candidate.SourceDecision.ID == d.ID {
			v = candidate
		}
	}
	if v.Job.ID == "" {
		return NativeActionResult{}, fmt.Errorf("missing assigned %s for %s", kind, d.ID)
	}
	f.replay.branch = "agent/" + actor + "/" + BindingKey(cfg, v.Job)[:16]
	checkout := NewCheckouts(cfg, f.replay)
	job, err := checkout.Prepare(f.t.Context(), v.Job)
	if err != nil {
		return NativeActionResult{}, err
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: d.Input.Goal, Job: job, Phase: "active"}
	if err = saveNativeExecution(cfg, x); err != nil {
		return NativeActionResult{}, err
	}
	prompt, err := nativePrompt(cfg, x, v)
	if err != nil || !strings.Contains(prompt, d.ID) {
		return NativeActionResult{}, fmt.Errorf("source prompt binding: %w", err)
	}
	action := NativeAction{Operation: kind, RequestID: fmt.Sprintf("%s-%d-%s", kind, n, d.ID), Verdict: "approved", Body: "Fresh independent source/target integration assessment", ReportBody: "Private exact-artifact integration QA in hermetic-stack/v1"}
	result, err := RunNativeAction(f.t.Context(), cfg, f.clients[actor], f.g, job.ID, action)
	if err != nil {
		return result, err
	}
	writes := len(f.replay.writes)
	again, err := RunNativeAction(f.t.Context(), cfg, f.clients[actor], f.g, job.ID, action)
	if err != nil || again != result || len(f.replay.writes) != writes {
		f.t.Fatal("same request repeated effect", kind, err)
	}
	return result, nil
}

func (f *sourceStackJourney) gates(n int, d c.SourceDecision) c.SourceDecisionInput {
	f.t.Helper()
	review, err := f.stage(n, "robin", "source-review", d)
	if err != nil {
		f.t.Fatal("review", n, err)
	}
	qa, err := f.stage(n, "wren", "source-qa", d)
	if err != nil {
		f.t.Fatal("QA", n, err)
	}
	in := d.Input
	in.Kind, in.ParentID, in.ActorID = "deliver", d.ID, "rook"
	in.RequestID = "execute-" + d.ID
	in.EngineeringReportID, in.EngineeringBodySHA = review.EvidenceMessageID, review.EvidenceBodySHA
	in.QAReportID, in.QABodySHA = qa.EvidenceMessageID, qa.EvidenceBodySHA
	return in
}

func TestSourceStackProductionJourney(t *testing.T) {
	f := newSourceStackJourney(t)
	var predecessor *c.SourceIntegrationReceipt
	var sibling c.SourceDecision
	for _, n := range []int{158, 159, 161, 162} {
		in := f.decision(n, predecessor)
		d := f.ownerDecision(in, n == 158)
		if retry := f.ownerDecision(in, false); retry.ID != d.ID {
			t.Fatal("registration retry duplicated intake")
		}
		execution := f.gates(n, d)
		if n != 158 {
			retarget := execution
			retarget.Kind, retarget.RequestID = "retarget", "retarget-"+d.ID
			grant := f.ownerDecision(retarget, false)
			if _, err := f.stage(n, "rook", "source-retarget", grant); err != nil {
				t.Fatal("retarget", n, err)
			}
		}
		grant := f.ownerDecision(execution, false)
		if _, err := f.stage(n, "rook", "source-deliver", grant); err != nil {
			t.Fatal("deliver", n, err)
		}
		effects, err := f.clients["rook"].SourceEffects(t.Context(), c.SourceDecisionValidation{Goal: in.Goal, ID: grant.ID})
		if err != nil || len(effects) != 1 {
			t.Fatal("missing unique terminal integration", err)
		}
		predecessor = &c.SourceIntegrationReceipt{Goal: in.Goal, ID: "si_" + grant.ID, DecisionID: grant.ID, PRURL: in.Source.PRURL, SourceSHA: c.SourceDigest(in.Source), TargetRepoID: in.Target.RepositoryID, TargetBranch: "main", BeforeSHA: in.Target.SHA, CommitSHA: effects[0].CommitSHA, TreeSHA: effects[0].TreeSHA, RequestID: effects[0].RequestID}
		if f.replay.main != predecessor.CommitSHA || predecessor.TreeSHA != f.replay.data[n].Tree {
			t.Fatal("source did not integrate into mocked main", n)
		}
		if n == 159 {
			sibling = f.ownerDecision(f.decision(160, predecessor), false)
		}
	}
	if f.provider.creates != 5 || f.provider.binds != 5 || f.provider.completes != 4 {
		t.Fatal("duplicated gate task or completion", f.provider.creates, f.provider.binds, f.provider.completes)
	}
	if len(f.replay.writes) != 15 {
		t.Fatal("expected eight new reviews, three retargets and four merges", f.replay.writes)
	}
	for _, n := range []int{161, 162} {
		fixture := f.replay.data[n]
		task, err := f.provider.Read(t.Context(), fixture.Binding.TaskURL)
		if err != nil || NativeAssignmentSHA(*task.Binding) != NativeAssignmentSHA(*fixture.Binding) {
			t.Fatal("native source assignment rewritten", n, err)
		}
	}
	if f.replay.merged[160] || f.replay.retargeted[160] {
		t.Fatal("sibling PR160 became prerequisite")
	}
	before := len(f.replay.writes)
	if _, err := f.stage(160, "robin", "source-review", sibling); err == nil {
		t.Fatal("stale sibling target admitted after intervening main integrations")
	}
	refresh := sibling.Input
	refresh.Kind, refresh.ParentID, refresh.RequestID = "transition", sibling.ID, "sibling-refresh"
	refresh.Target.SHA = f.replay.main
	current := f.ownerDecision(refresh, false)
	f.replay.integrationRequired = true
	if _, err := f.stage(160, "robin", "source-review", current); err == nil || !strings.Contains(err.Error(), "author integration") {
		t.Fatal("conflicting sibling must stop for explicit author integration", err)
	}
	if len(f.replay.writes) != before || f.replay.retargeted[160] || f.replay.merged[160] {
		t.Fatal("sibling refresh wrote source/target without author stage")
	}
}

func TestSourceProductionZeroEffectDriftMatrix(t *testing.T) {
	for _, mode := range []string{"head", "tree", "payload", "body", "contributors", "unknown-contributors", "pending-checks", "failed-checks", "missing-checks", "policy", "approval", "acceptance", "environment", "target", "revoked", "owner-disabled", "assessor-disabled", "hold", "foreign-retarget"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceStackJourney(t)
			in := f.decision(158, nil)
			if mode == "payload" {
				in.Source.BundleSHA = BodyHash("wrong canonical source")
				in.Source.PayloadSHA = in.Source.BundleSHA
			}
			if mode == "tree" {
				in.Source.TreeSHA = strings.Repeat("b", 40)
			}
			d := f.ownerDecision(in, false)
			cfg := f.configs["robin"]
			f.replay.self = cfg.Self
			switch mode {
			case "head", "body", "contributors", "unknown-contributors", "foreign-retarget":
				var pr map[string]any
				_ = json.Unmarshal(f.replay.data[158].PR, &pr)
				if mode == "head" {
					pr["head"].(map[string]any)["sha"] = strings.Repeat("a", 40)
				}
				if mode == "body" {
					pr["body"] = "foreign source body"
				}
				if mode == "foreign-retarget" {
					pr["base"].(map[string]any)["ref"] = "foreign"
				}
				if mode == "contributors" || mode == "unknown-contributors" {
					var data map[string]any
					_ = json.Unmarshal(f.replay.data[158].Commits, &data)
					nodes := data["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["commits"].(map[string]any)["nodes"].([]any)
					author := nodes[0].(map[string]any)["commit"].(map[string]any)["authors"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
					if mode == "unknown-contributors" {
						author["user"] = nil
					} else {
						author["user"].(map[string]any)["databaseId"] = float64(cfg.Self.ID)
					}
					f.replay.data[158].Commits, _ = json.Marshal(data)
				}
				f.replay.data[158].PR, _ = json.Marshal(pr)
			case "pending-checks":
				f.replay.checks = "pending"
			case "failed-checks":
				f.replay.checks = "failure"
			case "missing-checks":
				f.replay.checks = "missing"
			case "target":
				f.replay.main = strings.Repeat("a", 40)
			case "revoked":
				f.ownerDecision(c.SourceDecisionInput{Kind: "revoke", Goal: in.Goal, RevokeID: d.ID, RequestID: "revoke"}, false)
			case "owner-disabled", "assessor-disabled":
				id := "owner"
				caps := []string{"read", "send", "manage"}
				kind := "owner"
				bot := int64(0)
				if mode == "assessor-disabled" {
					id, kind, bot = "robin", "agent", 342161151
					caps = nil
				}
				if err := f.service.Store.Configure(t.Context(), []c.Principal{{ID: id, Alias: id, Kind: kind, GitHubBotID: bot, Engagements: []string{"uat"}, Capabilities: caps}}, nil); err != nil {
					t.Fatal(err)
				}
			case "hold":
				if err := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "policy":
				cfg.Team = &TeamPolicy{Version: 2, Planner: "rook", EngineeringPeers: cfg.Team.EngineeringPeers, Members: append([]TeamMember(nil), cfg.Team.Members...)}
				cfg.Team.Members[0].Capabilities = append(append([]string{}, cfg.Team.Members[0].Capabilities...), "plan")
				f.configs["robin"] = cfg
			case "approval", "acceptance", "environment":
				a := f.goal
				proposal := a.Revision.Proposal
				proposal.RequestID = "drift"
				switch mode {
				case "acceptance":
					proposal.Acceptance = []string{"different product"}
				case "environment":
					proposal.QA.EnvironmentRevision = "different"
				default:
					proposal.Scope += " revised"
				}
				revision, err := f.clients["owner"].ProposeGoal(t.Context(), proposal)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.clients["owner"].DecideGoal(t.Context(), c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "approve-drift"}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "hold" {
				f.configs["robin"] = cfg
			}
			if _, err := f.stage(158, "robin", "source-review", d); err == nil {
				t.Fatal("drift caused gate effect", mode)
			}
			if len(f.replay.writes) != 0 || f.provider.completes != 0 {
				t.Fatal("drift caused external write", mode, f.replay.writes)
			}
		})
	}
}

func TestSourceLostReviewResponseHeldAcrossRestart(t *testing.T) {
	f := newSourceStackJourney(t)
	d := f.ownerDecision(f.decision(158, nil), false)
	f.replay.lost = "review"
	if _, err := f.stage(158, "robin", "source-review", d); err == nil {
		t.Fatal("lost response falsely completed")
	}
	if len(f.replay.writes) != 1 {
		t.Fatal("expected exactly one review effect")
	}
	cfg := f.configs["robin"]
	cat, err := f.g.NativeSnapshot(t.Context(), cfg, f.clients["robin"])
	if err != nil {
		t.Fatal(err)
	}
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	var job Job
	for _, v := range jobs {
		if v.SourceDecision != nil && v.SourceDecision.ID == d.ID {
			job = v.Job
		}
	}
	request := fmt.Sprintf("source-review-%d-%s", 158, d.ID)
	for _, id := range []string{request, "replacement-request"} {
		if _, err := RunNativeAction(t.Context(), cfg, f.clients["robin"], NewGitHubClient(f.replay), job.ID, NativeAction{Operation: "source-review", RequestID: id, Verdict: "approved", Body: "Fresh independent source/target integration assessment", ReportBody: "Private exact-artifact integration QA in hermetic-stack/v1"}); err == nil {
			t.Fatal("uncertain effect replayed")
		}
	}
	before, err := os.ReadFile(nativeActionPath(cfg, job.ID, request))
	if err != nil {
		t.Fatal(err)
	}
	f.ownerDecision(c.SourceDecisionInput{Kind: "revoke", Goal: d.Input.Goal, RevokeID: d.ID, RequestID: "withdraw-uncertain"}, false)
	observed, err := ObserveSourceAction(t.Context(), cfg, NewGitHubClient(f.replay), job.ID, request)
	if err != nil || observed.State != "uncertain" {
		t.Fatal("read-only recovery", observed, err)
	}
	after, err := os.ReadFile(nativeActionPath(cfg, job.ID, request))
	if err != nil || string(before) != string(after) || len(f.replay.writes) != 1 {
		t.Fatal("observation repaired or replayed uncertain effect", err)
	}
}
