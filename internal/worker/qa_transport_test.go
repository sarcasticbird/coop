package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQATransportNativeBodyAndEditTime(t *testing.T) {
	f := newTeamReadFixture()
	r, _ := qaResultFixture()
	node := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
	node["body"] = qaMarker(r)
	edited := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	node["lastEditedAt"] = edited.In(time.FixedZone("offset", 3600))
	client := f.client(t)
	runner := client.runner.(*fakeCommands)
	respond := runner.respond
	runner.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		var request struct{ Query string }
		_ = json.Unmarshal(body, &request)
		if strings.Contains(request.Query, "WorkerTeamReviews") && (!strings.Contains(request.Query, " body ") || !strings.Contains(request.Query, " lastEditedAt ")) {
			t.Fatal("native review query omitted body or lastEditedAt")
		}
		return respond(owner, endpoint, method, body)
	}
	pr, err := client.readPR(context.Background(), f.version["url"].(string))
	if err != nil || len(pr.Reviews) != 1 || pr.Reviews[0].Body != node["body"] || pr.Reviews[0].LastEditedAt != edited {
		t.Fatalf("native body/edit not retained: %+v %v", pr.Reviews, err)
	}
	node["lastEditedAt"] = nil
	pr, err = f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil || !pr.Reviews[0].LastEditedAt.IsZero() {
		t.Fatalf("native null edit time not zero: %+v %v", pr.Reviews, err)
	}
}

func TestQATransportSecondReadBodyAndEditConsistency(t *testing.T) {
	for _, fault := range []string{"body", "lastEditedAt", "malformed edit"} {
		t.Run(fault, func(t *testing.T) {
			f := newTeamReadFixture()
			r, _ := qaResultFixture()
			node := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
			node["body"], node["lastEditedAt"] = qaMarker(r), nil
			second := map[string]any{"fullDatabaseId": 22, "state": "COMMENTED", "body": "other", "lastEditedAt": nil, "commit": node["commit"], "submittedAt": node["submittedAt"], "author": node["author"]}
			f.pages["reviews"] = []map[string]any{teamReadPage([]any{second}, 2, "next"), teamReadPage([]any{node}, 2, "")}
			f.hook = func(key string, _ map[string]any) {
				if key == "commit-head" {
					switch fault {
					case "body":
						node["body"] = qaMarker(r) + "changed"
					case "lastEditedAt":
						node["lastEditedAt"] = time.Now().Add(-time.Minute)
					case "malformed edit":
						node["lastEditedAt"] = "not-a-time"
					}
				}
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			if err == nil || pr.ReviewsComplete || pr.ContributorsComplete || f.calls["reviews"] != 4 {
				t.Fatalf("mixed paginated evidence accepted: err=%v reviews=%d", err, f.calls["reviews"])
			}
		})
	}
}
