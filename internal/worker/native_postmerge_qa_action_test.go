package worker

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type postMergeQAActionFixture struct {
	cfg       Config
	master    *releaseMasterFixture
	github    *GitHubClient
	transport *nativeDeliveryTransport
	execution NativeExecution
	action    NativeAction
	sends     int
	events    []string
	loseWrite bool
}

func newPostMergeQAActionFixture(t *testing.T) *postMergeQAActionFixture {
	t.Helper()
	cfg, m, catalog, _ := dispatchFixture(t)
	cfg.Projects, cfg.ProjectOwners = nil, nil
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[2].Name, cfg.Team.Members[2].Actor, "qa"
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "qa", cfg.Self.ID, "qa"
	task := catalog.catalog.Tasks[0]
	f := &postMergeQAActionFixture{cfg: cfg, master: &releaseMasterFixture{dispatchMaster: m}}
	s := &nativeDeliveryTransport{t: t, cfg: cfg, head: strings.Repeat("a", 40), merged: true, closed: true, taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding)}
	f.transport = s
	artifact := &QAArtifact{Kind: "workflow", HeadSHA: s.head, SHA256: strings.Repeat("e", 64), URL: "https://api.github.com/repos/code/repo/actions/artifacts/1", Revision: "1"}
	s.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: artifact})
	s.commands = &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		result, err := s.response(owner, endpoint, method, body)
		if method == "POST" && endpoint == "repos/code/repo/pulls/1/reviews" {
			var request struct{ Event string }
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			f.events = append(f.events, request.Event)
			// The shared premerge transport knows APPROVE/REQUEST_CHANGES only.
			if request.Event == "COMMENT" {
				s.reviews[len(s.reviews)-1]["state"] = "COMMENTED"
			}
			if f.loseWrite {
				return CommandResult{}, errors.New("synthetic lost review write response")
			}
		}
		return result, err
	}}
	f.github = NewGitHubClient(s)
	p, err := f.github.ReadNativeReleasePR(t.Context(), cfg, task, m.authority, "https://github.com/code/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	s.reviews = []map[string]any{{"fullDatabaseId": 1, "state": "APPROVED", "body": NativePeerMetadata(peer), "submittedAt": time.Now().Add(-time.Second).UTC(), "commit": map[string]any{"oid": s.head}, "author": map[string]any{"__typename": "Bot", "databaseId": task.Binding.PeerID, "login": cfg.Team.Members[0].Actor.Login}}}
	m.links = []c.ArtifactLink{{Kind: "task", Goal: task.Binding.Goal, URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}, {Kind: "pull_request", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
	f.master.handoff = c.Handoff{ID: "first-assessment", CreatedAt: time.Now().UTC(), Input: c.HandoffInput{Kind: "first-postmerge-qa", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, ArtifactSHA: artifact.SHA256, Environment: m.authority.Revision.Proposal.QA.Environment, EnvironmentRevision: m.authority.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: "owner-evidence", RequestID: "first-assessment"}}
	cat, err := f.github.NativeSnapshot(t.Context(), cfg, f.master)
	if err != nil {
		t.Fatal(err)
	}
	candidates := NativeCandidates(cfg, cat, nil, time.Now())
	if len(candidates) != 1 || !nativeIsPostMergeQA(candidates[0].Job) {
		t.Fatalf("missing real postmerge candidate: %+v", candidates)
	}
	v := candidates[0]
	f.execution = NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	if err := saveNativeExecution(cfg, f.execution); err != nil {
		t.Fatal(err)
	}
	m.onSend = func() { f.sends++ }
	f.action = NativeAction{Operation: "qa", RequestID: "assessment", Verdict: "approved", ReportBody: "Private synthetic customer QA evidence"}
	return f
}

func TestNativePostMergeQAActionEndToEnd(t *testing.T) {
	for _, verdict := range []string{"approved", "changes-needed"} {
		t.Run(verdict, func(t *testing.T) {
			f := newPostMergeQAActionFixture(t)
			f.action.Verdict = verdict
			result, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action)
			if err != nil {
				t.Fatal(err)
			}
			if result.URL != f.execution.Job.PRURL || result.EvidenceMessageID == "" || result.EvidenceBodySHA != BodyHash(f.action.ReportBody) {
				t.Fatalf("missing result/evidence: %+v", result)
			}
			if f.sends != 1 || !reflect.DeepEqual(f.events, []string{"COMMENT"}) || !reflect.DeepEqual(f.transport.writes, []string{"POST repos/code/repo/pulls/1/reviews"}) {
				t.Fatal("unexpected effects", f.sends, f.events, f.transport.writes)
			}
			task, err := f.github.ReadNativeTask(t.Context(), f.cfg, f.execution.Job.TaskURL)
			if err != nil || !task.Task.Closed || !task.Task.Completed {
				t.Fatal("completed task not preserved", task, err)
			}
			pr, err := f.github.ReadNativeReleasePR(t.Context(), f.cfg, task, f.master.authority, f.execution.Job.PRURL)
			if err != nil || !pr.Merged || !pr.Closed || len(pr.Reviews) != 2 {
				t.Fatal("merged PR or original peer review not preserved", pr, err)
			}
			review := nativeLatestReview(pr, task.Binding.QAID)
			var qa NativeQAResult
			if err := nativeMetadata(review.Body, "qa", &qa); err != nil {
				t.Fatal(err)
			}
			if review.Author.ID != task.Binding.QAID || qa.Assessment != "first-postmerge" || qa.HandoffID != f.master.handoff.ID || qa.Verdict != verdict || qa.ArtifactSHA != f.execution.Job.QAArtifact.SHA256 {
				t.Fatal("wrong original actor or assessment metadata", review, qa)
			}
			report, err := f.master.Message(t.Context(), result.EvidenceMessageID)
			if err != nil || ValidateNativePostMergeQA(task.Binding, f.master.authority, review, report, qa, f.master.handoff) != nil {
				t.Fatal("real assessment did not validate", err, qa)
			}
			if ValidateNativeQA(task.Binding, f.master.authority, review, report, qa) == nil {
				t.Fatal("postmerge result became premerge clearance")
			}
			again, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action)
			if err != nil || again != result || f.sends != 1 || len(f.events) != 1 || len(f.transport.writes) != 1 {
				t.Fatal("identical action replayed writes", again, err)
			}
			cat, err := nativeValidationSnapshot(t.Context(), f.cfg, f.master, f.github, f.execution.Job)
			if err != nil || len(NativeCandidates(f.cfg, cat, nil, time.Now())) != 0 {
				t.Fatal("formal assessment admitted another first QA", err)
			}
		})
	}
}

func TestNativePostMergeQAActionLostWriteHeld(t *testing.T) {
	f := newPostMergeQAActionFixture(t)
	f.loseWrite = true
	if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action); err == nil {
		t.Fatal("lost response reported success")
	}
	path := nativeActionPath(f.cfg, f.execution.Job.ID, f.action.RequestID)
	before, err := readPrivate(path, c.MaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	var record nativeActionRecord
	if err := strictTeamJSON(before, &record); err != nil || record.State != "uncertain" || record.Step != "review-published" || record.Result.EvidenceMessageID == "" {
		t.Fatal("lost response did not retain uncertain evidence journal", record, err)
	}
	f.loseWrite = false
	for _, request := range []string{f.action.RequestID, "different-request"} {
		in := f.action
		in.RequestID = request
		if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, in); err == nil {
			t.Fatal("uncertain result replayed", request)
		}
	}
	after, err := readPrivate(path, c.MaxJSON)
	if err != nil || string(before) != string(after) || f.sends != 1 || len(f.transport.reviews) != 2 || len(f.transport.writes) != 1 || len(f.events) != 1 {
		t.Fatal("uncertain journal/effects changed on retry", err, f.sends, f.transport.writes)
	}
	if _, err := os.Stat(nativeActionPath(f.cfg, f.execution.Job.ID, "different-request")); !os.IsNotExist(err) {
		t.Fatal("new request escaped uncertain job hold", err)
	}
}

func TestNativePostMergeQAActionAcknowledgedWriteReadFailureRetry(t *testing.T) {
	for _, change := range []string{"unchanged", "changed-latest-qa", "altered-body"} {
		t.Run(change, func(t *testing.T) {
			f := newPostMergeQAActionFixture(t)
			respond := f.transport.commands.respond
			readFailed := false
			f.transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if len(f.events) == 1 && !readFailed {
					readFailed = true
					return CommandResult{}, errors.New("synthetic post-write read failure")
				}
				return respond(owner, endpoint, method, body)
			}
			if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action); err == nil || !readFailed {
				t.Fatal("acknowledged write did not reach post-write read failure", err)
			}
			path := nativeActionPath(f.cfg, f.execution.Job.ID, f.action.RequestID)
			before, err := readPrivate(path, c.MaxJSON)
			if err != nil {
				t.Fatal(err)
			}
			var record nativeActionRecord
			if err := strictTeamJSON(before, &record); err != nil || record.Step != "review-published" || record.State != "prepared" || record.Result.EvidenceMessageID == "" || record.Result.EvidenceBodySHA != BodyHash(f.action.ReportBody) {
				t.Fatal("acknowledged write did not retain prepared result", record, err)
			}
			switch change {
			case "changed-latest-qa":
				latest := make(map[string]any)
				for key, value := range f.transport.reviews[1] {
					latest[key] = value
				}
				var qa NativeQAResult
				if err := nativeMetadata(latest["body"].(string), "qa", &qa); err != nil {
					t.Fatal(err)
				}
				qa.Verdict = "changes-needed"
				latest["fullDatabaseId"], latest["submittedAt"], latest["body"] = 3, time.Now().Add(time.Second).UTC(), NativeQAMetadata(qa)
				f.transport.reviews = append(f.transport.reviews, latest)
			case "altered-body":
				f.transport.reviews[1]["body"] = "Altered public assessment text\n" + f.transport.reviews[1]["body"].(string)
			}
			evidenceReads := 0
			f.master.onMessage = func() { evidenceReads++ }
			result, retryErr := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action)
			after, err := readPrivate(path, c.MaxJSON)
			if err != nil {
				t.Fatal(err)
			}
			if change == "unchanged" {
				if retryErr != nil || result.Phase != "completed" || result.URL != f.execution.Job.PRURL || result.EvidenceMessageID != record.Result.EvidenceMessageID || result.EvidenceBodySHA != record.Result.EvidenceBodySHA || evidenceReads == 0 {
					t.Fatal("retry did not verify its own formal result", result, evidenceReads, retryErr)
				}
				if err := strictTeamJSON(after, &record); err != nil || record.State != "completed" || record.Step != "review-published" {
					t.Fatal("verified retry did not complete original journal", record, err)
				}
			} else if retryErr == nil || string(before) != string(after) {
				t.Fatal("changed formal result was not held with original journal intact", retryErr)
			}
			if f.sends != 1 || !reflect.DeepEqual(f.events, []string{"COMMENT"}) || !reflect.DeepEqual(f.transport.writes, []string{"POST repos/code/repo/pulls/1/reviews"}) {
				t.Fatal("acknowledged result retry repeated effects", f.sends, f.events, f.transport.writes)
			}
		})
	}
}

func TestNativePostMergeQAActionRefusesChangedSafetyInputs(t *testing.T) {
	for _, timing := range []string{"before-action", "after-evidence"} {
		for _, change := range []string{"head", "artifact", "assignment", "epoch", "revoked-goal", "self-qa", "prior-qa", "peer", "source-active", "source-uncertain"} {
			t.Run(timing+"/"+change, func(t *testing.T) {
				f := newPostMergeQAActionFixture(t)
				mutate := func() {
					switch change {
					case "head":
						f.transport.head = strings.Repeat("b", 40)
					case "artifact":
						artifact := *f.execution.Job.QAArtifact
						artifact.SHA256 = BodyHash("changed artifact")
						f.transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: f.execution.Goal, TaskURL: f.execution.Job.TaskURL, AssignmentSHA: NativeAssignmentSHA(*f.execution.Binding), Artifact: &artifact})
					case "assignment":
						binding := *f.execution.Binding
						binding.ApprovalSequence++
						f.transport.metadata = NativeTaskMetadata(binding)
					case "epoch":
						f.master.self.RestoreEpoch++
					case "revoked-goal":
						f.master.authority.Valid, f.master.authority.State = false, "paused"
					case "self-qa":
						// Keep the viewer and master identity coherent, but not the assigned QA actor.
						f.cfg.Self = f.cfg.Team.Members[1].Actor
						f.cfg.Name, f.cfg.Master.AgentID = f.cfg.Team.Members[1].Name, "author"
						f.transport.cfg = f.cfg
						f.master.self.Principal.ID, f.master.self.Principal.GitHubBotID, f.master.self.Placement.AgentID = "author", f.cfg.Self.ID, "author"
					case "prior-qa":
						f.transport.reviews = append(f.transport.reviews, map[string]any{"fullDatabaseId": 2, "state": "DISMISSED", "body": "Earlier QA", "submittedAt": time.Now().Add(-time.Second).UTC(), "commit": map[string]any{"oid": f.transport.head}, "author": map[string]any{"__typename": "Bot", "databaseId": f.execution.Binding.QAID, "login": f.cfg.Self.Login}})
					case "peer":
						f.transport.reviews[0]["state"] = "CHANGES_REQUESTED"
					case "source-active", "source-uncertain":
						source := f.execution
						source.Job.ID, source.Job.Kind = BodyHash("outstanding source"), "deliver"
						if change == "source-uncertain" {
							source.Phase = "completed"
							if err := saveNativeAction(f.cfg, nativeActionRecord{Version: 3, JobID: source.Job.ID, RequestID: "merge", Operation: "deliver", State: "uncertain"}); err != nil {
								t.Fatal(err)
							}
						}
						if err := saveNativeExecution(f.cfg, source); err != nil {
							t.Fatal(err)
						}
					}
				}
				wantSends := 0
				if timing == "before-action" {
					mutate()
				} else {
					wantSends = 1
					f.master.onSend = func() { f.sends++; mutate() }
				}
				if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action); err == nil {
					t.Fatal("changed safety input allowed assessment")
				}
				if f.sends != wantSends || len(f.events) != 0 || len(f.transport.writes) != 0 || !f.transport.closed || !f.transport.merged {
					t.Fatal("refusal wrote or changed completed source", f.sends, f.events, f.transport.writes)
				}
			})
		}
	}
}

func TestNativePostMergeQADispatchOriginalActorOnce(t *testing.T) {
	f := newPostMergeQAActionFixture(t)
	code := &fakeOpenCode{running: true}
	d, err := NewNativeDispatcher(f.cfg, f.master, f.github, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	x := f.execution
	x.Phase = "selected"
	if err := saveNativeExecution(f.cfg, x); err != nil {
		t.Fatal(err)
	}
	if _, err := d.advance(t.Context(), &x); err != nil || x.Phase != "active" || code.calls != 1 {
		t.Fatal("original QA did not submit once", x.Phase, code.calls, err)
	}
	if x.Job.Master.AgentID != f.master.authority.Actors.QA.PrincipalID || x.Job.Master.ExecutorID != f.execution.Binding.QAID {
		t.Fatal("dispatcher substituted original QA", x.Job.Master)
	}
	if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, x.Job.ID, f.action); err != nil {
		t.Fatal(err)
	}
	code.running = false
	if done, err := d.advance(t.Context(), &x); err != nil || !done || x.Phase != "completed" {
		t.Fatal("real action result not consumed", done, x.Phase, err)
	}
	cat, err := f.github.NativeSnapshot(t.Context(), f.cfg, f.master)
	if err != nil || len(NativeCandidates(f.cfg, cat, map[string]NativeExecution{x.Job.ID: x}, time.Now())) != 0 || code.calls != 1 {
		t.Fatal("completed assessment submitted again", code.calls, err)
	}
	if f.sends != 1 || len(f.transport.writes) != 1 || !f.transport.closed || !f.transport.merged {
		t.Fatal("dispatcher mutated source or duplicated effects", f.sends, f.transport.writes)
	}
}
