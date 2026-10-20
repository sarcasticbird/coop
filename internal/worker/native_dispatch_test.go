package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type dispatchMaster struct {
	nativeMasterFixture
	self      c.SelfIdentity
	threads   []c.Thread
	offline   bool
	reports   map[string]c.Message
	onSend    func()
	onMessage func()
}

func (m *dispatchMaster) Self(context.Context) (c.SelfIdentity, error) {
	if m.offline {
		return c.SelfIdentity{}, c.ErrUnavailable
	}
	return m.self, nil
}
func (m *dispatchMaster) Thread(_ context.Context, id string) (c.Thread, error) {
	for _, th := range m.threads {
		if th.ID == id {
			return th, nil
		}
	}
	return c.Thread{}, c.ErrNotFound
}
func (m *dispatchMaster) Message(_ context.Context, id string) (c.Message, error) {
	v, ok := m.reports[id]
	if !ok {
		return v, c.ErrNotFound
	}
	if m.onMessage != nil {
		m.onMessage()
	}
	return v, nil
}
func (m *dispatchMaster) Send(_ context.Context, in c.SendMessage) (c.MessageReceipt, error) {
	if m.reports == nil {
		m.reports = map[string]c.Message{}
	}
	id := BodyHash(in.RequestID)
	m.reports[id] = c.Message{ID: id, ThreadID: in.ThreadID, Body: in.Body, AuthorID: m.self.Principal.ID, AuthorKind: "agent"}
	if m.onSend != nil {
		m.onSend()
	}
	return c.MessageReceipt{MessageID: id}, nil
}

type dispatchCatalog struct {
	catalog NativeCatalog
	err     error
}

func (g *dispatchCatalog) NativeSnapshot(context.Context, Config, NativeMaster) (NativeCatalog, error) {
	return g.catalog, g.err
}
func (g *dispatchCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, _ Job, _ ...string) (NativeCatalog, error) {
	return g.NativeSnapshot(ctx, cfg, m)
}
func dispatchFixture(t *testing.T) (Config, *dispatchMaster, *dispatchCatalog, *fakeOpenCode) {
	t.Helper()
	cfg, cat, _ := nativeWorkFixture(t)
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.WorkRoot = filepath.Join(t.TempDir(), "work")
	cfg.Master.InstanceID = BodyHash("instance")
	cfg.Master.Generation = 1
	cfg.Master.HostID = "host"
	m := &dispatchMaster{nativeMasterFixture: nativeMasterFixture{authority: cat.Goals[0]}}
	m.self = c.SelfIdentity{RestoreEpoch: 1, Principal: c.Principal{ID: cfg.Master.AgentID, Kind: "agent", GitHubBotID: cfg.Self.ID, Engagements: []string{"coop"}}, Placement: c.Placement{AgentID: cfg.Master.AgentID, HostID: "host", InstanceID: cfg.Master.InstanceID, Generation: 1}}
	m.threads = []c.Thread{{ID: "private", Engagement: "coop", Members: []string{"author", "peer", "qa"}}}
	saveNativeFixtureConnection(t, cfg)
	return cfg, m, &dispatchCatalog{catalog: cat}, &fakeOpenCode{running: true}
}
func saveNativeFixtureConnection(t *testing.T, cfg Config) {
	t.Helper()
	raw, _ := json.Marshal(masterConnectionState{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1})
	if err := atomicPrivate(filepath.Join(masterDir(cfg), "connection.json"), raw); err != nil {
		t.Fatal(err)
	}
}

type budgetedNativeCatalog struct {
	catalog   NativeCatalog
	remaining int
}

func (g *budgetedNativeCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, _ Job, _ ...string) (NativeCatalog, error) {
	return g.NativeSnapshot(ctx, cfg, m)
}

func (g *budgetedNativeCatalog) NativeSnapshot(context.Context, Config, NativeMaster) (NativeCatalog, error) {
	// A complete catalog can consume over half of the admission deadline.
	// Model that budget without wall-clock sleeps or timing-sensitive tests.
	if g.remaining == 0 {
		return NativeCatalog{}, context.DeadlineExceeded
	}
	g.remaining--
	return g.catalog, nil
}

func TestNativePreparedAdmissionFitsOneCatalogReadBudget(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "prepared"}
	x.Job.Checkout = filepath.Join(cfg.WorkRoot, "saved-checkout")
	x.Job.SessionID, x.Job.MessageID, x.Job.SubmissionState = "ses_saved", "msg_saved", "prepared"
	d, err := NewNativeDispatcher(cfg, m, &budgetedNativeCatalog{catalog: g.catalog, remaining: 1}, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal("prepared admission exhausted its catalog read budget", err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Phase != "active" || saved.Job.SubmissionState != "accepted" || saved.Job.SessionID != "ses_saved" || saved.Job.MessageID != "msg_saved" || saved.Job.Checkout != filepath.Join(cfg.WorkRoot, "saved-checkout") {
		t.Fatalf("saved admission = %+v, %v", saved, err)
	}
}

func TestNativeDispatcherPersistsIntentAndNeverReplays(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	code.lost = true
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	candidate := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Binding: &candidate.Task.Binding, Phase: "selected"}
	_, err = d.advance(context.Background(), &x)
	if err == nil {
		t.Fatal("expected lost submission response")
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Job.SessionID == "" || saved.Job.MessageID == "" || saved.Phase != "active" {
		t.Fatalf("lost durable intent: %+v %v", saved, err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.HasActive() {
		t.Fatal("lost active execution")
	}
	_, err = restarted.advance(context.Background(), &saved)
	if err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("uncertain native turn replayed")
	}
}
func TestNativeDispatcherFreshAdmissionAndOutageObservation(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	candidate := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Binding: &candidate.Task.Binding, Phase: "selected"}
	m.self.Placement.Generation++
	if _, err = d.advance(context.Background(), &x); err == nil || code.calls != 0 {
		t.Fatal("stale placement admitted")
	}
	m.self.Placement.Generation--
	g.err = errors.New("catalog offline")
	if _, err = d.advance(context.Background(), &x); err == nil || code.calls != 0 {
		t.Fatal("unreadable GitHub admitted")
	}
	g.err = nil
	if _, err = d.advance(context.Background(), &x); err != nil {
		t.Fatal(err)
	}
	m.offline = true
	if _, err = d.advance(context.Background(), &x); err != nil {
		t.Fatal("master outage prevented local native observation", err)
	}
	if code.calls != 1 {
		t.Fatal("outage resubmitted native prompt")
	}
}

func TestNativeDispatcherHealthAndTurnLimit(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	code.HealthError = errors.New("native server unavailable")
	if _, err = d.advance(context.Background(), &x); err == nil || code.calls != 0 {
		t.Fatal("unhealthy native server admitted")
	}
	code.HealthError = nil
	if _, err = d.advance(context.Background(), &x); err != nil {
		t.Fatal(err)
	}
	x.Job.Deadline = time.Now().Add(-time.Hour)
	if _, err = d.advance(context.Background(), &x); err != nil || x.Phase != "blocked" || code.aborts != 1 {
		t.Fatal("turn deadline did not use ownership-aware interruption", err)
	}
}
