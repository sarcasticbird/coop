package worker

import (
	"errors"
	"strings"
	"testing"
)

func TestTagOnlyRateLimitRefusalCannotAdoptForeignRef(t *testing.T) {
	for _, status := range []string{"429 Too Many Requests", "403 Forbidden"} {
		t.Run(status, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionFixture(t, false)
			previous := transport.commands.respond
			refWrites := 0
			transport.commands.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if method == "POST" && strings.HasSuffix(endpoint, "/git/refs") {
					refWrites++
					return CommandResult{Stdout: []byte("HTTP/1.1 " + status + "\r\nRetry-After: 60\r\nX-RateLimit-Remaining: 0\r\nContent-Type: application/json\r\n\r\n{}"), ExitCode: 1}, nil
				}
				return previous(owner, endpoint, method, body)
			}
			in := NativeAction{Operation: "tag-only", RequestID: "tag-request"}
			_, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in)
			var response *GitHubError
			if !errors.As(err, &response) || response.Category != "rate_limited" || response.StatusCode != 429 && response.StatusCode != 403 || response.RetryAt.IsZero() {
				t.Fatal("rate-limit refusal lost response identity", err)
			}
			j, err := readTagOnlyIntent(cfg, x.Job.Master.DeliveryHandoffID)
			if err != nil || j.Phase != "ref-rejected" || api.ref || refWrites != 1 || len(api.writes) != 1 {
				t.Fatal("definitive refusal not journaled", err, j.Phase, refWrites)
			}
			// A different writer creates the exact object/ref after our refusal.
			api.ref = true
			if _, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil {
				t.Fatal("adopted foreign equal ref after conclusive refusal")
			}
			if refWrites != 1 || len(api.writes) != 1 {
				t.Fatal("replayed external effects", refWrites, api.writes)
			}
			if done, err := nativeJobHasResult(cfg, x); err != nil || done {
				t.Fatal("refusal acquired a completed native receipt", err)
			}
		})
	}
}
