package worker

import (
	"context"
	"strings"
	"sync"
)

// trafficTotals is scoped to one discovery scan or one cache collection round.
// It contains counts only: never query variables, paths, bodies or credentials.
type trafficTotals struct {
	mu     sync.Mutex
	wire   map[string]int
	reused map[string]int
}

type trafficKey struct{}
type trafficStageKey struct{}

func withGitHubTraffic(ctx context.Context) (context.Context, *trafficTotals) {
	totals := &trafficTotals{wire: map[string]int{}, reused: map[string]int{}}
	return context.WithValue(ctx, trafficKey{}, totals), totals
}

func withGitHubTrafficStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, trafficStageKey{}, stage)
}

func trafficStage(ctx context.Context, endpoint, query string) string {
	if stage, ok := ctx.Value(trafficStageKey{}).(string); ok {
		return stage
	}
	return githubTrafficStage(endpoint, query)
}

func (t *trafficTotals) record(stage string, wire bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if wire {
		t.wire[stage]++
	} else {
		t.reused[stage]++
	}
}

func (t *trafficTotals) snapshot() (int, map[string]int, map[string]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	wire, reused := make(map[string]int, len(t.wire)), make(map[string]int, len(t.reused))
	total := 0
	for k, n := range t.wire {
		wire[k] = n
		total += n
	}
	for k, n := range t.reused {
		reused[k] = n
	}
	return total, wire, reused
}

// A GraphQL request containing both PR and check data is one wire call, not
// one call per field. Unknown operations stay in other instead of guessing.
func githubTrafficStage(endpoint, query string) string {
	if endpoint == "graphql" {
		fields := strings.Fields(query)
		if len(fields) < 2 || fields[0] != "query" {
			return "other"
		}
		name := strings.SplitN(fields[1], "(", 2)[0]
		switch {
		case strings.HasPrefix(name, "WorkerProject"), strings.HasPrefix(name, "WorkerNativeTaskProject"), strings.HasPrefix(name, "WorkerCacheProject"):
			return "project"
		case name == "WorkerTeamReviews":
			return "reviews"
		case name == "WorkerNativePR", name == "WorkerPR":
			return "pr_with_checks"
		case name == "WorkerTeamCommits", name == "WorkerTeamAuthors", name == "WorkerTeamBaseScope", name == "WorkerScopeHistory", name == "WorkerPRVersion", name == "WorkerNativeBase":
			return "pr_history"
		case name == "WorkerIdentity", name == "WorkerIssueGoal", name == "WorkerDiscussions", name == "WorkerGoalVersion", name == "WorkerDiscussionComments", name == "WorkerDiscussionReplies":
			return "task_discovery"
		default:
			return "other"
		}
	}
	if strings.Contains(endpoint, "/tarball/") {
		return "artifact"
	}
	if strings.Contains(endpoint, "/check-runs") || strings.Contains(endpoint, "/statuses/") {
		return "checks"
	}
	if strings.Contains(endpoint, "/pulls/") {
		if strings.Contains(endpoint, "/reviews") {
			return "reviews"
		}
		if strings.Contains(endpoint, "/commits") {
			return "pr_history"
		}
		return "pr"
	}
	if strings.Contains(endpoint, "/issues/") || strings.Contains(endpoint, "/issues?") {
		return "task_discovery"
	}
	if strings.Contains(endpoint, "/projects/") {
		return "project"
	}
	return "other"
}
