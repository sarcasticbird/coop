package master

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// This opt-in test writes only to the explicitly selected disposable fixture.
func TestKataNativeIntakeAdoptionIntegration(t *testing.T) {
	home := os.Getenv("COOP_KATA_TEST_HOME")
	if home == "" {
		t.Skip("requires explicitly selected disposable native profile")
	}
	cfg := taskstore.Config{Provider: "kata", Binary: "/opt/homebrew/bin/kata", Home: home, ProjectName: "coop-consolidation-acceptance", ProjectUID: "01M5N8JN3PA04EJRZSHHKHY4TJ", InstanceUID: "01M5N8JCKTYDTF0CE5FXR9AKX8", PolicySHA: strings.Repeat("a", 64)}
	backend, err := taskstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	work := backend.(taskstore.WorkBackend)
	cursor := taskstore.EventCursor{InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID}
	for {
		page, e := work.Events(t.Context(), cursor, 100)
		if e != nil {
			t.Fatal(e)
		}
		if page.Next.AfterID == cursor.AfterID {
			break
		}
		cursor = page.Next
	}
	s, o, p, create, _ := taskFixture(t)
	s.Tasks = backend
	a, err := s.ResolveGoal(t.Context(), o, create.Goal)
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.Store.Thread(t.Context(), o, a.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err = s.Store.Configure(t.Context(), []c.Principal{bridge}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.SetMembers(t.Context(), o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: append(th.Members, bridge.ID), RequestID: "native-add-bridge"}); err != nil {
		t.Fatal(err)
	}
	conf := ConversationConfig{InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID, BridgePrincipal: bridge.ID, IntakePrincipal: p.ID, TeamThreadID: th.ID, TeamIssueUID: "01M5N8JN47ME8RRF4482K89JDJ"}
	if err = s.Store.ConfigureConversations(t.Context(), work, conf); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.MapConversation(t.Context(), o, c.MapConversation{ThreadID: th.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: cfg.InstanceUID, ProjectUID: cfg.ProjectUID, IssueUID: conf.TeamIssueUID}, AfterEventID: cursor.AfterID, RequestID: "native-map-team"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), cfg.Binary, "--workspace", home, "--project", cfg.ProjectName, "--json", "--as", "external-fixture-human", "create", "Native same UID adoption acceptance "+rand.Text(), "--force-new", "--body", "Bounded native planning request")
	cmd.Dir = home
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "KATA_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "KATA_HOME="+home)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal("disposable native issue creation", err)
	}
	var created struct {
		Issue taskstore.Issue `json:"issue"`
	}
	if err = json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Store.Pending(t.Context(), p, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatal("native intake acknowledgement", pending, err)
	}
	ref, err := c.KataTaskReference(cfg.ProjectUID, created.Issue.UID, create.Repository)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.TaskRead(t.Context(), p, c.TaskReadInput{Reference: ref})
	if err != nil || observed.Binding != nil {
		t.Fatalf("planner current CAS inspection %+v %v", observed, err)
	}
	in := c.TaskAdoptInput{Reference: ref, Goal: create.Goal, Repository: create.Repository, BaseBranch: create.BaseBranch, ExpectedRevision: observed.Revision, ExpectedApprovalSequence: 1, RequestID: "actual-native-adopt"}
	adopted, err := s.TaskAdopt(t.Context(), p, in)
	if err != nil || adopted.IssueUID != created.Issue.UID || adopted.Binding == nil {
		t.Fatalf("same UID native adoption %+v %v", adopted, err)
	}
	again, err := s.TaskAdopt(t.Context(), p, in)
	if err != nil || again.Reference != adopted.Reference {
		t.Fatal("native replay", again, err)
	}
	page, err := s.Artifacts(t.Context(), o, c.ArtifactQuery{Goal: create.Goal})
	if err != nil || len(page.Links) != 1 || page.Links[0].URL != ref {
		t.Fatal("native artifact registry", page, err)
	}
	if err = s.Store.PollConversations(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Store.Pending(t.Context(), p, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatal("native receipt echo duplicated model turn", pending, err)
	}
	t.Logf("disposable native intake/adoption retained issue %s, initial revision %d, bound revision %d", created.Issue.UID, observed.Revision, adopted.Revision)
}
