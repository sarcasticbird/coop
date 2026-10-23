package pilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// Config is deliberately restricted to the disposable project and master.
// It contains token file references, never tokens. This spike cannot target live birds.
type Config struct {
	Native           *NativeConfig `json:"native,omitempty"`
	Fixture          bool          `json:"fixture"`
	MasterURL        string        `json:"master_url"`
	OwnerTokenFile   string        `json:"owner_token_file"`
	BirdTokenFile    string        `json:"bird_token_file"`
	ThreadID         string        `json:"thread_id"`
	SessionID        string        `json:"session_id"`
	StateDir         string        `json:"state_dir"`
	KataHome         string        `json:"kata_home"`
	KataURL          string        `json:"kata_url"`
	KataProject      string        `json:"kata_project"`
	KataBinary       string        `json:"kata_binary"`
	AgentsViewBinary string        `json:"agentsview_binary"`
	AgentsViewData   string        `json:"agentsview_data"`
	OpenCodeDir      string        `json:"opencode_dir"`
}

func loopback(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback() && u.Port() != ""
}
func (cfg Config) validate() error {
	if !cfg.Fixture || cfg.KataProject != "coop-kenn-spike" || !loopback(cfg.MasterURL) || !loopback(cfg.KataURL) || cfg.ThreadID == "" || (cfg.Native == nil && !strings.HasPrefix(cfg.SessionID, "opencode:")) {
		return c.ErrInvalid
	}
	if cfg.Native != nil {
		if e := cfg.Native.validate(); e != nil {
			return e
		}
	}
	for _, path := range []string{cfg.OwnerTokenFile, cfg.BirdTokenFile, cfg.StateDir, cfg.KataHome, cfg.KataBinary, cfg.AgentsViewBinary, cfg.AgentsViewData, cfg.OpenCodeDir} {
		if !filepath.IsAbs(path) {
			return c.ErrInvalid
		}
	}
	return nil
}
func Load(path string) (*Coordinator, error) {
	raw, e := credential.ReadPrivateFile(path)
	if e != nil {
		return nil, c.ErrForbidden
	}
	if len(raw) > c.MaxJSON {
		return nil, c.ErrSize
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&cfg); e != nil {
		return nil, c.ErrInvalid
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return nil, c.ErrInvalid
	}
	if e = cfg.validate(); e != nil {
		return nil, e
	}
	client := func(path string) (*masterclient.Client, error) {
		token, e := credential.ReadPrivateFile(path)
		if e != nil {
			return nil, c.ErrForbidden
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		return masterclient.New(masterclient.ClientConfig{URL: cfg.MasterURL, MasterID: MasterID, Token: strings.TrimSpace(string(token)), AllowHTTP: true, HTTPClient: &http.Client{Transport: transport}})
	}
	owner, e := client(cfg.OwnerTokenFile)
	if e != nil {
		return nil, e
	}
	bird, e := client(cfg.BirdTokenFile)
	if e != nil {
		return nil, e
	}
	a := &adapters{cfg: cfg, owner: owner, bird: bird}
	return &Coordinator{StateDir: cfg.StateDir, ConfigSHA: digest(cfg), BirdID: BirdID, Backend: a, Ledger: a, Bird: a}, nil
}

type adapters struct {
	cfg         Config
	owner, bird *masterclient.Client
}

func (a *adapters) Authority(ctx context.Context, ref c.GoalRef) (c.GoalAuthority, error) {
	return a.owner.ResolveGoal(ctx, ref)
}
func (a *adapters) Send(ctx context.Context, goal c.GoalAuthority, r Request) (c.MessageReceipt, error) {
	body, _ := json.Marshal(struct {
		Fixture bool      `json:"fixture"`
		Task    string    `json:"kata_task"`
		Goal    c.GoalRef `json:"goal"`
		Session string    `json:"archive_session"`
	}{true, r.Task, r.Goal, a.cfg.SessionID})
	return a.owner.Send(ctx, c.SendMessage{ThreadID: a.cfg.ThreadID, Recipients: []string{BirdID}, Body: string(body), RequestID: "pilot:" + r.RequestID})
}

// Child processes receive explicit isolated data paths; no shell or prompt is executed.
func (a *adapters) command(ctx context.Context, which string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	binary := a.cfg.KataBinary
	env := []string{}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "KATA_") || strings.HasPrefix(key, "AGENTSVIEW_") || key == "OPENCODE_DIR" {
			continue
		}
		env = append(env, item)
	}
	if which == "kata" {
		env = append(env, "KATA_HOME="+a.cfg.KataHome, "KATA_SERVER="+a.cfg.KataURL, "KATA_AUTHOR="+BirdID)
		args = append([]string{"--project", a.cfg.KataProject}, args...)
	} else {
		binary = a.cfg.AgentsViewBinary
		var err error
		env, err = archiveEnvironment(a.cfg)
		if err != nil {
			return nil, err
		}

	}
	if which == "agentsview" {
		probe := exec.CommandContext(ctx, binary, "--version")
		probe.Env = env
		version, e := probe.Output()
		if e != nil || !strings.HasPrefix(string(version), "agentsview v0.46.1 ") {
			return nil, c.ErrUnsupported
		}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Dir = a.cfg.KataHome
	out, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("%s fixture command failed: %w", which, e)
	}
	if len(out) > 16<<20 {
		return nil, c.ErrSize
	}
	return out, nil
}
func (a *adapters) ReadTask(ctx context.Context, ref string) (Task, error) {
	if !strings.HasPrefix(ref, a.cfg.KataProject+"#") || strings.ContainsAny(ref, "\r\n\x00") {
		return Task{}, c.ErrInvalid
	}
	b, e := a.command(ctx, "kata", "show", ref, "--json")
	if e != nil {
		return Task{}, e
	}
	var out struct {
		Issue Task `json:"issue"`
	}
	e = json.Unmarshal(b, &out)
	if e == nil && out.Issue.UID == "" {
		e = c.ErrInvalid
	}
	return out.Issue, e
}
func (a *adapters) Claim(ctx context.Context, ref string) error {
	_, e := a.command(ctx, "kata", "claim", ref, "--if-unowned", "--json")
	return e
}
func (a *adapters) Execute(ctx context.Context, r c.MessageReceipt) (Evidence, error) {
	if a.cfg.Native != nil {
		return a.executeNative(ctx, r)
	}
	var evidence Evidence
	identity, e := a.bird.Self(ctx)
	if e != nil {
		return evidence, e
	}
	if identity.Principal.ID != BirdID || identity.Principal.Kind != "agent" || identity.RestoreEpoch != r.RestoreEpoch {
		return evidence, c.ErrForbidden
	}
	d := r.Deliveries[0]
	intent := c.NativeIntent{SessionID: "fixture-audit:" + a.cfg.SessionID, MessageID: r.MessageID}
	update := c.DeliveryUpdate{RestoreEpoch: r.RestoreEpoch, DeliveryID: d.ID, InstanceID: identity.Placement.InstanceID, Generation: identity.Placement.Generation, State: "received"}
	if e = a.bird.SetDelivery(ctx, update); e != nil {
		return evidence, e
	}
	update.State = "running"
	update.Intent = &intent
	if e = a.bird.SetDelivery(ctx, update); e != nil {
		return evidence, e
	}
	b, e := a.command(ctx, "agentsview", "session", "messages", a.cfg.SessionID, "--json")
	if e != nil {
		return evidence, e
	}
	if e = verifyPublicFailure(b); e != nil {
		return evidence, e
	}
	evidence = Evidence{SessionID: a.cfg.SessionID, SHA256: digest(json.RawMessage(b)), ExitCode: 7}
	reply, e := a.bird.PublishReply(ctx, c.PublishReply{RestoreEpoch: r.RestoreEpoch, DeliveryID: d.ID, InstanceID: identity.Placement.InstanceID, Generation: identity.Placement.Generation, Intent: intent, Body: "FIXTURE AUDIT ONLY: public OpenCode history records a failed shell tool with exit code 7. No model or shell command was executed by this audit. Archive session: " + evidence.SessionID + "; evidence SHA256: " + evidence.SHA256, RequestID: "pilot-reply:" + r.MessageID})
	if e != nil {
		return evidence, e
	}
	evidence.ReplyID = reply.MessageID
	return evidence, nil
}
func verifyPublicFailure(raw []byte) error {
	var out struct {
		Messages []struct {
			ToolCalls []struct {
				ResultContent string `json:"result_content"`
				ResultEvents  []struct {
					Status  string `json:"status"`
					Content string `json:"content"`
				} `json:"result_events"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		return e
	}
	for _, m := range out.Messages {
		for _, t := range m.ToolCalls {
			for _, event := range t.ResultEvents {
				if event.Status == "errored" && strings.Contains(event.Content, "Command exited with code 7.") && strings.Contains(t.ResultContent, "Command exited with code 7.") {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("archive has no matching recorded fixture failure: %w", c.ErrInvalid)
}
func (a *adapters) Record(ctx context.Context, r Request, e Evidence) error {
	if _, err := a.command(ctx, "agentsview", "session", "label", e.SessionID, "task="+r.Task, "bird="+BirdID, "fixture=true", "--json"); err != nil {
		return err
	}
	if _, err := a.command(ctx, "kata", "meta", "set", r.Task, "spike.agentsview_session", e.SessionID, "--json"); err != nil {
		return err
	}
	if _, err := a.command(ctx, "kata", "meta", "set", r.Task, "spike.coop_reply", e.ReplyID, "--json"); err != nil {
		return err
	}
	if a.cfg.Native != nil && a.cfg.Native.Mode == "coding" {
		_, err := a.command(ctx, "kata", "close", r.Task, "--done", "--message", "Implemented and independently tested the disposable ready-work selector. Only ready.go changed; protected tests and fixtures were unchanged. Native session, tool output and reviewed source artifacts are linked; no production repository was modified.", "--test", "go test ./... (agent and independent operator)", "--reviewed", filepath.Join(a.cfg.OpenCodeDir, "ready.go"), "--reviewed", filepath.Join(a.cfg.OpenCodeDir, "ready.patch"), "--json")
		return err
	}
	message := "Verified the public OpenCode archive failure and linked its evidence through an approved disposable Coop delivery; no model execution or production change."
	if a.cfg.Native != nil {
		message = "Verified one real read-only OpenCode turn in a disposable Coop, archived its actual tool result and assistant response in AgentsView, and linked its Kata task and Coop delivery; no code change."
	}
	_, err := a.command(ctx, "kata", "close", r.Task, "--audit-no-change", "--message", message, "--evidence", "no-change-audit:agentsview:"+e.SessionID+" sha256:"+e.SHA256, "--reviewed", filepath.Join(a.cfg.AgentsViewData, "sessions.db"), "--json")
	return err
}
