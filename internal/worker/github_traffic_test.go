package worker

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestGitHubTrafficStageDoesNotSplitCombinedGraphQLRequests(t *testing.T) {
	for _, tc := range []struct{ endpoint, query, want string }{
		{"graphql", "query WorkerNativePR($owner:String!){repository{id}}", "pr_with_checks"},
		{"graphql", "query WorkerTeamReviews($owner:String!){repository{id}}", "reviews"},
		{"graphql", "query WorkerTeamCommits($owner:String!){repository{id}}", "pr_history"},
		{"graphql", "query WorkerProjectValues($id:ID!){node{id}}", "project"},
		{"graphql", "query WorkerNativeTaskProjectItems($id:ID!){node{id}}", "project"},
		{"graphql", "query Unrecognized{node{id}}", "other"},
		{"repos/code/repo/issues/1/comments?per_page=100", "", "task_discovery"},
		{"repos/code/repo/commits/sha/check-runs", "", "checks"},
		{"repos/code/repo/pulls/1", "", "pr"},
		{"repos/code/repo/tarball/sha", "", "artifact"},
	} {
		if got := githubTrafficStage(tc.endpoint, tc.query); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.endpoint, got, tc.want)
		}
	}
}

func TestGitHubTrafficCountsWireAndRetainedSeparately(t *testing.T) {
	now := time.Now().UTC()
	underlying := cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
		return response("200 OK", `{}`), nil
	})
	first := newObservationRunner(underlying, now, providerState{})
	if _, _, err := NewGitHubClient(first).api(context.Background(), "code", "GET", "repos/code/repo/pulls/1", nil); err != nil {
		t.Fatal(err)
	}
	second := newObservationRunner(underlying, now.Add(time.Second), first.state)
	ctx, totals := withGitHubTraffic(t.Context())
	reader := NewGitHubClient(second)
	for range 2 {
		if _, _, err := reader.api(ctx, "code", "GET", "repos/code/repo/pulls/1", nil); err != nil {
			t.Fatal(err)
		}
	}
	wire, stages, reused := totals.snapshot()
	if second.requests != 1 || wire != 1 || !reflect.DeepEqual(stages, map[string]int{"pr": 1}) || !reflect.DeepEqual(reused, map[string]int{"pr": 1}) {
		t.Fatalf("verification must reach provider: requests=%d wire=%d stages=%v reused=%v", second.requests, wire, stages, reused)
	}
	// The snapshot is a copy, not a mutable map exposed to log consumers.
	stages["pr"] = 100
	_, again, _ := totals.snapshot()
	if again["pr"] != 1 {
		t.Fatal("traffic snapshot was mutable")
	}
}

func TestGitHubTrafficLiveAndPostmergeQA(t *testing.T) {
	ctx, totals := withGitHubTraffic(t.Context())
	client := NewGitHubClient(cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
		return response("200 OK", `{"data":{"repository":{}}}`), nil
	}))
	query := "query WorkerNativePR($owner:String!){repository{id}}"
	if _, err := client.graphql(ctx, "code", query, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.api(withGitHubTrafficStage(ctx, "postmerge_qa"), "code", "GET", "repos/code/repo/pulls/1", nil); err != nil {
		t.Fatal(err)
	}
	wire, stages, _ := totals.snapshot()
	if wire != 2 || !reflect.DeepEqual(stages, map[string]int{"pr_with_checks": 1, "postmerge_qa": 1}) {
		t.Fatal(wire, stages)
	}
}

func TestCacheClaimCategoryDoesNotLeakErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{c.ErrForbidden, "authorization_denied"},
		{c.ErrConflict, "claim_conflict"},
		{c.ErrUnavailable, "master_unavailable"},
		{context.DeadlineExceeded, "master_unavailable"},
		{c.ErrCapacity, "capacity_exceeded"},
		{c.ErrInvalid, "invalid_claim"},
		{errors.New("private master response and credential"), "claim_failed"},
	} {
		if got := cacheClaimCategory(tc.err); got != tc.want {
			t.Fatalf("%v: got %s want %s", tc.err, got, tc.want)
		}
	}
}
