package master

import (
	"context"
	"errors"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func qaHandoffFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.Principal, c.HandoffInput) {
	t.Helper()
	s, owner, planner, taskInput, _ := taskFixture(t)
	ctx := context.Background()
	qa := c.Principal{ID: "agent-three", Kind: "agent", Alias: "three", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 12}
	if err := s.Store.Configure(ctx, []c.Principal{qa}, nil); err != nil {
		t.Fatal(err)
	}
	current, err := s.ResolveGoal(ctx, owner, taskInput.Goal)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := s.Store.Thread(ctx, owner, current.Revision.Proposal.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.SetMembers(ctx, owner, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: append(thread.Members, qa.ID), RequestID: "qa-member"}); err != nil {
		t.Fatal(err)
	}
	proposal := current.Revision.Proposal
	proposal.QA.Required = true
	proposal.QA.ActorID = qa.ID
	proposal.QA.Environment = "test"
	proposal.QA.EnvironmentRevision = "v1"
	privateThread, err := s.Store.CreateThread(ctx, owner, c.CreateThread{Title: "QA evidence", Engagement: "coop", Members: append([]string{qa.ID}, thread.Members...), RequestID: "qa-thread"})
	if err != nil {
		t.Fatal(err)
	}
	proposal.QA.EvidenceDestination = c.EvidenceDestination{MasterID: taskInput.Goal.MasterID, ThreadID: privateThread.ID, Engagement: "coop"}
	proposal.RequestID = "with-qa"
	revised, err := s.ProposeGoal(ctx, owner, proposal)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.DecideGoal(ctx, owner, c.GoalDecisionInput{GoalID: revised.Ref.GoalID, RevisionSHA256: revised.Ref.RevisionSHA256, ExpectedDecisionSequence: current.DecisionSequence, Action: "approve", RequestID: "approve-qa"})
	if err != nil {
		t.Fatal(err)
	}
	taskInput.Goal = revised.Ref
	taskInput.ExpectedApprovalSequence = approved.ApprovalSequence
	task, err := s.TaskCreate(ctx, owner, taskInput)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.Store.Send(ctx, owner, c.SendMessage{ThreadID: privateThread.ID, Body: "Owner UAT evidence received", RequestID: "handoff-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.HandoffInput{Kind: "reassess-qa", Goal: task.Goal, TaskURL: task.Reference, PRURL: "https://github.com/example/coop/pull/8", AssignmentSHA: digest(task.Binding), HeadSHA: strings.Repeat("a", 40), ArtifactSHA: strings.Repeat("b", 64), ReviewID: "review-8", EvidenceMessageID: evidence.MessageID, RequestID: "qa-again"}
	if _, err = s.RegisterArtifact(ctx, planner, c.ArtifactLink{Goal: task.Goal, Kind: "pull_request", URL: in.PRURL, TaskURL: task.Reference, AssignmentSHA: in.AssignmentSHA, RequestID: "register-pr"}); err != nil {
		t.Fatal(err)
	}
	return s, owner, planner, qa, in
}

func TestHandoffAuthorityIdentityAndRetry(t *testing.T) {
	s, owner, planner, _, in := qaHandoffFixture(t)
	ctx := t.Context()
	first, err := s.CreateHandoff(ctx, planner, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateHandoff(ctx, planner, in)
	if err != nil || again.ID != first.ID {
		t.Fatal("retry changed handoff", err)
	}
	in.HeadSHA = strings.Repeat("c", 40)
	if _, err = s.CreateHandoff(ctx, planner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	in.RequestID = "release"
	in.Kind = "release"
	in.Tag = "v0.2.7"
	in.Workflow = "release.yml"
	in.ReviewID = ""
	if _, err = s.CreateHandoff(ctx, planner, in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("planner granted release", err)
	}
	if _, err = s.CreateHandoff(ctx, owner, in); err != nil {
		t.Fatal(err)
	}
	in.RequestID = "another-release-request"
	if _, err = s.CreateHandoff(ctx, owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("duplicate repository/tag request accepted", err)
	}
	in.RequestID = "stale"
	in.AssignmentSHA = strings.Repeat("d", 64)
	if _, err = s.CreateHandoff(ctx, owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale assignment accepted", err)
	}
	page, err := s.Handoffs(ctx, owner, c.HandoffQuery{Goal: in.Goal})
	if err != nil || len(page.Handoffs) != 2 {
		t.Fatal("missing scoped requests", page, err)
	}
	if in.TaskURL != s.Tasks.(*testTasks).task.Reference || s.Tasks.(*testTasks).binds != 1 {
		t.Fatal("handoff rebound original task")
	}
}

func TestFirstPostmergeQAAuthorityAndRetry(t *testing.T) {
	for _, actor := range []string{"owner", "planner"} {
		for _, evidenceAuthor := range []string{"owner", "qa"} {
			t.Run(actor+"/"+evidenceAuthor, func(t *testing.T) {
				s, owner, planner, qa, in := qaHandoffFixture(t)
				in.Kind, in.ReviewID = "first-postmerge-qa", ""
				in.Environment, in.EnvironmentRevision = "test", "v1"
				if evidenceAuthor == "qa" {
					report, err := s.Store.Send(t.Context(), qa, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, in.Goal), Body: "Original QA evidence", RequestID: "qa-evidence"})
					if err != nil {
						t.Fatal(err)
					}
					in.EvidenceMessageID = report.MessageID
				}
				p := owner
				if actor == "planner" {
					p = planner
				}
				first, err := s.CreateHandoff(t.Context(), p, in)
				if err != nil || first.Input != in {
					t.Fatalf("handoff rejected or changed: %+v, %v", first, err)
				}
				again, err := s.CreateHandoff(t.Context(), p, in)
				if err != nil || again != first {
					t.Fatalf("retry changed handoff: %+v, %v", again, err)
				}
				for _, field := range []string{"head", "artifact", "evidence"} {
					changed := in
					switch field {
					case "head":
						changed.HeadSHA = strings.Repeat("c", 40)
					case "artifact":
						changed.ArtifactSHA = strings.Repeat("c", 64)
					case "evidence":
						report, err := s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, in.Goal), Body: "New evidence", RequestID: "changed-evidence"})
						if err != nil {
							t.Fatal(err)
						}
						changed.EvidenceMessageID = report.MessageID
					}
					if _, err = s.CreateHandoff(t.Context(), p, changed); !errors.Is(err, c.ErrConflict) {
						t.Fatalf("changed %s retry accepted: %v", field, err)
					}
				}
				page, err := s.Handoffs(t.Context(), owner, c.HandoffQuery{Goal: in.Goal})
				if err != nil || len(page.Handoffs) != 1 || page.Handoffs[0].Input != in || s.Tasks.(*testTasks).binds != 1 {
					t.Fatalf("request changed task or list: %+v, %v", page, err)
				}
			})
		}
	}
}

func TestFirstPostmergeQAGuards(t *testing.T) {
	for _, change := range []string{"environment", "revision", "no-qa", "unprivileged", "evidence-author", "evidence-thread", "task-binding", "pr-binding", "pr-parent", "foreign-pr", "paused", "quiescent"} {
		t.Run(change, func(t *testing.T) {
			s, owner, planner, qa, in := qaHandoffFixture(t)
			in.Kind, in.ReviewID = "first-postmerge-qa", ""
			in.Environment, in.EnvironmentRevision = "test", "v1"
			want := c.ErrForbidden
			var err error
			switch change {
			case "environment":
				in.Environment = "test "
			case "revision":
				in.EnvironmentRevision = "v2"
			case "no-qa":
				// The retained release fixture has an approved goal without required QA.
				s, owner, planner, in = githubHandoffFixture(t)
				in.Kind, in.Tag, in.Workflow = "first-postmerge-qa", "", ""
				in.Environment, in.EnvironmentRevision = "test", "v1"
			case "unprivileged":
				planner = qa
			case "evidence-author":
				var report c.MessageReceipt
				report, err = s.Store.Send(t.Context(), planner, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, in.Goal), Body: "Developer evidence", RequestID: "developer-evidence"})
				in.EvidenceMessageID = report.MessageID
			case "evidence-thread":
				var thread c.Thread
				thread, err = s.Store.CreateThread(t.Context(), owner, c.CreateThread{Title: "Unrelated", Engagement: "coop", Members: []string{owner.ID, planner.ID}, RequestID: "unrelated"})
				if err == nil {
					var report c.MessageReceipt
					report, err = s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Body: "Unrelated evidence", RequestID: "unrelated-evidence"})
					in.EvidenceMessageID = report.MessageID
				}
			case "task-binding":
				in.AssignmentSHA = strings.Repeat("d", 64)
				want = c.ErrConflict
			case "pr-binding":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE kind='pull_request'", strings.Repeat("d", 64))
				want = c.ErrConflict
			case "pr-parent":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET body=json_set(body,'$.task_url',?) WHERE kind='pull_request'", "kata:other")
				want = c.ErrConflict
			case "foreign-pr":
				in.PRURL = "https://github.com/foreign/repo/pull/8"
			case "paused":
				a, e := s.ResolveGoal(t.Context(), owner, in.Goal)
				if e != nil {
					t.Fatal(e)
				}
				_, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "pause", RequestID: "pause"})
			case "quiescent":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE meta SET value='1' WHERE key='quiescent'")
				want = c.ErrEpoch
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateHandoff(t.Context(), planner, in); !errors.Is(err, want) {
				t.Fatalf("unsafe handoff: got %v, want %v", err, want)
			}
		})
	}
}

// A retained GitHub assignment remains valid when the private task provider
// supports only Kata, or when no private task provider is configured.
func TestHandoffRegisteredGitHubTaskWithoutProviderRead(t *testing.T) {
	for _, provider := range []string{"kata", "none"} {
		t.Run(provider, func(t *testing.T) {
			s, owner, _, in := githubHandoffFixture(t)
			if provider == "none" {
				s.Tasks = nil
			}
			handoff, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatalf("registered GitHub handoff rejected: %v", err)
			}
			page, err := s.Handoffs(t.Context(), owner, c.HandoffQuery{Goal: in.Goal})
			if err != nil || len(page.Handoffs) != 1 || page.Handoffs[0].ID != handoff.ID || handoff.Input != in {
				t.Fatalf("handoff not retained: %+v, %v", page, err)
			}
		})
	}
}

func githubHandoffFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.HandoffInput) {
	t.Helper()
	s, owner, author, task, _ := taskFixture(t)
	evidence, err := s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, task.Goal), Body: "Owner approved release", RequestID: "github-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.HandoffInput{Kind: "release", Goal: task.Goal, TaskURL: "https://github.com/example/coop/issues/7", PRURL: "https://github.com/example/coop/pull/8", AssignmentSHA: strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40), ArtifactSHA: strings.Repeat("c", 64), EvidenceMessageID: evidence.MessageID, Tag: "v0.2.7", Workflow: "release.yml", RequestID: "github-release"}
	for _, link := range []c.ArtifactLink{
		{Goal: in.Goal, Kind: "task", URL: in.TaskURL, AssignmentSHA: in.AssignmentSHA, RequestID: "github-task"},
		{Goal: in.Goal, Kind: "pull_request", URL: in.PRURL, TaskURL: in.TaskURL, AssignmentSHA: in.AssignmentSHA, RequestID: "github-pr"},
	} {
		if _, err = s.RegisterArtifact(t.Context(), author, link); err != nil {
			t.Fatal(err)
		}
	}
	return s, owner, author, in
}

func mustHandoffThread(t *testing.T, s *Service, owner c.Principal, goal c.GoalRef) string {
	t.Helper()
	a, err := s.ResolveGoal(t.Context(), owner, goal)
	if err != nil {
		t.Fatal(err)
	}
	return a.Revision.Proposal.ThreadID
}

func TestHandoffGitHubRegistryAndRepositoryGuards(t *testing.T) {
	for _, change := range []string{"unregistered-task", "unregistered-pr", "task-assignment", "pr-assignment", "pr-parent", "foreign-task", "foreign-pr", "noncanonical-task", "planner-release", "read-only-owner", "paused-goal", "quiescent", "evidence-author", "evidence-thread"} {
		t.Run(change, func(t *testing.T) {
			s, owner, author, in := githubHandoffFixture(t)
			want := c.ErrConflict
			var err error
			switch change {
			case "unregistered-task":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM artifacts WHERE kind='task'")
			case "unregistered-pr":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM artifacts WHERE kind='pull_request'")
			case "task-assignment":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE kind='task'", strings.Repeat("d", 64))
			case "pr-assignment":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE kind='pull_request'", strings.Repeat("d", 64))
			case "pr-parent":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET body=json_set(body,'$.task_url',?) WHERE kind='pull_request'", "https://github.com/example/coop/issues/9")
			case "foreign-task":
				in.TaskURL = "https://github.com/foreign/repo/issues/7"
				in.PRURL = "https://github.com/foreign/repo/pull/8"
				want = c.ErrForbidden
			case "foreign-pr":
				in.PRURL = "https://github.com/foreign/repo/pull/8"
				want = c.ErrForbidden
			case "noncanonical-task":
				in.TaskURL += "?query=1"
				want = c.ErrInvalid
			case "paused-goal":
				_, err = s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "pause", RequestID: "pause-handoff"})
				want = c.ErrForbidden
			case "quiescent":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE meta SET value='1' WHERE key='quiescent'")
				want = c.ErrEpoch
			case "evidence-author":
				var sent c.MessageReceipt
				sent, err = s.Store.Send(t.Context(), author, c.SendMessage{ThreadID: mustHandoffThread(t, s, owner, in.Goal), Body: "Author evidence", RequestID: "author-evidence"})
				in.EvidenceMessageID = sent.MessageID
				want = c.ErrForbidden
			case "evidence-thread":
				var thread c.Thread
				thread, err = s.Store.CreateThread(t.Context(), owner, c.CreateThread{Title: "Unrelated evidence", Engagement: "coop", Members: []string{owner.ID}, RequestID: "other-evidence-thread"})
				if err == nil {
					var sent c.MessageReceipt
					sent, err = s.Store.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Body: "Unrelated evidence", RequestID: "other-evidence"})
					in.EvidenceMessageID = sent.MessageID
				}
				want = c.ErrForbidden
			case "planner-release":
				owner = author
				want = c.ErrForbidden
			case "read-only-owner":
				owner.Capabilities = []string{"read"}
				err = s.Store.Configure(t.Context(), []c.Principal{owner}, nil)
				want = c.ErrForbidden
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CreateHandoff(t.Context(), owner, in); !errors.Is(err, want) {
				t.Fatalf("unsafe handoff: got %v, want %v", err, want)
			}
		})
	}
}
