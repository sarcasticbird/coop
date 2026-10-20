package worker

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"io"
	"strings"
	"testing"
	"time"
)

func TestGitHubConditionalIndependentVerification(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(map[bool]string{false: "comments", true: "scope_history"}[graph], func(t *testing.T) {
			calls := 0
			r := newObservationRunner(cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
				calls++
				return response("200 OK", `{}`), nil
			}), time.Now(), providerState{})
			g := NewGitHubClient(r)
			method, endpoint := "GET", "repos/org/repo/issues/1/comments?per_page=100&page=1"
			var body any
			if graph {
				method, endpoint, body = "POST", "graphql", map[string]string{"query": "query WorkerScopeHistory{node{id}}"}
			}
			for range 2 {
				if _, _, err := g.api(t.Context(), "org", method, endpoint, body); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatal("verification replayed an earlier response", calls)
			}
		})
	}
}

func TestGitHubConditionalRejectsUnowned304(t *testing.T) {
	r := newObservationRunner(cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
		return response("304 Not Modified", ""), nil
	}), time.Now(), providerState{})
	_, err := r.Run(t.Context(), []string{"/usr/local/libexec/coop-github", "gh", "--owner", "org", "--", "api", "repos/org/repo/issues/1/comments?per_page=100&page=1"}, nil, nil)
	if !errors.Is(err, c.ErrConflict) {
		t.Fatal("304 accepted without a retained validated body", err)
	}
}

func TestGitHubConditionalPaginatedResultsHaveNoValidator(t *testing.T) {
	calls := 0
	base := cacheRunFunc(func(_ context.Context, argv, _ []string, _ io.Reader) (CommandResult, error) {
		calls++
		if strings.Contains(strings.Join(argv, " "), "If-None-Match") {
			t.Fatal("paginated collection sent a validator")
		}
		return CommandResult{Stdout: []byte("HTTP/1.1 200 OK\r\nETag: \"page\"\r\nLink: <https://api.github.com/next>; rel=\"next\"\r\n\r\n[]")}, nil
	})
	now := time.Now()
	r := newObservationRunner(base, now, providerState{})
	endpoint := "repos/org/repo/issues/1/comments?per_page=100&page=1"
	if _, _, err := NewGitHubClient(r).api(t.Context(), "org", "GET", endpoint, nil); err != nil {
		t.Fatal(err)
	}
	next := newObservationRunner(base, now.Add(6*time.Minute), r.state)
	if _, _, err := NewGitHubClient(next).api(t.Context(), "org", "GET", endpoint, nil); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
}

type cacheRunFunc func(context.Context, []string, []string, io.Reader) (CommandResult, error)

func (f cacheRunFunc) Run(ctx context.Context, a, e []string, r io.Reader) (CommandResult, error) {
	return f(ctx, a, e, r)
}
func TestGitHubConditionalReadSafety(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	conditional := false
	underlying := cacheRunFunc(func(_ context.Context, a, e []string, in io.Reader) (CommandResult, error) {
		calls++
		conditional = strings.Contains(strings.Join(a, " "), "If-None-Match")
		if calls == 2 {
			return response("304 Not Modified", ""), nil
		}
		return CommandResult{Stdout: []byte("HTTP/1.1 200 OK\r\nETag: \"v1\"\r\n\r\n[]")}, nil
	})
	r := newObservationRunner(underlying, now, providerState{})
	g := NewGitHubClient(r)
	_, _, err := g.api(context.Background(), "org", "GET", "repos/org/repo/issues/1/comments?per_page=100&page=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.now = now.Add(6 * time.Minute)
	_, _, err = g.api(context.Background(), "org", "GET", "repos/org/repo/issues/1/comments?per_page=100&page=1", nil)
	if err != nil || !conditional {
		t.Fatal(err, conditional)
	}
	r.now = now.Add(31 * time.Minute)
	_, _, err = g.api(context.Background(), "org", "GET", "repos/org/repo/issues/1/comments?per_page=100&page=1", nil)
	if err != nil || conditional {
		t.Fatal("304 extended unconditional deadline", err)
	}
}
func TestGitHubCollectorBudgets(t *testing.T) {
	calls := 0
	r := newObservationRunner(cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
		calls++
		return response("200 OK", `{}`), nil
	}), time.Now(), providerState{})
	g := NewGitHubClient(r)
	r.force = true
	for i := 0; i < 256; i++ {
		if _, _, e := g.api(context.Background(), "org", "GET", "repos/org/repo", nil); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := g.api(context.Background(), "org", "GET", "repos/org/repo", nil); e == nil {
		t.Fatal("unbounded provider calls")
	}
	if calls != 256 {
		t.Fatal(calls)
	}
}
