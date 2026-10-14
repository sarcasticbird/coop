package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDiscussionApprovalsUseClearOwnerLanguage(t *testing.T) {
	for _, text := range []string{"approved", "I approve this goal.", "this looks good to me \r\n", "@builder & @reviewer - i approve continuation of this work, including the merge of PRs for repo"} {
		c, s, n := policyFixture(t)
		g := s.Goals[0]
		g.Decisions[0].Command = text
		if a := EvaluateGoal(c, g, n); a.State != "approved" {
			t.Errorf("clear approval rejected %q: %+v", text, a)
		}
		g.Decisions[0].Author = c.Reviewer
		if EvaluateGoal(c, g, n).State == "approved" {
			t.Errorf("bot approval accepted %q", text)
		}
	}
	for _, text := range []string{"not approved", "approved if tests pass", "I approve this goal?", "> approved", "```\napproved\n```", "the owner said approved", "this looks good to me but do not merge", "@outsider approved", "i approve continuation of this work, but wait for my UAT", "i approve continuation of this work, including the merge of PRs for to but wait for UAT", "Please /coop approve"} {
		c, s, n := policyFixture(t)
		g := s.Goals[0]
		g.Decisions[0].Command = text
		if EvaluateGoal(c, g, n).State == "approved" {
			t.Errorf("ambiguous approval accepted %q", text)
		}
	}
}

func TestNaturalApprovalRepositoryQualifierCannotWidenAuthority(t *testing.T) {
	for _, text := range []string{"I approve continuation of this work, including the merge of PRs for foreign/other", "I approve continuation of this work, including the merge of PRs for other"} {
		c, s, n := policyFixture(t)
		s.Goals[0].Decisions[0].Command = text
		if EvaluateGoal(c, s.Goals[0], n).State == "approved" {
			t.Fatalf("foreign qualifier granted authority: %s", text)
		}
	}
	c, s, n := policyFixture(t)
	s.Goals[0].Repositories = append(s.Goals[0].Repositories, "code/other")
	s.Goals[0].Decisions[0].Command = "I approve continuation of this work, including the merge of PRs for repo"
	if EvaluateGoal(c, s.Goals[0], n).State == "approved" {
		t.Fatal("single-repo qualifier approved multiple repositories")
	}
}

func TestOwnerApprovalRecordBindsBodyAndRepositories(t *testing.T) {
	c, s, n := policyFixture(t)
	g := s.Goals[0]
	body, err := GoalApprovalComment(c, g, g.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	g.Decisions[0].Command = body
	if EvaluateGoal(c, g, n).State != "approved" {
		t.Fatal("bound owner approval rejected")
	}
	g.Body = "changed"
	g.BodySHA = BodyHash(g.Body)
	if EvaluateGoal(c, g, n).State == "approved" {
		t.Fatal("approval survived changed body")
	}
	g = s.Goals[0]
	g.Decisions[0].Command = body
	g.Repositories = []string{"code/other"}
	if EvaluateGoal(c, g, n).State == "approved" {
		t.Fatal("approval survived changed repositories")
	}
	g = s.Goals[0]
	g.Decisions[0].Command = body + "\nnot yet"
	if EvaluateGoal(c, g, n).State == "approved" {
		t.Fatal("extra instruction accepted")
	}
	if _, err = GoalApprovalComment(c, s.Goals[0], []string{"foreign/repo"}); err == nil {
		t.Fatal("out-of-scope host approval")
	}
}

func TestOwnerRecordSuppliesMissingGoalScopeWithoutBodyEdit(t *testing.T) {
	c, s, n := policyFixture(t)
	goal := s.Goals[0]
	body, err := GoalApprovalComment(c, goal, goal.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitHubClient(nil)
	client.config = c
	d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt, LastEditedAt: goal.LastEditedAt}
	comment := Comment{ID: "owner", Body: body, Author: c.Owner, CreatedAt: n.Add(-time.Minute)}
	g, _ := client.goalFromDiscussion(d, []Comment{comment})
	if !sameRepositories(g.Repositories, goal.Repositories) || EvaluateGoal(c, g, n).State != "approved" {
		t.Fatalf("scope not recovered: %+v", g)
	}
	comment.Author = c.Reviewer
	g, _ = client.goalFromDiscussion(d, []Comment{comment})
	if len(g.Repositories) != 0 || EvaluateGoal(c, g, n).State == "approved" {
		t.Fatal("reviewer supplied owner authority")
	}
}

func TestDeliveryAllowsConfirmedAbsentChecksOnlyWithCleanReviewedHead(t *testing.T) {
	for _, check := range []string{"none", "success", "unknown", "pending", "failure", "error"} {
		for _, merge := range []string{"CLEAN", "BLOCKED", "UNKNOWN"} {
			c, s, n := policyFixture(t)
			s.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, ChecksState: check, MergeState: merge}}
			s.PRs[0].Reviews = []Review{{ID: "review", HeadSHA: s.PRs[0].HeadSHA, Author: c.Reviewer, State: "APPROVED", SubmittedAt: n}}
			got := Select(c, s, State{}, n)
			want := merge == "CLEAN" && (check == "none" || check == "success")
			if (len(got) == 1 && got[0].Kind == "deliver") != want {
				t.Fatalf("checks=%s merge=%s jobs=%+v", check, merge, got)
			}
			s.PRs[0].Reviews[0].HeadSHA = strings.Repeat("b", 40)
			if len(Select(c, s, State{}, n)) != 0 {
				t.Fatal("stale review allowed")
			}
		}
	}
}

func TestGitHubDistinguishesAbsentAndUnknownChecks(t *testing.T) {
	for _, state := range []string{"none", "success", "pending", "unknown"} {
		c, s, _ := policyFixture(t)
		item := map[string]any{"url": "https://github.com/code/repo/pull/5", "body": encodeMetadata("pr", prMetadata{Task: s.Tasks[0].URL, Goal: s.Goals[0].URL}), "state": "OPEN", "headRefOid": strings.Repeat("a", 40), "mergeStateStatus": "CLEAN", "author": map[string]any{"__typename": "Bot", "databaseId": c.Implementer.ID, "login": c.Implementer.Login}}
		if state != "unknown" {
			commit := map[string]any{"statusCheckRollup": nil}
			if state != "none" {
				commit["statusCheckRollup"] = map[string]any{"state": strings.ToUpper(state)}
			}
			item["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": commit}}}
		}
		f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
			if e == "graphql" {
				return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": item}}}), nil
			}
			return response("200 OK", "[]"), nil
		}}
		g := NewGitHubClient(f)
		g.config = c
		p, err := g.readPR(context.Background(), item["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if p.ChecksState != state {
			raw, _ := json.Marshal(p)
			t.Fatalf("%s: %s", state, raw)
		}
	}
}
