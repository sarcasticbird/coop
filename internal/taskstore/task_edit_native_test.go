package taskstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Opt-in exercises the real installed Kata binary against a fresh private
// profile/project. It never uses the team's daemon, credentials or product work.
func TestKataTaskEditNative(t *testing.T) {
	binary := os.Getenv("COOP_KATA_TASK_EDIT_BINARY")
	if binary == "" {
		t.Skip("requires explicitly selected native Kata binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native test binary must be absolute")
	}
	home, err := os.MkdirTemp("", "kte-")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "KATA_") {
			env = append(env, value)
		}
	}
	env = append(env, "KATA_HOME="+home)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, binary, "--workspace", home, "daemon", "stop")
		stop.Env = env
		_ = stop.Run()
		_ = os.RemoveAll(home)
	})
	// Use an explicit fixture-only token in the same private profile format as
	// the installed gateway. Unix auto-start alone need not create that file.
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[auth]\ntoken = \""+rand.Text()+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "--workspace", home, "--json", "projects", "create", "coop-task-edit-fixture")
	command.Env, command.Dir = env, home
	raw, err := command.Output()
	if err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			t.Log(string(e.Stderr))
		}
		t.Fatal(err)
	}
	var created struct {
		Project struct {
			UID string `json:"uid"`
		} `json:"project"`
	}
	if err = json.Unmarshal(raw, &created); err != nil || created.Project.UID == "" {
		t.Fatalf("project creation: %s %v", raw, err)
	}
	backend, err := New(Config{Provider: "kata", Binary: binary, Home: home, ProjectName: "coop-task-edit-fixture", ProjectUID: created.Project.UID, PolicySHA: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	k := backend.(*kata)
	client, token, err := k.nativeHTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var instance struct {
		UID string `json:"instance_uid"`
	}
	if err = nativeCall(t.Context(), client, token, "GET", "/api/v1/instance", "", nil, &instance); err != nil {
		t.Fatal(err)
	}
	k.cfg.InstanceUID = instance.UID
	for _, bird := range []string{"rook", "robin", "hawk", "wren"} {
		t.Run(bird, func(t *testing.T) {
			actor := c.Principal{ID: bird}
			input := c.TaskCreateInput{Title: bird + " " + rand.Text(), Body: "Disposable rights check for " + bird + "; no production engineering or release.", RequestID: bird + "-create"}
			task, err := k.CreateIssue(t.Context(), actor, input)
			if err != nil {
				t.Fatal("create", err)
			}
			again, err := k.CreateIssue(t.Context(), actor, input)
			if err != nil || task.IssueUID != again.IssueUID {
				t.Fatal("create retry duplicated task", err)
			}
			for _, operation := range []string{"claim", "edit", "comment", "close", "reopen"} {
				issue, err := k.InspectIssue(t.Context(), task.IssueUID)
				if err != nil {
					t.Fatal(err)
				}
				in := c.TaskUpdateInput{Reference: task.IssueUID, Operation: operation, ExpectedRevision: issue.Revision, RequestID: bird + "-" + operation}
				switch operation {
				case "edit":
					title := input.Title + " (verified)"
					in.Title = &title
				case "comment":
					in.ExpectedRevision, in.Message = 0, "Responding under my own bird identity."
				case "close":
					in.Reason, in.Message = "wontfix", "Disposable access check; no production work is being claimed done."
				case "reopen":
					in.Message = "Verify reopening without an owner handoff."
				}
				task, err = k.UpdateIssue(t.Context(), actor, in)
				if err != nil {
					t.Fatal(operation, err)
				}
				if operation == "close" && task.State != "closed" || operation == "reopen" && task.State != "open" {
					t.Fatal("incorrect state", task)
				}
			}
		})
	}
	page, err := k.ListIssues(t.Context(), c.TaskListInput{})
	if err != nil || len(page.Tasks) != 4 || page.Next != "" {
		t.Fatal("native list", page, err)
	}
}
