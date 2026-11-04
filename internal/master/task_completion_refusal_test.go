package master

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

type failingCompletionBackend struct{ *testTasks }

func (b failingCompletionBackend) Complete(_ctx context.Context, _ c.TaskCompleteInput, _ c.Principal) (c.ProviderTask, error) {
	b.closes++
	b.task.State, b.task.ClosedReason = "closed", "done"
	return c.ProviderTask{}, c.ErrForbidden
}

func TestTaskCompletionNotStartedProofIsBeforeProviderOnly(t *testing.T) {
	for _, stage := range []string{"before-authority", "after-read", "provider-entered"} {
		t.Run(stage, func(t *testing.T) {
			s, owner, author, create, backend := taskFixture(t)
			task, err := s.TaskCreate(t.Context(), owner, create)
			if err != nil {
				t.Fatal(err)
			}
			rook := enrollTaskRook(t, s, owner, create.Goal)
			placement := c.Placement{AgentID: rook.ID, HostID: "host", InstanceID: "rook-instance", Generation: 1}
			if err := s.Store.Configure(t.Context(), nil, []c.Placement{placement}); err != nil {
				t.Fatal(err)
			}
			input := c.TaskCompleteInput{Reference: task.Reference, Goal: create.Goal, AssignmentSHA: digest(task.Binding), PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "same-completion"}
			if _, err := s.RegisterArtifact(t.Context(), author, c.ArtifactLink{Goal: create.Goal, Kind: "pull_request", URL: input.PullRequestURL, TaskURL: input.Reference, AssignmentSHA: input.AssignmentSHA, RequestID: "pr"}); err != nil {
				t.Fatal(err)
			}
			revoke := func() {
				p := rook
				p.Capabilities = []string{"read", "send"}
				if err := s.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
					t.Fatal(err)
				}
			}
			switch stage {
			case "before-authority":
				revoke()
			case "after-read":
				backend.onRead = func() { backend.onRead = nil; revoke() }
			case "provider-entered":
				s.Tasks = failingCompletionBackend{backend}
			}
			token := "completion-test-token"
			policy := &Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []Token{{PrincipalID: rook.ID, InstanceID: placement.InstanceID, Generation: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(token)))}}}
			server := httptest.NewServer(NewHandler(s, policy))
			defer server.Close()
			client, err := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: s.Store.id, Token: token, AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.TaskComplete(t.Context(), input)
			if !errors.Is(err, c.ErrForbidden) {
				t.Fatal(err)
			}
			var refused *c.TaskCompletionNotStartedError
			if stage == "provider-entered" {
				if errors.As(err, &refused) || backend.closes != 1 {
					t.Fatal("provider error falsely certified no effect")
				}
				return
			}
			if !errors.As(err, &refused) || backend.closes != 0 || refused.Refusal.InputSHA256 != c.TaskCompletionInputSHA(input) || refused.Refusal.RequestID != input.RequestID || refused.Refusal.MasterID != s.Store.id || refused.Refusal.RestoreEpoch != s.Store.epoch || refused.Refusal.Effect != "not_started" {
				t.Fatal("missing exact pre-provider refusal", err)
			}
			recorder := httptest.NewRecorder()
			writeError(recorder, err)
			var wire struct {
				Error   string                  `json:"error"`
				Refusal c.TaskCompletionRefusal `json:"task_completion_refusal"`
			}
			if recorder.Code != http.StatusForbidden || json.Unmarshal(recorder.Body.Bytes(), &wire) != nil || wire.Refusal != refused.Refusal || wire.Error != c.ErrForbidden.Error() {
				t.Fatal("wire proof drift")
			}
		})
	}
}
