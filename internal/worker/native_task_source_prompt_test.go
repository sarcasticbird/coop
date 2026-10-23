package worker

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

func TestNativePromptUsesTaskSourceLifecycle(t *testing.T) {
	const legacy = "Task creation and rebinding can select a configured Project with project_owner and project_number; one configured Project is selected automatically and confirmed Ready."
	for _, kata := range []bool{false, true} {
		name := "github"
		if kata {
			name = "kata"
		}
		t.Run(name, func(t *testing.T) {
			cfg, catalog, task := nativeWorkFixture(t)
			if kata {
				cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
				ref, err := c.KataTaskReference(providerProject, providerIssue, task.Task.Repo)
				if err != nil {
					t.Fatal(err)
				}
				task.Task.URL, task.Binding.TaskURL = ref, ref
				catalog.Tasks[0] = task
			}
			candidates := NativeCandidates(cfg, catalog, nil, time.Now())
			if len(candidates) != 1 {
				t.Fatal("missing native candidate")
			}
			v := candidates[0]
			prompt, err := nativePrompt(cfg, NativeExecution{Job: v.Job, Binding: &v.Task.Binding}, v)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"coop-worker native action --config", "Every action JSON requires operation and a stable request_id.", "Never self-review.", task.Task.URL} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("source prompt omitted guard or identity %q", required)
				}
			}
			if !kata {
				if strings.Count(prompt, legacy) != 1 || strings.Contains(prompt, "Kata is the authoritative task source") {
					t.Fatal("legacy task lifecycle instructions changed")
				}
				return
			}
			for _, required := range []string{"Kata is the authoritative task source", "kata://<lowercase-project-uid>/<uppercase-issue-uid>/<owner>/<repo>", "no GitHub issue or Project writes", "no Project Ready status"} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("Kata prompt omitted provider lifecycle %q", required)
				}
			}
			if strings.Contains(prompt, legacy) || strings.Contains(prompt, "project_owner and project_number") {
				t.Fatal("Kata prompt requested GitHub Project placement")
			}
		})
	}
}
