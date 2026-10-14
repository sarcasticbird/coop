package agent

import (
	"context"
	"io"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
)

type commandRunner struct{ args []string }

func (r *commandRunner) Exec(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error {
	return nil
}
func (r *commandRunner) Attach(_ context.Context, _, _ string, args []string) error {
	r.args = args
	return nil
}
func TestOneOffCommandsAndShellsUseAppCredentialWrapper(t *testing.T) {
	r := &commandRunner{}
	s := Session{Runner: r}
	s.Spec.Config = config.Config{IncludeCredentials: []string{"app"}, Credentials: map[string]config.Credential{"app": {Expose: []config.CredentialInjection{{Type: "file", PathEnv: "COOP_GITHUB_APP_BUNDLE_FILE"}}}}}
	if err := s.Command(context.Background(), []string{"gh", "pr", "list"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/libexec/coop-env", "/usr/local/libexec/coop-github", "env", "--", "gh", "pr", "list"}
	if len(r.args) != len(want) {
		t.Fatal("one-off command bypassed App wrapper")
	}
	for i := range want {
		if r.args[i] != want[i] {
			t.Fatal("incorrect App command wrapper")
		}
	}
	s.Spec.Config.IncludeCredentials = nil
	if err := s.Command(context.Background(), []string{"zsh", "-l"}); err != nil {
		t.Fatal(err)
	}
	if len(r.args) != 3 || r.args[1] != "zsh" {
		t.Fatal("ordinary command behavior changed")
	}
}
