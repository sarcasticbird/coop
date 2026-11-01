package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// Only the task provider and GitHub boundaries are simulated. Master HTTP,
// authentication, authorization, hashing, private reports and worker helpers are
// production implementations. Historical records come from the captured source.
type pr9Provider struct {
	task                      c.ProviderTask
	policy                    string
	creates, binds, completes int
}

func (p *pr9Provider) Capabilities() c.TaskCapabilities {
	return c.TaskCapabilities{Provider: "kata", ProjectUID: p.task.ProjectUID, PolicySHA: p.policy}
}
func (p *pr9Provider) Read(_ context.Context, ref string) (c.ProviderTask, error) {
	if ref != pr9Task {
		return c.ProviderTask{}, c.ErrNotFound
	}
	return p.task, nil
}
func (p *pr9Provider) Create(context.Context, c.TaskCreateInput, c.Principal) (c.ProviderTask, error) {
	p.creates++
	return c.ProviderTask{}, c.ErrForbidden
}
func (p *pr9Provider) Bind(context.Context, c.TaskBindInput, c.Principal) (c.ProviderTask, error) {
	p.binds++
	return c.ProviderTask{}, c.ErrForbidden
}
func (p *pr9Provider) Complete(_ context.Context, in c.TaskCompleteInput, actor c.Principal) (c.ProviderTask, error) {
	if in.Reference != pr9Task || in.AssignmentSHA != pr9Assignment || in.PullRequestURL != pr9URL || actor.ID != "rook" || actor.GitHubBotID != c.DeliveryGitHubBotID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	p.completes++
	p.task.State, p.task.ClosedReason = "closed", "done"
	return p.task, nil
}

type pr9Replay struct {
	t           *testing.T
	data        pr9FixtureData
	archive     []byte
	self        Actor
	merged      bool
	writes      []string
	commands    *fakeCommands
	branch      string
	unavailable int
	afterMerge  func()
}

func (r *pr9Replay) Run(ctx context.Context, argv, env []string, input io.Reader) (CommandResult, error) {
	return r.commands.Run(ctx, argv, env, input)
}
func (r *pr9Replay) RunIn(_ context.Context, _ string, argv, _ []string, _ io.Reader) (CommandResult, error) {
	if len(argv) < 2 || argv[0] != "git" {
		r.t.Fatal("unexpected checkout command", argv)
	}
	out := ""
	switch argv[1] {
	case "clone", "switch", "status":
	case "fetch":
		if len(argv) != 4 || argv[3] != "refs/pull/9/head" {
			r.t.Fatal("checkout fetched something other than exact PR9", argv)
		}
	case "reset":
		if len(argv) != 4 || argv[2] != "--hard" || argv[3] != "FETCH_HEAD" {
			r.t.Fatal("checkout reset not bound to verified head", argv)
		}
	case "branch":
		out = r.branch
	case "rev-parse":
		out = pr9Head
	case "config":
		out = "https://github.com/sarcasticbird/gh-sweep.git"
		if strings.Contains(strings.Join(argv, " "), "pushurl") {
			out = "/dev/null/coop-qa-push-disabled"
		}
	case "remote":
		out = "/dev/null/coop-qa-push-disabled"
	default:
		r.t.Fatal("unapproved checkout effect", argv)
	}
	return CommandResult{Stdout: []byte(out)}, nil
}

func pr9SameInput(a, b []byte) bool {
	if bytes.Equal(a, []byte("null")) {
		a = nil
	}
	if bytes.Equal(b, []byte("null")) {
		b = nil
	}
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var left, right bytes.Buffer
	return json.Compact(&left, a) == nil && json.Compact(&right, b) == nil && bytes.Equal(left.Bytes(), right.Bytes())
}
func (r *pr9Replay) response(_ string, endpoint, method string, input []byte) (CommandResult, error) {
	if r.unavailable > 0 {
		r.unavailable--
		return CommandResult{}, c.ErrUnavailable
	}
	if method != "GET" && endpoint != "graphql" {
		r.writes = append(r.writes, method+" "+endpoint)
		if method == "PUT" && endpoint == "repos/sarcasticbird/gh-sweep/pulls/9/merge" {
			var in map[string]string
			if json.Unmarshal(input, &in) != nil || in["sha"] != pr9Head || in["merge_method"] != "squash" || r.merged {
				r.t.Fatal("wrong or repeated merge")
			}
			r.merged = true
			if r.afterMerge != nil {
				r.afterMerge()
			}
			return jsonResponse(map[string]any{"merged": true}), nil
		}
		r.t.Fatalf("unapproved simulated GitHub mutation %s %s", method, endpoint)
	}
	if endpoint == "graphql" && bytes.Contains(input, []byte("WorkerIdentity")) {
		return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "User", "databaseId": r.self.ID, "login": strings.TrimSuffix(r.self.Login, "[bot]")}}}), nil
	}
	if endpoint == "repos/sarcasticbird/gh-sweep/tarball/"+pr9Head {
		return CommandResult{Stdout: append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n"), r.archive...)}, nil
	}
	if endpoint == "user/342161151" {
		return jsonResponse(map[string]any{"id": r.data.PR.Author.ID, "login": r.data.PR.Author.Login, "type": "Bot"}), nil
	}
	if endpoint == "repos/sarcasticbird/gh-sweep/git/commits/"+strings.Repeat("f", 40) {
		return jsonResponse(map[string]any{"sha": strings.Repeat("f", 40), "parents": []any{map[string]any{"sha": pr9Base}}}), nil
	}
	for _, read := range r.data.Reads {
		if read.Endpoint != endpoint || !pr9SameInput(read.Input, input) {
			continue
		}
		body := read.Body
		if r.merged {
			var object map[string]any
			if err := json.Unmarshal(body, &object); err != nil {
				r.t.Fatal(err)
			}
			if endpoint == "repos/sarcasticbird/gh-sweep/pulls/9" {
				object["merged"], object["state"], object["merge_commit_sha"] = true, "closed", strings.Repeat("f", 40)
			}
			if endpoint == "graphql" && bytes.Contains(input, []byte("WorkerNativePR")) {
				pr := object["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)
				pr["merged"], pr["state"] = true, "MERGED"
			}
			body, _ = json.Marshal(object)
		}
		return CommandResult{Stdout: append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"), body...)}, nil
	}
	r.t.Fatal("unrecorded GitHub read; request payload withheld")
	return CommandResult{}, c.ErrNotFound
}

type pr9Journey struct {
	t           *testing.T
	data        pr9FixtureData
	provider    *pr9Provider
	replay      *pr9Replay
	github      *GitHubClient
	service     *master.Service
	clients     map[string]*masterclient.Client
	configs     map[string]Config
	ownerConfig string
	cli         string
}

func requirePR9PrivateFixture(t *testing.T) string {
	t.Helper()
	privatePath := os.Getenv("COOP_PR9_PRIVATE_FIXTURE")
	if privatePath == "" {
		t.Skip("exact original PR9 task/goal/private QA evidence is not distributed; set COOP_PR9_PRIVATE_FIXTURE to an external private capture; generic retained regressions remain enabled")
	}
	return privatePath
}

func loadPR9(t *testing.T) (pr9FixtureData, []byte) {
	t.Helper()
	var data pr9FixtureData
	privatePath := requirePR9PrivateFixture(t)
	repo, err := filepath.Abs("../..")
	if err != nil || !filepath.IsAbs(privatePath) {
		t.Fatal("private PR9 fixture must have an absolute path outside the repository")
	}
	rel, err := filepath.Rel(repo, filepath.Clean(privatePath))
	if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("private PR9 fixture must remain outside the repository")
	}
	raw, err := readPrivate(privatePath, MaxSnapshot)
	if err != nil {
		t.Fatal("cannot read protected external PR9 fixture; require a private regular file owned by this user")
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal("cannot decode external PR9 fixture; contents withheld")
	}
	archive, err := os.ReadFile("testdata/retained_pr9/source.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if data.Task.Binding == nil || NativeAssignmentSHA(*data.Task.Binding) != pr9Assignment || data.Task.Binding.TaskScopeSHA != pr9Scope || TaskScopeHash(data.Task.Title, data.Task.Body) != pr9Scope || data.Task.Binding.PolicySHA != pr9SourcePolicy || data.Task.Binding.ApprovalSequence != 1 || data.Task.Binding.BaseBranch != "main" || len(data.Task.Binding.DependsOn) != 0 {
		t.Fatal("fixture is not the original immutable assignment")
	}
	if data.Task.Binding.AuthorID != 342161151 || data.Task.Binding.PeerID != 342159661 || data.Task.Binding.QAID != 343005406 || data.Task.Reference != pr9Task || data.PR.URL != pr9URL || data.PR.HeadSHA != pr9Head || data.PR.BaseSHA != pr9Base {
		t.Fatal("fixture identities changed")
	}
	sha, err := sourceBundleSHA(archive)
	if err != nil || sha != pr9Artifact || data.PR.QAArtifact == nil || data.PR.QAArtifact.SHA256 != sha || data.PR.QAArtifact.Revision != "source-bundle-v1" || data.PR.QAArtifact.Kind != "source" || data.PR.QAArtifact.HeadSHA != pr9Head {
		t.Fatal("not the original source archive", sha, err)
	}
	if BodyHash(data.SourceReport.Body) != "1b5a21bc8339e204e10ff3cd998980e6ca96fcd6b308aa6633653c93cd9da992" {
		t.Fatal("original private evidence changed")
	}
	if data.Task.Goal != (c.GoalRef{Source: "master", MasterID: "coop-master-20261031", GoalID: "goal_A72JLAJEXNT2TOEXHXCRWZGXJU", RevisionSHA256: "94c8f3482a88ed3cb1a09f241ba8b28ecf7151e9aa7e4f284b0735d56ca5cdb5"}) || data.Authority.Revision.Ref != data.Task.Goal || NativeAcceptanceSHA(data.Authority) != "1d51ea800e437f6d1073b60f166749458d62dae8ca48964575a342860ed32244" {
		t.Fatal("original goal or acceptance changed")
	}
	return data, archive
}

func newPR9Journey(t *testing.T) *pr9Journey {
	t.Helper()
	data, archive := loadPR9(t)
	team := &TeamPolicy{Version: 2, Planner: "rook", EngineeringPeers: map[string]string{"robin": "hawk", "hawk": "robin"}, Members: []TeamMember{
		{Name: "robin", Actor: Actor{ID: 342161151, Kind: "Bot", Login: "sarcastic-bird-robin[bot]"}, Capabilities: []string{"implement", "engineering-review"}},
		{Name: "hawk", Actor: Actor{ID: 342159661, Kind: "Bot", Login: "sarcastic-bird-hawk[bot]"}, Capabilities: []string{"implement", "engineering-review"}},
		{Name: "wren", Actor: Actor{ID: 343005406, Kind: "Bot", Login: "winged-keys-wren[bot]"}, Capabilities: []string{"customer-qa"}},
		{Name: "rook", Actor: Actor{ID: c.DeliveryGitHubBotID, Kind: "Bot", Login: "winged-keys-rook[bot]"}, Capabilities: []string{"plan", "deliver"}},
	}}
	policy, err := TeamPolicySHA(*team)
	if err != nil || policy == pr9SourcePolicy {
		t.Fatal("execution policy must differ from retained source", err)
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "master.sqlite")
	store, err := master.OpenStore(t.Context(), master.StoreConfig{Path: dbPath, MasterID: data.Task.Goal.MasterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principals := []c.Principal{{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"uat"}, Capabilities: []string{"read", "send", "manage", "decide"}}}
	placements := []c.Placement{}
	tokens := []master.Token{{PrincipalID: "owner", SHA256: BodyHash("pr9-owner-test-token")}}
	for _, member := range team.Members {
		caps := []string{"read", "send"}
		if member.Name == "rook" {
			caps = append(caps, "plan")
		}
		principals = append(principals, c.Principal{ID: member.Name, Alias: member.Name, Kind: "agent", GitHubBotID: member.Actor.ID, Engagements: []string{"uat"}, Capabilities: caps})
		instance := BodyHash("pr9-test-" + member.Name)
		placements = append(placements, c.Placement{AgentID: member.Name, HostID: "fixture-host", InstanceID: instance, Generation: 1})
		tokens = append(tokens, master.Token{PrincipalID: member.Name, InstanceID: instance, Generation: 1, SHA256: BodyHash("pr9-" + member.Name + "-test-token")})
	}
	if err = store.Configure(t.Context(), principals, placements); err != nil {
		t.Fatal(err)
	}
	// Restore historical identifiers only into this disposable database. The
	// proposal is then hashed and approved through the real authenticated API.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	thread := c.Thread{ID: data.Authority.Revision.Proposal.ThreadID, Title: "PR9 exact retained fixture", Engagement: "uat", Members: []string{"owner", "rook", "robin", "hawk", "wren"}}
	encode := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err = db.Exec("INSERT INTO threads(id,engagement,body) VALUES(?,?,?)", thread.ID, thread.Engagement, encode(thread)); err != nil {
		t.Fatal(err)
	}
	for _, member := range thread.Members {
		if _, err = db.Exec("INSERT INTO members(thread_id,principal_id) VALUES(?,?)", thread.ID, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO goals(id,thread_id,state) VALUES(?,?,'draft')", data.Task.Goal.GoalID, thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO messages(seq,id,thread_id,author_id,body) VALUES(?,?,?,?,?)", data.SourceReport.Sequence, data.SourceReport.ID, thread.ID, data.SourceReport.AuthorID, encode(data.SourceReport)); err != nil {
		t.Fatal(err)
	}
	provider := &pr9Provider{task: data.Task, policy: policy}
	svc := &master.Service{Store: store, Tasks: provider}
	server := httptest.NewServer(master.NewHandler(svc, &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: tokens}))
	t.Cleanup(server.Close)
	f := &pr9Journey{t: t, data: data, provider: provider, service: svc, clients: map[string]*masterclient.Client{}, configs: map[string]Config{}}
	for _, p := range principals {
		client, err := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: data.Task.Goal.MasterID, Token: "pr9-" + p.ID + "-test-token", AllowHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		f.clients[p.ID] = client
		clientPath := filepath.Join(root, p.ID+"-master.toml")
		tokenPath := filepath.Join(root, p.ID+"-token")
		if err = os.WriteFile(tokenPath, []byte("pr9-"+p.ID+"-test-token"), 0600); err != nil {
			t.Fatal(err)
		}
		clientBody := fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", server.URL, data.Task.Goal.MasterID, tokenPath)
		if err = os.WriteFile(clientPath, []byte(clientBody), 0600); err != nil {
			t.Fatal(err)
		}
		if p.ID == "owner" {
			f.ownerConfig = clientPath
			continue
		}
		cfg, _, _, _ := dispatchFixture(t)
		cfg.Name, cfg.Team, cfg.CodeOwners, cfg.Repositories = p.ID, team, []string{"sarcasticbird"}, []string{"sarcasticbird/gh-sweep"}
		cfg.Projects, cfg.ProjectOwners = nil, nil
		cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: data.Task.ProjectUID}
		cfg.Master.MasterID, cfg.Master.AgentID, cfg.Master.Engagement = data.Task.Goal.MasterID, p.ID, "uat"
		cfg.Master.Backend = "master"
		cfg.Master.ClientConfig, cfg.Master.HostID, cfg.Master.InstanceID = clientPath, "fixture-host", BodyHash("pr9-test-"+p.ID)
		cfg.StateDir, cfg.WorkRoot = filepath.Join(root, p.ID, "state"), filepath.Join(root, p.ID, "work")
		for _, m := range team.Members {
			if m.Name == p.ID {
				cfg.Self = m.Actor
			}
		}
		saveNativeFixtureConnection(t, cfg)
		f.configs[p.ID] = cfg
	}
	revision, err := f.clients["rook"].ProposeGoal(t.Context(), data.Authority.Revision.Proposal)
	if err != nil || revision.Ref != data.Task.Goal {
		t.Fatal("original proposal failed real master hashing", revision.Ref, err)
	}
	_, err = f.clients["owner"].DecideGoal(t.Context(), c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "fixture-approve"})
	if err != nil {
		t.Fatal(err)
	}
	for i, link := range []c.ArtifactLink{{Goal: revision.Ref, Kind: "task", URL: pr9Task, AssignmentSHA: pr9Assignment}, {Goal: revision.Ref, Kind: "pull_request", URL: pr9URL, TaskURL: pr9Task, AssignmentSHA: pr9Assignment}} {
		link.RequestID = fmt.Sprint("fixture-registration-", i)
		actor := "owner"
		if link.Kind == "pull_request" {
			actor = "robin"
		}
		if _, err = f.clients[actor].RegisterArtifact(t.Context(), link); err != nil {
			t.Fatal("register original", link.Kind, err)
		}
	}
	f.replay = &pr9Replay{t: t, data: data, archive: archive}
	f.replay.commands = &fakeCommands{respond: f.replay.response}
	f.github = NewGitHubClient(f.replay)
	return f
}

func (f *pr9Journey) as(name string) (Config, *masterclient.Client) {
	cfg := f.configs[name]
	f.replay.self = cfg.Self
	return cfg, f.clients[name]
}

func (f *pr9Journey) ownerHandoff(in c.HandoffInput, cli bool) c.Handoff {
	f.t.Helper()
	if !cli {
		h, err := f.clients["owner"].CreateHandoff(f.t.Context(), in)
		if err != nil {
			f.t.Fatal(err)
		}
		return h
	}
	if f.cli == "" {
		f.cli = filepath.Join(f.t.TempDir(), "coop")
		cmd := exec.Command("go", "build", "-o", f.cli, "./cmd/coop")
		cmd.Dir = "../.."
		if _, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatal("build CLI failed; subprocess output withheld", err)
		}
	}
	raw, _ := json.Marshal(in)
	input := filepath.Join(f.t.TempDir(), "handoff.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(f.cli, "--master-config", f.ownerConfig, "task", "authorize-delivery", "--file", input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatal("owner CLI handoff failed; output withheld", err)
	}
	var h c.Handoff
	if err = json.Unmarshal(out, &h); err != nil {
		f.t.Fatal("cannot decode owner CLI handoff; output withheld")
	}
	return h
}

func (f *pr9Journey) gates(cli bool) c.Handoff {
	f.t.Helper()
	a := f.data.Authority
	p := f.data.PR
	peer := nativeLatestReview(p, 342159661)
	qa := nativeLatestReview(p, 343005406)
	owner, err := f.clients["owner"].Send(f.t.Context(), c.SendMessage{ThreadID: a.Revision.Proposal.ThreadID, Body: "Synthetic test decision: renew exact retained PR9 source assertions, then permit only assigned Rook delivery after both native private gates. No source rewrite, release or restart.", RequestID: "fixture-owner-decision"})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.ownerHandoff(c.HandoffInput{Kind: "retained-gates", Goal: f.data.Task.Goal, TaskURL: pr9Task, PRURL: pr9URL, AssignmentSHA: pr9Assignment, HeadSHA: pr9Head, BaseSHA: pr9Base, BaseBranch: "main", TaskScopeSHA: pr9Scope, ApprovalSequence: 1, ArtifactSHA: pr9Artifact, PRScopeRevision: p.ScopeRevision, AcceptanceSHA: NativeAcceptanceSHA(a), PeerReviewID: peer.ID, QAReviewID: qa.ID, QAEvidenceMessageID: f.data.SourceReport.ID, QAEvidenceBodySHA: BodyHash(f.data.SourceReport.Body), Environment: a.Revision.Proposal.QA.Environment, EnvironmentRevision: a.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: owner.MessageID, RequestID: "fixture-retained-gates", Retained: c.RetainedDeliveryAuthority{SourcePolicySHA: pr9SourcePolicy, ExecutionPolicySHA: f.provider.policy, SourceTreeSHA: "88a6d8efe9db403584a8bf69bac874b41c926614", PayloadSHA: pr9Artifact, SourcePeerReviewID: peer.ID, SourcePeerBodySHA: BodyHash(peer.Body), SourceQAReviewID: qa.ID, SourceQABodySHA: BodyHash(qa.Body), SourceReportID: f.data.SourceReport.ID, SourceReportBodySHA: BodyHash(f.data.SourceReport.Body), SourceEnvironment: a.Revision.Proposal.QA.Environment, SourceEnvironmentRevision: a.Revision.Proposal.QA.EnvironmentRevision, DeliveryAcceptanceSHA: NativeAcceptanceSHA(a), Override: c.RetainedDeliveryOverride}}, cli)
}

func (f *pr9Journey) selected(name, kind string) (Config, *masterclient.Client, NativeExecution) {
	f.t.Helper()
	cfg, m := f.as(name)
	cat, err := f.github.NativeSnapshot(f.t.Context(), cfg, m)
	if err != nil {
		f.t.Fatal("real HTTP production discovery", name, err)
	}
	jobs := NativeCandidates(cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != kind {
		f.t.Fatalf("unexpected %s candidate count/stage (count=%d); private candidate details withheld", name, len(jobs))
	}
	v := jobs[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: f.data.Task.Goal, Binding: &v.Task.Binding, Job: v.Job, Phase: "selected"}
	x.Job.SessionID, x.Job.MessageID = "ses_pr9"+name, "msg_pr9"+name
	x.Job.Phase, x.Job.SubmissionState = "active", "accepted"
	if err = saveNativeExecution(cfg, x); err != nil {
		f.t.Fatal(err)
	}
	f.replay.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
	d, err := NewNativeDispatcher(cfg, m, f.github, &fakeOpenCode{running: true}, NewCheckouts(cfg, f.replay), RealClock{})
	if err != nil {
		f.t.Fatal("dispatcher", err)
	}
	if _, err = d.advance(f.t.Context(), &x); err != nil || x.Phase != "active" || x.Job.SubmissionState != "accepted" {
		f.t.Fatal("exact stage prepare/submit", kind, err, x.Phase)
	}
	return cfg, m, x
}

func (f *pr9Journey) ready(gates c.Handoff, cli bool) c.Handoff {
	f.t.Helper()
	in := gates.Input
	in.Kind, in.RequestID = "retained-deliver", "fixture-retained-deliver"
	for _, name := range []string{"hawk", "wren"} {
		kind := "delivery-review"
		if name == "wren" {
			kind = "delivery-qa"
		}
		cfg, m, x := f.selected(name, kind)
		result, err := RunNativeAction(f.t.Context(), cfg, m, f.github, x.Job.ID, NativeAction{Operation: "delivery-readiness", RequestID: "fixture-readiness", Verdict: "ready"})
		if err != nil {
			f.t.Fatal("native readiness", name, err)
		}
		report, err := m.Message(f.t.Context(), result.EvidenceMessageID)
		var body c.RetainedGateReport
		if err != nil || report.AuthorID != name || report.Imported || report.ThreadID != f.data.SourceReport.ThreadID || c.DecodeJSON([]byte(report.Body), &body) != nil || body.GateHandoffID != gates.ID || body.Binding != gates.Input || body.AssignmentSHA != pr9Assignment {
			f.t.Fatal("native report lost real authenticated author/binding", name, err)
		}
		if name == "hawk" {
			in.Retained.EngineeringReportID, in.Retained.EngineeringReportBodySHA = report.ID, BodyHash(report.Body)
		} else {
			in.Retained.QAReportID, in.Retained.QAReportBodySHA = report.ID, BodyHash(report.Body)
		}
		if name == "hawk" {
			if _, err := f.clients["owner"].CreateHandoff(f.t.Context(), in); err == nil {
				f.t.Fatal("one renewed gate authorized delivery without Wren QA")
			}
		}
	}
	if len(f.replay.writes) != 0 || f.provider.completes != 0 {
		f.t.Fatal("assessment caused an effect")
	}
	return f.ownerHandoff(in, cli)
}

func TestRetainedPR9ExactMasterWorkerCLIJourney(t *testing.T) {
	f := newPR9Journey(t)
	cfg, m := f.as("hawk")
	if _, err := m.TaskRead(t.Context(), pr9Task); err != nil {
		t.Fatal("master must permit original immutable read", err)
	}
	if _, err := f.github.ReadNativeTask(t.Context(), cfg, pr9Task); err == nil {
		t.Fatal("ordinary stale engineering read was permitted")
	}
	before, _ := json.Marshal(f.provider.task.Binding)
	gates := f.gates(true)
	grant := f.ready(gates, true)
	cfg, m, x := f.selected("rook", "deliver")
	if x.Job.Master.DeliveryHandoffID != grant.ID || x.Job.Master.AssignmentSHA != pr9Assignment || x.Binding.PolicySHA != pr9SourcePolicy {
		t.Fatal("delivery replaced immutable source")
	}
	result, err := RunNativeAction(t.Context(), cfg, m, f.github, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "fixture-original-delivery"})
	if err != nil || result.Phase != "completed" {
		t.Fatal("exact PR9 delivery", err)
	}
	if _, err = RunNativeAction(t.Context(), cfg, m, f.github, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "fixture-original-delivery"}); err != nil {
		t.Fatal("idempotent result", err)
	}
	if err = nativeConfirmDelivery(t.Context(), cfg, m, f.github, f.data.Authority, x); err != nil {
		t.Fatal("exact confirmation", err)
	}
	after, _ := json.Marshal(f.provider.task.Binding)
	if !bytes.Equal(before, after) || f.provider.creates != 0 || f.provider.binds != 0 || f.provider.completes != 1 || len(f.replay.writes) != 1 || f.replay.writes[0] != "PUT repos/sarcasticbird/gh-sweep/pulls/9/merge" || f.provider.task.Reference != pr9Task || f.provider.task.State != "closed" {
		t.Fatalf("wrong exact effects: create=%d bind=%d complete=%d github=%v", f.provider.creates, f.provider.binds, f.provider.completes, f.replay.writes)
	}
	// CLI task reads are immutable-source reads, not engineering-policy renewal.
	for _, binary := range []string{"coop", "coop-worker"} {
		var cmd *exec.Cmd
		if binary == "coop" {
			cmd = exec.Command(f.cli, "--master-config", f.ownerConfig, "task", "read", pr9Task)
		} else {
			workerCLI := filepath.Join(t.TempDir(), "coop-worker")
			build := exec.Command("go", "build", "-o", workerCLI, "./cmd/coop-worker")
			build.Dir = "../.."
			if _, err := build.CombinedOutput(); err != nil {
				t.Fatal("build worker CLI failed; subprocess output withheld", err)
			}
			configPath, inputPath := filepath.Join(t.TempDir(), "worker.json"), filepath.Join(t.TempDir(), "read.json")
			raw, _ := json.Marshal(cfg)
			if err = os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(c.TaskReadInput{Reference: pr9Task})
			if err = os.WriteFile(inputPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cmd = exec.Command(workerCLI, "tasks", "read", "--config", configPath, "--input", inputPath)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(binary, "immutable task read failed; private output withheld", err)
		}
		var read c.ProviderTask
		if err = json.Unmarshal(out, &read); err != nil || read.Binding == nil || NativeAssignmentSHA(*read.Binding) != pr9Assignment || read.Reference != pr9Task || read.State != "closed" {
			t.Fatal(binary, "lost exact original task", err)
		}
	}
}

func TestRetainedPR9ExactAuthorityDenials(t *testing.T) {
	requirePR9PrivateFixture(t)
	for _, mode := range []string{"missing-bridge", "revoked-gates", "revoked-delivery", "ordinary-replacement", "ordinary-withdrawal", "execution-policy", "source-scope", "source-archive", "source-author-delivery"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR9Journey(t)
			if mode == "missing-bridge" {
				cfg, _ := f.as("hawk")
				if _, err := f.github.ReadNativeTask(t.Context(), cfg, pr9Task); err == nil {
					t.Fatal("stale policy accepted without bridge")
				}
				return
			}
			gates := f.gates(false)
			grant := f.ready(gates, false)
			cfg, m, x := f.selected("rook", "deliver")
			switch mode {
			case "ordinary-replacement", "ordinary-withdrawal":
				in := grant.Input
				in.Kind, in.RequestID, in.Retained = "deliver", "fixture-replace-retained", c.RetainedDeliveryAuthority{}
				f.ownerHandoff(in, false)
				if mode == "ordinary-withdrawal" {
					in.Revoked, in.RequestID = true, "fixture-withdraw-ordinary"
					f.ownerHandoff(in, false)
				}
			case "revoked-gates", "revoked-delivery":
				in := gates.Input
				if mode == "revoked-delivery" {
					in = grant.Input
				}
				in.Revoked, in.RequestID = true, "fixture-revoke"
				f.ownerHandoff(in, false)
			case "execution-policy":
				f.provider.policy = BodyHash("replacement execution policy")
			case "source-scope":
				f.provider.task.Body += "drift"
			case "source-archive":
				f.replay.archive = []byte("not the immutable source archive")
			case "source-author-delivery":
				cfg, m = f.as("robin")
				x.InstanceID = cfg.Master.InstanceID
				x.Job.Master.AgentID, x.Job.Master.ExecutorID = "robin", 342161151
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RunNativeAction(t.Context(), cfg, m, f.github, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "fixture-denied"}); err == nil {
				t.Fatal("unsafe exact retained delivery accepted")
			}
			if mode != "source-archive" {
				if _, err := m.TaskComplete(t.Context(), c.TaskCompleteInput{Goal: f.data.Task.Goal, Reference: pr9Task, AssignmentSHA: pr9Assignment, PullRequestURL: pr9URL, DeliveryHandoffID: grant.ID, RequestID: "fixture-direct-master-denied"}); err == nil {
					t.Fatal("real master accepted stale/revoked retained authority")
				}
			}
			if f.provider.completes != 0 || f.provider.binds != 0 || f.provider.creates != 0 || len(f.replay.writes) != 0 {
				t.Fatal("denial crossed effect boundary", f.replay.writes)
			}
		})
	}
}

func TestRetainedPR9ExactAcknowledgedMergeRecovery(t *testing.T) {
	requirePR9PrivateFixture(t)
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint("revoked=", revoked), func(t *testing.T) {
			f := newPR9Journey(t)
			grant := f.ready(f.gates(false), false)
			cfg, m, x := f.selected("rook", "deliver")
			in := NativeAction{Operation: "deliver", RequestID: "fixture-acknowledged-delivery"}
			f.replay.afterMerge = func() { f.replay.unavailable = 1 }
			if _, err := RunNativeAction(t.Context(), cfg, m, f.github, x.Job.ID, in); err == nil {
				t.Fatal("expected post-merge observation interruption")
			}
			if !f.replay.merged || len(f.replay.writes) != 1 || f.provider.completes != 0 {
				t.Fatal("wrong acknowledged checkpoint", f.replay.writes, f.provider.completes)
			}
			x.Phase, x.Reason = "blocked", "native_completed_without_result"
			x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now().UTC()
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			if revoked {
				in := grant.Input
				in.Revoked, in.RequestID = true, "fixture-recovery-revoke"
				f.ownerHandoff(in, false)
			}
			result, err := RecoverNativeDelivery(t.Context(), cfg, m, f.github, &fakeOpenCode{running: false}, x.Job.ID, x.Job.SessionID, x.Job.MessageID, in)
			if revoked {
				if err == nil || f.provider.completes != 0 {
					t.Fatal("revoked recovery completed original task", err)
				}
			} else if err != nil || result.Phase != "completed" || f.provider.completes != 1 {
				t.Fatal("acknowledged recovery did not finish original task", err, result, f.provider.completes)
			}
			if len(f.replay.writes) != 1 || f.provider.binds != 0 || f.provider.creates != 0 || NativeAssignmentSHA(*f.provider.task.Binding) != pr9Assignment {
				t.Fatal("recovery replayed merge or replaced source", f.replay.writes)
			}
		})
	}
}

func TestRetainedPR9OrdinaryDeliveryDoesNotInheritBridge(t *testing.T) {
	f := newPR9Journey(t)
	gates := f.gates(false)
	in := gates.Input
	in.Kind, in.RequestID, in.Retained = "deliver", "fixture-ordinary-delivery", c.RetainedDeliveryAuthority{}
	grant := f.ownerHandoff(in, false)
	if _, err := f.clients["rook"].TaskComplete(t.Context(), c.TaskCompleteInput{Goal: in.Goal, Reference: pr9Task, AssignmentSHA: pr9Assignment, PullRequestURL: pr9URL, DeliveryHandoffID: grant.ID, RequestID: "fixture-ordinary-complete"}); err == nil {
		t.Fatal("ordinary delivery migrated the retained source policy")
	}
	if f.provider.completes != 0 || f.provider.binds != 0 || f.provider.creates != 0 || len(f.replay.writes) != 0 {
		t.Fatal("ordinary stale delivery caused effects")
	}
}

func TestRetainedPR9SourceAuthorCannotPublishRenewedGate(t *testing.T) {
	f := newPR9Journey(t)
	f.gates(false)
	_, _, gate := f.selected("hawk", "delivery-review")
	cfg, robin := f.as("robin")
	before, err := f.clients["owner"].Messages(t.Context(), f.data.SourceReport.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An adversarial local job rewrite cannot turn Robin's real authenticated
	// source-author identity into the independently assigned Hawk gate actor.
	gate.InstanceID = cfg.Master.InstanceID
	gate.Job.Master.AgentID, gate.Job.Master.ExecutorID = "robin", 342161151
	if err = saveNativeExecution(cfg, gate); err != nil {
		t.Fatal(err)
	}
	if _, err = RunNativeAction(t.Context(), cfg, robin, f.github, gate.Job.ID, NativeAction{Operation: "delivery-readiness", RequestID: "fixture-wrong-gate", Verdict: "ready"}); err == nil {
		t.Fatal("source author published renewed gate")
	}
	after, err := f.clients["owner"].Messages(t.Context(), f.data.SourceReport.ThreadID, 0)
	if err != nil || len(after.Messages) != len(before.Messages) || f.provider.completes != 0 || len(f.replay.writes) != 0 {
		t.Fatal("source-author denial emitted evidence or effects", err)
	}
}
