package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

// Embedding keeps unexpected calls fatal rather than silently returning success.
type coordinationMock struct {
	Coordination
	self     c.SelfIdentity
	selfErr  error
	threads  func(c.ThreadQuery) (c.ThreadPage, error)
	thread   func(string) (c.Thread, error)
	messages func(string, int64) (c.MessagePage, error)
	pending  func(c.PendingQuery) (c.DeliveryPage, error)
	message  func(string) (c.Message, error)
	goals    func(string, string) (c.GoalPage, error)
	status   func(string) (c.MessageReceipt, error)
	send     func(c.SendMessage) (c.MessageReceipt, error)
	decide   func(c.GoalDecisionInput) (c.GoalAuthority, error)
	resolve  func(c.GoalRef) (c.GoalAuthority, error)
}

func (s *coordinationMock) MasterID() string                             { return "test-master" }
func (s *coordinationMock) Self(context.Context) (c.SelfIdentity, error) { return s.self, s.selfErr }
func (s *coordinationMock) Threads(_ context.Context, q c.ThreadQuery) (c.ThreadPage, error) {
	return s.threads(q)
}
func (s *coordinationMock) Thread(_ context.Context, id string) (c.Thread, error) {
	return s.thread(id)
}
func (s *coordinationMock) Messages(_ context.Context, id string, after int64) (c.MessagePage, error) {
	return s.messages(id, after)
}
func (s *coordinationMock) Pending(_ context.Context, q c.PendingQuery) (c.DeliveryPage, error) {
	return s.pending(q)
}
func (s *coordinationMock) Message(_ context.Context, id string) (c.Message, error) {
	return s.message(id)
}
func (s *coordinationMock) ApprovedGoals(_ context.Context, engagement, after string) (c.GoalPage, error) {
	return s.goals(engagement, after)
}
func (s *coordinationMock) MessageStatus(_ context.Context, id string) (c.MessageReceipt, error) {
	return s.status(id)
}
func (s *coordinationMock) Send(_ context.Context, in c.SendMessage) (c.MessageReceipt, error) {
	return s.send(in)
}
func (s *coordinationMock) DecideGoal(_ context.Context, in c.GoalDecisionInput) (c.GoalAuthority, error) {
	return s.decide(in)
}
func (s *coordinationMock) ResolveGoal(_ context.Context, ref c.GoalRef) (c.GoalAuthority, error) {
	return s.resolve(ref)
}

func coordinationFixture() (fleetModel, *coordinationMock) {
	s := &coordinationMock{self: c.SelfIdentity{Principal: c.Principal{ID: "owner", Kind: "owner", Capabilities: []string{"read", "send", "manage", "decide"}}}}
	m := fleetModel{workspace: 1, ctx: context.Background(), services: Services{Coordination: s}, coordination: coordState{section: "threads", self: s.self, seen: time.Now()}}
	return m, s
}

func coordinationKey(m fleetModel, key string) (fleetModel, tea.Cmd) {
	k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "esc":
		k = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		k = tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		k = tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		k = tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+r":
		k = tea.KeyMsg{Type: tea.KeyCtrlR}
	case "ctrl+v":
		k = tea.KeyMsg{Type: tea.KeyCtrlV}
	case "pgdown":
		k = tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		k = tea.KeyMsg{Type: tea.KeyPgUp}
	}
	out, cmd := m.Update(k)
	return out.(fleetModel), cmd
}

func coordinationRun(t *testing.T, m fleetModel, cmd tea.Cmd) fleetModel {
	t.Helper()
	for i := 0; cmd != nil; i++ {
		if i > 10 {
			t.Fatal("coordination command loop")
		}
		out, next := m.Update(cmd())
		m, cmd = out.(fleetModel), next
	}
	return m
}

func TestCoordinationPaginationAndInboxOpen(t *testing.T) {
	for _, section := range []string{"threads", "inbox", "goals"} {
		t.Run(section, func(t *testing.T) {
			m, s := coordinationFixture()
			m.coordination.section = section
			var cursors []string
			next := func(after string) string {
				cursors = append(cursors, after)
				if after == "" {
					return "page-2"
				}
				return ""
			}
			s.threads = func(q c.ThreadQuery) (c.ThreadPage, error) {
				return c.ThreadPage{Threads: []c.Thread{{ID: "thread-" + q.After}}, Next: next(q.After)}, nil
			}
			s.pending = func(q c.PendingQuery) (c.DeliveryPage, error) {
				return c.DeliveryPage{Deliveries: []c.Delivery{{MessageID: "message"}}, Next: next(q.After)}, nil
			}
			s.goals = func(engagement, after string) (c.GoalPage, error) {
				if engagement != "" {
					t.Fatal("unexpected engagement filter")
				}
				return c.GoalPage{Goals: []c.GoalAuthority{{State: "approved"}}, Next: next(after)}, nil
			}
			cmd := m.coordLoad(false)
			m = coordinationRun(t, m, cmd)
			m.coordination.cursor = 4
			m, cmd = coordinationKey(m, "]")
			m = coordinationRun(t, m, cmd)
			if !reflect.DeepEqual(cursors, []string{"", "page-2"}) || m.coordination.cursor != 0 || m.coordination.next != "" {
				t.Fatalf("pagination: cursors=%v state=%+v", cursors, m.coordination)
			}
			m, cmd = coordinationKey(m, "]")
			if cmd != nil {
				t.Fatal("requested absent next page")
			}
			if section == "inbox" {
				s.message = func(id string) (c.Message, error) {
					if id != "message" {
						t.Fatal(id)
					}
					return c.Message{ThreadID: "inbox-thread"}, nil
				}
				s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id}, nil }
				s.messages = func(id string, after int64) (c.MessagePage, error) {
					if id != "inbox-thread" || after != 0 {
						t.Fatalf("wrong reader %s/%d", id, after)
					}
					return c.MessagePage{Messages: []c.Message{{ID: "message", Sequence: 3}}}, nil
				}
				m, cmd = coordinationKey(m, "enter")
				m = coordinationRun(t, m, cmd)
				if m.coordination.section != "threads" || m.coordination.thread.ID != "inbox-thread" || m.coordination.after != 3 {
					t.Fatal("inbox did not open private reader")
				}
			}
		})
	}
}

func TestCoordinationFollowAndReceiptPolling(t *testing.T) {
	m, s := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "thread"}
	m.coordination.messages = []c.Message{{ID: "old", Sequence: 2}}
	m.coordination.after = 2
	s.messages = func(id string, after int64) (c.MessagePage, error) {
		if id != "thread" || after != 2 {
			t.Fatalf("follow cursor %s/%d", id, after)
		}
		return c.MessagePage{Messages: []c.Message{{ID: "old", Sequence: 2}, {ID: "new", Sequence: 4}}, Next: 5}, nil
	}
	m, _ = coordinationKey(m, "f")
	// Suppress Fleet refresh and execute only the coordination command in the batch.
	m.refreshing = true
	out, cmd := m.Update(fleetTick(time.Now()))
	m = out.(fleetModel)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || !m.coordination.busy {
		t.Fatal("follow did not schedule a read")
	}
	if len(batch) != 2 {
		t.Fatalf("expected coordination read and clock, got %d commands", len(batch))
	}
	// The last batch command is the two-second clock, not part of this read.
	m = coordinationRun(t, m, batch[0])
	if len(m.coordination.messages) != 2 || m.coordination.after != 5 {
		t.Fatalf("follow lost cursor/deduplication: %+v", m.coordination)
	}
	var polls int
	s.status = func(id string) (c.MessageReceipt, error) {
		if id != "old" {
			t.Fatal(id)
		}
		polls++
		return c.MessageReceipt{MessageID: id, Backend: "native", Deliveries: []c.Delivery{{AgentID: "robin", State: "acknowledged", ReplyMessageID: "reply"}}}, nil
	}
	m, cmd = coordinationKey(m, "s")
	m = coordinationRun(t, m, cmd)
	cmd = m.coordLoad(false)
	m = coordinationRun(t, m, cmd)
	if polls != 2 || !strings.Contains(m.View(), "acknowledged") || !strings.Contains(m.View(), "reply: reply") {
		t.Fatal("receipt status/polling missing")
	}
	m, _ = coordinationKey(m, "esc")
	if m.coordination.receipt != nil || m.coordination.thread == nil {
		t.Fatal("receipt back lost reader")
	}
	m, _ = coordinationKey(m, "esc")
	if m.coordination.thread != nil || len(m.coordination.messages) != 0 || m.coordination.follow {
		t.Fatal("reader back retained private history/follow")
	}
	m, _ = coordinationKey(m, "esc")
	if m.workspace != 0 {
		t.Fatal("back did not return to Fleet")
	}
}

func TestCoordinationComposeReplyAndSensitiveBuffer(t *testing.T) {
	for _, key := range []string{"c", "p"} {
		t.Run(key, func(t *testing.T) {
			m, s := coordinationFixture()
			m.coordination.thread = &c.Thread{ID: "original"}
			m.coordination.messages = []c.Message{{ID: "parent", AuthorID: "robin"}}
			var sent c.SendMessage
			s.send = func(in c.SendMessage) (c.MessageReceipt, error) {
				sent = in
				return c.MessageReceipt{MessageID: "committed"}, nil
			}
			m, _ = coordinationKey(m, key)
			m, _ = coordinationKey(m, "tab")
			m, _ = coordinationKey(m, "private-body-sentinel")
			if strings.Contains(m.View(), "private-body-sentinel") {
				t.Fatal("body revealed by default")
			}
			m, _ = coordinationKey(m, "ctrl+v")
			if !strings.Contains(m.View(), "private-body-sentinel") {
				t.Fatal("explicit reveal unavailable")
			}
			m, cmd := coordinationKey(m, "ctrl+s")
			if cmd != nil || !m.coordination.form.preview || sent.RequestID != "" {
				t.Fatal("preview submitted write")
			}
			m.coordination.thread.ID = "changed-selection"
			m, cmd = coordinationKey(m, "ctrl+s")
			if !m.coordination.form.frozen {
				t.Fatal("submitted buffer not frozen")
			}
			m = coordinationRun(t, m, cmd)
			if sent.ThreadID != "original" || sent.Body != "private-body-sentinel" || sent.RequestID == "" || len(sent.Recipients) != 0 {
				t.Fatalf("wrong immutable send: %+v", sent)
			}
			if (key == "p") != (sent.ParentMessageID == "parent") {
				t.Fatalf("reply parent: %+v", sent)
			}
			if m.coordination.form != nil || strings.Contains(m.View(), "private-body-sentinel") {
				t.Fatal("success retained compose buffer")
			}
			if !strings.Contains(m.View(), "commitment is not acknowledgment") {
				t.Fatal("receipt falsely implies acknowledgment")
			}
		})
	}
	m, _ := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "thread"}
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "discard-private-sentinel")
	m, _ = coordinationKey(m, "esc")
	if m.coordination.form != nil || strings.Contains(m.View(), "discard-private-sentinel") {
		t.Fatal("cancel retained private form")
	}
}

func TestCoordinationPermissionsAndIdentityDenial(t *testing.T) {
	for _, tc := range []struct {
		name, kind, action      string
		caps                    []string
		failed, unseen, allowed bool
	}{
		{name: "owner create", kind: "owner", action: "n", caps: []string{"send", "manage"}, allowed: true},
		{name: "agent create", kind: "agent", action: "n", caps: []string{"send", "manage"}},
		{name: "owner no manage", kind: "owner", action: "n", caps: []string{"send"}},
		{name: "agent decision", kind: "agent", action: "d", caps: []string{"send", "decide"}},
		{name: "owner decision", kind: "owner", action: "d", caps: []string{"send", "decide"}, allowed: true},
		{name: "owner no decide", kind: "owner", action: "d", caps: []string{"send"}},
		{name: "failed snapshot", kind: "owner", action: "c", caps: []string{"send"}, failed: true},
		{name: "no snapshot", kind: "owner", action: "c", caps: []string{"send"}, unseen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := coordinationFixture()
			m.coordination.self.Principal.Kind, m.coordination.self.Principal.Capabilities = tc.kind, tc.caps
			m.coordination.failed = tc.failed
			if tc.unseen {
				m.coordination.seen = time.Time{}
			}
			m.coordination.thread, m.coordination.authority = &c.Thread{ID: "thread"}, &c.GoalAuthority{}
			m, cmd := coordinationKey(m, tc.action)
			if cmd != nil || (m.coordination.form != nil) != tc.allowed {
				t.Fatalf("permission gate: form=%+v status=%s", m.coordination.form, m.coordination.status)
			}
		})
	}
	m, s := coordinationFixture()
	s.selfErr = fmt.Errorf("private-error-sentinel: %w", c.ErrForbidden)
	called := false
	cmd := m.coordCall("read", func(context.Context, Coordination) (any, error) { called = true; return nil, nil })
	m = coordinationRun(t, m, cmd)
	if called || !m.coordination.failed || m.coordination.allowed("send") || strings.Contains(m.View(), "private-error-sentinel") {
		t.Fatal("identity denial leaked or dispatched operation")
	}
}

func TestCoordinationDecisionKeepsExactRevisionAndSequenceOnConflict(t *testing.T) {
	for _, conflict := range []string{"sequence", "revision"} {
		t.Run(conflict, func(t *testing.T) {
			m, s := coordinationFixture()
			m.coordination.authority = &c.GoalAuthority{Revision: c.GoalRevision{Ref: c.GoalRef{GoalID: "goal", RevisionSHA256: strings.Repeat("a", 64)}}, DecisionSequence: 7}
			var requests []c.GoalDecisionInput
			s.decide = func(in c.GoalDecisionInput) (c.GoalAuthority, error) {
				requests = append(requests, in)
				return c.GoalAuthority{}, fmt.Errorf("private-%s: %w", conflict, c.ErrConflict)
			}
			m, _ = coordinationKey(m, "d")
			m, _ = coordinationKey(m, "approve")
			m.coordination.authority.DecisionSequence = 8
			m.coordination.authority.Revision.Ref.RevisionSHA256 = strings.Repeat("b", 64)
			m, _ = coordinationKey(m, "ctrl+s")
			m, cmd := coordinationKey(m, "ctrl+s")
			m = coordinationRun(t, m, cmd)
			m, _ = coordinationKey(m, "reject")
			m, cmd = coordinationKey(m, "ctrl+r")
			m = coordinationRun(t, m, cmd)
			if len(requests) != 2 || requests[0] != requests[1] || requests[0].ExpectedDecisionSequence != 7 || requests[0].RevisionSHA256 != strings.Repeat("a", 64) || requests[0].Action != "approve" {
				t.Fatalf("decision rebound after conflict: %+v", requests)
			}
			if !m.coordination.form.frozen || strings.Contains(m.View(), "private-"+conflict) {
				t.Fatal("conflict unfroze buffer or leaked server details")
			}
		})
	}
}

func TestCoordinationMalformedProposalNeverSubmits(t *testing.T) {
	valid := `{"thread_id":"thread","purpose":"purpose","scope":"scope","repositories":[{"name":"sarcasticbird/coop","base_branches":["main"]}],"acceptance":["works"],"author_id":"hawk","peer_id":"robin"}`
	for _, raw := range []string{"{private-invalid-sentinel", valid + ` {}`, strings.Replace(valid, `"scope":"scope"`, `"unknown":"private-invalid-sentinel"`, 1), strings.Replace(valid, `"base_branches":["main"]`, `"base_branches":[]`, 1), strings.Replace(valid, `"peer_id":"robin"`, `"peer_id":"hawk"`, 1), strings.Replace(valid, `"acceptance":["works"]`, `"acceptance":[]`, 1)} {
		m, _ := coordinationFixture()
		m, _ = coordinationKey(m, "o")
		m, _ = coordinationKey(m, raw)
		m, cmd := coordinationKey(m, "ctrl+s")
		if cmd != nil || m.coordination.form.preview || m.coordination.form.frozen || !strings.Contains(m.coordination.status, "Invalid") || strings.Contains(m.View(), "private-invalid-sentinel") {
			t.Fatal("invalid proposal submitted or disclosed")
		}
	}
	f := coordForm{kind: "proposal", values: []string{valid}, requestID: "ui-request"}
	if _, err := f.build(); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
}

func TestCoordinationStaleResultCannotReplaceCurrentState(t *testing.T) {
	m, _ := coordinationFixture()
	m.coordination.serial, m.coordination.busy = 5, true
	m.coordination.status = "current request"
	before := m.coordination
	for _, result := range []coordResult{{serial: 4, data: c.Thread{ID: "stale"}}, {serial: 4, err: errors.New("private-stale-error")}} {
		out, cmd := m.Update(result)
		m = out.(fleetModel)
		if cmd != nil || !reflect.DeepEqual(m.coordination, before) {
			t.Fatal("stale result changed current state")
		}
	}
}

func TestCoordinationTeamRefreshKeepsSelectionSafe(t *testing.T) {
	m, _ := coordinationFixture()
	m.coordination.section = "team"
	m.coordination.team = []c.Presence{{Placement: c.Placement{AgentID: "hawk"}}, {Placement: c.Placement{AgentID: "robin"}}}
	m.coordination.cursor = 1
	out, _ := m.Update(coordResult{serial: m.coordination.serial, self: m.coordination.self, data: []c.Presence{{Placement: c.Placement{AgentID: "hawk"}}}})
	m = out.(fleetModel)
	if m.coordination.cursor < 0 || m.coordination.cursor >= len(m.coordination.team) {
		t.Fatalf("team refresh left selection %d outside %d rows; prompt would panic", m.coordination.cursor, len(m.coordination.team))
	}
	m, _ = coordinationKey(m, "p")
	if m.coordination.form == nil || m.coordination.form.values[0] != "hawk" {
		t.Fatal("prompt did not use remaining team member")
	}
}

func TestCoordinationDetailsFollowLatestResponse(t *testing.T) {
	m, _ := coordinationFixture()
	m.coordination.receipt = &c.MessageReceipt{MessageID: "old-message"}
	authority := c.GoalAuthority{Revision: c.GoalRevision{Ref: c.GoalRef{GoalID: "new-goal"}, Proposal: c.GoalProposal{Purpose: "inspect-purpose", Scope: "inspect-scope", Repositories: []c.RepositoryScope{{Name: "fixture/repo", BaseBranches: []string{"main"}}}, Acceptance: []string{"inspect-criterion"}}}}
	out, _ := m.Update(coordResult{serial: m.coordination.serial, self: m.coordination.self, data: authority})
	m = out.(fleetModel)
	if m.coordination.receipt != nil || m.coordination.authority == nil {
		t.Fatal("old receipt hid new authority")
	}
	for _, want := range []string{"new-goal", "inspect-purpose", "inspect-scope", "fixture/repo", "inspect-criterion"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("authority omits %s", want)
		}
	}
	out, _ = m.Update(coordResult{serial: m.coordination.serial, self: m.coordination.self, data: c.MessageReceipt{MessageID: "new-message"}})
	m = out.(fleetModel)
	if m.coordination.authority != nil || m.coordination.receipt.MessageID != "new-message" {
		t.Fatal("old authority interferes with new receipt polling")
	}
}

func TestCoordinationReaderBoundsRetainedHistory(t *testing.T) {
	m, _ := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "thread"}
	var messages []c.Message
	for i := 1; i <= 505; i++ {
		messages = append(messages, c.Message{ID: fmt.Sprint(i), Sequence: int64(i)})
	}
	out, _ := m.Update(coordResult{serial: m.coordination.serial, self: m.coordination.self, data: c.MessagePage{Messages: messages}})
	m = out.(fleetModel)
	if len(m.coordination.messages) != 500 || m.coordination.messages[0].ID != "6" || m.coordination.after != 505 {
		t.Fatal("reader retained unbounded history or lost high-water sequence")
	}
}

type coordinationDropResponse struct {
	base    http.RoundTripper
	dropped bool
}

func (r *coordinationDropResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := r.base.RoundTrip(req)
	if err == nil && req.Method == http.MethodPost && req.URL.Path == "/api/v1/messages" && !r.dropped {
		r.dropped = true
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return nil, errors.New("private-transport-sentinel after commit")
	}
	return res, err
}

func TestCoordinationHTTPRetryAfterCommitPreservesRequest(t *testing.T) {
	ctx := context.Background()
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "test-master"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	p := c.Principal{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage"}}
	if err = store.Configure(ctx, []c.Principal{p}, nil); err != nil {
		t.Fatal(err)
	}
	token := "coordination-test-token"
	hash := sha256.Sum256([]byte(token))
	handler := master.NewHandler(&master.Service{Store: store}, &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: p.ID, SHA256: hex.EncodeToString(hash[:])}}})
	var bodies [][]byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/messages" {
			raw, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Error(readErr)
			}
			bodies = append(bodies, raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	httpClient := server.Client()
	drop := &coordinationDropResponse{base: httpClient.Transport}
	httpClient.Transport = drop
	client, err := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: "test-master", Token: token, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := client.CreateThread(ctx, c.CreateThread{Title: "retry test", Engagement: "coop", RequestID: "create-thread"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := coordinationFixture()
	m.services.Coordination = client
	m.coordination.thread = &thread
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "private-retry-body")
	m, _ = coordinationKey(m, "ctrl+s")
	m, cmd := coordinationKey(m, "ctrl+s")
	m = coordinationRun(t, m, cmd)
	if !drop.dropped || !m.coordination.failed || m.coordination.form == nil || !m.coordination.form.frozen {
		t.Fatal("post-commit transport failure did not retain frozen retry")
	}
	if strings.Contains(m.View(), "private-transport-sentinel") || strings.Contains(m.View(), "private-retry-body") {
		t.Fatal("transport failure leaked private details")
	}
	page, err := client.Messages(ctx, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("first request did not commit: %+v %v", page, err)
	}
	committedID := page.Messages[0].ID
	m, _ = coordinationKey(m, "attempted edit")
	m, cmd = coordinationKey(m, "ctrl+r")
	m = coordinationRun(t, m, cmd)
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry changed serialized request: %q", bodies)
	}
	if m.coordination.form != nil || m.coordination.receipt == nil || m.coordination.receipt.MessageID != committedID {
		t.Fatal("retry did not recover original receipt and clear buffer")
	}
	page, err = client.Messages(ctx, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Body != "private-retry-body" {
		t.Fatalf("retry duplicated/changed committed message: %+v %v", page, err)
	}
}

func coordinationMasterFixture(t *testing.T) (*masterclient.Client, *masterclient.Client) {
	t.Helper()
	ctx := context.Background()
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "test-master"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principals := []c.Principal{
		{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}},
		{ID: "hawk", Kind: "agent", Alias: "hawk", GitHubBotID: 101, Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}},
		{ID: "robin", Kind: "agent", Alias: "robin", GitHubBotID: 102, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}},
	}
	if err = store.Configure(ctx, principals, []c.Placement{{AgentID: "hawk", HostID: "test-host", InstanceID: "hawk-instance", Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}}
	for _, id := range []string{"owner", "hawk"} {
		hash := sha256.Sum256([]byte("test-token-for-" + id))
		entry := master.Token{PrincipalID: id, SHA256: hex.EncodeToString(hash[:])}
		if id == "hawk" {
			entry.InstanceID, entry.Generation = "hawk-instance", 1
		}
		policy.Tokens = append(policy.Tokens, entry)
	}
	server := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: store}, policy))
	t.Cleanup(server.Close)
	client := func(id string) *masterclient.Client {
		cl, e := masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: "test-master", Token: "test-token-for-" + id, HTTPClient: server.Client()})
		if e != nil {
			t.Fatal(e)
		}
		return cl
	}
	return client("owner"), client("hawk")
}

func coordinationSubmit(t *testing.T, m fleetModel) fleetModel {
	t.Helper()
	if m.coordination.form == nil {
		t.Fatalf("missing form: %s", m.coordination.status)
	}
	m, cmd := coordinationKey(m, "ctrl+s")
	if cmd != nil || !m.coordination.form.preview {
		t.Fatalf("preview failed: %s", m.coordination.status)
	}
	m, cmd = coordinationKey(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("submit failed: %s", m.coordination.status)
	}
	return coordinationRun(t, m, cmd)
}

func TestCoordinationHTTPThreadMembershipLifecycle(t *testing.T) {
	owner, agent := coordinationMasterFixture(t)
	m, _ := coordinationFixture()
	m.services.Coordination = owner
	m, _ = coordinationKey(m, "n")
	for i, value := range []string{"UI-created thread", "coop", "owner, hawk"} {
		if i > 0 {
			m, _ = coordinationKey(m, "tab")
		}
		m, _ = coordinationKey(m, value)
	}
	m = coordinationSubmit(t, m)
	if m.coordination.failed || m.coordination.thread == nil || m.coordination.thread.Title != "UI-created thread" || m.coordination.form != nil {
		t.Fatalf("create transition: %+v", m.coordination)
	}
	thread := *m.coordination.thread
	if !reflect.DeepEqual(thread.Members, []string{"hawk", "owner"}) {
		t.Fatalf("create members: %v", thread.Members)
	}
	m, _ = coordinationKey(m, "m")
	if m.coordination.form.values[0] != "hawk,owner" {
		t.Fatal("membership form did not snapshot current members")
	}
	m.coordination.form.values[0] = "owner, robin"
	// Another owner session wins before this form is submitted.
	concurrent, err := owner.SetMembers(context.Background(), c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: []string{"owner", "hawk", "robin"}, RequestID: "concurrent-members"})
	if err != nil {
		t.Fatal(err)
	}
	m = coordinationSubmit(t, m)
	if !m.coordination.failed || m.coordination.form == nil || !m.coordination.form.frozen || !strings.Contains(m.coordination.status, c.ErrConflict.Error()) {
		t.Fatal("stale exact-set update was not held on conflict")
	}
	actual, err := owner.Thread(context.Background(), thread.ID)
	if err != nil || !reflect.DeepEqual(actual.Members, concurrent.Members) {
		t.Fatalf("conflict overwrote concurrent members: %+v %v", actual, err)
	}
	m, _ = coordinationKey(m, "esc")
	// Open the new snapshot and successfully replace the full set, including a removal.
	m.coordination.thread = &actual
	cmd := m.coordLoad(false)
	m = coordinationRun(t, m, cmd)
	m, _ = coordinationKey(m, "m")
	if m.coordination.form == nil {
		t.Fatalf("membership form unavailable after refresh: %s", m.coordination.status)
	}
	m.coordination.form.values[0] = "owner, hawk"
	m = coordinationSubmit(t, m)
	if m.coordination.failed || m.coordination.form != nil || !reflect.DeepEqual(m.coordination.thread.Members, thread.Members) {
		t.Fatalf("replacement failed: %+v", m.coordination)
	}
	if _, err = agent.SetMembers(context.Background(), c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: thread.Members, RequestID: "agent-members"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("nonowner manage API accepted: %v", err)
	}
	if _, err = agent.CreateThread(context.Background(), c.CreateThread{Title: "agent thread", Engagement: "coop", Members: []string{"owner", "hawk"}, RequestID: "agent-create"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("nonowner create API accepted: %v", err)
	}
}

func TestCoordinationHTTPGoalLifecycleAndStaleAuthority(t *testing.T) {
	ctx := context.Background()
	owner, agent := coordinationMasterFixture(t)
	thread, err := owner.CreateThread(ctx, c.CreateThread{Title: "goal thread", Engagement: "coop", Members: []string{"owner", "hawk", "robin"}, RequestID: "goal-thread"})
	if err != nil {
		t.Fatal(err)
	}
	proposal := c.GoalProposal{ThreadID: thread.ID, Purpose: "Improve coordination", Scope: "bounded UI work", Repositories: []c.RepositoryScope{{Name: "sarcasticbird/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"tests pass"}, AuthorID: "hawk", PeerID: "robin"}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := coordinationFixture()
	m.services.Coordination = agent
	m, _ = coordinationKey(m, "o")
	m, _ = coordinationKey(m, string(raw))
	m = coordinationSubmit(t, m)
	if m.coordination.failed || m.coordination.authority == nil || m.coordination.authority.State != "draft" || m.coordination.authority.Valid || m.coordination.form != nil {
		t.Fatalf("proposal/resolve transition: %+v", m.coordination)
	}
	ref := m.coordination.authority.Revision.Ref
	if ref.MasterID != owner.MasterID() || len(ref.RevisionSHA256) != 64 || m.coordination.authority.Actors.Peer.PrincipalID != "robin" {
		t.Fatalf("proposal not resolved: %+v", m.coordination.authority)
	}
	if _, err = agent.DecideGoal(ctx, c.GoalDecisionInput{GoalID: ref.GoalID, RevisionSHA256: ref.RevisionSHA256, Action: "approve", RequestID: "agent-decide"}); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("nonowner decide API accepted: %v", err)
	}
	m.services.Coordination = owner
	cmd := m.coordLoad(false)
	m = coordinationRun(t, m, cmd)
	for i, action := range []string{"approve", "pause", "resume", "complete", "reopen", "approve", "reject"} {
		m, _ = coordinationKey(m, "d")
		m, _ = coordinationKey(m, action)
		m = coordinationSubmit(t, m)
		a := m.coordination.authority
		states := []string{"approved", "paused", "approved", "completed", "unapproved", "approved", "unapproved"}
		if m.coordination.failed || m.coordination.form != nil || a.DecisionSequence != int64(i+1) || a.State != states[i] || a.Valid != (action == "approve" || action == "resume") || a.Revision.Ref != ref {
			t.Fatalf("%s: authority=%+v status=%s", action, a, m.coordination.status)
		}
	}
	// A competing owner decision invalidates the already-open UI snapshot.
	m, _ = coordinationKey(m, "d")
	m, _ = coordinationKey(m, "approve")
	latest, err := owner.DecideGoal(ctx, c.GoalDecisionInput{GoalID: ref.GoalID, RevisionSHA256: ref.RevisionSHA256, ExpectedDecisionSequence: 7, Action: "approve", RequestID: "competing-approval"})
	if err != nil {
		t.Fatal(err)
	}
	m = coordinationSubmit(t, m)
	if !m.coordination.failed || !strings.Contains(m.coordination.status, c.ErrConflict.Error()) || !m.coordination.form.frozen {
		t.Fatal("stale decision sequence accepted")
	}
	m, _ = coordinationKey(m, "esc")
	proposal.GoalID, proposal.Scope, proposal.RequestID = ref.GoalID, "revised UI scope", "revision-two"
	revision, err := agent.ProposeGoal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	newAuthority, err := owner.DecideGoal(ctx, c.GoalDecisionInput{GoalID: ref.GoalID, RevisionSHA256: revision.Ref.RevisionSHA256, ExpectedDecisionSequence: latest.DecisionSequence, Action: "approve", RequestID: "approve-revision-two"})
	if err != nil {
		t.Fatal(err)
	}
	cmd = m.coordLoad(false)
	m = coordinationRun(t, m, cmd)
	if m.coordination.authority.Revision.Ref != ref || m.coordination.authority.Valid || m.coordination.authority.ActiveRevisionSHA256 != newAuthority.ActiveRevisionSHA256 {
		t.Fatal("refresh silently rebound old revision to active revision")
	}
	m, _ = coordinationKey(m, "d")
	m, _ = coordinationKey(m, "pause")
	m = coordinationSubmit(t, m)
	if !m.coordination.failed || !strings.Contains(m.coordination.status, c.ErrConflict.Error()) {
		t.Fatal("pause accepted superseded revision")
	}
	actual, err := owner.ResolveGoal(ctx, revision.Ref)
	if err != nil || !actual.Valid || actual.DecisionSequence != newAuthority.DecisionSequence {
		t.Fatalf("denied stale revision changed active authority: %+v %v", actual, err)
	}
}

func TestCoordinationCapabilitySpecificForms(t *testing.T) {
	for _, tc := range []struct{ key, capability string }{{"n", "manage"}, {"m", "manage"}, {"d", "decide"}, {"a", "read"}, {"s", "read"}, {"o", "send"}, {"c", "send"}} {
		t.Run(tc.key, func(t *testing.T) {
			m, _ := coordinationFixture()
			m.coordination.thread = &c.Thread{ID: "thread"}
			m.coordination.authority = &c.GoalAuthority{}
			m.coordination.self.Principal.Capabilities = []string{tc.capability}
			opened, cmd := coordinationKey(m, tc.key)
			if cmd != nil || opened.coordination.form == nil {
				t.Fatalf("%s alone did not permit form: %s", tc.capability, opened.coordination.status)
			}
			m.coordination.self.Principal.Capabilities = nil
			denied, cmd := coordinationKey(m, tc.key)
			if cmd != nil || denied.coordination.form != nil {
				t.Fatal("form opened without required capability")
			}
		})
	}
}

func TestCoordinationAuthorityRefreshKeepsExactReference(t *testing.T) {
	m, s := coordinationFixture()
	ref := c.GoalRef{Source: "master", MasterID: "test-master", GoalID: "goal", RevisionSHA256: strings.Repeat("a", 64)}
	m.coordination.authority = &c.GoalAuthority{Revision: c.GoalRevision{Ref: ref}, DecisionSequence: 2}
	var calls int
	s.resolve = func(got c.GoalRef) (c.GoalAuthority, error) {
		calls++
		if got != ref {
			t.Fatalf("authority refresh changed ref: %+v", got)
		}
		return c.GoalAuthority{Revision: c.GoalRevision{Ref: ref}, DecisionSequence: 3, State: "paused"}, nil
	}
	for _, key := range []string{"r", "]"} {
		var cmd tea.Cmd
		m, cmd = coordinationKey(m, key)
		m = coordinationRun(t, m, cmd)
		if m.coordination.authority.DecisionSequence != 3 || m.coordination.authority.State != "paused" {
			t.Fatal("authority did not refresh")
		}
	}
	if calls != 2 {
		t.Fatalf("expected two exact authority reads, got %d", calls)
	}
}

func TestCoordinationStatusLookupFormUsesReadOnlyIdentity(t *testing.T) {
	m, s := coordinationFixture()
	m.coordination.self.Principal.Capabilities = []string{"read"}
	s.self = m.coordination.self
	s.status = func(id string) (c.MessageReceipt, error) {
		if id != "committed-message" {
			t.Fatalf("status ID changed: %s", id)
		}
		return c.MessageReceipt{MessageID: id, Deliveries: []c.Delivery{{AgentID: "hawk", State: "blocked", Reason: "test reason"}}}, nil
	}
	m, _ = coordinationKey(m, "s")
	if m.coordination.form == nil || m.coordination.form.kind != "status" {
		t.Fatal("status lookup form did not open")
	}
	m, _ = coordinationKey(m, "committed-message")
	m = coordinationSubmit(t, m)
	if m.coordination.failed || m.coordination.form != nil || m.coordination.receipt == nil || !strings.Contains(m.View(), "blocked") || !strings.Contains(m.View(), "test reason") {
		t.Fatal("status lookup did not display receipt")
	}
}

func TestCoordinationSanitizesEveryRenderedSubview(t *testing.T) {
	poison := "safe\x1b]52;c;clipboard\a\r\t\x00\x7f\u009b\nsecond-line"
	for _, subview := range []string{"team", "threads", "inbox", "goals", "reader", "receipt", "authority", "form", "members", "decision"} {
		t.Run(subview, func(t *testing.T) {
			m, _ := coordinationFixture()
			m.coordination.self.Principal.ID = poison
			m.coordination.status = poison
			ref := c.GoalRef{GoalID: poison, RevisionSHA256: poison}
			switch subview {
			case "team":
				m.coordination.section = "team"
				m.coordination.team = []c.Presence{{Alias: poison}}
			case "threads":
				m.coordination.threads = []c.Thread{{ID: poison, Title: poison}}
			case "inbox":
				m.coordination.section = "inbox"
				m.coordination.inbox = []c.Delivery{{MessageID: poison, State: poison}}
			case "goals":
				m.coordination.section = "goals"
				m.coordination.goals = []c.GoalAuthority{{Revision: c.GoalRevision{Ref: ref}}}
			case "reader":
				m.coordination.thread = &c.Thread{ID: poison, Title: poison, Members: []string{poison}}
				m.coordination.messages = []c.Message{{ID: poison, Body: poison}}
			case "receipt":
				m.coordination.receipt = &c.MessageReceipt{MessageID: poison, Backend: poison, Deliveries: []c.Delivery{{Reason: poison, ReplyMessageID: poison}}}
			case "authority":
				m.coordination.authority = &c.GoalAuthority{Revision: c.GoalRevision{Ref: ref}, State: poison}
			default:
				m.coordination.form = &coordForm{kind: subview, requestID: poison, thread: c.Thread{ID: poison, Members: []string{poison}}, parent: poison, authority: c.GoalAuthority{Revision: c.GoalRevision{Ref: ref}}, labels: []string{"private"}, values: []string{poison}, reveal: true}
			}
			view := m.View()
			if strings.IndexFunc(view, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
				t.Fatalf("terminal control escaped sanitization: %q", view)
			}
			if !strings.Contains(view, "safe") || !strings.Contains(view, "\nsecond-line") {
				t.Fatal("sanitization erased printable content/newlines")
			}
		})
	}
}

func TestCoordinationSessionUnreadMarkersArePerThreadAndImmutable(t *testing.T) {
	m, _ := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "thread-one"}
	m.coordination.messages = []c.Message{{ID: "first", Sequence: 2}, {ID: "second", Sequence: 4}}
	m.coordination.after = 4
	m.coordination.readThrough = map[string]int64{"other-thread": 10}
	before := m
	if strings.Count(m.View(), "[unread this session]") != 2 {
		t.Fatal("newly loaded messages not marked unread")
	}
	m, cmd := coordinationKey(m, "enter")
	if cmd != nil || strings.Contains(m.View(), "[unread this session]") || m.coordination.readThrough["thread-one"] != 4 || m.coordination.readThrough["other-thread"] != 10 {
		t.Fatal("mark-read changed wrong thread or scheduled server mutation")
	}
	if before.coordination.readThrough["thread-one"] != 0 {
		t.Fatal("mark-read mutated previous model map")
	}
	out, _ := m.Update(coordResult{serial: m.coordination.serial, self: m.coordination.self, data: c.MessagePage{Messages: []c.Message{{ID: "third", Sequence: 7}}}})
	m = out.(fleetModel)
	if strings.Count(m.View(), "[unread this session]") != 1 {
		t.Fatal("new follow result did not preserve prior read boundary")
	}
	m, _ = coordinationKey(m, "esc")
	if m.coordination.readThrough["thread-one"] != 4 {
		t.Fatal("back lost session read boundary")
	}
	m.coordination.thread = &c.Thread{ID: "thread-two"}
	m.coordination.messages = []c.Message{{ID: "different", Sequence: 3}}
	if !strings.Contains(m.View(), "[unread this session]") {
		t.Fatal("another thread inherited read state")
	}
}

func TestCoordinationSmallTerminalSelectionAndPaging(t *testing.T) {
	m, _ := coordinationFixture()
	m.width, m.height = 72, 12
	m.coordination.thread = &c.Thread{ID: "thread"}
	for i := 0; i < 20; i++ {
		m.coordination.messages = append(m.coordination.messages, c.Message{ID: fmt.Sprintf("message-%02d", i), Body: fmt.Sprintf("body-%02d", i), Sequence: int64(i + 1)})
	}
	for i := 0; i < 19; i++ {
		m, _ = coordinationKey(m, "j")
	}
	if m.coordination.cursor != 19 || m.scroll == 0 || !strings.Contains(m.View(), "> [unread this session] message-19") {
		t.Fatalf("selected message offscreen at height %d: %s", m.height, m.View())
	}
	for i := 0; i < 5; i++ {
		m, _ = coordinationKey(m, "pgdown")
	}
	if !strings.Contains(m.View(), "body-19") {
		t.Fatal("paging cannot reach final private body")
	}
	if len(strings.Split(m.View(), "\n")) > m.height {
		t.Fatalf("view exceeds terminal height: %d", len(strings.Split(m.View(), "\n")))
	}
	for i := 0; i < 19; i++ {
		m, _ = coordinationKey(m, "k")
	}
	if !strings.Contains(m.View(), "> [unread this session] message-00") {
		t.Fatal("upward selection did not scroll back")
	}
	m, _ = coordinationKey(m, "esc")
	if m.scroll != 0 {
		t.Fatal("back retained reader scroll")
	}
}
