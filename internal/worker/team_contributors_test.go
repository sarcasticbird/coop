package worker

import (
	"context"
	"testing"
	"time"
)

func TestTeamContributorsCompletePages(t *testing.T) {
	f := newTeamReadFixture()
	firstReview := f.pages["reviews"][0]["nodes"].([]any)[0]
	secondReview := map[string]any{"fullDatabaseId": 22, "state": "DISMISSED", "commit": map[string]any{"oid": "old-head"}, "submittedAt": f.version["updatedAt"], "author": graphActor{ID: 11, Kind: "Bot", Login: "renamed[bot]"}}
	f.pages["reviews"] = []map[string]any{teamReadPage([]any{firstReview}, 2, "next"), teamReadPage([]any{secondReview}, 2, "")}
	f.pages["commits"] = []map[string]any{
		teamReadPage([]any{map[string]any{"commit": map[string]any{"id": "commit-old", "oid": "old-head"}}}, 2, "next"),
		teamReadPage([]any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": "head"}}}, 2, ""),
	}
	f.pages["commit-old"] = []map[string]any{teamReadPage([]any{map[string]any{"user": graphActor{ID: 14}}}, 1, "")}
	f.pages["commit-head"] = []map[string]any{
		teamReadPage([]any{map[string]any{"user": graphActor{ID: 14}}}, 3, "next"),
		teamReadPage([]any{map[string]any{"user": graphActor{ID: 11}}, map[string]any{"user": graphActor{ID: 98}}}, 3, ""),
	}
	f.accounts[11] = restActor{ID: 11, Login: "renamed-peer[bot]", Kind: "Bot"}
	f.accounts[98] = restActor{ID: 98, Login: "human", Kind: "User"}
	pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !pr.ReviewsComplete || !pr.ContributorsComplete || len(pr.Reviews) != 2 || pr.Reviews[1].State != "DISMISSED" || len(pr.Contributors) != 3 {
		t.Fatalf("incomplete evidence: %+v", pr)
	}
	if pr.Reviews[0].ID != "4294967297" {
		t.Fatalf("64-bit review identity lost: %+v", pr.Reviews)
	}
	if pr.Contributors[0].ID != 11 || pr.Contributors[0].Kind != "Bot" || pr.Contributors[1].ID != 14 || pr.Contributors[2].ID != 98 {
		t.Fatalf("immutable contributors not retained: %+v", pr.Contributors)
	}
	for key, count := range map[string]int{"reviews": 2, "commits": 2, "commit-head": 2, "commit-old": 1, "version": 1} {
		if f.calls[key] != count {
			t.Fatalf("%s reads=%d want %d", key, f.calls[key], count)
		}
	}
}

func TestTeamContributorsIncompleteEvidenceFailsClosed(t *testing.T) {
	for _, stage := range []string{"reviews", "commits", "commit-head"} {
		for _, fault := range []string{"missing_page_info", "missing_has_next", "missing_count", "truncated", "stalled", "null_node", "duplicate", "count_changed"} {
			t.Run(stage+"/"+fault, func(t *testing.T) {
				f := newTeamReadFixture()
				p := f.pages[stage][0]
				nodes := p["nodes"].([]any)
				switch fault {
				case "missing_page_info":
					delete(p, "pageInfo")
				case "missing_has_next":
					p["pageInfo"] = map[string]any{}
				case "missing_count":
					delete(p, "totalCount")
				case "truncated":
					p["totalCount"] = 2
				case "stalled":
					p["totalCount"] = 3
					p["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "same"}
					f.pages[stage] = []map[string]any{p, p}
				case "null_node":
					p["nodes"] = []any{nil}
				case "duplicate":
					p["nodes"], p["totalCount"] = []any{nodes[0], nodes[0]}, 2
				case "count_changed":
					p["totalCount"] = 2
					p["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "next"}
					f.pages[stage] = []map[string]any{p, teamReadPage(nodes, 3, "")}
				}
				pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
				if err == nil || pr.ContributorsComplete || pr.ReviewsComplete {
					t.Fatalf("partial evidence authorized: %+v %v", pr, err)
				}
			})
		}
	}
}

func TestTeamContributorsUnknownIdentityFailsClosed(t *testing.T) {
	for _, fault := range []string{"null_author", "zero_id", "unknown_account", "wrong_account", "unknown_kind", "missing_head", "review_author", "review_id", "review_time"} {
		t.Run(fault, func(t *testing.T) {
			f := newTeamReadFixture()
			switch fault {
			case "null_author":
				f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": nil}}
			case "zero_id":
				f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{Login: "fourth[bot]"}}}
			case "unknown_account":
				delete(f.accounts, 14)
			case "wrong_account":
				f.accounts[14] = restActor{ID: 99, Kind: "Bot", Login: "fourth[bot]"}
			case "unknown_kind":
				f.accounts[14] = restActor{ID: 14, Kind: "Organization", Login: "fourth"}
			case "missing_head":
				f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": "other-head"}}}
			case "review_author", "review_id", "review_time":
				r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
				key := map[string]string{"review_author": "author", "review_id": "fullDatabaseId", "review_time": "submittedAt"}[fault]
				delete(r, key)
			}
			if _, err := f.client(t).readPR(context.Background(), f.version["url"].(string)); err == nil {
				t.Fatalf("%s evidence accepted", fault)
			}
		})
	}
}

func TestTeamContributorsEmptyReviewsAndDistinctPRAuthor(t *testing.T) {
	f := newTeamReadFixture()
	f.pages["reviews"] = []map[string]any{teamReadPage([]any{}, 0, "")}
	f.pages["commit-head"] = []map[string]any{teamReadPage([]any{map[string]any{"user": graphActor{ID: 98}}}, 1, "")}
	f.accounts[98] = restActor{ID: 98, Kind: "User", Login: "commit-author"}
	pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil || !pr.ReviewsComplete || len(pr.Reviews) != 0 || !pr.ContributorsComplete || len(pr.Contributors) != 2 || pr.Contributors[0].ID != 14 || pr.Contributors[1].ID != 98 {
		t.Fatalf("PR author or empty review evidence lost: %+v %v", pr, err)
	}
}

func TestTeamContributorsMalformedReviewsFailClosed(t *testing.T) {
	for _, fault := range []string{"unknown_state", "lowercase_state", "missing_state", "null_state", "future_time", "malformed_time", "pending_submitted", "overflow_id", "fractional_id"} {
		t.Run(fault, func(t *testing.T) {
			f := newTeamReadFixture()
			r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
			switch fault {
			case "unknown_state":
				r["state"] = "FUTURE_REVIEW_STATE"
			case "lowercase_state":
				r["state"] = "approved"
			case "missing_state":
				delete(r, "state")
			case "null_state":
				r["state"] = nil
			case "future_time":
				r["submittedAt"] = time.Now().Add(time.Hour)
			case "malformed_time":
				r["submittedAt"] = "not-a-time"
			case "pending_submitted":
				r["state"] = "PENDING"
			case "overflow_id":
				r["fullDatabaseId"] = "9223372036854775808"
			case "fractional_id":
				r["fullDatabaseId"] = "1.5"
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			if err == nil || pr.ReviewsComplete || pr.ContributorsComplete {
				t.Fatalf("malformed review evidence accepted: %+v %v", pr, err)
			}
		})
	}
}
