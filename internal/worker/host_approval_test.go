package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type hostApprovalCommands struct {
	fn func([]string, []byte) (CommandResult, error)
}

func (f hostApprovalCommands) Run(_ context.Context, args, env []string, in io.Reader) (CommandResult, error) {
	if len(env) != 0 {
		return CommandResult{}, errors.New("unexpected environment")
	}
	var body []byte
	if in != nil {
		body, _ = io.ReadAll(in)
	}
	return f.fn(args, body)
}

func TestHostApprovalVerifiesOwnerAndBindsCurrentScope(t *testing.T) {
	for _, scenario := range []string{"success", "wrong owner", "bot owner", "changed body", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			c, s, n := policyFixture(t)
			goal := s.Goals[0]
			goal.URL = "https://github.com/planning/coordination/discussions/1"
			c.DiscussionRepositories = []string{"planning/coordination"}
			approved, _ := GoalApprovalComment(c, goal, goal.Repositories)
			reads, writes := 0, 0
			fake := hostApprovalCommands{fn: func(args []string, body []byte) (CommandResult, error) {
				if len(args) < 8 || args[0] != "gh" || args[1] != "api" || !strings.Contains(strings.Join(args, " "), "--hostname github.com") {
					t.Fatalf("not host GitHub: %v", args)
				}
				switch args[2] {
				case "user":
					actor := map[string]any{"id": c.Owner.ID, "login": c.Owner.Login, "type": "User"}
					if scenario == "wrong owner" {
						actor["id"] = int64(99)
					}
					if scenario == "bot owner" {
						actor["type"] = "Bot"
					}
					return jsonResponse(actor), nil
				case "graphql":
					var req struct {
						Query     string
						Variables map[string]any
					}
					if json.Unmarshal(body, &req) != nil {
						t.Fatal("bad JSON")
					}
					if strings.Contains(req.Query, "HostApprovalGoal") {
						reads++
						text := goal.Body
						if scenario == "changed body" && reads > 1 {
							text = "new scope"
						}
						return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"discussion": map[string]any{"id": "D_goal", "url": goal.URL, "body": text, "createdAt": goal.CreatedAt, "lastEditedAt": goal.LastEditedAt}}}}), nil
					}
					if strings.Contains(req.Query, "WorkerDiscussionComments") {
						nodes := []any{}
						if scenario == "duplicate" {
							nodes = append(nodes, map[string]any{"id": "existing", "url": goal.URL + "#discussioncomment-1", "body": approved, "createdAt": n.Add(-time.Minute), "author": map[string]any{"__typename": "User", "databaseId": c.Owner.ID, "login": c.Owner.Login}})
						}
						return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"comments": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
					}
					if strings.Contains(req.Query, "WorkerDiscussionReplies") {
						return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"replies": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
					}
					if strings.Contains(req.Query, "HostApproveGoal") {
						writes++
						input := req.Variables["input"].(map[string]any)
						if input["body"] != approved || input["discussionId"] != "D_goal" {
							t.Fatal("incorrect bound approval")
						}
						return jsonResponse(map[string]any{"data": map[string]any{"addDiscussionComment": map[string]any{"comment": map[string]any{"id": "posted", "url": goal.URL + "#discussioncomment-2", "body": approved, "createdAt": n, "author": map[string]any{"__typename": "User", "databaseId": c.Owner.ID, "login": c.Owner.Login}}}}}), nil
					}
				}
				return CommandResult{}, errors.New("unexpected request")
			}}
			result, err := ApproveGoal(context.Background(), c, goal.URL, goal.Repositories, fake)
			good := scenario == "success" || scenario == "duplicate"
			if (err == nil) != good {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			want := 0
			if scenario == "success" {
				want = 1
			}
			if writes != want {
				t.Fatalf("writes %d want %d", writes, want)
			}
		})
	}
}
