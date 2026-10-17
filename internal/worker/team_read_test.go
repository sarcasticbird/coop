package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type teamReadFixture struct {
	c        Config
	version  map[string]any
	pages    map[string][]map[string]any
	accounts map[int64]restActor
	hook     func(string, map[string]any)
	calls    map[string]int
}

func teamReadPage(nodes []any, total int, next string) map[string]any {
	return map[string]any{"nodes": nodes, "totalCount": total, "pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next}}
}

func newTeamReadFixture() *teamReadFixture {
	p := teamPolicyFixture()
	p.Members = append(p.Members, TeamMember{Name: "fourth", Actor: Actor{ID: 14, Login: "fourth[bot]", Kind: "Bot"}, Capabilities: []string{"implement"}})
	p.EngineeringPeers["fourth"] = "engineer"
	author := p.Members[3].Actor
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	body := teamMarker("pr", TeamPRMetadata{Task: "https://github.com/code/repo/issues/4", Goal: "https://github.com/planning/board/discussions/1", AssignmentSHA: BodyHash("assignment")})
	f := &teamReadFixture{
		c:       Config{Version: 2, Team: &p, Repositories: []string{"code/repo"}, CodeOwners: []string{"code"}},
		version: map[string]any{"baseRefName": "feature/integration", "url": "https://github.com/code/repo/pull/5", "body": body, "headRefOid": "head", "createdAt": now.Add(-time.Hour), "updatedAt": now, "state": "OPEN", "author": graphActor{ID: author.ID, Kind: author.Kind, Login: author.Login}},
		pages: map[string][]map[string]any{
			"timelineItems": {teamReadPage([]any{}, 0, "")},
			"reviews":       {teamReadPage([]any{map[string]any{"fullDatabaseId": "4294967297", "state": "APPROVED", "commit": map[string]any{"oid": "head"}, "submittedAt": now, "author": graphActor{ID: 11, Kind: "Bot", Login: "renamed[bot]"}}}, 1, "")},
			"commits":       {teamReadPage([]any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": "head", "committer": map[string]any{"user": graphActor{ID: author.ID, Kind: "User"}}}}}, 1, "")},
			"commit-head":   {teamReadPage([]any{map[string]any{"user": graphActor{ID: author.ID, Kind: "User", Login: author.Login}}}, 1, "")},
		},
		accounts: map[int64]restActor{author.ID: restActor(author)}, calls: map[string]int{},
	}
	return f
}

func (f *teamReadFixture) client(t *testing.T) *GitHubClient {
	t.Helper()
	r := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if method != "GET" && endpoint != "graphql" {
			t.Fatal("unexpected mutation")
		}
		if strings.HasPrefix(endpoint, "user/") {
			var id int64
			_, _ = fmt.Sscanf(endpoint, "user/%d", &id)
			return jsonResponse(f.accounts[id]), nil
		}
		if strings.Contains(endpoint, "/pulls?") {
			return jsonResponse([]any{map[string]any{"html_url": f.version["url"]}}), nil
		}
		if strings.Contains(endpoint, "/issues?") {
			return jsonResponse([]any{}), nil
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		key := ""
		switch {
		case strings.Contains(req.Query, "WorkerTeamReviews"):
			key = "reviews"
		case strings.Contains(req.Query, "WorkerTeamCommits"):
			key = "commits"
		case strings.Contains(req.Query, "WorkerTeamAuthors"):
			key, _ = req.Variables["id"].(string)
		case strings.Contains(req.Query, "WorkerTeamBaseScope"):
			key = "timelineItems"
		case strings.Contains(req.Query, "WorkerScopeHistory"):
			key = "userContentEdits"
		case strings.Contains(req.Query, "WorkerPRVersion"):
			key = "version"
		case strings.Contains(req.Query, "WorkerPR("):
			key = "pr"
		default:
			t.Fatalf("unexpected endpoint/query: %s %s", endpoint, req.Query)
		}
		f.calls[key]++
		if f.hook != nil {
			f.hook(key, req.Variables)
		}
		pr := f.version
		var data any = map[string]any{"repository": map[string]any{"pullRequest": pr}}
		if pages, ok := f.pages[key]; ok {
			page := 0
			if req.Variables["after"] != nil {
				page = 1
			}
			if page >= len(pages) {
				t.Fatalf("unexpected extra page for %s", key)
			}
			if strings.HasPrefix(key, "commit-") {
				data = map[string]any{"node": map[string]any{"authors": pages[page]}}
			} else {
				data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{key: pages[page]}}}
			}
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	g := NewGitHubClient(r)
	g.config = f.c
	return g
}

func TestTeamReadRosterDiscovery(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			f := newTeamReadFixture()
			if !linked {
				f.version["body"] = "Manual unlinked PR"
			}
			s, err := f.client(t).readSnapshot(context.Background(), Snapshot{})
			if err != nil || len(s.PRs) != 1 {
				t.Fatalf("fourth implementer discovery: %+v %v", s.PRs, err)
			}
			pr := s.PRs[0]
			if linked && (pr.AssignmentSHA != BodyHash("assignment") || !pr.ReviewsComplete || !pr.ContributorsComplete) {
				t.Fatalf("missing v2 evidence: %+v", pr)
			}
		})
	}
	f := newTeamReadFixture()
	for _, a := range []Actor{f.c.Team.Members[2].Actor, {ID: 99, Login: "fourth[bot]", Kind: "Bot"}, {ID: 14, Login: "fourth[bot]", Kind: "User"}} {
		if configuredImplementer(f.c, a) {
			t.Fatalf("nonimplementer discovered: %+v", a)
		}
	}
	if !configuredImplementer(Config{Version: 1, Implementer: Actor{ID: 14, Kind: "Bot"}}, Actor{ID: 14, Kind: "Bot"}) {
		t.Fatal("legacy identity behavior changed")
	}
}

func TestTeamReadMetadataFailsClosed(t *testing.T) {
	for _, scenario := range []string{"v1", "duplicate", "invalid_digest", "foreign_task", "unknown_field"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTeamReadFixture()
			body := f.version["body"].(string)
			switch scenario {
			case "v1":
				body = encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/4", Goal: "https://github.com/planning/board/discussions/1"})
			case "duplicate":
				body += body
			case "invalid_digest":
				body = strings.ReplaceAll(body, BodyHash("assignment"), "bad")
			case "foreign_task":
				body = strings.ReplaceAll(body, "code/repo/issues", "other/repo/issues")
			case "unknown_field":
				body = strings.Replace(body, `{"task":`, `{"extra":true,"task":`, 1)
			}
			f.version["body"] = body
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			if err != nil || pr.TaskURL != "" || pr.AssignmentSHA != "" || pr.ReviewsComplete || pr.ContributorsComplete {
				t.Fatalf("malformed metadata admitted: %+v %v", pr, err)
			}
		})
	}
}

func TestTeamReadHeadChangesDuringEvidence(t *testing.T) {
	f := newTeamReadFixture()
	f.hook = func(key string, _ map[string]any) {
		if key == "version" {
			f.version["headRefOid"] = "new-head"
		}
	}
	if _, err := f.client(t).readPR(context.Background(), f.version["url"].(string)); err == nil {
		t.Fatal("mixed-head evidence accepted")
	}
}

func TestTeamReadAssignmentScopeContinuity(t *testing.T) {
	for _, scenario := range []string{"prose", "assignment_restored", "v1_interlude"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTeamReadFixture()
			created := f.version["createdAt"].(time.Time)
			oldest, middle, latest := created.Add(time.Minute), created.Add(2*time.Minute), created.Add(3*time.Minute)
			body := f.version["body"].(string)
			oldBody := body
			if scenario == "assignment_restored" {
				oldBody = strings.ReplaceAll(body, BodyHash("assignment"), BodyHash("other"))
			}
			if scenario == "v1_interlude" {
				oldBody = encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/4", Goal: "https://github.com/planning/board/discussions/1"})
			}
			f.version["lastEditedAt"] = latest
			edits := []any{map[string]any{"id": "latest", "editedAt": latest, "diff": body}, map[string]any{"id": "middle", "editedAt": middle, "diff": "prose\n" + oldBody}, map[string]any{"id": "oldest", "editedAt": oldest, "diff": body}}
			f.pages["userContentEdits"] = []map[string]any{teamReadPage(edits, 3, "")}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			if err != nil {
				t.Fatal(err)
			}
			want := latest
			if scenario == "prose" {
				want = oldest
			}
			if !pr.ScopeUpdatedAt.Equal(want) || f.calls["userContentEdits"] != 2 {
				t.Fatalf("scope boundary=%s want %s; history reads=%d", pr.ScopeUpdatedAt, want, f.calls["userContentEdits"])
			}
		})
	}
}

func TestTeamReadScopeHistoryFailsClosed(t *testing.T) {
	for _, fault := range []string{"missing_page", "duplicate_edit", "deleted_edit", "changed_during_evidence"} {
		t.Run(fault, func(t *testing.T) {
			f := newTeamReadFixture()
			at := f.version["createdAt"].(time.Time).Add(time.Minute)
			f.version["lastEditedAt"] = at
			edit := map[string]any{"id": "edit", "editedAt": at, "diff": f.version["body"]}
			f.pages["userContentEdits"] = []map[string]any{teamReadPage([]any{edit}, 1, "")}
			switch fault {
			case "missing_page":
				f.pages["userContentEdits"][0]["totalCount"] = 2
			case "duplicate_edit":
				f.pages["userContentEdits"][0] = teamReadPage([]any{edit, edit}, 2, "")
			case "deleted_edit":
				edit["deletedAt"] = at
			case "changed_during_evidence":
				f.hook = func(key string, _ map[string]any) {
					if key == "reviews" {
						edit["id"] = "replacement-edit"
					}
				}
			}
			if _, err := f.client(t).readPR(context.Background(), f.version["url"].(string)); err == nil {
				t.Fatal("incoherent history accepted")
			}
		})
	}
}

func TestTeamReadEvidenceEngineeringGate(t *testing.T) {
	for _, scenario := range []string{"valid", "unresolved_coauthor", "truncated_reviews", "truncated_authors", "unknown_review_state", "future_review", "peer_coauthor", "pending_review"} {
		t.Run(scenario, func(t *testing.T) {
			c, s, _ := engineeringFixture(t, false)
			task, source := s.Tasks[0], s.PRs[0]
			f := newTeamReadFixture()
			f.c = c
			f.version["author"] = graphActor{ID: source.Author.ID, Kind: source.Author.Kind, Login: source.Author.Login}
			f.version["headRefOid"] = source.HeadSHA
			f.version["createdAt"] = time.Now().Add(-time.Hour)
			f.version["body"] = teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: task.GoalURL, AssignmentSHA: source.AssignmentSHA})
			r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
			r["commit"] = map[string]any{"oid": source.HeadSHA}
			r["submittedAt"] = time.Now().Add(-time.Minute)
			f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": source.HeadSHA, "committer": map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User"}}}}}
			f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User", Login: source.Author.Login}}}
			f.accounts[source.Author.ID] = restActor(source.Author)
			switch scenario {
			case "unresolved_coauthor":
				f.pages["commit-head"][0] = teamReadPage([]any{map[string]any{"user": graphActor{ID: source.Author.ID}}, map[string]any{"user": nil}}, 2, "")
			case "truncated_reviews":
				f.pages["reviews"][0]["totalCount"] = 2
			case "truncated_authors":
				f.pages["commit-head"][0]["totalCount"] = 2
			case "unknown_review_state":
				r["state"] = "NEW_STATE"
			case "future_review":
				r["submittedAt"] = time.Now().Add(time.Hour)
			case "peer_coauthor":
				peer := source.Reviews[0].Author
				f.pages["commit-head"][0] = teamReadPage([]any{map[string]any{"user": graphActor{ID: source.Author.ID}}, map[string]any{"user": graphActor{ID: peer.ID, Kind: "User", Login: "renamed-peer[bot]"}}}, 2, "")
				f.accounts[peer.ID] = restActor(peer)
			case "pending_review":
				r["state"], r["submittedAt"] = "PENDING", nil
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			_, cleared := EngineeringReviewFor(c, task, pr)
			if scenario == "valid" {
				if err != nil || !cleared {
					t.Fatalf("valid native evidence did not clear engineering: %+v %v", pr, err)
				}
			} else if cleared {
				t.Fatalf("unsafe native evidence cleared engineering: %+v", pr)
			} else if scenario != "peer_coauthor" && scenario != "pending_review" && err == nil {
				t.Fatal("malformed or incomplete native evidence did not fail ingestion")
			}
		})
	}
}

func TestTeamReadMergedDeliveryPreservesSavedScope(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%t/edited=%t", reverse, edited), func(t *testing.T) {
				c, s, _ := engineeringFixture(t, reverse)
				task, source := s.Tasks[0], s.PRs[0]
				f := newTeamReadFixture()
				f.c = c
				created := task.MetadataUpdatedAt.Add(time.Minute)
				boundary := created
				body := teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: task.GoalURL, AssignmentSHA: source.AssignmentSHA})
				f.version["url"], f.version["body"] = source.URL, body
				f.version["createdAt"], f.version["headRefOid"] = created, source.HeadSHA
				f.version["author"] = graphActor{ID: source.Author.ID, Kind: source.Author.Kind, Login: source.Author.Login}
				f.version["mergeStateStatus"] = "CLEAN"
				f.version["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}}
				if edited {
					boundary = created.Add(2 * time.Minute)
					f.version["lastEditedAt"] = boundary
					previous := strings.ReplaceAll(body, source.AssignmentSHA, BodyHash("previous assignment"))
					f.pages["userContentEdits"] = []map[string]any{teamReadPage([]any{
						map[string]any{"id": "restored", "editedAt": boundary, "diff": body},
						map[string]any{"id": "changed", "editedAt": created.Add(time.Minute), "diff": previous},
						map[string]any{"id": "original", "editedAt": created.Add(30 * time.Second), "diff": body},
					}, 3, "")}
				}
				reviewedAt := boundary.Add(time.Minute)
				f.version["updatedAt"] = reviewedAt
				r := f.pages["reviews"][0]["nodes"].([]any)[0].(map[string]any)
				r["commit"], r["submittedAt"] = map[string]any{"oid": source.HeadSHA}, reviewedAt
				peer := source.Reviews[0].Author
				r["author"] = graphActor{ID: peer.ID, Kind: peer.Kind, Login: peer.Login}
				f.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": source.HeadSHA, "committer": map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User"}}}}}
				f.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{ID: source.Author.ID, Kind: "User", Login: source.Author.Login}}}
				f.accounts[source.Author.ID] = restActor(source.Author)
				g := f.client(t)
				open, err := g.readPR(context.Background(), source.URL)
				if err != nil {
					t.Fatal(err)
				}
				rawScope := open.ScopeRevision
				bindReviewScope(&open, task)
				if open.Merged || !open.ScopeUpdatedAt.Equal(boundary) || !task.MetadataUpdatedAt.Before(boundary) {
					t.Fatalf("unexpected open scope: %+v; task metadata=%s", open, task.MetadataUpdatedAt)
				}
				s.PRs = []PR{open}
				state := newState()
				state.Version = 2
				jobs := Select(c, s, state, reviewedAt.Add(time.Minute))
				if len(jobs) != 1 || jobs[0].Kind != "deliver" || jobs[0].Binding == nil || jobs[0].PRScopeRevision != open.ScopeRevision {
					t.Fatalf("open PR did not select a scope-bound delivery: %+v", jobs)
				}
				jobs[0].Phase, jobs[0].SessionID = "active", "saved-delivery-session"
				rawJob, err := json.Marshal(jobs[0])
				if err != nil {
					t.Fatal(err)
				}
				var saved Job
				if err := json.Unmarshal(rawJob, &saved); err != nil {
					t.Fatal(err)
				}
				f.version["merged"], f.version["state"] = true, "MERGED"
				f.version["updatedAt"] = reviewedAt.Add(time.Minute)
				merged, err := g.readPR(context.Background(), source.URL)
				if err != nil {
					t.Fatal(err)
				}
				if !merged.Merged || !merged.Closed || merged.ScopeRevision != rawScope || !merged.ScopeUpdatedAt.Equal(boundary) {
					t.Fatalf("merge alone changed raw PR scope: open=%+v merged=%+v", open, merged)
				}
				bindReviewScope(&merged, task)
				if merged.ScopeRevision != saved.PRScopeRevision || !merged.ScopeUpdatedAt.Equal(open.ScopeUpdatedAt) {
					t.Fatalf("merge alone changed saved scope: saved=%s merged=%s", saved.PRScopeRevision, merged.ScopeRevision)
				}
				if edited && f.calls["userContentEdits"] != 4 {
					t.Fatalf("open/merged reads did not both verify durable history: %d reads", f.calls["userContentEdits"])
				}
				task.Completed, task.Closed = true, true
				artifacts := Artifacts{Task: &task, PR: &merged, IssueCompleted: true}
				if !artifactCompletes(c, saved, artifacts) {
					t.Fatalf("merged PR did not complete saved active v2 delivery: job=%+v artifacts=%+v", saved, artifacts)
				}
				artifacts.IssueCompleted = false
				if artifactCompletes(c, saved, artifacts) {
					t.Fatal("merged PR completed saved delivery before issue completion")
				}
			})
		}
	}
}
