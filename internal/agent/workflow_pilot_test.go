package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/githubapp"
	"github.com/sarcasticbird/coop/internal/master"
	rt "github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// This opt-in LIVE test creates a private GitHub sandbox and four disposable
// portable birds. It spends provider tokens and performs real reviews/merge and
// native Kata writes. It must never be enabled in ordinary tests or CI.
func TestLiveFleetWorkflowPilot(t *testing.T) {
	if os.Getenv("COOP_LIVE_FLEET_UAT") != "1" {
		t.Skip("explicit live GitHub/provider/Kata authorization required")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("live UAT requires a macOS Apple Container host")
	}
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("missing %s", key)
		}
		return v
	}
	root, repo, tag := required("COOP_UAT_ROOT"), required("COOP_UAT_REPOSITORY"), required("COOP_UAT_TAG")
	kataHome, kataBin, projectUID := required("COOP_UAT_KATA_HOME"), required("COOP_UAT_KATA_BINARY"), required("COOP_UAT_PROJECT_UID")
	projectName, image, gateway := required("COOP_UAT_PROJECT_NAME"), required("COOP_UAT_IMAGE"), required("COOP_UAT_GATEWAY")
	binDir := required("COOP_UAT_BIN")
	if !filepath.IsAbs(root) || !filepath.IsAbs(kataHome) || !filepath.IsAbs(kataBin) || !filepath.IsAbs(binDir) || repo != "sarcasticbird/coop-fleet-uat-"+tag || !strings.HasPrefix(projectName, "coop-fleet-uat-") || !strings.HasPrefix(filepath.Base(kataHome), "coop-uat-kata-") || config.ValidateProfileName("pilot-uat-"+tag+"-robin") != nil {
		t.Fatal("invalid isolated live UAT targets")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("fresh state required; never reuse or erase an attempt", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	// A fresh run is bounded to eight turns; retries need remaining owner budget.
	turnLimits := map[string]int{"robin": 2, "hawk": 3, "wren": 1, "rook": 2}
	evidence := map[string]any{"repository": repo, "project_uid": projectUID, "started_at": time.Now().UTC(), "model": "openai/gpt-6.1-sol", "native_turn_cap": 8}
	t.Cleanup(func() {
		evidence["passed"] = !t.Failed()
		evidence["finished_at"] = time.Now().UTC()
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "workflow.json"), raw, 0600)
	})
	write := func(path string, raw []byte, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, mode); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dir string, args ...string) []byte {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = dir
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", args[0], err, raw)
		}
		return raw
	}
	ghRead := func(endpoint string, out any) {
		raw := run(root, "gh", "api", endpoint)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	var ownerActor worker.Actor
	var user struct {
		ID    int64
		Login string
	}
	ghRead("user", &user)
	if user.Login != "sarcasticbird" {
		t.Fatal("wrong sandbox owner")
	}
	ownerActor = worker.Actor{ID: user.ID, Login: user.Login, Kind: "User"}
	registrations := map[string]githubapp.Registration{}
	team := worker.TeamPolicy{Version: 2, Planner: "hawk", EngineeringPeers: map[string]string{"robin": "hawk", "hawk": "robin"}}
	for _, name := range []string{"robin", "hawk", "wren", "rook"} {
		r, err := githubapp.LoadRegistration(filepath.Join(os.Getenv("HOME"), ".config/coop/agents", name, "github-app.json"))
		if err != nil {
			t.Fatal(name, err)
		}
		registrations[name] = r
		caps := map[string][]string{"robin": {"implement", "engineering-review"}, "hawk": {"plan", "implement", "engineering-review"}, "wren": {"customer-qa"}, "rook": {"deliver"}}[name]
		team.Members = append(team.Members, worker.TeamMember{Name: name, Actor: worker.Actor{ID: r.BotID, Login: r.BotLogin, Kind: "Bot"}, Capabilities: caps})
	}
	policySHA, err := worker.TeamPolicySHA(team)
	if err != nil {
		t.Fatal(err)
	}
	// Only the human bootstrap uses host gh. All bird-side credentials below are
	// minted from their own Apps, scoped by GitHub to exactly this new repository.
	checkoutSHA := strings.TrimSpace(string(run(root, "gh", "api", "repos/actions/checkout/git/ref/tags/v4", "--jq", ".object.sha")))
	if len(checkoutSHA) != 40 {
		t.Fatal("cannot pin checkout action")
	}
	repoDir := filepath.Join(root, "bootstrap")
	if err := os.Mkdir(repoDir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"greet.sh":                   "#!/bin/bash\nprintf 'TODO\\n'\n",
		"test.sh":                    "#!/bin/bash\nset -euo pipefail\ntest \"$(bash greet.sh)\" = 'Hello, world!'\ntest \"$(bash greet.sh 'Ada Lovelace')\" = 'Hello, Ada Lovelace!'\n",
		"acceptance.sh":              "#!/bin/bash\nset -euo pipefail\nbash test.sh\nset +e\noutput=$(bash greet.sh '   ')\nstatus=$?\nset -e\ntest \"$status\" = 64\ntest -z \"$output\"\nprintf 'ACCEPTANCE_PASS\\n'\n",
		"AGENTS.md":                  "# Disposable Coop fleet UAT\nOnly greet.sh may change. Do not edit tests, workflows, instructions or metadata. Never delegate to subagents. All remote mutations must use the Coop native action helper; never run gh mutations or git push yourself. This test intentionally exercises a real review/fix loop: on the implement stage build only the ordinary-name/no-argument greeting behavior; leave whitespace-only rejection for the fix stage. The first peer review must run acceptance.sh, report the observed blank-name failure with changes-needed, and approve only a later head that passes. The author fix must reject a whitespace-only argument with exit 64 and no stdout, without changing tests. Each role must finish using its assigned native action. Rook integrates only and must not modify source.\n",
		".github/workflows/test.yml": "name: UAT checks\non: [push, pull_request]\npermissions:\n  contents: read\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@" + checkoutSHA + "\n      - run: bash test.sh\n",
	}
	for path, body := range files {
		if path == "AGENTS.md" {
			body += "\nFlox is local development tooling only. In this guest, run the installed Bash and the test scripts directly; do not activate Flox, resolve dependencies or upgrade tools. Record the actual shell version.\n"
		}
		write(filepath.Join(repoDir, path), []byte(body), 0600)
	}
	run(repoDir, "git", "init", "-b", "main")
	run(repoDir, "git", "add", ".")
	run(repoDir, "git", "-c", "user.name=Coop UAT", "-c", "user.email=uat@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Initialize disposable fleet UAT fixture")
	run(repoDir, "gh", "repo", "create", repo, "--private", "--source", repoDir, "--remote", "origin", "--push", "--description", "Disposable Coop fleet workflow acceptance; no deployments")
	var repository struct {
		ID       int64
		Private  bool
		FullName string `json:"full_name"`
	}
	ghRead("repos/"+repo, &repository)
	if !repository.Private || repository.FullName != repo || repository.ID <= 0 {
		t.Fatal("sandbox identity/visibility mismatch")
	}
	evidence["repository_id"] = repository.ID
	backendTasks, err := taskstore.New(taskstore.Config{Provider: "kata", Binary: kataBin, Home: kataHome, ProjectName: projectName, ProjectUID: projectUID, PolicySHA: policySHA})
	if err != nil {
		t.Fatal(err)
	}
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(root, "master.sqlite"), MasterID: "fleet-uat-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner := c.Principal{ID: "uat-owner", Alias: "uat-owner", Kind: "owner", Engagements: []string{"uat"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	principals := []c.Principal{owner}
	var placements []c.Placement
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1", gateway}}
	bindings := map[string]c.ExecutionBinding{}
	tokens := map[string]string{}
	actors := map[string]c.Principal{}
	for _, m := range team.Members {
		caps := []string{"read", "send"}
		if m.Name == "rook" {
			caps = append(caps, "deliver")
		}
		if m.Name == "hawk" {
			caps = append(caps, "plan")
		}
		p := c.Principal{ID: m.Name, Alias: m.Name, Kind: "agent", GitHubBotID: m.Actor.ID, Engagements: []string{"uat"}, Capabilities: caps}
		actors[m.Name] = p
		principals = append(principals, p)
		sum := sha256.Sum256([]byte(tag + "/" + m.Name))
		b := c.ExecutionBinding{MasterID: "fleet-uat-" + tag, Placement: c.Placement{AgentID: m.Name, HostID: "uat-host", InstanceID: hex.EncodeToString(sum[:]), Generation: 1}}
		bindings[m.Name] = b
		placements = append(placements, b.Placement)
		token := rand.Text()
		tokens[m.Name] = token
		h := sha256.Sum256([]byte(token))
		policy.Tokens = append(policy.Tokens, master.Token{PrincipalID: m.Name, SHA256: hex.EncodeToString(h[:]), InstanceID: b.InstanceID, Generation: 1})
	}
	if err := store.Configure(ctx, principals, placements); err != nil {
		t.Fatal(err)
	}
	svc := &master.Service{Store: store, Tasks: backendTasks}
	// A real isolated-master capability revocation after the merge forces the
	// acknowledged-merge recovery path. GitHub and Kata are never simulated.
	var completionFault atomic.Bool
	normal := master.NewHandler(svc, policy)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/tasks/complete" && completionFault.CompareAndSwap(false, true) {
			revoked := actors["rook"]
			revoked.Capabilities = []string{"read", "send"}
			if err := store.Configure(r.Context(), []c.Principal{revoked}, nil); err != nil {
				http.Error(w, "fixture capability fault", 500)
				return
			}
		}
		normal.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	thread, err := store.CreateThread(ctx, owner, c.CreateThread{Title: "Isolated fleet UAT", Engagement: "uat", Members: []string{"robin", "hawk", "wren", "rook"}, RequestID: "uat-thread"})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := svc.ProposeGoal(ctx, owner, c.GoalProposal{ThreadID: thread.ID, Purpose: "Validate a real isolated implementation, requested-changes, fix, peer review, QA, Rook merge and Kata completion", Scope: "Only " + repo + " on main. Plan exactly one Kata task. Implement greet.sh, and do not change tests, workflow or AGENTS.md. Follow the intentionally staged review/fix protocol in AGENTS.md. Final requirements: no args prints Hello, world!; a name prints Hello, NAME!; whitespace-only input exits 64 with no stdout. Run bash test.sh for CI and bash acceptance.sh for final acceptance. No deployments, tags, releases, production work or subagents.", Repositories: []c.RepositoryScope{{Name: repo, BaseBranches: []string{"main"}}}, Acceptance: []string{"Exactly one Kata task and one PR", "Peer requests changes for the initial real whitespace failure, then approves the repaired exact head", "Wren independently tests the exact registered source artifact using bash acceptance.sh and verifies its qa-source-digest", "CI passes; only Rook merges and completes Kata done", "Recovery after acknowledged merge must not submit a second merge"}, AuthorID: "robin", PeerID: "hawk", QA: c.QAPolicy{Required: true, ActorID: "wren", Environment: "disposable Linux ARM64 portable guest; bash acceptance.sh", EnvironmentRevision: "fleet-uat-bash-v1", EvidenceDestination: c.EvidenceDestination{MasterID: "fleet-uat-" + tag, ThreadID: thread.ID, Engagement: "uat"}}, RequestID: "uat-goal"})
	if err != nil {
		t.Fatal(err)
	}
	evidence["goal"] = goal.Ref
	storeHost := workspace.FileStore{Root: filepath.Join(root, "ownership")}
	runtimeBackend := workspace.ContainerBackend{Runtime: rt.AppleMachine{}, Store: storeHost, Guard: executionGuard}
	var sessions []*Session
	t.Cleanup(func() {
		for _, s := range sessions {
			stopCtx, done := context.WithTimeout(context.Background(), time.Minute)
			_ = s.Down(stopCtx)
			done()
		}
	})
	for _, member := range team.Members {
		name := member.Name
		b := bindings[name]
		profileName := "pilot-uat-" + tag + "-" + name
		hostToken := filepath.Join(root, name, "token")
		write(hostToken, []byte(tokens[name]), 0600)
		hostClient := filepath.Join(root, name, "client.toml")
		write(hostClient, []byte(fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", fmt.Sprintf("http://127.0.0.1:%d", port), b.MasterID, hostToken)), 0600)
		cfg := config.Default()
		cfg.Seeds = nil
		cfg.IncludeCredentials = []string{"uat-github", "uat-openai"}
		hostBundle := filepath.Join(root, name, "github-bundle.json")
		cfg.Credentials = map[string]config.Credential{
			"uat-github": {Source: config.CredentialSource{Type: "file", Path: hostBundle}, Expose: []config.CredentialInjection{{Type: "file", PathEnv: "COOP_GITHUB_APP_BUNDLE_FILE"}}},
			"uat-openai": {Source: config.CredentialSource{Type: "file", Path: "~/.config/coop/secrets/rook-openai"}, Expose: []config.CredentialInjection{{Type: "environment", Name: "OPENAI_API_KEY"}}},
		}
		cfg.Resources = config.Resources{CPUs: 2, Memory: "4G"}
		profile := config.Profile{Runtime: "container", Command: []string{"sleep", "infinity"}, Restart: "no", Execution: &config.AgentExecution{ExecutionBinding: b, ClientConfig: hostClient}, Services: map[string]config.Service{"server": {Command: []string{"/usr/local/libexec/uat-server"}}, "worker": {Command: []string{"/usr/local/libexec/uat-worker"}}}}
		spec := workspace.Spec{Mode: workspace.Agent, Identity: profileName, RuntimeName: "coop-agent-" + profileName, Home: "/root", Workdir: "/workspace", Image: image, Config: cfg, Profile: profile}
		spec.Fingerprint = workspace.Fingerprint(spec)
		s := &Session{Spec: spec, Controller: workspace.Controller{Backend: runtimeBackend, Store: storeHost}, Runner: runtimeBackend, CredentialManager: credential.NewManager(""), Output: io.Discard}
		if err := s.Controller.Ensure(ctx, spec); err != nil {
			t.Fatal(name, err)
		}
		sessions = append(sessions, s)
		guest := func(input io.Reader, args ...string) []byte {
			var out, stderr bytes.Buffer
			if err := s.Runner.Exec(ctx, spec.RuntimeName, "/", args, input, &out, &stderr); err != nil {
				t.Fatalf("%s guest %s: %v\n%s", name, args[0], err, stderr.String())
			}
			return out.Bytes()
		}
		guestWrite := func(path string, raw []byte, mode string) {
			guest(bytes.NewReader(raw), "/bin/bash", "-euc", `umask 077; mkdir -p -- "$(dirname "$1")"; cat > "$1"; chmod "$2" "$1"`, "uat-write", path, mode)
		}
		for file, dest := range map[string]string{"coop-worker": "/usr/local/libexec/coop-worker", "coop-github": "/usr/local/libexec/coop-github-real", "uat-turn-proxy": "/usr/local/libexec/uat-turn-proxy", "pilot-master-proxy": "/usr/local/libexec/uat-master-proxy"} {
			raw, err := os.ReadFile(filepath.Join(binDir, file))
			if err != nil {
				t.Fatal(err)
			}
			guestWrite(dest, raw, "0700")
		}
		guestWrite("/usr/local/libexec/coop-github", []byte("#!/bin/bash\nset -euo pipefail\nif [[ ${1:-} == gh ]]; then printf '%s\\n' \"$*\" >> /var/lib/coop/uat/gh-requests.log; fi\nexec /usr/local/libexec/coop-github-real \"$@\"\n"), "0700")
		bundle := scopedUATBundle(t, ctx, registrations[name], repository.ID)
		write(hostBundle, bundle, 0600)
		guestWrite("/var/lib/coop/github-app-id", []byte(fmt.Sprint(registrations[name].AppID)), "0600")
		// Give the model a usable Git identity instead of making it invent one.
		// GitHub still resolves the actual commit accounts at the normal gate.
		email := fmt.Sprintf("%d+%s@users.noreply.github.com", member.Actor.ID, member.Actor.Login)
		guest(nil, "/usr/local/libexec/coop-env", "git", "config", "--global", "user.name", member.Actor.Login)
		guest(nil, "/usr/local/libexec/coop-env", "git", "config", "--global", "user.email", email)
		if identity := string(guest(nil, "/usr/local/libexec/coop-env", "git", "var", "GIT_AUTHOR_IDENT")); !strings.HasPrefix(identity, member.Actor.Login+" <"+email+"> ") {
			t.Fatal(name, "Git identity differs from the assigned bot")
		}
		guestWrite("/var/lib/coop/uat/server-password", []byte(rand.Text()), "0600")
		guestWrite("/var/lib/coop/uat/master-token", []byte(tokens[name]), "0600")
		guestWrite("/var/lib/coop/uat/master-client.toml", []byte(fmt.Sprintf("url = \"http://127.0.0.1:17744\"\nmaster_id = %q\ntoken_file = \"/var/lib/coop/uat/master-token\"\nallow_loopback_http = true\n", b.MasterID)), "0600")
		workerCfg := worker.Defaults(worker.Config{Version: 3, Name: name, Model: "openai/gpt-6.1-sol", Team: &team, Owner: ownerActor, Self: member.Actor, CodeOwners: []string{"sarcasticbird"}, Repositories: []string{repo}, StateDir: "/var/lib/coop/worker/state", WorkRoot: "/workspace/worker", OpenCode: worker.OpenCodeConfig{PasswordFile: "/var/lib/coop/uat/server-password"}, Master: &worker.MasterConfig{Backend: "master", ClientConfig: "/var/lib/coop/uat/master-client.toml", MasterID: b.MasterID, AgentID: name, HostID: b.HostID, InstanceID: b.InstanceID, Generation: 1, Engagement: "uat", BlockerRecipient: owner.ID}, TaskSource: &worker.TaskSourceConfig{Provider: "kata", ProjectUID: projectUID}, Limits: worker.Limits{PollInterval: 5 * time.Second, TurnLimit: 5 * time.Minute, RetryBase: 5 * time.Second, RetryCap: 30 * time.Second, MaxAttempts: 1, MaxReviewCycles: 3}})
		if err := worker.ValidateConfig(workerCfg); err != nil {
			t.Fatal(name, err)
		}
		raw, _ := json.Marshal(workerCfg)
		guestWrite("/var/lib/coop/worker/config.json", raw, "0600")
		guestWrite("/root/.config/opencode/opencode.json", []byte(`{"permission":{"question":"deny","task":"deny"},"model":"openai/gpt-6.1-sol","agent":{"build":{"model":"openai/gpt-6.1-sol"}}}`), "0600")
		guestWrite("/root/.config/opencode/AGENTS.md", []byte("Disposable approved fleet UAT only. Work only in the assigned sandbox. Never access production repositories, credentials, or signing keys. Do not delegate. Use the configured Git author/committer identity; do not invent or override it. Use the native action helper for all remote mutations. Never change tests/workflows/AGENTS.md. Never print environment or credentials. On helper refusal, report concrete evidence and preserve its original request ID. Wren: independently download the registered exact-head source archive, verify it with coop-worker qa-source-digest against PR metadata, and test those extracted bytes using bash acceptance.sh; report unsupported claims honestly. Rook: deliver only; source repair belongs to Robin.\n"), "0600")
		limit := turnLimits[name]
		serverScript := fmt.Sprintf("#!/bin/bash\nset -euo pipefail\nexport OPENCODE_SERVER_PASSWORD=$(cat /var/lib/coop/uat/server-password)\n/usr/local/libexec/uat-turn-proxy --counter /var/lib/coop/uat/turns.json --limit %d &\nproxy=$!\ntrap 'kill \"$proxy\" 2>/dev/null || true' EXIT\n/usr/local/libexec/coop-github env -- /usr/local/bin/coop-user-env -- opencode serve --hostname 127.0.0.1 --port 4097\n", limit)
		guestWrite("/usr/local/libexec/uat-server", []byte(serverScript), "0700")
		guestWrite("/usr/local/libexec/uat-worker", []byte("#!/bin/bash\nset -euo pipefail\nexec /usr/local/libexec/coop-github env -- /usr/local/libexec/coop-worker run --config /var/lib/coop/worker/config.json\n"), "0700")
		guest(nil, "/usr/local/libexec/coop-worker", "init-state", "--config", "/var/lib/coop/worker/config.json")
		guest(nil, "systemd-run", "--unit=uat-master-proxy", "/usr/local/libexec/uat-master-proxy", "--upstream", fmt.Sprintf("http://%s:%d", gateway, port))
		// Make the private master proxy restart with the guest without touching the
		// saved worker/service configuration or provider credentials.
		unit := fmt.Sprintf("[Unit]\nDescription=Disposable UAT master transport\n[Service]\nExecStart=/usr/local/libexec/uat-master-proxy --upstream http://%s:%d\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n", gateway, port)
		guestWrite("/etc/systemd/system/uat-master-persistent.service", []byte(unit), "0600")
		guest(nil, "systemctl", "daemon-reload")
		guest(nil, "systemctl", "enable", "uat-master-persistent.service")
		if err := s.Up(ctx, nil); err != nil {
			t.Fatal(name, err)
		}
		t.Log("disposable worker started:", name)
	}
	if _, err := svc.DecideGoal(ctx, owner, c.GoalDecisionInput{GoalID: goal.Ref.GoalID, RevisionSHA256: goal.Ref.RevisionSHA256, ExpectedDecisionSequence: 0, Action: "approve", RequestID: "uat-owner-approved"}); err != nil {
		t.Fatal(err)
	}
	t.Log("approved isolated goal; autonomous workflow running")
	var prs []struct {
		URL      string `json:"html_url"`
		Number   int
		Head     struct{ SHA string }
		MergedAt *time.Time `json:"merged_at"`
	}
	restarted := false
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("live workflow timed out; inspect private evidence and stopped birds")
		case <-ticker.C:
		}
		for _, s := range sessions {
			var out, stderr bytes.Buffer
			err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", []string{"/bin/bash", "-c", `for file in /var/lib/coop/worker/state/master/v1/engineering/*.json; do test -f "$file" && cat "$file"; done`}, nil, &out, &stderr)
			if err != nil {
				continue
			}
			write(filepath.Join(root, s.Spec.Identity, "engineering.jsonl"), out.Bytes(), 0600)
			decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
			for {
				var x worker.NativeExecution
				if err := decoder.Decode(&x); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if x.Phase == "blocked" && (x.Reason == "native_completed_without_result" || x.Reason == "native_observation_unavailable") && (!completionFault.Load() || x.Job.Kind != "deliver") {
					t.Fatalf("%s %s held: %s; inspect retained evidence", s.Spec.Identity, x.Job.Kind, x.Reason)
				}
			}
		}
		ghRead("repos/"+repo+"/pulls?state=all", &prs)
		if len(prs) > 1 {
			t.Fatal("more than one PR created")
		}
		page, err := svc.TaskList(ctx, owner, c.TaskListInput{Goal: goal.Ref})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Tasks) > 1 {
			t.Fatal("more than one task created")
		}
		if len(page.Tasks) == 1 {
			evidence["task"] = page.Tasks[0].Reference
			evidence["task_state"] = page.Tasks[0].State
		}
		if completionFault.Load() && !restarted {
			rook := sessions[3]
			var out bytes.Buffer
			if err := rook.Runner.Exec(ctx, rook.Spec.RuntimeName, "/", []string{"/bin/bash", "-c", `for file in /var/lib/coop/worker/state/master/v1/engineering/*.json; do test -f "$file" && cat "$file"; done`}, nil, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&out)
			terminal := false
			for {
				var x worker.NativeExecution
				if err := decoder.Decode(&x); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if x.Job.Kind == "deliver" && !x.Job.TerminalAt.IsZero() {
					terminal = true
				}
			}
			if terminal {
				if err := rook.Down(ctx); err != nil {
					t.Fatal(err)
				}
				if err := store.Configure(ctx, []c.Principal{actors["rook"]}, nil); err != nil {
					t.Fatal(err)
				}
				if err := rook.Up(ctx, nil); err != nil {
					t.Fatal(err)
				}
				restarted = true
				t.Log("restarted Rook after acknowledged merge; recovering original completion")
			}
		}
		if len(prs) == 1 && prs[0].MergedAt != nil && len(page.Tasks) == 1 && page.Tasks[0].ClosedReason == "done" {
			break
		}
	}
	if !restarted {
		t.Fatal("post-merge restart recovery was not exercised")
	}
	var merged struct {
		Merged   bool
		MergedBy struct{ ID int64 } `json:"merged_by"`
		Head     struct{ SHA string }
	}
	ghRead(fmt.Sprintf("repos/%s/pulls/%d", repo, prs[0].Number), &merged)
	if !merged.Merged || merged.MergedBy.ID != registrations["rook"].BotID {
		t.Fatal("merge was not performed by Rook")
	}
	var reviews []struct {
		State    string
		CommitID string `json:"commit_id"`
		User     struct{ ID int64 }
		Body     string
	}
	ghRead(fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, prs[0].Number), &reviews)
	changes, peer, qa := false, false, false
	for _, r := range reviews {
		if r.User.ID == registrations["hawk"].BotID && r.State == "CHANGES_REQUESTED" {
			changes = true
		}
		if r.CommitID == merged.Head.SHA && r.State == "APPROVED" {
			if r.User.ID == registrations["hawk"].BotID {
				peer = true
			}
			if r.User.ID == registrations["wren"].BotID {
				qa = true
			}
		}
	}
	if !changes || !peer || !qa {
		t.Fatal("missing real requested changes, exact-head peer review or QA")
	}
	rook := sessions[3]
	var audit bytes.Buffer
	if err := rook.Runner.Exec(ctx, rook.Spec.RuntimeName, "/", []string{"cat", "/var/lib/coop/uat/gh-requests.log"}, nil, &audit, io.Discard); err != nil {
		t.Fatal(err)
	}
	merges := 0
	for _, line := range strings.Split(audit.String(), "\n") {
		if strings.Contains(line, "/merge ") && strings.Contains(line, "--method PUT") {
			merges++
		}
	}
	if merges != 1 {
		t.Fatal("acknowledged merge was replayed", merges)
	}
	evidence["merge_requests"] = merges
	evidence["pr"] = prs[0].URL
	evidence["head"] = merged.Head.SHA
	evidence["requested_changes"] = changes
	evidence["peer_approved"] = peer
	evidence["qa_approved"] = qa
	evidence["rook_restart_after_merge"] = restarted
	t.Log("live workflow completed:", prs[0].URL)
}

type uatScopeTransport struct {
	repositoryID int64
	next         http.RoundTripper
}

func (s uatScopeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	next := s.next
	if next == nil {
		next = http.DefaultTransport
	}
	if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("unexpected GitHub target")
	}
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens") {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		body["repository_ids"] = []int64{s.repositoryID}
		raw, _ = json.Marshal(body)
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		res, err := next.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		raw, err = io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			return nil, err
		}
		var token struct {
			Selection    string `json:"repository_selection"`
			Repositories []struct {
				ID       int64
				FullName string `json:"full_name"`
			}
		}
		if res.StatusCode != 201 || json.Unmarshal(raw, &token) != nil || token.Selection != "selected" || len(token.Repositories) != 1 || token.Repositories[0].ID != s.repositoryID || !strings.HasPrefix(token.Repositories[0].FullName, "sarcasticbird/coop-fleet-uat-") {
			return nil, fmt.Errorf("UAT token was not restricted to the sandbox")
		}
		res.Body = io.NopCloser(bytes.NewReader(raw))
		return res, nil
	}
	if r.Method != "GET" {
		return nil, fmt.Errorf("unexpected App mutation")
	}
	return next.RoundTrip(r)
}
func scopedUATBundle(t *testing.T, ctx context.Context, r githubapp.Registration, repoID int64) []byte {
	t.Helper()
	r.Installations = map[string]githubapp.Installation{"sarcasticbird": r.Installations["sarcasticbird"]}
	b, err := (githubapp.Client{HTTP: &http.Client{Transport: uatScopeTransport{repositoryID: repoID}}}).Mint(ctx, r)
	if err != nil {
		t.Fatal("mint repository-scoped UAT App token", err)
	}
	var out bytes.Buffer
	if err := b.Encode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type uatTransportFunc func(*http.Request) (*http.Response, error)

func (f uatTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUATTokenCannotExpandBeyondSandbox(t *testing.T) {
	for _, tc := range []struct {
		selection string
		ids       []int64
		repo      string
		allow     bool
	}{
		{"selected", []int64{7}, "sarcasticbird/coop-fleet-uat-test", true},
		{"all", []int64{7}, "sarcasticbird/coop-fleet-uat-test", false},
		{"selected", []int64{7, 8}, "sarcasticbird/coop-fleet-uat-test", false},
		{"selected", []int64{8}, "sarcasticbird/coop-fleet-uat-test", false},
		{"selected", []int64{7}, "sarcasticbird/production", false},
	} {
		t.Run(tc.selection+fmt.Sprint(tc.ids)+tc.repo, func(t *testing.T) {
			transport := uatScopeTransport{repositoryID: 7, next: uatTransportFunc(func(r *http.Request) (*http.Response, error) {
				var in struct {
					IDs []int64 `json:"repository_ids"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.IDs) != 1 || in.IDs[0] != 7 {
					t.Fatal("mint request was not constrained")
				}
				repos := []map[string]any{}
				for _, id := range tc.ids {
					repos = append(repos, map[string]any{"id": id, "full_name": tc.repo})
				}
				raw, _ := json.Marshal(map[string]any{"repository_selection": tc.selection, "repositories": repos})
				return &http.Response{StatusCode: 201, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			req, _ := http.NewRequest("POST", "https://api.github.com/app/installations/10/access_tokens", strings.NewReader(`{"permissions":{"contents":"write"}}`))
			res, err := transport.RoundTrip(req)
			if res != nil {
				_ = res.Body.Close()
			}
			if (err == nil) != tc.allow {
				t.Fatal("wrong sandbox scope decision", err)
			}
		})
	}
}
