package masteracceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/worker"
)

type nativeCode struct {
	calls   int
	running bool
}

func (*nativeCode) Health(context.Context) (worker.ServerHealth, error) {
	return worker.ServerHealth{Healthy: true}, nil
}
func (*nativeCode) EnsureSession(_ context.Context, _ worker.Config, j worker.Job) (worker.Job, error) {
	j.SessionID = "ses_acceptance"
	return j, nil
}
func (n *nativeCode) Submit(_ context.Context, _ worker.Config, j worker.Job, _ string) (worker.Turn, error) {
	n.calls++
	n.running = true
	return worker.Turn{SessionID: j.SessionID, MessageID: j.MessageID, Accepted: true}, nil
}
func (n *nativeCode) Observe(_ context.Context, j worker.Job) (worker.Turn, error) {
	return worker.Turn{SessionID: j.SessionID, MessageID: j.MessageID, Accepted: true, Terminal: !n.running}, nil
}
func (n *nativeCode) Abort(context.Context, worker.Job) error { n.running = false; return nil }

type noCheckout struct{}

func (noCheckout) Prepare(context.Context, worker.Job) (worker.Job, error) {
	return worker.Job{}, errors.New("unexpected engineering admission")
}
func (noCheckout) Progress(context.Context, worker.Job) (string, error) { return "", nil }

type hungCatalog struct {
	started, release chan struct{}
	once             sync.Once
}

func (h *hungCatalog) NativeSnapshot(ctx context.Context, _ worker.Config, _ worker.NativeMaster) (worker.NativeCatalog, error) {
	h.once.Do(func() { close(h.started) })
	select {
	case <-ctx.Done():
		return worker.NativeCatalog{}, ctx.Err()
	case <-h.release:
		return worker.NativeCatalog{}, c.ErrUnavailable
	}
}

type connection struct {
	base    http.RoundTripper
	offline atomic.Bool
}

func (c *connection) RoundTrip(r *http.Request) (*http.Response, error) {
	if c.offline.Load() {
		return nil, errors.New("fixture disconnected")
	}
	return c.base.RoundTrip(r)
}

type child struct {
	cfg     worker.Config
	cl      *masterclient.Client
	comms   *worker.MasterComms
	engine  *worker.Engine
	code    *nativeCode
	network *connection
}

func TestMasterLocalRemoteAcceptance(t *testing.T) {
	ctx := context.Background()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "acceptance-master"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	owner := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	ps := []c.Principal{owner}
	var placements []c.Placement
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}}
	var cfgs []worker.Config
	for i, name := range []string{"builder", "planner"} {
		raw, err := os.ReadFile("../../examples/worker-master.json")
		if err != nil {
			t.Fatal(err)
		}
		var cfg worker.Config
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.Name = name
		cfg.Self = cfg.Team.Members[i].Actor
		cfg.StateDir = filepath.Join(t.TempDir(), "state")
		cfg.WorkRoot = filepath.Join(t.TempDir(), "work")
		cfg.Master.MasterID = "acceptance-master"
		cfg.Master.AgentID = name
		cfg.Master.Engagement = "coop"
		cfg.Master.HostID = []string{"local-host", "simulated-remote-host"}[i]
		cfg.Master.InstanceID = worker.BodyHash(name)
		cfg = worker.Defaults(cfg)
		if _, err = worker.InitializeTeamState(cfg); err != nil {
			t.Fatal(err)
		}
		cfgs = append(cfgs, cfg)
		ps = append(ps, c.Principal{ID: name, Alias: name, Kind: "agent", GitHubBotID: cfg.Self.ID, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}})
		placements = append(placements, c.Placement{AgentID: name, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: 1})
		h := sha256.Sum256([]byte("acceptance-token-" + name))
		policy.Tokens = append(policy.Tokens, master.Token{PrincipalID: name, SHA256: hex.EncodeToString(h[:]), InstanceID: cfg.Master.InstanceID, Generation: 1})
	}
	if err = s.Configure(ctx, ps, placements); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: s}, policy))
	defer ts.Close()
	th, err := s.CreateThread(ctx, owner, c.CreateThread{Title: "Private coordination", Engagement: "coop", Members: []string{"builder", "planner"}, RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	hung := &hungCatalog{started: make(chan struct{}), release: make(chan struct{})}
	defer close(hung.release)
	var children []child
	for _, cfg := range cfgs {
		net := &connection{base: ts.Client().Transport}
		httpClient := &http.Client{Transport: net}
		cl, err := masterclient.New(masterclient.ClientConfig{URL: ts.URL, MasterID: cfg.Master.MasterID, Token: "acceptance-token-" + cfg.Name, HTTPClient: httpClient})
		if err != nil {
			t.Fatal(err)
		}
		code := &nativeCode{}
		comms, err := worker.NewMasterComms(cfg, cl, code, worker.RealClock{})
		if err != nil {
			t.Fatal(err)
		}
		d, err := worker.NewNativeDispatcher(cfg, cl, hung, code, noCheckout{}, worker.RealClock{})
		if err != nil {
			t.Fatal(err)
		}
		e := &worker.Engine{Config: cfg, Store: worker.NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &worker.MasterRuntime{Comms: comms, Engineering: d}}
		if err = e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{cfg, cl, comms, e, code, net})
	}
	select {
	case <-hung.started:
	case <-time.After(time.Second):
		t.Fatal("catalog fixture did not block")
	}
	started := time.Now()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{"builder", "planner"}, Body: "What is your current status?", RequestID: "owner-question"})
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err = child.engine.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.MessageStatus(ctx, owner, r.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second || len(status.Deliveries) != 2 {
		t.Fatal("idle receipt bound missed")
	}
	for _, d := range status.Deliveries {
		if d.State != "running" {
			t.Fatal("catalog blocked native conversation", d)
		}
	}
	for _, ch := range children {
		turn, err := ch.comms.Active()
		if err != nil || turn == nil {
			t.Fatal(err)
		}
		if err = worker.QueueMasterReply(ch.cfg, turn.Delivery.ID, "I have persisted the request and can answer."); err != nil {
			t.Fatal(err)
		}
		ch.network.offline.Store(true)
		if err = ch.comms.FlushOutbox(ctx); err == nil {
			t.Fatal("disconnected publication reported success")
		}
		ch.network.offline.Store(false)
		restarted, err := worker.NewMasterComms(ch.cfg, ch.cl, ch.code, worker.RealClock{})
		if err != nil {
			t.Fatal(err)
		}
		if err = restarted.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
		ch.code.running = false
		if err = ch.engine.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if ch.code.calls != 1 {
			t.Fatal("outbox recovery replayed native turn")
		}
	}
	status, err = s.MessageStatus(ctx, owner, r.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range status.Deliveries {
		if d.State != "replied" || d.ReplyMessageID == "" {
			t.Fatal("reply was not durably linked", d)
		}
	}
	peer, err := children[0].cl.Send(ctx, c.SendMessage{ThreadID: th.ID, Recipients: []string{"planner"}, Body: "Please review my proposed next step.", RequestID: "peer-question"})
	if err != nil {
		t.Fatal(err)
	}
	ch := children[1]
	if err = ch.engine.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := ch.comms.Active()
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = worker.QueueMasterReply(ch.cfg, turn.Delivery.ID, "I will review the proposal and respond here."); err != nil {
		t.Fatal(err)
	}
	if err = ch.comms.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	peerStatus, err := s.MessageStatus(ctx, owner, peer.MessageID)
	if err != nil || peerStatus.Deliveries[0].State != "replied" {
		t.Fatal("peer reply missing", err)
	}
	if err = children[0].engine.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := children[0].cl.Pending(ctx, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatal("peer reply did not address original author", err)
	}
}
