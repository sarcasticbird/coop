package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTeamTaskReadRejectsAssigneeChurn(t *testing.T) {
	c, task, _, _, now := teamScopeFixture(t)
	issue := restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: now}
	raw, _ := json.Marshal(issue)
	var initial map[string]any
	_ = json.Unmarshal(raw, &initial)
	initial["assignees"] = []any{}
	raw, _ = json.Marshal(initial)
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatal(err)
	}
	comments := 0
	f := &fakeCommands{respond: func(_, endpoint, method string, _ []byte) (CommandResult, error) {
		if method != "GET" {
			t.Fatal("read mutated GitHub")
		}
		if strings.Contains(endpoint, "/comments?") {
			comments++
			return jsonResponse([]any{}), nil
		}
		fresh := map[string]any{}
		for k, v := range initial {
			fresh[k] = v
		}
		if comments > 0 {
			fresh["assignees"] = []any{map[string]any{"id": 99, "login": "human", "type": "User"}}
		}
		return jsonResponse(fresh), nil
	}}
	g := NewGitHubClient(f)
	g.config = c
	if _, err := g.readTask(context.Background(), task.Repo, issue); err == nil || !strings.Contains(err.Error(), "task_version_changed") {
		t.Fatalf("assignee churn admitted: %v", err)
	}
}

func TestTeamTaskReadAssigneesCompleteness(t *testing.T) {
	for _, name := range []string{"missing", "null", "empty", "assigned"} {
		t.Run(name, func(t *testing.T) {
			c, task, _, _, now := teamScopeFixture(t)
			body := map[string]any{"html_url": task.URL, "number": 1, "title": task.Title, "body": task.Body, "state": "open", "updated_at": now}
			switch name {
			case "null":
				body["assignees"] = nil
			case "empty":
				body["assignees"] = []any{}
			case "assigned":
				body["assignees"] = []any{map[string]any{"id": 99, "login": "human", "type": "User"}}
			}
			raw, _ := json.Marshal(body)
			var issue restIssue
			if err := json.Unmarshal(raw, &issue); err != nil {
				t.Fatal(err)
			}
			f := &fakeCommands{respond: func(_, endpoint, method string, _ []byte) (CommandResult, error) {
				if method != "GET" {
					t.Fatal("read mutated API")
				}
				if strings.Contains(endpoint, "/comments?") {
					return jsonResponse([]any{}), nil
				}
				return jsonResponse(body), nil
			}}
			g := NewGitHubClient(f)
			g.config = c
			got, err := g.readTask(context.Background(), task.Repo, issue)
			if err != nil {
				t.Fatal(err)
			}
			if got.AssigneesComplete != (name == "empty" || name == "assigned") || len(got.Assignees) != map[bool]int{true: 1, false: 0}[name == "assigned"] {
				t.Fatalf("wrong assignee completeness: %+v", got)
			}
		})
	}
}
