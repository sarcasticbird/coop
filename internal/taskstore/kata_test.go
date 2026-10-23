package taskstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testProject = "01M5MCPEHHP4C98MZSHJEE3M8D"
const testIssue = "01M5MSA8FZCCD7DWMTP6RWM3MD"

func testConfig(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return Config{Provider: "kata", Binary: "/bin/echo", Home: home, ProjectName: "coop-test", ProjectUID: testProject, PolicySHA: strings.Repeat("a", 64)}
}
func TestConfigRequiresPrivatePinnedNativeProfile(t *testing.T) {
	cfg := testConfig(t)
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Home = "relative" }, func(c *Config) { c.ProjectUID = "mutable-name" }, func(c *Config) { c.PolicySHA = "bad" }, func(c *Config) { c.Binary = "kata" }, func(c *Config) { c.Provider = "github" }} {
		bad := cfg
		change(&bad)
		if _, err := New(bad); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	if err := os.Chmod(cfg.Home, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestConfigRejectsSymlinkProfileWithTrailingSlash(t *testing.T) {
	cfg := testConfig(t)
	link := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(cfg.Home, link); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{link, link + "/"} {
		cfg.Home = home
		if _, err := New(cfg); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("accepted symlink profile %q: %v", home, err)
		}
	}
}
func TestReadPinsIdentityAndDoesNotInheritRemoteCredentials(t *testing.T) {
	cfg := testConfig(t)
	cfg.Binary = filepath.Join(t.TempDir(), "kata")
	script := `#!/bin/sh
[ -z "$KATA_SERVER$KATA_AUTH_TOKEN$KATA_AUTHOR" ] || exit 1
[ "$KATA_HOME" = "$PWD" ] || exit 1
case "$*" in *"daemon locate"*) echo '{"kind":"local","source":"local_default","network":"unix","address":"unix:///private/tmp/test.sock"}'; exit 0;; esac
cat "$KATA_HOME/issue.json"
`
	if err := os.WriteFile(cfg.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"issue":{"uid":"` + testIssue + `","project_uid":"` + testProject + `","title":"task","body":"work","status":"closed","closed_reason":"superseded","revision":3,"metadata":{"coop.creation":"{\"goal\":{\"source\":\"master\",\"master_id\":\"master\",\"goal_id\":\"goal\",\"revision_sha256\":\"abc\"},\"repository\":\"example/coop\"}"}}}`
	if err := os.WriteFile(filepath.Join(cfg.Home, "issue.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATA_SERVER", "https://unsafe.invalid")
	t.Setenv("KATA_AUTH_TOKEN", "secret")
	t.Setenv("KATA_AUTHOR", "spoof")
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := c.KataTaskReference(testProject, testIssue, "example/coop")
	task, err := b.Read(context.Background(), ref)
	if err != nil || task.ClosedReason != "superseded" || task.Goal.GoalID != "goal" || task.Binding != nil {
		t.Fatalf("%+v %v", task, err)
	}
	wrong, _ := c.KataTaskReference(testProject, testIssue, "other/repo")
	if _, err = b.Read(context.Background(), wrong); !errors.Is(err, c.ErrConflict) {
		t.Fatal("repository identity accepted", err)
	}
}

// TestKataProcess emulates only the external CLI boundary. Provider mutation,
// CAS, request keys and reconciliation all run through the real adapter.
func TestKataProcess(t *testing.T) {
	if os.Getenv("COOP_KATA_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	for len(args) > 0 {
		if args[0] == "--json" {
			args = args[1:]
			continue
		}
		if args[0] == "--workspace" || args[0] == "--project" || args[0] == "--as" {
			args = args[2:]
			continue
		}
		break
	}
	if len(args) >= 2 && args[0] == "daemon" && args[1] == "locate" {
		fmt.Print(`{"kind":"local","source":"local_default","network":"unix","address":"unix:///private/tmp/test.sock"}`)
		os.Exit(0)
	}
	state := filepath.Join(os.Getenv("KATA_HOME"), "issue.json")
	raw, err := os.ReadFile(state)
	if err != nil {
		os.Exit(4)
	}
	var envelope struct {
		Issue rawIssue `json:"issue"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		os.Exit(3)
	}
	switch args[0] {
	case "projects":
		project := testProject
		if os.Getenv("COOP_KATA_WRONG_PROJECT") == "1" {
			project = testIssue
		}
		fmt.Printf(`{"project":{"uid":%q}}`, project)
		os.Exit(0)
	case "show":
		fmt.Print(string(raw))
		os.Exit(0)
	case "meta":
		rev, _ := strconv.ParseInt(args[len(args)-1], 10, 64)
		if rev != envelope.Issue.Revision {
			os.Exit(5)
		}
		envelope.Issue.Metadata[args[3]] = json.RawMessage(args[4])
		envelope.Issue.Revision++
	case "close":
		envelope.Issue.Status = "closed"
		envelope.Issue.ClosedReason = "done"
		envelope.Issue.Revision++
	case "create":
		var metadataValue string
		for i, arg := range args {
			if arg == "--meta" {
				metadataValue = strings.TrimPrefix(args[i+1], "coop.creation=")
			}
		}
		encoded, _ := json.Marshal(metadataValue)
		envelope.Issue.Metadata["coop.creation"] = encoded
	default:
		os.Exit(3)
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		os.Exit(1)
	}
	if err = os.WriteFile(state, raw, 0600); err != nil {
		os.Exit(1)
	}
	// Simulate a successful side effect whose CLI response was lost.
	if os.Getenv("COOP_KATA_LOST_REPLY") == args[0] {
		os.Exit(7)
	}
	fmt.Print(string(raw))
	os.Exit(0)
}
func mutationBackend(t *testing.T) (Backend, c.ProviderTask) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Binary = filepath.Join(t.TempDir(), "kata")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.Binary, []byte("#!/bin/sh\nexec \"$COOP_KATA_TEST_BINARY\" -test.run=TestKataProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_KATA_HELPER", "1")
	t.Setenv("COOP_KATA_TEST_BINARY", exe)
	goal := c.GoalRef{Source: "master", MasterID: "master", GoalID: "goal", RevisionSHA256: strings.Repeat("b", 64)}
	ref, _ := c.KataTaskReference(testProject, testIssue, "example/coop")
	creationValue, _ := json.Marshal(creation{Goal: goal, Repository: "example/coop"})
	i := rawIssue{UID: testIssue, ProjectUID: testProject, Title: "task", Body: "work", Status: "open", Revision: 1, Metadata: map[string]json.RawMessage{"coop.creation": creationValue}}
	raw, _ := json.Marshal(map[string]any{"issue": i})
	if err = os.WriteFile(filepath.Join(cfg.Home, "issue.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	task, err := backend.Read(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return backend, task
}
func TestBindCASReconcilesAmbiguousReplyAndIdenticalRetry(t *testing.T) {
	b, task := mutationBackend(t)
	binding := c.NativeTaskBinding{Version: 3, Goal: task.Goal, TaskURL: task.Reference, TaskScopeSHA: strings.Repeat("b", 64), PolicySHA: b.Capabilities().PolicySHA, ApprovalSequence: 1, BaseBranch: "main", AuthorID: 1, PeerID: 2}
	input := c.TaskBindInput{Binding: binding, ExpectedRevision: task.Revision, RequestID: "bind"}
	actor := c.Principal{ID: "planner", Kind: "agent"}
	t.Setenv("COOP_KATA_LOST_REPLY", "meta")
	bound, err := b.Bind(context.Background(), input, actor)
	if err != nil || bound.BindingActor.ID != "planner" || bound.Revision != 2 {
		t.Fatalf("ambiguous write %+v %v", bound, err)
	}
	retried, err := b.Bind(context.Background(), input, actor)
	if err != nil || retried.Revision != 2 {
		t.Fatal("identical retry mutated", err)
	}
	input.Binding.BaseBranch = "release"
	input.ExpectedAssignmentSHA = hash(binding)
	if _, err = b.Bind(context.Background(), input, actor); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	input.ExpectedRevision = 2
	input.ExpectedAssignmentSHA = "wrong"
	if _, err = b.Bind(context.Background(), input, actor); !errors.Is(err, c.ErrConflict) {
		t.Fatal("wrong assignment CAS accepted", err)
	}
}
func TestCompleteReconcilesLostReplyAndRejectsWrongAssignment(t *testing.T) {
	b, task := mutationBackend(t)
	binding := c.NativeTaskBinding{Version: 3, Goal: task.Goal, TaskURL: task.Reference, PolicySHA: b.Capabilities().PolicySHA}
	actor := c.Principal{ID: "author"}
	bound, err := b.Bind(context.Background(), c.TaskBindInput{Binding: binding, ExpectedRevision: 1, RequestID: "bind"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	in := c.TaskCompleteInput{Reference: task.Reference, Goal: task.Goal, AssignmentSHA: "wrong", PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "close"}
	if _, err = b.Complete(context.Background(), in, actor); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	in.AssignmentSHA = hash(*bound.Binding)
	t.Setenv("COOP_KATA_LOST_REPLY", "close")
	closed, err := b.Complete(context.Background(), in, actor)
	if err != nil || closed.ClosedReason != "done" {
		t.Fatalf("%+v %v", closed, err)
	}
	if _, err = b.Complete(context.Background(), in, actor); err != nil {
		t.Fatal(err)
	}
}

func TestKataNativeProfileIntegration(t *testing.T) {
	home := os.Getenv("COOP_KATA_UAT_HOME")
	if home == "" {
		t.Skip("requires an explicitly provisioned private native Kata acceptance profile")
	}
	cfg := Config{Provider: "kata", Binary: "/opt/homebrew/bin/kata", Home: home, ProjectName: os.Getenv("COOP_KATA_UAT_PROJECT_NAME"), ProjectUID: os.Getenv("COOP_KATA_UAT_PROJECT_UID"), PolicySHA: strings.Repeat("a", 64)}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor := c.Principal{ID: "coop-provider-acceptance", Kind: "owner"}
	in := c.TaskCreateInput{Goal: c.GoalRef{Source: "master", MasterID: "provider-acceptance", GoalID: "provider-integration", RevisionSHA256: strings.Repeat("b", 64)}, ExpectedApprovalSequence: 1, Repository: "example/coop", BaseBranch: "main", Title: "Coop native provider acceptance fixture", Body: "Disposable local task for Create, Bind and Read adapter acceptance. This fixture does not authorize engineering work or GitHub actions.", RequestID: "native-profile-create-v1"}
	task, err := b.Create(ctx, in, actor)
	if err != nil {
		t.Fatal(err)
	}
	binding := c.NativeTaskBinding{Version: 3, Goal: in.Goal, TaskURL: task.Reference, TaskScopeSHA: hash(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{in.Title, in.Body}), PolicySHA: cfg.PolicySHA, ApprovalSequence: 1, BaseBranch: "main", AuthorID: 1, PeerID: 2}
	bound, err := b.Bind(ctx, c.TaskBindInput{Binding: binding, ExpectedRevision: task.Revision, RequestID: "native-profile-bind-v1"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, task.Reference)
	if err != nil || read.Binding == nil || hash(*read.Binding) != hash(binding) || read.BindingActor.ID != actor.ID || read.Revision != bound.Revision {
		t.Fatalf("native read %+v %v", read, err)
	}
	again, err := b.Create(ctx, in, actor)
	if err != nil || again.Reference != task.Reference {
		t.Fatalf("native create retry %+v %v", again, err)
	}
	t.Logf("native Unix daemon Create/Bind/Read and stable create replay verified: %s revision %d", read.Reference, read.Revision)
}

func TestCreatePinsProjectBeforeMutation(t *testing.T) {
	b, task := mutationBackend(t)
	t.Setenv("COOP_KATA_WRONG_PROJECT", "1")
	_, err := b.Create(context.Background(), c.TaskCreateInput{Goal: task.Goal, Repository: task.Repository, BaseBranch: "main", Title: task.Title, Body: task.Body, RequestID: "wrong-project"}, c.Principal{ID: "owner"})
	if !errors.Is(err, c.ErrForbidden) {
		t.Fatal("created under mutable project name without validating its UID", err)
	}
}

func TestClosedNonDoneTaskCannotComplete(t *testing.T) {
	b, task := mutationBackend(t)
	binding := c.NativeTaskBinding{Version: 3, Goal: task.Goal, TaskURL: task.Reference, PolicySHA: b.Capabilities().PolicySHA}
	actor := c.Principal{ID: "author"}
	if _, err := b.Bind(context.Background(), c.TaskBindInput{Binding: binding, ExpectedRevision: 1, RequestID: "bind"}, actor); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.(*kata).cfg.Home, "issue.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Issue rawIssue `json:"issue"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"superseded", "canceled", "wontfix", "audit-no-change"} {
		envelope.Issue.Status = "closed"
		envelope.Issue.ClosedReason = reason
		raw, err = json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = b.Complete(context.Background(), c.TaskCompleteInput{Reference: task.Reference, Goal: task.Goal, AssignmentSHA: hash(binding), PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "complete"}, actor)
		if !errors.Is(err, c.ErrConflict) {
			t.Fatalf("%s satisfied completion: %v", reason, err)
		}
	}
}
func TestNativeGatewayRefusesRemoteProfileSelection(t *testing.T) {
	cfg := testConfig(t)
	cfg.Binary = filepath.Join(t.TempDir(), "kata")
	if err := os.WriteFile(cfg.Binary, []byte("#!/bin/sh\necho '{\"kind\":\"remote\",\"source\":\"workspace\",\"network\":\"tcp\",\"address\":\"https://unsafe.invalid\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := c.KataTaskReference(testProject, testIssue, "example/coop")
	if _, err = b.Read(context.Background(), ref); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("remote selection accepted", err)
	}
}

func TestCreateAcceptsExplicitEmptyDependencies(t *testing.T) {
	b, task := mutationBackend(t)
	created, err := b.Create(context.Background(), c.TaskCreateInput{Goal: task.Goal, Repository: task.Repository, BaseBranch: "main", Title: task.Title, Body: task.Body, DependsOn: []string{}, RequestID: "empty-dependencies"}, c.Principal{ID: "owner"})
	if err != nil || created.Reference != task.Reference {
		t.Fatalf("explicit empty dependencies failed metadata round trip %+v %v", created, err)
	}
}
