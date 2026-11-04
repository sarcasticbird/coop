package masterclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestTaskCompleteOnlyAcceptsExactNotStartedResponse(t *testing.T) {
	for _, fault := range []string{"valid", "bare", "truncated", "master", "epoch", "epoch-header", "request", "payload", "effect", "version", "status", "unknown-field", "other-route", "other-operation"} {
		t.Run(fault, func(t *testing.T) {
			input := c.TaskCompleteInput{Goal: c.GoalRef{Source: "master", MasterID: "master", GoalID: "goal", RevisionSHA256: "revision"}, Reference: "task", AssignmentSHA: "assignment", PullRequestURL: "pr", RequestID: "original"}
			proof := c.TaskCompletionRefusal{Version: 1, MasterID: "master", RestoreEpoch: 1, RequestID: input.RequestID, InputSHA256: c.TaskCompletionInputSHA(input), Effect: "not_started"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Coop-Master-ID", "master")
				w.Header().Set("X-Coop-Restore-Epoch", "1")
				status := 403
				switch fault {
				case "master":
					proof.MasterID = "foreign"
				case "epoch":
					proof.RestoreEpoch++
				case "epoch-header":
					w.Header().Del("X-Coop-Restore-Epoch")
				case "request":
					proof.RequestID = "foreign"
				case "payload":
					proof.InputSHA256 = "foreign"
				case "effect":
					proof.Effect = "unknown"
				case "version":
					proof.Version++
				case "status":
					status = 503
				}
				body := map[string]any{"error": c.ErrForbidden.Error()}
				if fault != "bare" {
					body["task_completion_refusal"] = proof
				}
				if fault == "unknown-field" {
					body["untrusted"] = true
				}
				w.WriteHeader(status)
				if fault == "truncated" {
					_, _ = w.Write([]byte(`{"error":"forbidden","task_completion_refusal":`))
					return
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			client, err := New(ClientConfig{URL: srv.URL, MasterID: "master", Token: "fixture-token-long", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/tasks/complete"
			var operation any = input
			if fault == "other-route" {
				path = "/api/v1/tasks/bind"
			}
			if fault == "other-operation" {
				operation = c.TaskReadInput{Reference: input.Reference}
			}
			err = client.call(t.Context(), "POST", path, operation, nil)
			var rejected *c.TaskCompletionNotStartedError
			if errors.As(err, &rejected) != (fault == "valid") {
				t.Fatal("invalid refusal accepted or valid proof dropped", err)
			}
			if fault == "valid" && !errors.Is(err, c.ErrForbidden) {
				t.Fatal("lost refusal cause")
			}
		})
	}
}
