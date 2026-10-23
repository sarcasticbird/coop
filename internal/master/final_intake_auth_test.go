package master

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"slices"
	"testing"
)

type finalReviewRevokingIntake struct {
	*adoptTasks
	revoke func()
}

func (b *finalReviewRevokingIntake) InspectIssue(context.Context, string) (taskstore.Issue, error) {
	b.revoke()
	return b.issue, nil
}
func TestFinalReviewIntakeReadRechecksMembershipAfterProviderIO(t *testing.T) {
	for _, change := range []string{"membership", "read", "plan", "engagement", "mapping", "registered"} {
		t.Run(change, func(t *testing.T) {
			s, owner, bot, _, n := mappedConversation(t)
			bot.Capabilities = append(bot.Capabilities, "plan")
			if err := s.Configure(t.Context(), []c.Principal{bot}, nil); err != nil {
				t.Fatal(err)
			}
			s.work = &intakeConversation{n}
			n.events = []taskstore.WorkEvent{{ID: 1, UID: "intake-created", OriginInstanceUID: conversationProject, ProjectUID: conversationProject, IssueUID: intakeIssue, Type: "issue.created", Actor: "human"}}
			if err := s.PollConversations(t.Context()); err != nil {
				t.Fatal(err)
			}
			var thread string
			if err := s.db.QueryRowContext(t.Context(), "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", intakeIssue).Scan(&thread); err != nil {
				t.Fatal(err)
			}
			th, err := s.Thread(t.Context(), owner, thread)
			if err != nil {
				t.Fatal(err)
			}
			b := &finalReviewRevokingIntake{adoptTasks: &adoptTasks{testTasks: &testTasks{}, issue: taskstore.Issue{UID: intakeIssue, ProjectUID: conversationProject, Body: "private intake body", Title: "private intake", Status: "open", Revision: 1}}}
			b.revoke = func() {
				if change == "registered" {
					goalThread, e := s.CreateThread(t.Context(), owner, c.CreateThread{Title: "Concurrent admission", Engagement: "coop", Members: []string{owner.ID, bot.ID, "agent-two"}, RequestID: "concurrent-goal-thread"})
					if e != nil {
						t.Fatal(e)
					}
					revision, e := (&Service{Store: s}).ProposeGoal(t.Context(), bot, c.GoalProposal{ThreadID: goalThread.ID, Purpose: "bounded work", Scope: "current admission", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"tests pass"}, AuthorID: bot.ID, PeerID: "agent-two", RequestID: "concurrent-goal"})
					if e != nil {
						t.Fatal(e)
					}
					ref, e := c.KataTaskReference(conversationProject, intakeIssue, "example/coop")
					if e != nil {
						t.Fatal(e)
					}
					if _, e = s.db.ExecContext(t.Context(), "INSERT INTO artifacts(goal_id,revision,url,kind,assignment_sha,body) VALUES(?,?,?,'task','registered',?)", revision.Ref.GoalID, revision.Ref.RevisionSHA256, ref, []byte(`{}`)); e != nil {
						t.Fatal(e)
					}
					return
				}
				if change == "mapping" {
					if _, err := s.db.ExecContext(t.Context(), "DELETE FROM conversation_sources WHERE issue_uid=?", intakeIssue); err != nil {
						t.Fatal(err)
					}
					return
				}
				if change != "membership" {
					if change == "engagement" {
						bot.Engagements = []string{"elsewhere"}
					} else {
						bot.Capabilities = slices.DeleteFunc(bot.Capabilities, func(cap string) bool { return cap == change })
					}
					if err := s.Configure(t.Context(), []c.Principal{bot}, nil); err != nil {
						t.Fatal(err)
					}
					return
				}
				var members []string
				for _, id := range th.Members {
					if id != bot.ID {
						members = append(members, id)
					}
				}
				if _, err := s.SetMembers(t.Context(), owner, c.SetMembers{ThreadID: thread, ExpectedMembers: th.Members, Members: members, RequestID: "revoke-during-read"}); err != nil {
					t.Fatal(err)
				}
			}
			svc := &Service{Store: s, Tasks: b}
			ref, _ := c.KataTaskReference(conversationProject, intakeIssue, "example/coop")
			task, err := svc.TaskRead(t.Context(), bot, c.TaskReadInput{Reference: ref})
			if err == nil {
				t.Fatalf("revoked planner received private native issue after provider IO: title=%q body=%q", task.Title, task.Body)
			}
		})
	}
}
