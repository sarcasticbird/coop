package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func TestFinalReviewPlacementHistory(t *testing.T) {
	for _, replaceInstance := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-instance", true: "new-instance"}[replaceInstance], func(t *testing.T) {
			cfg, comms, s, o, b, th, code := commsFixtureWithRetiredHistory(t, true)
			ctx := context.Background()
			client := comms.Client.(*masterclient.Client)
			if _, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "before move", RequestID: "move-turn"}); e != nil {
				t.Fatal(e)
			}
			if e := comms.Tick(ctx); e != nil {
				t.Fatal(e)
			}
			turn, e := comms.Next(ctx)
			if e != nil || turn == nil {
				t.Fatal(e)
			}
			if e = comms.Advance(ctx, turn); e != nil {
				t.Fatal(e)
			}
			if e = QueueMasterReply(cfg, turn.Delivery.ID, "saved reply"); e != nil {
				t.Fatal(e)
			}
			if e = comms.FlushOutbox(ctx); e != nil {
				t.Fatal(e)
			}
			code.running = false
			goal := c.GoalRef{Source: "master", MasterID: cfg.Master.MasterID, GoalID: "native-goal", RevisionSHA256: BodyHash("goal")}
			native := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: goal, Phase: "completed", Job: Job{ID: BodyHash("native-move"), Kind: "implement", Phase: "completed", SessionID: "saved-native-session", MessageID: "saved-native-message", SubmissionState: "completed", Master: &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ExecutorID: cfg.Self.ID, Goal: &goal}}}
			if e = saveNativeExecution(cfg, native); e != nil {
				t.Fatal(e)
			}
			if e = saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: native.Job.ID, RequestID: "move-action", State: "completed"}); e != nil {
				t.Fatal(e)
			}
			releaseAPI := &releaseTestAPI{}
			releaseHandoff := c.Handoff{ID: "ho_placement", Input: c.HandoffInput{Goal: goal, Tag: "v0.2.7", Workflow: "release.yml"}}
			releaseSource := releaseTarget{Repository: "code/repo", SHA: strings.Repeat("c", 40)}
			if receipt, err := publishNativeRelease(ctx, cfg, releaseAPI, releaseHandoff, releaseSource); err != nil || receipt.Phase != "waiting" {
				t.Fatal("release fixture", receipt, err)
			}
			_, scheduleCatalog, _ := nativeWorkFixture(t)
			scheduleCatalog.Tasks = nil
			oldDispatcher, e := NewNativeDispatcher(cfg, client, &dispatchCatalog{catalog: scheduleCatalog}, code, &fakeCheckouts{}, RealClock{})
			if e != nil {
				t.Fatal(e)
			}
			candidates, e := oldDispatcher.candidates(scheduleCatalog)
			if e != nil || len(candidates) != 1 {
				t.Fatalf("old production candidates: %v", e)
			}
			scheduleBefore, e := os.ReadFile(filepath.Join(masterDir(cfg), "schedule.json"))
			if e != nil {
				t.Fatal(e)
			}
			oldCheckout, origin := placementCheckoutFixture(t, cfg, native.Job)
			ledgerBefore, e := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
			if e != nil {
				t.Fatal(e)
			}
			oldPath := filepath.Join(t.TempDir(), "previous.json")
			raw, _ := json.Marshal(cfg)
			if e = os.WriteFile(oldPath, raw, 0600); e != nil {
				t.Fatal(e)
			}
			next := cfg
			nextMaster := *cfg.Master
			next.Master = &nextMaster
			next.Master.HostID = "moved-host"
			next.Master.Generation = 2
			if replaceInstance {
				next.Master.InstanceID = BodyHash("replacement-instance")
			}
			if _, e = NewNativeDispatcher(next, client, &dispatchCatalog{}, code, &fakeCheckouts{}, RealClock{}); e == nil {
				t.Fatal("unproven schedule placement accepted")
			}
			if e = CheckInstall(next, oldPath); e == nil {
				t.Fatal("unproven installer placement accepted")
			}
			if e = s.Quiesce(ctx, o, 1); e != nil {
				t.Fatal(e)
			}
			if _, e = ReconcileMaster(ctx, cfg, client, code, false); e != nil {
				t.Fatal(e)
			}
			evidence, e := s.RecoveryEvidence(ctx, o, b.ID, "")
			if e != nil {
				t.Fatal(e)
			}
			if len(evidence) < 5 {
				t.Fatalf("history missing: %d", len(evidence))
			}
			for _, r := range evidence {
				if e = s.ResolveRecovery(ctx, o, c.RecoveryResolution{RestoreEpoch: 1, AgentID: b.ID, EvidenceID: r.ID, Reason: "terminal old placement"}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = ReconcileMaster(ctx, cfg, client, code, true); e != nil {
				t.Fatal(e)
			}
			move := c.PlacementChange{Placement: c.Placement{AgentID: b.ID, HostID: next.Master.HostID, InstanceID: next.Master.InstanceID, Generation: 2}, ExpectedGeneration: 1, RestoreEpoch: 1}
			if _, e = s.SetPlacement(ctx, o, move); e != nil {
				t.Fatal(e)
			}
			hash := sha256.Sum256([]byte("new-placement-token"))
			policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: b.ID, SHA256: hex.EncodeToString(hash[:]), InstanceID: next.Master.InstanceID, Generation: 2}}}
			server := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: s}, policy))
			defer server.Close()
			moved, e := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: cfg.Master.MasterID, Token: "new-placement-token", HTTPClient: server.Client()})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = client.Pending(ctx, c.PendingQuery{}); e == nil {
				t.Fatal("old token generation accepted")
			}
			for _, state := range []string{"received", "running"} {
				if e = client.SetDelivery(ctx, c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: turn.Delivery.ID, InstanceID: cfg.Master.InstanceID, Generation: 1, State: state, Intent: &c.NativeIntent{SessionID: turn.Job.SessionID, MessageID: turn.Job.MessageID}}); e == nil {
					t.Fatal("old generation delivery update accepted", state)
				}
			}
			if _, e = client.PublishReply(ctx, c.PublishReply{RestoreEpoch: 1, DeliveryID: turn.Delivery.ID, InstanceID: cfg.Master.InstanceID, Generation: 1, Intent: c.NativeIntent{SessionID: turn.Job.SessionID, MessageID: turn.Job.MessageID}, Body: "old replay", RequestID: "old-generation-reply"}); e == nil {
				t.Fatal("old generation reply accepted")
			}
			schedulePath := filepath.Join(masterDir(cfg), "schedule.json")
			if e = atomicPrivate(schedulePath, append(append([]byte{}, scheduleBefore...), '\n')); e != nil {
				t.Fatal(e)
			}
			if _, e = AdoptMasterPlacement(ctx, cfg, next, moved); e == nil {
				t.Fatal("changed schedule accepted before adoption")
			}
			if e = atomicPrivate(schedulePath, scheduleBefore); e != nil {
				t.Fatal(e)
			}
			if _, e = AdoptMasterPlacement(ctx, cfg, next, moved); e != nil {
				t.Fatalf("stopped placement adoption: %v", e)
			}
			writes := func() int {
				n := 0
				for _, call := range releaseAPI.calls {
					if strings.HasPrefix(call, "POST ") {
						n++
					}
				}
				return n
			}
			writesBefore := writes()
			if receipt, err := publishNativeRelease(ctx, next, releaseAPI, releaseHandoff, releaseSource); err != nil || receipt.Phase != "waiting" || writes() != writesBefore {
				t.Fatal("pending publication lost or replayed after accepted placement", receipt, err)
			}
			releaseAPI.published = true
			for i := 0; i < 2; i++ {
				if receipt, err := publishNativeRelease(ctx, next, releaseAPI, releaseHandoff, releaseSource); err != nil || receipt.Phase != "published" || writes() != writesBefore {
					t.Fatal("accepted publication observation could not advance or replayed writes", receipt, err)
				}
			}
			adoption, e := loadPlacementAdoption(next)
			if e != nil || string(adoption.ScheduleBefore) != string(scheduleBefore) {
				t.Fatal("original schedule evidence lost", e)
			}
			movedSchedule, e := os.ReadFile(schedulePath)
			if e != nil {
				t.Fatal(e)
			}
			// Exercise the two interrupted publication boundaries using the
			// retained proof, then verify an unrelated edit is never repaired.
			for _, beforeSchedule := range []bool{true, false} {
				if beforeSchedule {
					if e = atomicPrivate(schedulePath, scheduleBefore); e != nil {
						t.Fatal(e)
					}
				}
				if e = atomicPrivate(filepath.Join(masterDir(next), "connection.json"), mustRecoveryJSON(adoption.ConnectionBefore)); e != nil {
					t.Fatal(e)
				}
				if _, e = AdoptMasterPlacement(ctx, cfg, next, moved); e != nil {
					t.Fatalf("interrupted adoption retry: %v", e)
				}
			}
			if e = atomicPrivate(schedulePath, append(append([]byte{}, movedSchedule...), '\n')); e != nil {
				t.Fatal(e)
			}
			if _, e = AdoptMasterPlacement(ctx, cfg, next, moved); e == nil {
				t.Fatal("changed schedule accepted on retry")
			}
			if e = atomicPrivate(schedulePath, movedSchedule); e != nil {
				t.Fatal(e)
			}

			if _, e = AdoptMasterPlacement(ctx, cfg, next, moved); e != nil {
				t.Fatalf("adoption retry: %v", e)
			}
			if e = CheckInstall(next, oldPath); e != nil {
				t.Fatalf("proven installation rejected: %v", e)
			}
			if _, e = ReconcileMaster(ctx, next, moved, code, false); e != nil {
				t.Fatalf("new placement report: %v", e)
			}
			newEvidence, e := s.RecoveryEvidence(ctx, o, b.ID, "")
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range newEvidence {
				if e = s.ResolveRecovery(ctx, o, c.RecoveryResolution{RestoreEpoch: 1, AgentID: b.ID, EvidenceID: r.ID, Reason: "terminal old placement"}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = ReconcileMaster(ctx, next, moved, code, true); e != nil {
				t.Fatalf("new placement acceptance: %v", e)
			}
			if e = s.Resume(ctx, o, 1); e != nil {
				t.Fatal(e)
			}
			restarted, e := NewMasterComms(next, moved, code, RealClock{})
			if e != nil {
				t.Fatal(e)
			}
			if e = restarted.Tick(ctx); e != nil {
				t.Fatal(e)
			}
			work, e := restarted.Next(ctx)
			if e != nil || work != nil {
				t.Fatalf("old conversation reopened %+v %v", work, e)
			}
			dispatcher, e := NewNativeDispatcher(next, moved, &dispatchCatalog{}, code, &fakeCheckouts{}, RealClock{})
			if e != nil || dispatcher.HasActive() {
				t.Fatalf("native history replayed %v", e)
			}
			afterCandidates, e := dispatcher.candidates(scheduleCatalog)
			if e != nil || len(afterCandidates) != 1 || afterCandidates[0].Job.FirstEligibleAt != candidates[0].Job.FirstEligibleAt {
				t.Fatal("move lost scheduler fairness", e)
			}
			verifyPlacementCheckout(t, next, oldCheckout, origin)
			if code.calls != 1 {
				t.Fatalf("native resubmitted %d", code.calls)
			}
			saved, e := loadMasterTurn(next, turn.Delivery.ID)
			if e != nil || saved.InstanceID != cfg.Master.InstanceID || saved.Generation != 1 {
				t.Fatalf("historical identity rewritten: %+v %v", saved, e)
			}
			after, e := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
			if e != nil || string(after) != string(ledgerBefore) {
				t.Fatal("ledger identity relabelled", e)
			}
			raw, e = os.ReadFile(nativeExecutionPath(next, native.Job.ID))
			if e != nil {
				t.Fatal(e)
			}
			var original NativeExecution
			if json.Unmarshal(raw, &original) != nil || original.InstanceID != cfg.Master.InstanceID || original.Generation != 1 {
				t.Fatal("native identity relabelled")
			}
			finalReviewFreshPlacementWork(t, next, restarted, s, o, b, th, moved, code, ledgerBefore)
			// A record changed after adoption is not covered by the transition proof.
			saved.Message.Body = "tampered"
			saved.BodySHA = BodyHash(saved.Message.Body)
			raw, _ = json.Marshal(saved)
			if e = atomicPrivate(masterRecordPath(next, saved.Delivery.ID), raw); e != nil {
				t.Fatal(e)
			}
			if _, e = loadMasterTurn(next, saved.Delivery.ID); e == nil {
				t.Fatal("modified historical record accepted")
			}
		})
	}
}

func finalReviewFreshPlacementWork(t *testing.T, cfg Config, comms *MasterComms, s *master.Store, owner, bot c.Principal, thread c.Thread, client *masterclient.Client, code *fakeOpenCode, ledger []byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bot.ID}, Body: "after move", RequestID: "new-placement-turn"}); err != nil {
		t.Fatal(err)
	}
	if err := comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatalf("new conversation missing: %v", err)
	}
	code.running = true
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "new placement reply"); err != nil {
		t.Fatal(err)
	}
	if err = comms.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	code.running = false
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	saved, err := loadMasterTurn(cfg, turn.Delivery.ID)
	if err != nil || saved.InstanceID != cfg.Master.InstanceID || saved.Generation != cfg.Master.Generation || saved.Phase != "completed" {
		t.Fatalf("new conversation identity: %+v %v", saved, err)
	}
	if code.calls != 2 {
		t.Fatalf("new conversation did not submit exactly once: %d", code.calls)
	}
	author := c.Principal{ID: "new-author", Alias: "new-author", Kind: "agent", GitHubBotID: cfg.Team.Members[1].Actor.ID, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	qa := author
	qa.ID = "new-qa"
	qa.Alias = "new-qa"
	qa.GitHubBotID = cfg.Team.Members[2].Actor.ID
	bot.Capabilities = append(bot.Capabilities, "plan")
	if err = s.Configure(ctx, []c.Principal{bot, author, qa}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMembers(ctx, owner, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: []string{owner.ID, bot.ID, author.ID, qa.ID}, RequestID: "new-placement-members"}); err != nil {
		t.Fatal(err)
	}
	service := &master.Service{Store: s}
	revision, err := service.ProposeGoal(ctx, bot, c.GoalProposal{ThreadID: thread.ID, Purpose: "new placement work", Scope: "bounded", Repositories: []c.RepositoryScope{{Name: "code/repo", BaseBranches: []string{"main"}}}, Acceptance: []string{"fresh work runs"}, AuthorID: author.ID, PeerID: bot.ID, QA: c.QAPolicy{Required: true, ActorID: qa.ID, Environment: "test", EnvironmentRevision: "revision", EvidenceDestination: c.EvidenceDestination{MasterID: cfg.Master.MasterID, ThreadID: thread.ID, Engagement: "coop"}}, RequestID: "new-placement-goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.DecideGoal(ctx, owner, c.GoalDecisionInput{GoalID: revision.Ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, Action: "approve", RequestID: "new-placement-approve"}); err != nil {
		t.Fatal(err)
	}
	authority, err := nativeCurrentGoal(ctx, cfg, client, revision.Ref)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &dispatchCatalog{catalog: NativeCatalog{Complete: true, Goals: []c.GoalAuthority{authority}}}
	dispatcher, err := NewNativeDispatcher(cfg, client, catalog, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := dispatcher.candidates(catalog.catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Job.Kind != "plan" {
		t.Fatalf("fresh engineering missing: %+v", candidates)
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: revision.Ref, Job: candidates[0].Job, Phase: "selected"}
	if _, err = dispatcher.advance(ctx, &x); err != nil {
		t.Fatal(err)
	}
	if code.calls != 3 {
		t.Fatalf("fresh engineering did not submit exactly once: %d", code.calls)
	}
	var metadata string
	created := 0
	now := time.Now().UTC()
	commands := &fakeCommands{respond: func(_, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "graphql" {
			return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": cfg.Self.ID, "login": cfg.Self.Login}}}), nil
		}
		issue := map[string]any{"html_url": "https://github.com/code/repo/issues/1", "number": 1, "title": "Task", "body": "scope", "state": "open"}
		if endpoint == "repos/code/repo/issues" && method == "POST" {
			created++
			return jsonResponse(issue), nil
		}
		if endpoint == "repos/code/repo/issues/1/comments" && method == "POST" {
			var in struct{ Body string }
			if err := json.Unmarshal(body, &in); err != nil {
				return CommandResult{}, err
			}
			metadata = in.Body
			return jsonResponse(map[string]any{"id": 1}), nil
		}
		if endpoint == "repos/code/repo/issues/1" {
			return jsonResponse(issue), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
			return jsonResponse([]any{map[string]any{"id": 1, "body": metadata, "user": map[string]any{"id": cfg.Self.ID, "login": cfg.Self.Login, "type": "Bot"}, "created_at": now}}), nil
		}
		t.Fatalf("unexpected %s %s", method, endpoint)
		return CommandResult{}, nil
	}}
	// Exercise the actual Engine initialization against the original ledger too.
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err = os.WriteFile(tokenPath, []byte("new-placement-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Master.ClientConfig = filepath.Join(t.TempDir(), "client.toml")
	if err = os.WriteFile(cfg.Master.ClientConfig, []byte(fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\n", "https://127.0.0.1:443", cfg.Master.MasterID, tokenPath)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Projects = nil
	cfg.ProjectOwners = nil
	engine := Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), GitHub: NewGitHubClient(commands), OpenCode: code, Checkouts: &fakeCheckouts{}}
	if err = engine.initMaster(); err != nil {
		t.Fatalf("engine rejected adopted ledger: %v", err)
	}
	action := NativeAction{Operation: "create-task", RequestID: "fresh-placement-task", Repository: "code/repo", Title: "Task", Body: "scope", BaseBranch: "main"}
	result, err := RunNativeAction(ctx, cfg, client, NewGitHubClient(commands), x.Job.ID, action)
	if err != nil || result.URL != "https://github.com/code/repo/issues/1" {
		t.Fatalf("new native helper ownership: %+v %v", result, err)
	}
	if _, err = RunNativeAction(ctx, cfg, client, NewGitHubClient(commands), x.Job.ID, action); err != nil || created != 1 {
		t.Fatalf("new helper replay: %d %v", created, err)
	}
	after, err := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil || string(after) != string(ledger) {
		t.Fatal("new work relabelled ledger", err)
	}
}

func TestFinalReviewCorruptRecoveryAcceptance(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	dir := filepath.Join(masterDir(cfg), "recovery")
	if err := ensurePrivateDir(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(filepath.Join(dir, "accepted.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryRetired(cfg, "engineering", "unknown.json", []byte("{}")); err == nil {
		t.Fatal("corrupt acceptance ignored")
	}
}

func placementCheckoutFixture(t *testing.T, cfg Config, job Job) (Job, string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "add", "code.txt")
	localGit(t, origin, "commit", "-m", "local fixture")
	job.TaskURL = "https://github.com/code/repo/issues/99"
	binding := *job.Master
	job.Master = &binding
	job.Master.AssignmentSHA = BodyHash("retained assignment")
	job.Master.BaseBranch = "main"
	job.Master.Role = "implementer"
	checkouts := NewCheckouts(cfg, ExecRunner{})
	checkouts.origin = func(string) string { return origin }
	owned, err := checkouts.Prepare(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(owned.Checkout, "implementation.txt"), []byte("published implementation"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, owned.Checkout, "add", "implementation.txt")
	localGit(t, owned.Checkout, "commit", "-m", "published implementation fixture")
	localGit(t, owned.Checkout, "push", "origin", "HEAD:refs/heads/"+owned.Branch)
	owned.PRURL = "https://github.com/code/repo/pull/99"
	owned.HeadSHA = localGit(t, owned.Checkout, "rev-parse", "HEAD")
	localGit(t, origin, "update-ref", "refs/pull/99/head", owned.HeadSHA)
	if err = os.WriteFile(filepath.Join(owned.Checkout, "checkpoint.txt"), []byte("retained checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	return owned, origin
}
func verifyPlacementCheckout(t *testing.T, cfg Config, old Job, origin string) {
	t.Helper()
	next := old
	next.ID = BodyHash(old.ID + "later fix")
	next.InputRevision = BodyHash("new input")
	next.Kind = "fix"
	next.Checkout = ""
	next.Branch = ""
	next.SessionID = ""
	next.MessageID = ""
	checkouts := NewCheckouts(cfg, ExecRunner{})
	checkouts.origin = func(string) string { return origin }
	// Wrong-head initialization must remain rejected on retry too.
	badCfg := cfg
	badMaster := *cfg.Master
	badCfg.Master = &badMaster
	badCfg.Master.Generation += 100
	bad := NewCheckouts(badCfg, ExecRunner{})
	bad.origin = checkouts.origin
	mismatch := next
	mismatch.HeadSHA = localGit(t, origin, "rev-parse", "main")
	for i := 0; i < 2; i++ {
		if _, err := bad.Prepare(context.Background(), mismatch); err == nil {
			t.Fatal("mismatched fresh PR head accepted")
		}
	}
	fresh, err := checkouts.Prepare(context.Background(), next)
	if err != nil {
		t.Fatalf("same assignment fix after move: %v", err)
	}
	if head := localGit(t, fresh.Checkout, "rev-parse", "HEAD"); head != next.HeadSHA {
		t.Fatalf("fresh fix starts at %s, want PR head %s", head, next.HeadSHA)
	}
	if raw, err := os.ReadFile(filepath.Join(fresh.Checkout, "implementation.txt")); err != nil || string(raw) != "published implementation" {
		t.Fatal("fresh fix lost existing implementation", err)
	}
	if err = os.WriteFile(filepath.Join(fresh.Checkout, "fix.txt"), []byte("fix"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, fresh.Checkout, "add", "fix.txt")
	localGit(t, fresh.Checkout, "commit", "-m", "fix fixture")
	fixedHead := localGit(t, fresh.Checkout, "rev-parse", "HEAD")
	localGit(t, fresh.Checkout, "push", "origin", "HEAD:refs/heads/"+fresh.Branch)
	if err = os.WriteFile(filepath.Join(fresh.Checkout, "local-checkpoint.txt"), []byte("keep local checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = checkouts.Prepare(context.Background(), fresh); err != nil {
		t.Fatal("publication preparation rejected local fix", err)
	}
	if localGit(t, fresh.Checkout, "rev-parse", "HEAD") != fixedHead {
		t.Fatal("publication preparation reset fix commit")
	}
	if raw, err := os.ReadFile(filepath.Join(fresh.Checkout, "local-checkpoint.txt")); err != nil || string(raw) != "keep local checkpoint" {
		t.Fatal("publication preparation erased checkpoint", err)
	}
	if fresh.Checkout == old.Checkout {
		t.Fatal("move reused old checkout")
	}
	raw, err := os.ReadFile(filepath.Join(old.Checkout, "checkpoint.txt"))
	if err != nil || string(raw) != "retained checkpoint" {
		t.Fatal("old checkpoint lost", err)
	}
	if _, err = checkouts.Prepare(context.Background(), old); err == nil {
		t.Fatal("old checkout path accepted under new placement")
	}
	markerRoot := filepath.Dir(fresh.Checkout)
	if cfg.CheckoutModes["code/repo"] == "wt" {
		markerRoot = filepath.Dir(markerRoot)
	}
	marker := filepath.Join(markerRoot, "owner.json")
	raw, err = os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var owner checkoutOwner
	if err = json.Unmarshal(raw, &owner); err != nil {
		t.Fatal(err)
	}
	owner.AgentID = "foreign"
	if err = atomicPrivate(marker, mustRecoveryJSON(owner)); err != nil {
		t.Fatal(err)
	}
	if _, err = checkouts.Prepare(context.Background(), fresh); err == nil {
		t.Fatal("foreign marker accepted")
	}
}
