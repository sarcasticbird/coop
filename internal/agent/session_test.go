package agent

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type failingBackend struct{ creates int }

func (b *failingBackend) Inspect(context.Context, workspace.Spec) (workspace.Info, error) {
	return workspace.Info{}, nil
}
func (b *failingBackend) Create(context.Context, workspace.Spec) error { b.creates++; return nil }
func (b *failingBackend) Start(context.Context, workspace.Spec) error  { return nil }
func (b *failingBackend) Stop(context.Context, workspace.Spec) error   { return nil }
func (b *failingBackend) Remove(context.Context, workspace.Spec) error { return nil }
func (b *failingBackend) Apply(context.Context, workspace.Spec) error  { return nil }
func (b *failingBackend) Exec(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return nil
}
func (b *failingBackend) Attach(context.Context, string, string, []string) error { return nil }

func TestAcquisitionFailureLeavesMachineUntouched(t *testing.T) {
	b := &failingBackend{}
	s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Config: config.Config{
		IncludeCredentials: []string{"token"}, Credentials: map[string]config.Credential{"token": {Source: config.CredentialSource{Type: "file", Path: filepath.Join(t.TempDir(), "absent-token")}, Expose: []config.CredentialInjection{{Type: "environment", Name: "GH_TOKEN"}}}},
	}}, Controller: workspace.Controller{Backend: b}, Runner: b, CredentialManager: credential.NewManager("")}
	if err := s.Up(context.Background(), nil); err == nil {
		t.Fatal("acquisition failure ignored")
	}
	if b.creates != 0 {
		t.Fatal("machine created before credentials were acquired")
	}
}
