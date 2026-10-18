package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGraphQLHTTP200QuotaClassification(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name, status, headers, body, category string
		exit                                  int
	}{
		{"quota-cli-failure", "200 OK", fmt.Sprintf("X-RateLimit-Remaining: 0\r\nX-RateLimit-Reset: %d\r\n", reset.Unix()), `{"data":null,"errors":[{"type":"RATE_LIMIT","message":"sentinel-secret"}]}`, "rate_limited", 1},
		{"quota-cli-success", "200 OK", "Retry-After: 120\r\n", `{"errors":[{"type":"RATE_LIMITED"}]}`, "rate_limited", 0},
		{"ordinary-graphql-error", "200 OK", "", `{"data":null,"errors":[{"type":"FORBIDDEN"}]}`, "graphql_failed", 0},
		{"ordinary-cli-failure", "200 OK", "", `{"errors":[{"type":"FORBIDDEN"}]}`, "request_failed", 1},
		{"success-last-budget-unit", "200 OK", "X-RateLimit-Remaining: 0\r\n", `{"data":{"ok":true}}`, "", 0},
		{"rest-limit", "403 Forbidden", "X-RateLimit-Remaining: 0\r\nRetry-After: 120\r\n", `{}`, "rate_limited", 1},
		{"http429", "429 Too Many Requests", "Retry-After: 120\r\n", `{}`, "rate_limited", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
				return CommandResult{Stdout: []byte("HTTP/2.0 " + tc.status + "\r\n" + tc.headers + "\r\n" + tc.body), ExitCode: tc.exit, Stderr: []byte("sentinel-secret")}, nil
			}}
			_, err := NewGitHubClient(f).graphql(context.Background(), "code", "query{ok}", nil)
			if tc.category == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var got *GitHubError
			if !errors.As(err, &got) || got.Category != tc.category {
				t.Fatalf("got %v, want %s", err, tc.category)
			}
			if strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatal("quota response leaked")
			}
			if tc.category == "rate_limited" && !got.RetryAt.After(time.Now()) {
				t.Fatal("missing future reset")
			}
			if tc.name == "quota-cli-failure" && !got.RetryAt.Equal(reset) {
				t.Fatalf("got %s, want %s", got.RetryAt, reset)
			}
		})
	}
}

func TestGraphQLQuotaRejectsUnboundedReset(t *testing.T) {
	f := &fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
		return CommandResult{Stdout: []byte("HTTP/2.0 200 OK\r\nRetry-After: 999999999999999\r\nX-RateLimit-Reset: 999999999999999\r\n\r\n" + `{"errors":[{"type":"RATE_LIMIT"}]}`), ExitCode: 1}, nil
	}}
	_, err := NewGitHubClient(f).graphql(context.Background(), "code", "query{ok}", nil)
	var got *GitHubError
	if !errors.As(err, &got) || got.Category != "rate_limited" || got.RetryAt.Before(time.Now()) || got.RetryAt.After(time.Now().Add(2*time.Minute)) {
		t.Fatalf("unsafe quota deadline: %v", got)
	}
}
