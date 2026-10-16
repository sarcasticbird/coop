package worker

import (
	"context"
	"testing"
	"time"
)

func contributorEngineeringFixture(t *testing.T) (*teamReadFixture, Task, Actor) {
	t.Helper()
	c, s, _ := engineeringFixture(t, false)
	task, source := s.Tasks[0], s.PRs[0]
	f := newTeamReadFixture()
	f.c = c
	f.version["author"] = graphActor{ID: source.Author.ID, Kind: source.Author.Kind, Login: source.Author.Login}
	f.version["headRefOid"] = source.HeadSHA
	f.version["createdAt"] = time.Now().Add(-time.Hour)
	f.version["body"] = teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: task.GoalURL, AssignmentSHA: source.AssignmentSHA})
	r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
	peer := source.Reviews[0].Author
	r["author"] = graphActor{ID: peer.ID, Kind: peer.Kind, Login: peer.Login}
	r["commit"], r["submittedAt"] = map[string]any{"oid": source.HeadSHA}, time.Now().Add(-time.Minute)
	f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": source.HeadSHA, "committer": map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User"}}}}}
	f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User"}}}
	f.accounts[source.Author.ID] = restActor(source.Author)
	return f, task, peer
}

func TestTeamContributorsReviewsChangedDuringRead(t *testing.T) {
	for _, change := range []string{"dismissed", "changes_requested", "removed", "id", "author", "head", "time", "incomplete_reread"} {
		t.Run(change, func(t *testing.T) {
			f, task, peer := contributorEngineeringFixture(t)
			// The mutation occurs on the terminal co-author page, after reviews
			// have already been read. PR version fields deliberately stay fixed.
			f.pages["commit-head"] = []map[string]any{
				teamReadPage([]any{map[string]any{"user": graphActor{ID: 14}}}, 2, "next"),
				teamReadPage([]any{map[string]any{"user": graphActor{ID: 98}}}, 2, ""),
			}
			f.accounts[98] = restActor{ID: 98, Kind: "User", Login: "coauthor"}
			f.hook = func(key string, vars map[string]any) {
				if key != "commit-head" || vars["after"] == nil {
					return
				}
				r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
				switch change {
				case "dismissed":
					r["state"] = "DISMISSED"
				case "changes_requested":
					added := map[string]any{"fullDatabaseId": 22, "state": "CHANGES_REQUESTED", "commit": r["commit"], "submittedAt": r["submittedAt"], "author": graphActor{ID: peer.ID, Kind: peer.Kind, Login: peer.Login}}
					f.pages["reviews"] = []map[string]any{teamReadPage([]any{r}, 2, "next"), teamReadPage([]any{added}, 2, "")}
				case "removed":
					f.pages["reviews"] = []map[string]any{teamReadPage([]any{}, 0, "")}
				case "id":
					r["fullDatabaseId"] = 22
				case "author":
					r["author"] = graphActor{ID: 98, Kind: "User", Login: "other"}
				case "head":
					r["commit"] = map[string]any{"oid": "other-head"}
				case "time":
					r["submittedAt"] = time.Now().Add(-30 * time.Second)
				case "incomplete_reread":
					delete(f.pages["reviews"][0], "pageInfo")
				}
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			_, cleared := EngineeringReviewFor(f.c, task, pr)
			if err == nil || pr.ReviewsComplete || pr.ContributorsComplete || cleared {
				t.Fatalf("changed review evidence accepted: err=%v complete=%t/%t cleared=%t", err, pr.ReviewsComplete, pr.ContributorsComplete, cleared)
			}
			if change == "changes_requested" && f.calls["reviews"] != 3 {
				t.Fatalf("review reread did not traverse all pages: %d", f.calls["reviews"])
			}
		})
	}
}

func TestTeamContributorsCanonicalReviewRecords(t *testing.T) {
	f := newTeamReadFixture()
	first := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
	second := map[string]any{"fullDatabaseId": 22, "state": "COMMENTED", "commit": map[string]any{"oid": "head"}, "submittedAt": f.version["updatedAt"], "author": graphActor{ID: 98, Kind: "User", Login: "human"}}
	f.pages["reviews"] = []map[string]any{teamReadPage([]any{first}, 2, "next"), teamReadPage([]any{second}, 2, "")}
	f.hook = func(key string, _ map[string]any) {
		if key == "commit-head" {
			// Page order and timestamp encoding are not evidence changes.
			first["submittedAt"] = f.version["updatedAt"].(time.Time).In(time.FixedZone("offset", 3600)).Format(time.RFC3339)
			f.pages["reviews"] = []map[string]any{teamReadPage([]any{second}, 2, "next"), teamReadPage([]any{first}, 2, "")}
		}
	}
	pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil || !pr.ReviewsComplete || len(pr.Reviews) != 2 || f.calls["reviews"] != 4 {
		t.Fatalf("equivalent review records rejected: %+v %v reads=%d", pr.Reviews, err, f.calls["reviews"])
	}
}

func TestTeamContributorsCommitterIndependence(t *testing.T) {
	for _, identity := range []string{"independent", "peer", "missing", "null_user", "zero_id", "unknown_account", "wrong_account", "unknown_kind"} {
		t.Run(identity, func(t *testing.T) {
			f, task, peer := contributorEngineeringFixture(t)
			commit := f.pages["commits"][0]["nodes"].([]any)[0].(map[string]any)["commit"].(map[string]any)
			id := int64(98)
			if identity == "peer" {
				id = peer.ID
			}
			// GitActor.user is User-shaped even when REST identifies a Bot.
			commit["committer"] = map[string]any{"user": graphActor{ID: id, Kind: "User", Login: "renamed-committer"}}
			f.accounts[id] = restActor{ID: id, Kind: "Bot", Login: "resolved-committer[bot]"}
			switch identity {
			case "missing":
				delete(commit, "committer")
			case "null_user":
				commit["committer"] = map[string]any{"user": nil}
			case "zero_id":
				commit["committer"] = map[string]any{"user": graphActor{Kind: "User"}}
			case "unknown_account":
				delete(f.accounts, id)
			case "wrong_account":
				f.accounts[id] = restActor{ID: 99, Kind: "Bot", Login: "wrong[bot]"}
			case "unknown_kind":
				f.accounts[id] = restActor{ID: id, Kind: "Organization", Login: "wrong"}
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			_, cleared := EngineeringReviewFor(f.c, task, pr)
			if identity == "independent" || identity == "peer" {
				if err != nil || cleared != (identity == "independent") {
					t.Fatalf("committer independence: err=%v cleared=%t contributors=%+v", err, cleared, pr.Contributors)
				}
				found := false
				for _, actor := range pr.Contributors {
					found = found || actor.ID == id && actor.Kind == "Bot" && actor.Login == "resolved-committer[bot]"
				}
				if !found {
					t.Fatalf("REST committer identity missing: %+v", pr.Contributors)
				}
			} else if err == nil || cleared || pr.ContributorsComplete || pr.ReviewsComplete {
				t.Fatalf("unresolved committer accepted: err=%v cleared=%t", err, cleared)
			}
		})
	}
}

func TestTeamContributorsCompletePages(t *testing.T) {
	f := newTeamReadFixture()
	firstReview := f.pages["reviews"][0]["nodes"].([]any)[0]
	secondReview := map[string]any{"fullDatabaseId": 22, "state": "DISMISSED", "commit": map[string]any{"oid": "old-head"}, "submittedAt": f.version["updatedAt"], "author": graphActor{ID: 11, Kind: "Bot", Login: "renamed[bot]"}}
	f.pages["reviews"] = []map[string]any{teamReadPage([]any{firstReview}, 2, "next"), teamReadPage([]any{secondReview}, 2, "")}
	f.pages["commits"] = []map[string]any{
		teamReadPage([]any{map[string]any{"commit": map[string]any{"id": "commit-old", "oid": "old-head", "committer": map[string]any{"user": graphActor{ID: 14}}}}}, 2, "next"),
		teamReadPage([]any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": "head", "committer": map[string]any{"user": graphActor{ID: 14}}}}}, 2, ""),
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
	if !pr.ReviewsComplete || !pr.ContributorsComplete || len(pr.Reviews) != 2 || pr.Reviews[0].State != "DISMISSED" || len(pr.Contributors) != 3 {
		t.Fatalf("incomplete evidence: %+v", pr)
	}
	if pr.Reviews[1].ID != "4294967297" {
		t.Fatalf("64-bit review identity lost: %+v", pr.Reviews)
	}
	if pr.Contributors[0].ID != 11 || pr.Contributors[0].Kind != "Bot" || pr.Contributors[1].ID != 14 || pr.Contributors[2].ID != 98 {
		t.Fatalf("immutable contributors not retained: %+v", pr.Contributors)
	}
	for key, count := range map[string]int{"reviews": 4, "commits": 2, "commit-head": 2, "commit-old": 1, "version": 1} {
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
				f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": "other-head", "committer": map[string]any{"user": graphActor{ID: 14}}}}}
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
