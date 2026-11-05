package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReadQAArtifactFailsClosed(t *testing.T) {
	head := strings.Repeat("a", 40)
	source := QAArtifact{Kind: "source", HeadSHA: head, SHA256: BodyHash("old source")}
	workflow := QAArtifact{Kind: "workflow", HeadSHA: head, SHA256: BodyHash("artifact"), URL: "https://api.github.com/repos/code/repo/actions/artifacts/17", Revision: "23"}
	for _, fault := range []string{"source missing", "source transport", "source invalid", "source changed", "workflow missing", "workflow expired", "workflow malformed", "invalid candidate", "invalid head"} {
		t.Run(fault, func(t *testing.T) {
			candidate, endpoint := source, "repos/code/repo/tarball/"+head
			result, runErr, category := response("404 Not Found", "sentinel-secret"), error(nil), "not_found"
			if strings.HasPrefix(fault, "workflow") || fault == "invalid candidate" {
				candidate, endpoint = workflow, "repos/code/repo/actions/artifacts/17"
			}
			switch fault {
			case "source transport":
				result = response("200 OK", "")
				runErr, category = errors.New("sentinel-secret"), "request_failed"
			case "source invalid":
				result, category = response("200 OK", "not gzip sentinel-secret"), "invalid_QA_source_bundle"
			case "source changed":
				result, category = response("200 OK", string(qaBundle(t, qaBundleEntry{name: "root/file", content: "new"}))), "QA_artifact_changed"
			case "workflow expired":
				result, category = jsonResponse(map[string]any{"id": 17, "expired": true}), "invalid_QA_artifact"
			case "workflow malformed":
				result, category = response("200 OK", "{"), "invalid_QA_artifact"
			case "invalid candidate":
				candidate.URL, category = "https://example.com/sentinel-secret", "invalid_QA_artifact"
			case "invalid head":
				category = "invalid_QA_head"
			}
			g, _ := qaArtifactClient(t, endpoint, result, runErr)
			p := PR{HeadSHA: head, QAArtifact: &candidate, QAArtifactError: "old error"}
			if fault == "invalid head" {
				p.HeadSHA = "invalid"
			}
			g.readQAArtifact(context.Background(), Task{Repo: "code/repo", QA: QAPolicy{Required: true}}, &p)
			if p.QAArtifact != nil || p.QAArtifactError != category {
				t.Fatalf("artifact failure retained unverified identity or unsafe error: %+v", p)
			}
		})
	}
	for _, skip := range []string{"optional", "merged", "success"} {
		t.Run(skip, func(t *testing.T) {
			raw := qaBundle(t, qaBundleEntry{name: "root/file", content: "source"})
			g, calls := qaArtifactClient(t, "repos/code/repo/tarball/"+head, response("200 OK", string(raw)), nil)
			p := PR{HeadSHA: head, QAArtifactError: "stale", Merged: skip == "merged"}
			if skip != "success" {
				p.QAArtifact = &source
			}
			g.readQAArtifact(context.Background(), Task{Repo: "code/repo", QA: QAPolicy{Required: skip != "optional"}}, &p)
			wantReads := 0
			if skip == "success" {
				wantReads = 1
			}
			if p.QAArtifactError != "" || (p.QAArtifact != nil) != (skip == "success") || len(calls.calls) != wantReads {
				t.Fatalf("read did not clear stale state or bind native source: %+v", p)
			}
		})
	}
}

// Exercise the real catalog and revalidation readers, including native PR reads.
func qaRecoveryClient(t *testing.T) (Config, *GitHubClient, *bool, *bool, time.Time) {
	t.Helper()
	c, s, now := qaGateFixture(t)
	f, _, _ := teamBaseReadFixture(t, false)
	f.c = c
	task, goal, p := s.Tasks[0], s.Goals[0], s.PRs[0]
	p.QAArtifact.Kind, p.QAArtifact.URL, p.QAArtifact.Revision = "workflow", "https://api.github.com/repos/code/repo/actions/artifacts/17", "23"
	f.version["body"] = teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: p.AssignmentSHA, Artifact: p.QAArtifact})
	var reviews []any
	for _, r := range p.Reviews {
		reviews = append(reviews, map[string]any{"fullDatabaseId": r.ID, "state": r.State, "body": r.Body, "commit": map[string]any{"oid": r.HeadSHA}, "submittedAt": r.SubmittedAt, "author": graphActor{ID: r.Author.ID, Login: r.Author.Login, Kind: r.Author.Kind}})
	}
	f.pages["reviews"] = []map[string]any{teamReadPage(reviews, len(reviews), "")}
	c = Defaults(c)
	c.Repositories, c.Projects, c.ProjectOwners = []string{task.Repo}, nil, nil
	c.DiscussionRepositories = []string{"planning/board"}
	g := f.client(t)
	g.config, g.goalIDs[goal.URL] = c, "goal"
	unavailable, paused := false, false
	runner := g.runner.(*fakeCommands)
	fallback := runner.respond
	runner.respond = func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
		switch {
		case endpoint == "repos/code/repo/actions/artifacts/17":
			if unavailable {
				return response("404 Not Found", "sentinel-secret"), nil
			}
			return jsonResponse(map[string]any{"id": 17, "digest": "sha256:" + p.QAArtifact.SHA256, "workflow_run": map[string]any{"id": 23, "head_sha": p.HeadSHA}}), nil
		case strings.HasPrefix(endpoint, "repos/code/repo/issues?"):
			return jsonResponse([]restIssue{{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open"}}), nil
		case endpoint == "repos/code/repo/issues/1":
			return jsonResponse(restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open"}), nil
		case strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?"):
			return jsonResponse([]restComment{{ID: 2, Author: restActor(task.MetadataAuthor), CreatedAt: task.MetadataUpdatedAt, Body: teamMarker("task", TeamTaskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA, Assignment: *task.Assignment})}}), nil
		}
		var req struct{ Query string }
		_ = json.Unmarshal(raw, &req)
		var data any
		d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt}
		page := func(nodes any) any {
			return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
		}
		switch {
		case strings.Contains(req.Query, "WorkerIdentity"):
			data = map[string]any{"viewer": graphActor{ID: g.config.Self.ID, Login: g.config.Self.Login, Kind: g.config.Self.Kind}}
		case strings.Contains(req.Query, "WorkerDiscussions"):
			data = map[string]any{"repository": map[string]any{"discussions": page([]graphDiscussion{d})}}
		case strings.Contains(req.Query, "WorkerGoalRefresh"), strings.Contains(req.Query, "WorkerGoalVersion"):
			data = map[string]any{"node": d}
		case strings.Contains(req.Query, "WorkerDiscussionComments"):
			var comments []graphComment
			for _, decision := range goal.Decisions {
				comments = append(comments, graphComment{ID: decision.ID, Body: decision.Command, CreatedAt: decision.CreatedAt, Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}})
			}
			if paused {
				comments = append(comments, graphComment{ID: "pause", Body: "/coop pause", CreatedAt: now, Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}})
			}
			data = map[string]any{"node": map[string]any{"comments": page(comments)}}
		case strings.Contains(req.Query, "WorkerDiscussionReplies"):
			data = map[string]any{"node": map[string]any{"replies": page([]any{})}}
		default:
			return fallback(owner, endpoint, method, raw)
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}
	return c, g, &unavailable, &paused, now.Add(time.Minute)
}

func TestQAArtifactFailureRetainsFreshSnapshot(t *testing.T) {
	for _, path := range []string{"snapshot", "revalidate"} {
		t.Run(path, func(t *testing.T) {
			c, g, unavailable, paused, now := qaRecoveryClient(t)
			s, err := g.Snapshot(context.Background(), c, Snapshot{})
			if err != nil || !s.Complete || s.PRs[0].QAArtifact == nil {
				t.Fatalf("initial artifact read failed: %+v %v", s, err)
			}
			j := qaGateJob(t, c, s, now, "deliver")
			*unavailable, *paused = true, true
			if path == "snapshot" {
				s, err = g.Snapshot(context.Background(), c, s)
			} else {
				s, err = g.Revalidate(context.Background(), j, s)
			}
			if err != nil || !s.Complete || s.PRs[0].QAArtifact != nil || s.PRs[0].QAArtifactError != "not_found" || EvaluateGoal(c, s.Goals[0], now).State != "paused" {
				t.Fatalf("artifact failure discarded fresh authorization: %+v %v", s, err)
			}
			// Isolate missing-artifact admission from the independently verified pause.
			s.Goals[0].Decisions = s.Goals[0].Decisions[:1]
			for _, kind := range []string{"qa", "deliver"} {
				actor := c
				if kind == "qa" {
					actor = qaGateActor(c, "qa")
				}
				job := qaGateJob(t, actor, s, now, kind)
				if ok, _ := Admit(actor, job, s, now); ok {
					t.Fatalf("%s admitted missing artifact", kind)
				}
			}
		})
	}
}

func TestQAContinuationRejectsMissingOrChangedArtifactDuringEngineeringRerun(t *testing.T) {
	for _, fault := range []string{"missing", "digest", "head", "revision", "URL", "kind", "saved missing"} {
		t.Run(fault, func(t *testing.T) {
			c, s, j, now := qaRuntimePromptFixture(t, false)
			// No QA completion and no current engineering clearance may hide an
			// artifact identity change from the saved binding check.
			s.PRs[0].Reviews = nil
			e := &legacyInspection{Config: c, state: State{SnapshotCache: s}, Clock: &fakeClock{now: now}}
			if !e.savedBindingAllowed(j) {
				t.Fatal("unchanged artifact rejected")
			}
			artifact := *s.PRs[0].QAArtifact
			s.PRs[0].QAArtifact = &artifact
			switch fault {
			case "missing":
				s.PRs[0].QAArtifact = nil
			case "digest":
				artifact.SHA256 = BodyHash("changed")
			case "head":
				artifact.HeadSHA = BodyHash("changed")
			case "revision":
				artifact.Revision = "changed"
			case "URL":
				artifact.URL += "changed"
			case "kind":
				artifact.Kind = "workflow"
			case "saved missing":
				j.QAArtifact = nil
			}
			e.state.SnapshotCache = s
			if artifactCompletes(c, j, snapshotArtifacts(c, j, s)) || e.savedBindingAllowed(j) {
				t.Fatal("artifact identity change bypassed saved QA binding")
			}
			if ok, _ := e.continuationAllowed(j); ok {
				t.Fatal("changed artifact continued QA")
			}
		})
	}
}
