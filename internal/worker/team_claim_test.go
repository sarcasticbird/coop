package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type teamClaimFixture struct {
	g                       *GitHubClient
	j                       Job
	issue                   restIssue
	comments                map[string][]restComment
	target                  string
	posts, taskReads, goals int
	losePost                bool
	beforeTask              func()
}

func newTeamClaimFixture(t *testing.T, reverse bool, kind string) *teamClaimFixture {
	t.Helper()
	c, s, _ := engineeringFixture(t, reverse)
	task, goal, pr := s.Tasks[0], s.Goals[0], s.PRs[0]
	if kind == "implement" {
		s.PRs = nil
	} else {
		for _, member := range c.Team.Members {
			if sameActor(member.Actor, pr.Reviews[0].Author) {
				c.Name, c.Self = member.Name, member.Actor
			}
		}
	}
	f := &teamClaimFixture{
		issue:    restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: task.MetadataUpdatedAt},
		comments: map[string][]restComment{},
		target:   "repos/code/repo/issues/1/comments",
	}
	f.comments[f.target] = []restComment{{ID: 2, Author: restActor(task.MetadataAuthor), CreatedAt: task.MetadataUpdatedAt, UpdatedAt: task.MetadataUpdatedAt,
		Body: teamMarker("task", TeamTaskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA, Assignment: *task.Assignment})}}
	if kind == "review" {
		f.target = "repos/code/repo/issues/2/comments"
	}
	reads := newTeamReadFixture()
	reads.c = c
	reads.version["url"], reads.version["headRefOid"] = pr.URL, pr.HeadSHA
	reads.version["createdAt"], reads.version["updatedAt"] = task.MetadataUpdatedAt.Add(-time.Hour), task.MetadataUpdatedAt
	reads.version["body"] = teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: pr.AssignmentSHA})
	reads.version["author"] = graphActor{ID: pr.Author.ID, Kind: pr.Author.Kind, Login: pr.Author.Login}
	reads.pages["reviews"] = []map[string]any{teamReadPage([]any{}, 0, "")}
	reads.pages["commits"][0]["nodes"] = []any{map[string]any{"commit": map[string]any{"id": "commit-head", "oid": pr.HeadSHA, "committer": map[string]any{"user": graphActor{ID: pr.Author.ID, Kind: "User", Login: pr.Author.Login}}}}}
	reads.pages["commit-head"][0]["nodes"] = []any{map[string]any{"user": graphActor{ID: pr.Author.ID, Kind: "User", Login: pr.Author.Login}}}
	reads.accounts[pr.Author.ID] = restActor(pr.Author)
	base := reads.client(t).runner.(*fakeCommands).respond
	discussion := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt, Author: graphActor{ID: c.Owner.ID, Kind: c.Owner.Kind, Login: c.Owner.Login}}
	runner := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo/issues/1" {
			if method != "GET" || owner != "code" {
				t.Fatalf("unexpected task request: %s %s", owner, method)
			}
			f.taskReads++
			if f.beforeTask != nil {
				f.beforeTask()
			}
			return jsonResponse(f.issue), nil
		}
		path := strings.Split(endpoint, "?")[0]
		if path == "repos/code/repo/issues/1/comments" || path == "repos/code/repo/issues/2/comments" {
			if owner != "code" {
				t.Fatal("comments routed through wrong owner")
			}
			if method == "GET" {
				return jsonResponse(f.comments[path]), nil
			}
			if method != "POST" || path != f.target {
				t.Fatalf("unexpected mutation: %s %s", method, endpoint)
			}
			f.posts++
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			comment := restComment{ID: int64(100 + f.posts), Body: payload.Body, Author: restActor(c.Self), CreatedAt: time.Now(), UpdatedAt: time.Now()}
			f.comments[path] = append(f.comments[path], comment)
			if f.losePost {
				f.losePost = false
				return CommandResult{}, errors.New("response lost after accepted POST")
			}
			return jsonResponse(comment), nil
		}
		if endpoint == "graphql" {
			var req struct{ Query string }
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			var node any
			switch {
			case strings.Contains(req.Query, "WorkerGoalRefresh"), strings.Contains(req.Query, "WorkerGoalVersion"):
				f.goals++
				node = discussion
			case strings.Contains(req.Query, "WorkerDiscussionComments"):
				d := goal.Decisions[0]
				node = map[string]any{"comments": teamReadPage([]any{graphComment{ID: d.ID, Body: d.Command, CreatedAt: d.CreatedAt, Author: graphActor{ID: d.Author.ID, Kind: d.Author.Kind, Login: d.Author.Login}}}, 1, "")}
			case strings.Contains(req.Query, "WorkerDiscussionReplies"):
				node = map[string]any{"replies": teamReadPage([]any{}, 0, "")}
			default:
				return base(owner, endpoint, method, body)
			}
			if owner != "planning" {
				t.Fatal("goal routed through wrong owner")
			}
			return jsonResponse(map[string]any{"data": map[string]any{"node": node}}), nil
		}
		return base(owner, endpoint, method, body)
	}}
	f.g = NewGitHubClient(runner)
	f.g.config, f.g.snapshot = c, s
	f.g.goalIDs[goal.URL] = discussion.ID
	// Select against API-ingested PR evidence and its actual durable scope, not
	// the synthetic scope revision supplied by engineeringFixture.
	if kind == "review" {
		fresh, err := f.g.readPR(context.Background(), pr.URL)
		if err != nil {
			t.Fatal(err)
		}
		bindReviewScope(&fresh, task)
		f.g.snapshot.PRs = []PR{fresh}
	}
	for _, j := range Select(c, f.g.snapshot, newState(), time.Now()) {
		if j.Kind == kind {
			f.j = j
		}
	}
	if f.j.ID == "" {
		t.Fatalf("fixture did not select %s", kind)
	}
	return f
}

func (f *teamClaimFixture) comment(author Actor, instance string, binding JobBinding) restComment {
	return restComment{ID: 90, Author: restActor(author), Body: marker(f.j, "claim") + "\n" + teamMarker("claim", struct {
		Instance string     `json:"instance"`
		Binding  JobBinding `json:"binding"`
	}{instance, binding})}
}

func TestTeamClaimRetry(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review"} {
			for _, lost := range []bool{false, true} {
				name := map[bool]string{false: "forward/", true: "reverse/"}[reverse] + kind + map[bool]string{false: "/repeat", true: "/lost-response"}[lost]
				t.Run(name, func(t *testing.T) {
					f := newTeamClaimFixture(t, reverse, kind)
					instance := BodyHash("live instance")
					f.losePost = lost
					first, err := f.g.Claim(context.Background(), f.j, instance)
					if lost && err == nil {
						t.Fatal("lost response was not surfaced")
					}
					if !lost && err != nil {
						t.Fatal(err)
					}
					again, err := f.g.Claim(context.Background(), f.j, instance)
					if err != nil {
						t.Fatal("retry did not recover durable claim:", err)
					}
					if f.posts != 1 || again.ID != "101" || again.Marker != marker(f.j, "claim") || again.InstanceID != instance || !sameActor(again.Author, f.g.config.Self) || !reflect.DeepEqual(again.Binding, f.j.Binding) {
						t.Fatalf("wrong recovered claim: %+v; POSTs=%d", again, f.posts)
					}
					if !lost && !reflect.DeepEqual(first, again) {
						t.Fatal("repeat changed claim")
					}
					if f.taskReads != 6 || f.goals != 6 {
						t.Fatalf("retry skipped full revalidation: task=%d goal=%d", f.taskReads, f.goals)
					}
					if kind == "review" && len(f.comments["repos/code/repo/issues/1/comments"]) != 1 {
						t.Fatal("review claimed implementation issue")
					}
				})
			}
		}
	}
}

func TestTeamClaimConflicts(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review"} {
			for _, conflict := range []string{"author", "instance", "assignment"} {
				t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind+"/"+conflict, func(t *testing.T) {
					f := newTeamClaimFixture(t, reverse, kind)
					instance, author, binding := BodyHash("live instance"), f.g.config.Self, *f.j.Binding
					savedInstance := instance
					switch conflict {
					case "author":
						author = f.g.config.Team.Members[2].Actor
						binding.ExecutorID = author.ID
					case "instance":
						savedInstance = BodyHash("other live instance")
					case "assignment":
						binding.AssignmentSHA = BodyHash("other assignment")
					}
					f.comments[f.target] = append(f.comments[f.target], f.comment(author, savedInstance, binding))
					_, err := f.g.Claim(context.Background(), f.j, instance)
					want := "conflicting_claim"
					if kind == "implement" && conflict != "instance" {
						want = "claim_scope_changed"
					}
					var apiErr *GitHubError
					if !errors.As(err, &apiErr) || apiErr.Category != want {
						t.Fatalf("conflicting ownership: got %v, want %s", err, want)
					}
					if f.posts != 0 || f.taskReads != 3 || f.goals != 3 {
						t.Fatalf("conflict bypassed fresh validation or posted: %+v", f)
					}
				})
			}
		}
	}
}

func TestTeamClaimReadTaskRejectsLastWins(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, conflict := range []string{"author", "instance", "assignment"} {
			for _, matchingLast := range []bool{false, true} {
				t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+conflict+map[bool]string{false: "/conflict-last", true: "/matching-last"}[matchingLast], func(t *testing.T) {
					f := newTeamClaimFixture(t, reverse, "implement")
					instance, binding, author := BodyHash("live instance"), *f.j.Binding, f.g.config.Self
					matching := f.comment(author, instance, binding)
					switch conflict {
					case "author":
						author = f.g.config.Team.Members[2].Actor
						binding.ExecutorID = author.ID
					case "instance":
						instance = BodyHash("other instance")
					case "assignment":
						binding.AssignmentSHA = BodyHash("other assignment")
					}
					other := f.comment(author, instance, binding)
					other.ID = 91
					claims := []restComment{matching, other}
					if matchingLast {
						claims = []restComment{other, matching}
					}
					f.comments[f.target] = append(f.comments[f.target], claims...)
					fresh, err := f.g.readTask(context.Background(), "code/repo", f.issue)
					if err != nil {
						t.Fatal("claim conflict failed the task read:", err)
					}
					if !fresh.ClaimConflict {
						t.Fatal("conflicting claims did not mark the task blocked")
					}
					if _, err := ValidateTeamScope(f.g.config, fresh, f.g.snapshot.Goals[0], time.Now()); err == nil {
						t.Fatal("conflicting task claims passed scope validation")
					}
					if f.posts != 0 {
						t.Fatal("task read mutated GitHub")
					}
				})
			}
		}
	}
}

func TestTeamClaimOutsidersIgnored(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review"} {
			for _, scenario := range []string{"duplicate", "malformed", "duplicate-metadata"} {
				t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind+"/"+scenario, func(t *testing.T) {
					f := newTeamClaimFixture(t, reverse, kind)
					instance := BodyHash("live instance")
					// A matching login does not make an outsider a trusted roster actor.
					outsider := f.g.config.Self
					outsider.ID = 999
					binding := *f.j.Binding
					binding.ExecutorID = outsider.ID
					first := f.comment(outsider, BodyHash("outsider instance"), binding)
					binding.AssignmentSHA = BodyHash("outsider assignment")
					second := f.comment(outsider, BodyHash("other outsider instance"), binding)
					second.ID = 91
					switch scenario {
					case "malformed":
						first.Body = marker(f.j, "claim") + "\n<!-- coop-claim:v2 invalid -->"
						second.Body = first.Body
					case "duplicate-metadata":
						first.Body += first.Body
						second.Body += second.Body
					}
					paths := []string{"repos/code/repo/issues/1/comments"}
					if kind == "review" {
						paths = append(paths, f.target)
					}
					for _, path := range paths {
						f.comments[path] = append(f.comments[path], first, second)
					}
					fresh, err := f.g.readTask(context.Background(), "code/repo", f.issue)
					if err != nil || fresh.ClaimConflict || fresh.Claim != nil {
						t.Fatalf("outsider comments poisoned task read: %+v, %v", fresh, err)
					}
					if _, err := ValidateTeamScope(f.g.config, fresh, f.g.snapshot.Goals[0], time.Now()); err != nil {
						t.Fatal("outsider comments blocked healthy scope:", err)
					}
					claim, err := f.g.Claim(context.Background(), f.j, instance)
					if err != nil {
						t.Fatal("outsider comments blocked claiming:", err)
					}
					again, err := f.g.Claim(context.Background(), f.j, instance)
					if err != nil || !reflect.DeepEqual(claim, again) || f.posts != 1 || claim.ID != "101" || !reflect.DeepEqual(claim.Binding, f.j.Binding) {
						t.Fatalf("healthy claim/retry failed: %+v, %v; POSTs=%d", again, err, f.posts)
					}
					fresh, err = f.g.readTask(context.Background(), "code/repo", f.issue)
					if err != nil || fresh.ClaimConflict {
						t.Fatalf("task read became unhealthy after legitimate claim: %+v, %v", fresh, err)
					}
					if kind == "implement" && !reflect.DeepEqual(fresh.Claim, &claim) {
						t.Fatal("task read did not retain legitimate implementation claim")
					}
					if _, err := ValidateTeamScope(f.g.config, fresh, f.g.snapshot.Goals[0], time.Now()); err != nil {
						t.Fatal("claimed task scope became unhealthy:", err)
					}
				})
			}
		}
	}
}

func TestTeamClaimFreshAssignmentChange(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review"} {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind, func(t *testing.T) {
				f := newTeamClaimFixture(t, reverse, kind)
				wantAuthor := f.g.snapshot.Tasks[0].Assignment.Peer
				f.beforeTask = func() {
					if f.taskReads != 1 {
						return
					}
					comments := f.comments["repos/code/repo/issues/1/comments"]
					var m TeamTaskMetadata
					if err := teamMetadata(comments[0].Body, "task", &m); err != nil {
						t.Fatal(err)
					}
					m.Assignment.Author, m.Assignment.Peer = m.Assignment.Peer, m.Assignment.Author
					comments[0].Body = teamMarker("task", m)
				}
				_, err := f.g.Claim(context.Background(), f.j, BodyHash("live instance"))
				var apiErr *GitHubError
				if !errors.As(err, &apiErr) || apiErr.Category != "claim_scope_changed" {
					t.Fatalf("fresh assignment change not rejected by admission: %v", err)
				}
				if f.posts != 0 || f.taskReads != 3 || f.goals != 3 {
					t.Fatalf("changed assignment posted or skipped fresh reads: %+v", f)
				}
				fresh := f.g.snapshot.Tasks[0]
				if fresh.Assignment == nil || fresh.Assignment.Author != wantAuthor {
					t.Fatal("assignment change was only a metadata parse failure")
				}
			})
		}
	}
}

func TestTeamClaimAssignmentChangeDuringRead(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, kind := range []string{"implement", "review"} {
			t.Run(map[bool]string{false: "forward/", true: "reverse/"}[reverse]+kind, func(t *testing.T) {
				f := newTeamClaimFixture(t, reverse, kind)
				f.beforeTask = func() {
					if f.taskReads != 2 {
						return
					}
					comments := f.comments["repos/code/repo/issues/1/comments"]
					var m TeamTaskMetadata
					if err := teamMetadata(comments[0].Body, "task", &m); err != nil {
						t.Fatal(err)
					}
					m.Assignment.Author, m.Assignment.Peer = m.Assignment.Peer, m.Assignment.Author
					comments[0].Body = teamMarker("task", m)
				}
				_, err := f.g.Claim(context.Background(), f.j, BodyHash("live instance"))
				var apiErr *GitHubError
				if !errors.As(err, &apiErr) || apiErr.Category != "task_version_changed" {
					t.Fatalf("assignment change during read not rejected: %v", err)
				}
				if f.posts != 0 {
					t.Fatal("assignment change during read posted a claim")
				}
			})
		}
	}
}
