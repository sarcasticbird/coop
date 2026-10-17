package worker

import (
	"context"
	"strings"
	"testing"
)

func TestInitialAssignmentNativeHistoryIncludesClosedPRs(t *testing.T) {
	for _, state := range []string{"closed", "merged", "open", "none", "changed", "malformed", "malformed_other_issue", "missing_body"} {
		t.Run(state, func(t *testing.T) {
			c, task, goal, _, now := teamScopeFixture(t)
			task.AssigneesComplete = true
			reads := 0
			body := teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: BodyHash("previous assignment")})
			f := &fakeCommands{respond: func(owner, endpoint, method string, _ []byte) (CommandResult, error) {
				if owner != "code" || method != "GET" || !strings.HasPrefix(endpoint, "repos/code/repo/pulls?state=all&") {
					t.Fatalf("history not read natively/all states: %s %s %s", owner, method, endpoint)
				}
				reads++
				if state == "none" {
					return jsonResponse([]any{}), nil
				}
				row := map[string]any{"html_url": "https://github.com/code/repo/pull/5", "number": 5, "body": body, "state": "closed", "updated_at": now}
				if state == "open" {
					row["state"] = "open"
				}
				if state == "merged" {
					row["merged_at"] = now
				}
				if state == "changed" && reads == 2 {
					row["body"] = body + " changed"
				}
				if state == "malformed" {
					row["body"] = "<!-- coop-pr:v2 broken " + task.URL + " -->"
				}
				if state == "malformed_other_issue" {
					row["body"] = "<!-- coop-pr:v2 broken " + task.URL + "0 -->"
				}
				if state == "missing_body" {
					delete(row, "body")
				}
				return jsonResponse([]any{row}), nil
			}}
			g := NewGitHubClient(f)
			g.config = c
			before := Snapshot{Complete: true, Tasks: []Task{task}, Goals: []Goal{goal}}
			s, err := g.completeInitialPRHistory(context.Background(), before, task.URL)
			if state == "changed" || state == "malformed" || state == "missing_body" {
				if err == nil {
					t.Fatal("unstable/ambiguous PR history accepted")
				}
				return
			}
			if err != nil || reads != 2 {
				t.Fatalf("history incomplete: reads=%d error=%v", reads, err)
			}
			text, err := TaskMetadataForAuthor(c, s, task.URL, goal.URL, "engineer", nil, now)
			if state == "none" || state == "malformed_other_issue" {
				if err != nil || !strings.Contains(text, "coop-task:v2") {
					t.Fatal("empty verified history blocked", err)
				}
			} else if err == nil || text != "" {
				t.Fatal("existing native task PR bypassed initial-assignment conflict")
			}
			if before.Tasks[0].PRHistoryComplete || len(before.PRs) != 0 {
				t.Fatal("history completion mutated caller snapshot")
			}
		})
	}
}
