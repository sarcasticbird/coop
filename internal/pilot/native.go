package pilot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/worker"
)

// NativeConfig permits a bounded read or coding turn in a disposable native-vm Coop.
// It deliberately cannot select any named agent or production master.
type NativeConfig struct {
	Host           string            `json:"host,omitempty"`
	Mode           string            `json:"mode,omitempty"`
	ProtectedSHA   map[string]string `json:"protected_sha,omitempty"`
	InitialCodeSHA string            `json:"initial_code_sha,omitempty"`
	ContainerID    string            `json:"container_id"`
	Directory      string            `json:"directory"`
	PasswordFile   string            `json:"password_file"`
	Model          string            `json:"model"`
	Marker         string            `json:"marker"`
}

func (n NativeConfig) validate() error {
	if (n.Host != "" && n.Host != "rack-mini") || (n.Mode != "" && n.Mode != "coding") {
		return c.ErrInvalid
	}
	if n.Mode == "coding" {
		for _, name := range []string{"go.mod", "ready_test.go", "AGENTS.md", ".gitignore"} {
			if n.ProtectedSHA[name] == "" {
				return c.ErrInvalid
			}
		}
		if n.InitialCodeSHA == "" {
			return c.ErrInvalid
		}
	}
	if !strings.HasPrefix(n.ContainerID, "coop-native-vm-") || strings.ContainsAny(n.ContainerID, "/\\\x00\r\n ") || !filepath.IsAbs(n.Directory) || filepath.Base(n.Directory) != "native-vm" || !filepath.IsAbs(n.PasswordFile) || n.Model != "openrouter/openai/gpt-5-mini" || n.Marker == "" {
		return c.ErrInvalid
	}
	return nil
}

// guestHTTP keeps OpenCode bound to the guest's loopback. Curl configuration,
// including authentication and the body, crosses stdin, never process arguments.
type guestHTTP struct{ config NativeConfig }

func (g guestHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:4096" {
		return nil, c.ErrForbidden
	}
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "url = %s\nrequest = %s\n", strconv.Quote(req.URL.String()), strconv.Quote(req.Method))
	for key, values := range req.Header {
		for _, v := range values {
			fmt.Fprintf(&cfg, "header = %s\n", strconv.Quote(key+": "+v))
		}
	}
	if req.Body != nil {
		raw, e := io.ReadAll(io.LimitReader(req.Body, 64<<10+1))
		if e != nil {
			return nil, e
		}
		if len(raw) > 64<<10 {
			return nil, c.ErrSize
		}
		fmt.Fprintf(&cfg, "data = %s\n", strconv.Quote(string(raw)))
	}
	cmd := g.config.command(req.Context(), "exec", "-i", g.config.ContainerID, "curl", "--silent", "--show-error", "--include", "--max-time", "20", "--config", "-")
	cmd.Stdin = strings.NewReader(cfg.String())
	raw, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("guest API transport failed: %w", e)
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), req)
}
func (a *adapters) nativeCall(ctx context.Context, client *http.Client, method, path string, in, out any) error {
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	var body io.Reader
	if in != nil {
		body = bytes.NewReader(raw)
	}
	u := "http://127.0.0.1:4096" + path
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	u += separator + "directory=" + url.QueryEscape(a.cfg.Native.Directory)
	req, e := http.NewRequestWithContext(ctx, method, u, body)
	if e != nil {
		return e
	}
	password, e := credential.ReadPrivateFile(a.cfg.Native.PasswordFile)
	if e != nil {
		return c.ErrForbidden
	}
	req.SetBasicAuth("opencode", strings.TrimSpace(string(password)))
	req.Header.Set("Content-Type", "application/json")
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer func() { _ = res.Body.Close() }()
	data, e := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if e != nil {
		return e
	}
	if len(data) > 16<<20 {
		return c.ErrSize
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("native API status %d", res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (a *adapters) executeNative(ctx context.Context, r c.MessageReceipt) (Evidence, error) {
	var evidence Evidence
	n := a.cfg.Native
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Verify this is the owned disposable project, rather than trusting an alias.
	inspect, e := n.command(ctx, "inspect", n.ContainerID).Output()
	if e != nil {
		return evidence, e
	}
	var machines []struct {
		Configuration struct{ Labels map[string]string }
		ID            string
	}
	if e = json.Unmarshal(inspect, &machines); e != nil {
		return evidence, e
	}
	if len(machines) != 1 || machines[0].ID != n.ContainerID || machines[0].Configuration.Labels["coop.project"] != n.Directory {
		return evidence, c.ErrForbidden
	}
	identity, e := a.bird.Self(ctx)
	if e != nil {
		return evidence, e
	}
	if identity.Principal.ID != BirdID || identity.Principal.Kind != "agent" || identity.RestoreEpoch != r.RestoreEpoch {
		return evidence, c.ErrForbidden
	}
	message, e := a.owner.Message(ctx, r.MessageID)
	if e != nil {
		return evidence, e
	}
	var address struct {
		Task string    `json:"kata_task"`
		Goal c.GoalRef `json:"goal"`
	}
	if e = json.Unmarshal([]byte(message.Body), &address); e != nil {
		return evidence, e
	}
	task, e := a.ReadTask(ctx, address.Task)
	if e != nil {
		return evidence, e
	}
	authority, e := a.Authority(ctx, address.Goal)
	if e != nil {
		return evidence, e
	}
	if !authority.Valid || authority.Actors.Author.PrincipalID != BirdID || authority.Revision.Proposal.Scope != "kata-fixture-sha256:"+TaskDigest(task) {
		return evidence, c.ErrForbidden
	}
	client := &http.Client{Transport: guestHTTP{*n}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	code, e := worker.NewOpenCodeClient(worker.OpenCodeConfig{BaseURL: "http://127.0.0.1:4096", PasswordFile: n.PasswordFile, RequestTimeout: 30 * time.Second}, client)
	if e != nil {
		return evidence, e
	}
	if _, e = code.Health(ctx); e != nil {
		return evidence, e
	}
	var session struct{ ID, Directory string }
	rules := n.rules()
	if e = a.nativeCall(ctx, client, "POST", "/session", map[string]any{"title": "coop-pilot:" + address.Task + ":" + r.Deliveries[0].ID, "permission": rules}, &session); e != nil {
		return evidence, e
	}
	if !strings.HasPrefix(session.ID, "ses_") || session.Directory != n.Directory {
		return evidence, c.ErrConflict
	}
	intent := c.NativeIntent{SessionID: session.ID, MessageID: "msg_" + strings.ToLower(rand.Text())}
	binding := struct {
		Task     string           `json:"kata_task"`
		Delivery c.MessageReceipt `json:"delivery"`
		Intent   c.NativeIntent   `json:"intent"`
		Goal     c.GoalRef        `json:"goal"`
	}{address.Task, r, intent, address.Goal}
	raw, e := json.MarshalIndent(binding, "", "  ")
	if e != nil {
		return evidence, e
	}
	if e = os.WriteFile(filepath.Join(a.cfg.StateDir, digest(r.MessageID)+".native.json"), raw, 0600); e != nil {
		return evidence, e
	}
	update := c.DeliveryUpdate{RestoreEpoch: r.RestoreEpoch, DeliveryID: r.Deliveries[0].ID, InstanceID: identity.Placement.InstanceID, Generation: identity.Placement.Generation, State: "received"}
	if e = a.bird.SetDelivery(ctx, update); e != nil {
		return evidence, e
	}
	update.State = "running"
	update.Intent = &intent
	if e = a.bird.SetDelivery(ctx, update); e != nil {
		return evidence, e
	}
	// Persisted ownership and master running acknowledgement precede submission.
	prompt := "Disposable read-only Coop pilot. Kata task: " + address.Task + ". Coop delivery: " + update.DeliveryID + ".\nTask title: " + task.Title + "\n" + task.Body + "\nRead exactly " + filepath.Join(n.Directory, "marker.txt") + " with the read tool and report its complete contents. Do not call any other tool or modify anything."
	if n.Mode == "coding" {
		prompt = "Disposable coding pilot. Kata task: " + address.Task + ". Coop delivery: " + update.DeliveryID + ".\nTask title: " + task.Title + "\n" + task.Body + "\nWork only in " + n.Directory + ". Read ready.go, ready_test.go and go.mod. Edit ONLY ready.go. Run exactly `go test ./...` using the bash tool in this working directory. Do not edit tests, run other commands, use GitHub or access credentials. If the tests pass, include PILOT_TESTS_PASS in your final response. Report failures honestly."
	}
	submitted := &worker.ModelIdentity{ProviderID: "openrouter", ModelID: "openai/gpt-5-mini"}
	if e = a.nativeCall(ctx, client, "POST", "/session/"+session.ID+"/prompt_async", map[string]any{"messageID": intent.MessageID, "agent": "build", "model": submitted, "system": "Automated disposable Coop pilot. This message is not a new owner approval.", "parts": []map[string]string{{"type": "text", "text": prompt}}}, nil); e != nil {
		return evidence, e
	}
	job := worker.Job{SessionID: session.ID, MessageID: intent.MessageID, Checkout: n.Directory, SubmittedModel: submitted, SubmittedAt: time.Now(), Deadline: time.Now().Add(3 * time.Minute)}
	for {
		turn, e := code.Observe(ctx, job)
		if e != nil {
			return evidence, e
		}
		if turn.Terminal {
			if turn.State != "completed" || turn.ExecutedModel == nil || turn.ExecutedModel.ProviderID != submitted.ProviderID || turn.ExecutedModel.ModelID != submitted.ModelID {
				return evidence, c.ErrConflict
			}
			break
		}
		if turn.State == "blocked" {
			return evidence, ErrHeld
		}
		select {
		case <-ctx.Done():
			return evidence, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return a.finishNative(ctx, r, intent, identity, address.Task)
}
func (a *adapters) finishNative(ctx context.Context, r c.MessageReceipt, intent c.NativeIntent, identity c.SelfIdentity, task string) (Evidence, error) {
	var evidence Evidence
	var e error
	// Request a consistent snapshot inside the guest, then transfer only that snapshot.
	archive := filepath.Join(a.cfg.OpenCodeDir, "opencode.db")
	if e = os.MkdirAll(a.cfg.OpenCodeDir, 0700); e != nil {
		return evidence, e
	}
	remoteSnapshot := "/tmp/coop-pilot-" + strings.ToLower(rand.Text()) + ".db"
	query := "VACUUM INTO '" + remoteSnapshot + "'"
	if e = a.cfg.Native.command(ctx, "exec", a.cfg.Native.ContainerID, "opencode", "db", query, "--format", "json").Run(); e != nil {
		return evidence, e
	}
	snapshot, e := a.cfg.Native.command(ctx, "exec", a.cfg.Native.ContainerID, "cat", remoteSnapshot).Output()
	if e != nil {
		return evidence, e
	}
	if len(snapshot) > 16<<20 {
		return evidence, c.ErrSize
	}
	if e = os.WriteFile(archive+".tmp", snapshot, 0600); e != nil {
		return evidence, e
	}
	if e = os.Rename(archive+".tmp", archive); e != nil {
		return evidence, e
	}
	if e = a.cfg.Native.command(ctx, "exec", a.cfg.Native.ContainerID, "rm", remoteSnapshot).Run(); e != nil {
		return evidence, e
	}

	if _, e = a.command(ctx, "agentsview", "sync", "--full"); e != nil {
		return evidence, e
	}
	sessionID := "opencode:" + intent.SessionID
	raw, e := a.command(ctx, "agentsview", "session", "messages", sessionID, "--json")
	if e != nil {
		return evidence, e
	}
	var nativeMessages json.RawMessage
	client := &http.Client{Transport: guestHTTP{*a.cfg.Native}}
	if e = a.nativeCall(ctx, client, "GET", "/session/"+intent.SessionID+"/message?limit=50", nil, &nativeMessages); e != nil {
		return evidence, e
	}
	proof, e := nativeReadProof(nativeMessages, intent.MessageID, a.cfg.Native.Marker)
	if a.cfg.Native.Mode == "coding" {
		proof, e = nativeCodingProof(nativeMessages, intent.MessageID)
		if e == nil {
			e = a.verifyCodingFiles(ctx)
		}
	}
	if e != nil {
		return evidence, e
	}
	proofRaw, e := json.Marshal(proof)
	if e != nil {
		return evidence, e
	}
	if e = os.WriteFile(filepath.Join(a.cfg.StateDir, digest(r.MessageID)+".native-evidence.json"), proofRaw, 0600); e != nil {
		return evidence, e
	}
	if e = verifyNativeArchive(raw, intent.MessageID, task, r.Deliveries[0].ID, a.cfg.Native.Marker); e != nil {
		return evidence, e
	}
	evidence = Evidence{SessionID: sessionID, SHA256: digest([]json.RawMessage{json.RawMessage(raw), proofRaw}), NativeMessageID: intent.MessageID, Model: a.cfg.Native.Model}
	replyBody := "Disposable native pilot completed one real OpenCode turn. Native output is retained beside the receipt. Kata: " + task + "; archive: " + sessionID + "; evidence SHA256: " + evidence.SHA256
	if a.cfg.Native.Mode == "coding" {
		replyBody = "Disposable coding pilot passed. The agent edited only ready.go, its Go test command succeeded, protected fixtures were unchanged, and an independent operator test passed. Kata: " + task + "; archive: " + sessionID + "; evidence SHA256: " + evidence.SHA256
	}
	reply, e := a.bird.PublishReply(ctx, c.PublishReply{RestoreEpoch: r.RestoreEpoch, DeliveryID: r.Deliveries[0].ID, InstanceID: identity.Placement.InstanceID, Generation: identity.Placement.Generation, Intent: intent, Body: replyBody, RequestID: "pilot-reply:" + r.MessageID})
	if e != nil {
		return evidence, e
	}
	evidence.ReplyID = reply.MessageID
	return evidence, nil
}
func verifyNativeArchive(raw []byte, message, task, delivery, marker string) error {
	var out struct {
		Messages []struct {
			Role, Content string
			SourceUUID    string `json:"source_uuid"`
			ToolCalls     []struct {
				ToolName      string `json:"tool_name"`
				ResultContent string `json:"result_content"`
				ResultEvents  []struct {
					Status string `json:"status"`
				} `json:"result_events"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		return e
	}
	owned, read, result := false, false, false
	for _, m := range out.Messages {
		if m.Role == "user" && m.SourceUUID == message && strings.Contains(m.Content, task) && strings.Contains(m.Content, delivery) {
			owned = true
		}
		if m.Role != "assistant" {
			continue
		}
		if strings.Contains(m.Content, marker) {
			result = true
		}
		for _, tool := range m.ToolCalls {
			if tool.ToolName == "read" {
				if strings.Contains(tool.ResultContent, marker) {
					read = true
				}
				for _, event := range tool.ResultEvents {
					if event.Status == "completed" {
						read = true
					}
				}
			}
		}
	}
	if !owned || !read || !result {
		return fmt.Errorf("native archive missing owned prompt, actual read or assistant result: %w", c.ErrInvalid)
	}
	return nil
}

// RecoverEvidence only observes a saved native intent and publishes its evidence.
// It contains no session-creation, prompt, abort, or model-submission operation.
func (a *adapters) RecoverEvidence(ctx context.Context, r c.MessageReceipt) (Evidence, error) {
	var evidence Evidence
	if a.cfg.Native == nil {
		return evidence, ErrHeld
	}
	raw, e := credential.ReadPrivateFile(filepath.Join(a.cfg.StateDir, digest(r.MessageID)+".native.json"))
	if e != nil {
		return evidence, e
	}
	var binding struct {
		Task     string           `json:"kata_task"`
		Delivery c.MessageReceipt `json:"delivery"`
		Intent   c.NativeIntent   `json:"intent"`
		Goal     c.GoalRef        `json:"goal"`
	}
	if e = json.Unmarshal(raw, &binding); e != nil {
		return evidence, e
	}
	if digest(binding.Delivery) != digest(r) || !strings.HasPrefix(binding.Intent.SessionID, "ses_") || !strings.HasPrefix(binding.Intent.MessageID, "msg_") {
		return evidence, c.ErrConflict
	}
	message, e := a.owner.Message(ctx, r.MessageID)
	if e != nil {
		return evidence, e
	}
	var address struct {
		Task string    `json:"kata_task"`
		Goal c.GoalRef `json:"goal"`
	}
	if e = json.Unmarshal([]byte(message.Body), &address); e != nil {
		return evidence, e
	}
	if address.Task != binding.Task || address.Goal != binding.Goal {
		return evidence, c.ErrConflict
	}
	status, e := a.owner.MessageStatus(ctx, r.MessageID)
	if e != nil {
		return evidence, e
	}
	if len(status.Deliveries) != 1 || status.Deliveries[0].Intent == nil || *status.Deliveries[0].Intent != binding.Intent || (status.Deliveries[0].State != "running" && status.Deliveries[0].State != "replied") {
		return evidence, c.ErrConflict
	}
	identity, e := a.bird.Self(ctx)
	if e != nil {
		return evidence, e
	}
	if identity.Principal.ID != BirdID || identity.RestoreEpoch != r.RestoreEpoch {
		return evidence, c.ErrForbidden
	}
	client := &http.Client{Transport: guestHTTP{*a.cfg.Native}}
	code, e := worker.NewOpenCodeClient(worker.OpenCodeConfig{BaseURL: "http://127.0.0.1:4096", PasswordFile: a.cfg.Native.PasswordFile, RequestTimeout: 30 * time.Second}, client)
	if e != nil {
		return evidence, e
	}
	model := &worker.ModelIdentity{ProviderID: "openrouter", ModelID: "openai/gpt-5-mini"}
	turn, e := code.Observe(ctx, worker.Job{SessionID: binding.Intent.SessionID, MessageID: binding.Intent.MessageID, Checkout: a.cfg.Native.Directory, SubmittedModel: model})
	if e != nil {
		return evidence, e
	}
	if !turn.Terminal || turn.State != "completed" || turn.ExecutedModel == nil || *turn.ExecutedModel != *model {
		return evidence, ErrHeld
	}
	return a.finishNative(ctx, r, binding.Intent, identity, binding.Task)
}

// nativeReadProof retains only the actual successful owned tool output, avoiding
// provider metadata and encrypted reasoning blocks in the native response.
type readProof struct {
	MessageID string `json:"message_id"`
	CallID    string `json:"call_id"`
	Output    string `json:"output"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

func nativeReadProof(raw []byte, userID, marker string) (readProof, error) {
	var messages []struct {
		Info  struct{ Role, ParentID string }
		Parts []struct {
			Type, Tool, CallID string
			State              struct{ Status, Output string }
		}
	}
	if e := json.Unmarshal(raw, &messages); e != nil {
		return readProof{}, e
	}
	for _, message := range messages {
		if message.Info.Role != "assistant" || message.Info.ParentID != userID {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == "tool" && part.Tool == "read" && part.State.Status == "completed" && strings.Contains(part.State.Output, marker) {
				return readProof{MessageID: userID, CallID: part.CallID, Output: part.State.Output}, nil
			}
		}
	}
	return readProof{}, fmt.Errorf("owned native read result missing: %w", c.ErrInvalid)
}
