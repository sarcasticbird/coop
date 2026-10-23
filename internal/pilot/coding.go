package pilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
)

func remoteQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", "'\"'\"'") + "'"
	}
	return strings.Join(out, " ")
}
func (n NativeConfig) command(ctx context.Context, args ...string) *exec.Cmd {
	if n.Host == "" {
		return exec.CommandContext(ctx, "container", args...)
	}
	return exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", n.Host, remoteQuote(append([]string{"/opt/homebrew/bin/container"}, args...)))
}
func (n NativeConfig) rules() []worker.PermissionRule {
	rules := []worker.PermissionRule{{Permission: "*", Pattern: "*", Action: "deny"}}
	files := []string{"marker.txt"}
	if n.Mode == "coding" {
		files = []string{"ready.go", "ready_test.go", "go.mod", "AGENTS.md"}
	}
	for _, file := range files {
		rules = append(rules, worker.PermissionRule{Permission: "read", Pattern: file, Action: "allow"})
	}
	if n.Mode == "coding" {
		rules = append(rules, worker.PermissionRule{Permission: "edit", Pattern: "ready.go", Action: "allow"}, worker.PermissionRule{Permission: "bash", Pattern: "go test ./...", Action: "allow"})
	}
	return rules
}
func nativeCodingProof(raw []byte, userID string) (readProof, error) {
	var messages []struct {
		Info  struct{ Role, ParentID string }
		Parts []struct {
			Type, Tool, CallID string
			State              struct {
				Status, Output string
				Input          struct{ Command string }
				Metadata       struct {
					Exit *int `json:"exit"`
				} `json:"metadata"`
			}
		}
	}
	if e := json.Unmarshal(raw, &messages); e != nil {
		return readProof{}, e
	}
	for _, m := range messages {
		if m.Info.Role != "assistant" || m.Info.ParentID != userID {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == "tool" && p.Tool == "bash" && p.State.Status == "completed" && p.State.Metadata.Exit != nil && *p.State.Metadata.Exit == 0 && p.State.Input.Command == "go test ./..." && strings.Contains(p.State.Output, "ok") {
				return readProof{MessageID: userID, CallID: p.CallID, Output: p.State.Output, ExitCode: p.State.Metadata.Exit}, nil
			}
		}
	}
	return readProof{}, fmt.Errorf("owned successful Go test tool result missing: %w", c.ErrInvalid)
}
func (a *adapters) verifyCodingFiles(ctx context.Context) error {
	n := a.cfg.Native
	for _, name := range []string{"go.mod", "ready_test.go", "AGENTS.md", ".gitignore"} {
		raw, e := n.command(ctx, "exec", n.ContainerID, "cat", filepath.Join(n.Directory, name)).Output()
		if e != nil {
			return e
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != n.ProtectedSHA[name] {
			return fmt.Errorf("protected fixture changed: %w", c.ErrConflict)
		}
	}
	raw, e := n.command(ctx, "exec", n.ContainerID, "git", "-C", n.Directory, "diff", "--name-only").Output()
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(raw)) != "ready.go" {
		return fmt.Errorf("unexpected changed paths: %w", c.ErrConflict)
	}
	raw, e = n.command(ctx, "exec", n.ContainerID, "git", "-C", n.Directory, "ls-files", "--others", "--exclude-standard").Output()
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(raw))) != 0 {
		return fmt.Errorf("unexpected untracked paths: %w", c.ErrConflict)
	}
	raw, e = n.command(ctx, "exec", n.ContainerID, "cat", filepath.Join(n.Directory, "ready.go")).Output()
	if e != nil {
		return e
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) == n.InitialCodeSHA {
		return fmt.Errorf("fixture source unchanged: %w", c.ErrConflict)
	}
	if e = os.WriteFile(filepath.Join(a.cfg.OpenCodeDir, "ready.go"), raw, 0600); e != nil {
		return e
	}
	output, e := n.command(ctx, "exec", "-w", n.Directory, n.ContainerID, "env", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "FLOX_DISABLE_METRICS=true", "flox", "activate", "--dir", "/opt/coop-pilot-tools", "--", "go", "test", "-count=1", "./...").CombinedOutput()
	if e != nil {
		return fmt.Errorf("independent fixture tests failed: %w", e)
	}
	if e = os.WriteFile(filepath.Join(a.cfg.OpenCodeDir, "operator-test-output.txt"), output, 0600); e != nil {
		return e
	}
	patch, e := n.command(ctx, "exec", n.ContainerID, "git", "-C", n.Directory, "diff", "--", "ready.go").Output()
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(a.cfg.OpenCodeDir, "ready.patch"), patch, 0600)
}
