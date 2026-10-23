// Package taskstore is the master's private task-provider adapter. Workers never
// receive its daemon credentials or choose its endpoint.
package taskstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type Config struct {
	Provider    string `toml:"provider"`
	Binary      string `toml:"binary"`
	Home        string `toml:"home"`
	ProjectName string `toml:"project_name"`
	ProjectUID  string `toml:"project_uid"`
	PolicySHA   string `toml:"policy_sha256"`
}
type Backend interface {
	Capabilities() c.TaskCapabilities
	Read(context.Context, string) (c.ProviderTask, error)
	Create(context.Context, c.TaskCreateInput, c.Principal) (c.ProviderTask, error)
	Bind(context.Context, c.TaskBindInput, c.Principal) (c.ProviderTask, error)
	Complete(context.Context, c.TaskCompleteInput, c.Principal) (c.ProviderTask, error)
}
type kata struct{ cfg Config }

func New(cfg Config) (Backend, error) {
	if cfg.Provider != "kata" || !filepath.IsAbs(cfg.Binary) || !filepath.IsAbs(cfg.Home) || strings.TrimSpace(cfg.ProjectName) == "" || strings.ContainsAny(cfg.ProjectName, "\x00\r\n") {
		return nil, c.ErrInvalid
	}
	if _, err := c.KataTaskReference(cfg.ProjectUID, cfg.ProjectUID, "validation/repository"); err != nil {
		return nil, c.ErrInvalid
	}
	sha, err := hex.DecodeString(cfg.PolicySHA)
	if err != nil || len(sha) != 32 || cfg.PolicySHA != strings.ToLower(cfg.PolicySHA) {
		return nil, c.ErrInvalid
	}
	cfg.Home = filepath.Clean(cfg.Home)
	info, err := os.Lstat(cfg.Home)
	if err != nil {
		return nil, fmt.Errorf("stat Kata profile: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, c.ErrForbidden
	}
	return &kata{cfg: cfg}, nil
}
func (k *kata) Capabilities() c.TaskCapabilities {
	return c.TaskCapabilities{Provider: "kata", ProjectUID: k.cfg.ProjectUID, ProjectName: k.cfg.ProjectName, PolicySHA: k.cfg.PolicySHA}
}
func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func key(kind string, goal c.GoalRef, request string) string {
	return "coop-" + hash(struct {
		Kind    string
		Goal    c.GoalRef
		Request string
	}{kind, goal, request})
}

// limitBuffer prevents an unhealthy provider from exhausting the master process.
type limitBuffer struct{ bytes.Buffer }

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > c.MaxJSON {
		return 0, c.ErrSize
	}
	return b.Buffer.Write(p)
}
func (k *kata) run(ctx context.Context, actor string, args ...string) ([]byte, error) {
	base := []string{"--workspace", k.cfg.Home, "--project", k.cfg.ProjectName, "--json"}
	if actor != "" {
		base = append(base, "--as", actor)
	}
	// Resolve without auto-start before every operation. A private profile that
	// accidentally selects a remote daemon must fail closed.
	location, err := k.execute(ctx, base, "daemon", "locate")
	if err != nil {
		return nil, err
	}
	var endpoint struct {
		Kind    string `json:"kind"`
		Source  string `json:"source"`
		Network string `json:"network"`
		Address string `json:"address"`
	}
	if json.Unmarshal(location, &endpoint) != nil || endpoint.Kind != "local" || endpoint.Source != "local_default" || endpoint.Network != "unix" || !strings.HasPrefix(endpoint.Address, "unix:///") {
		return nil, c.ErrForbidden
	}
	return k.execute(ctx, base, args...)
}
func (k *kata) execute(ctx context.Context, base []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, k.cfg.Binary, append(slices.Clone(base), args...)...)
	cmd.Dir = k.cfg.Home
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "KATA_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "KATA_HOME="+k.cfg.Home)
	var out, stderr limitBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			switch e.ExitCode() {
			case 2, 3:
				return nil, c.ErrInvalid
			case 4:
				return nil, c.ErrNotFound
			case 5:
				return nil, c.ErrConflict
			}
		}
		return nil, fmt.Errorf("kata command %s failed: %w", args[0], errors.Join(c.ErrUnavailable, err))
	}
	return out.Bytes(), nil
}

type rawIssue struct {
	UID          string                     `json:"uid"`
	ProjectUID   string                     `json:"project_uid"`
	Title        string                     `json:"title"`
	Body         string                     `json:"body"`
	Status       string                     `json:"status"`
	ClosedReason string                     `json:"closed_reason"`
	Owner        string                     `json:"owner"`
	Revision     int64                      `json:"revision"`
	Metadata     map[string]json.RawMessage `json:"metadata"`
}
type creation struct {
	Input      c.TaskCreateInput `json:"input"`
	Goal       c.GoalRef         `json:"goal"`
	Repository string            `json:"repository"`
}
type native struct {
	Binding c.NativeTaskBinding `json:"binding"`
	Actor   c.Principal         `json:"actor"`
}

func metadata(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return c.ErrNotFound
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	if json.Unmarshal(raw, out) != nil {
		return c.ErrInvalid
	}
	return nil
}
func (k *kata) decode(raw []byte, expected string) (c.ProviderTask, error) {
	var out c.ProviderTask
	var envelope struct {
		Issue rawIssue `json:"issue"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return out, c.ErrInvalid
	}
	issue := envelope.Issue
	if issue.ProjectUID != k.cfg.ProjectUID || issue.Revision < 1 {
		return out, c.ErrConflict
	}
	var cr creation
	var n native
	repo := ""
	goal := c.GoalRef{}
	var binding *c.NativeTaskBinding
	if raw := issue.Metadata["coop.creation"]; len(raw) > 0 {
		if err := metadata(raw, &cr); err != nil {
			return out, err
		}
		repo = cr.Repository
		goal = cr.Goal
	}
	if raw := issue.Metadata["coop.native"]; len(raw) > 0 {
		if err := metadata(raw, &n); err != nil {
			return out, err
		}
		p, uid, r, err := c.ParseKataTaskReference(n.Binding.TaskURL)
		if err != nil || p != issue.ProjectUID || uid != issue.UID || (repo != "" && repo != r) || (goal != (c.GoalRef{}) && goal != n.Binding.Goal) {
			return out, c.ErrConflict
		}
		repo = r
		goal = n.Binding.Goal
		binding = &n.Binding
	}
	ref, err := c.KataTaskReference(issue.ProjectUID, issue.UID, repo)
	if err != nil {
		return out, err
	}
	if expected != "" && ref != expected {
		return out, c.ErrConflict
	}
	out = c.ProviderTask{Reference: ref, ProjectUID: issue.ProjectUID, IssueUID: issue.UID, Repository: repo, Title: issue.Title, Body: issue.Body, State: issue.Status, ClosedReason: issue.ClosedReason, Owner: issue.Owner, Revision: issue.Revision, Goal: goal, Binding: binding, BindingActor: n.Actor}
	return out, nil
}
func (k *kata) Read(ctx context.Context, ref string) (c.ProviderTask, error) {
	p, uid, _, err := c.ParseKataTaskReference(ref)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if p != k.cfg.ProjectUID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	raw, err := k.run(ctx, "", "show", uid)
	if err != nil {
		return c.ProviderTask{}, err
	}
	return k.decode(raw, ref)
}
func validRequest(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 256 && !strings.ContainsAny(id, "\x00\r\n")
}
func (k *kata) Create(ctx context.Context, in c.TaskCreateInput, actor c.Principal) (c.ProviderTask, error) {
	if len(in.DependsOn) == 0 {
		in.DependsOn = nil
	}
	if !validRequest(in.RequestID) || actor.ID == "" || strings.TrimSpace(in.Title) == "" || len(in.Title) > 1024 || len(in.Body) > c.MaxText || in.Goal.MasterID == "" || in.Goal.GoalID == "" {
		return c.ProviderTask{}, c.ErrInvalid
	}
	if _, err := c.KataTaskReference(k.cfg.ProjectUID, k.cfg.ProjectUID, in.Repository); err != nil {
		return c.ProviderTask{}, err
	}
	projectRaw, err := k.run(ctx, actor.ID, "projects", "show", k.cfg.ProjectName)
	if err != nil {
		return c.ProviderTask{}, err
	}
	var project struct {
		Project struct {
			UID string `json:"uid"`
		} `json:"project"`
	}
	if json.Unmarshal(projectRaw, &project) != nil || project.Project.UID != k.cfg.ProjectUID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	cr := creation{Input: in, Goal: in.Goal, Repository: in.Repository}
	encoded, _ := json.Marshal(cr)
	raw, err := k.run(ctx, actor.ID, "create", in.Title, "--body", in.Body, "--idempotency-key", key("create", in.Goal, in.RequestID), "--meta", "coop.creation="+string(encoded))
	if err != nil {
		return c.ProviderTask{}, err
	}
	task, err := k.decode(raw, "")
	if err != nil {
		return task, err
	}
	var envelope struct {
		Issue rawIssue `json:"issue"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return task, err
	}
	var actual creation
	if metadata(envelope.Issue.Metadata["coop.creation"], &actual) != nil || !reflect.DeepEqual(cr, actual) || task.Title != in.Title || task.Body != in.Body || task.Repository != in.Repository || task.Goal != in.Goal {
		return c.ProviderTask{}, c.ErrConflict
	}
	return task, nil
}
func (k *kata) Bind(ctx context.Context, in c.TaskBindInput, actor c.Principal) (c.ProviderTask, error) {
	if len(in.Binding.DependsOn) == 0 {
		in.Binding.DependsOn = nil
	}
	if !validRequest(in.RequestID) || actor.ID == "" {
		return c.ProviderTask{}, c.ErrInvalid
	}
	task, err := k.Read(ctx, in.Binding.TaskURL)
	if err != nil {
		return task, err
	}
	if task.Goal != in.Binding.Goal || in.Binding.PolicySHA != k.cfg.PolicySHA {
		return task, c.ErrConflict
	}
	if task.Binding != nil && reflect.DeepEqual(*task.Binding, in.Binding) {
		return task, nil
	}
	if task.State != "open" || task.Revision != in.ExpectedRevision {
		return task, c.ErrConflict
	}
	current := ""
	if task.Binding != nil {
		current = hash(*task.Binding)
	}
	if current != in.ExpectedAssignmentSHA {
		return task, c.ErrConflict
	}
	raw, _ := json.Marshal(native{Binding: in.Binding, Actor: actor})
	_, err = k.run(ctx, actor.ID, "meta", "set", task.IssueUID, "coop.native", string(raw), "--json-value", "--if-match", strconv.FormatInt(in.ExpectedRevision, 10))
	// A lost reply is reconciled by reading the atomic metadata object. Never
	// advance the revision or change payload to make a retry succeed.
	result, readErr := k.Read(ctx, task.Reference)
	if readErr == nil && result.Binding != nil && reflect.DeepEqual(*result.Binding, in.Binding) {
		return result, nil
	}
	if err != nil {
		return task, err
	}
	if readErr != nil {
		return task, readErr
	}
	return task, c.ErrConflict
}
func (k *kata) Complete(ctx context.Context, in c.TaskCompleteInput, actor c.Principal) (c.ProviderTask, error) {
	if !validRequest(in.RequestID) || actor.ID == "" || in.PullRequestURL == "" {
		return c.ProviderTask{}, c.ErrInvalid
	}
	task, err := k.Read(ctx, in.Reference)
	if err != nil {
		return task, err
	}
	if task.Goal != in.Goal || task.Binding == nil || hash(*task.Binding) != in.AssignmentSHA {
		return task, c.ErrConflict
	}
	if task.State == "closed" {
		if task.ClosedReason == "done" || task.ClosedReason == "completed" {
			return task, nil
		}
		return task, c.ErrConflict
	}
	if task.State != "open" {
		return task, c.ErrConflict
	}
	_, err = k.run(ctx, actor.ID, "close", task.IssueUID, "--done", "--message", "Coop native engineering completed this exact approved assignment; registered pull request carries merge, test and independent review evidence.", "--pr", in.PullRequestURL, "--if-match", strconv.FormatInt(task.Revision, 10), "--idempotency-key", key("complete", in.Goal, in.RequestID))
	result, readErr := k.Read(ctx, in.Reference)
	if readErr == nil && result.Binding != nil && hash(*result.Binding) == in.AssignmentSHA && result.State == "closed" && (result.ClosedReason == "done" || result.ClosedReason == "completed") {
		return result, nil
	}
	if err != nil {
		return task, err
	}
	if readErr != nil {
		return task, readErr
	}
	return task, c.ErrConflict
}
