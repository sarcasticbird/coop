package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func composeKey(m fleetModel, key tea.KeyType) (fleetModel, tea.Cmd) {
	out, cmd := m.Update(tea.KeyMsg{Type: key})
	return out.(fleetModel), cmd
}

func composeReview(m fleetModel) fleetModel {
	lines, _, _ := m.content()
	for range len(lines) {
		m, _ = coordinationKey(m, "pgdown")
	}
	return m
}

func assertComposeBounds(t *testing.T, m fleetModel) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) > m.height {
		t.Fatalf("view height %d exceeds %d", len(lines), m.height)
	}
	for _, line := range lines {
		if textansi.StringWidth(line) > m.width {
			t.Fatalf("view width %d exceeds %d", textansi.StringWidth(line), m.width)
		}
	}
}

func TestComposeCreationFullPageAndCapturedPreview(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			owner, _ := coordinationMasterFixture(t)
			m, _ := coordinationFixture()
			m.services.Coordination = owner
			var err error
			m.coordination.self, err = owner.Self(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			m.width, m.height = size[0], size[1]
			m, _ = coordinationKey(m, "n")
			for i, value := range []string{"Synthetic creation", "coop", "owner,hawk,robin"} {
				if i > 0 {
					m, _ = coordinationKey(m, "tab")
				}
				m, _ = coordinationKey(m, value)
			}
			id := m.coordination.form.requestID
			m, _ = coordinationKey(m, "esc")
			m, _ = coordinationKey(m, "k")
			m, _ = coordinationKey(m, "n")
			if m.coordination.form.requestID != id || len(m.coordination.drafts) != 0 {
				t.Fatal("new-thread entry did not resume the original intent")
			}
			m, cmd := coordinationKey(m, "ctrl+s")
			if cmd != nil || !m.coordination.form.preview {
				t.Fatal("creation preview dispatched")
			}
			m, _ = coordinationKey(m, "ctrl+v")
			preview := strings.Join(m.composeLines(), "\n")
			for _, want := range []string{"test-master", "owner (owner)", "hawk,owner,robin", "coop", id, "no message or wake"} {
				if !strings.Contains(preview, want) {
					t.Fatalf("preview omitted %s", want)
				}
			}
			assertComposeBounds(t, m)
			m = composeReview(m)
			m, cmd = coordinationKey(m, "ctrl+s")
			m = coordinationRun(t, m, cmd)
			if m.coordination.failed || m.coordination.form != nil || m.coordination.thread == nil || len(m.coordination.messages) != 0 || m.coordination.readerReveal {
				t.Fatal("creation did not open an empty masked reader")
			}
			messages, err := owner.Messages(context.Background(), m.coordination.thread.ID, 0)
			if err != nil || len(messages.Messages) != 0 {
				t.Fatal("thread creation produced a message")
			}
			assertComposeBounds(t, m)
		})
	}
}

func TestComposePrivateDraftLifecycleAndCursor(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := coordinationFixture()
			m.width, m.height = size[0], size[1]
			m.coordination.thread = &c.Thread{ID: "original", Engagement: "coop", Members: []string{"owner", "hawk"}}
			m.coordination.messages = []c.Message{{ID: "selected-parent", Body: "synthetic-reader-secret"}}
			m, _ = coordinationKey(m, "p")
			m, _ = coordinationKey(m, "tab")
			m, _ = coordinationKey(m, "synthetic-draft-secret\n界e\u0301")
			m, _ = composeKey(m, tea.KeyLeft)
			m, _ = coordinationKey(m, "X")
			m, _ = coordinationKey(m, "ctrl+v")
			f := m.coordination.form
			values, positions, id, scroll := slices.Clone(f.values), slices.Clone(f.positions), f.requestID, m.scroll
			m, _ = coordinationKey(m, "esc")
			if strings.Contains(m.View(), "synthetic-draft-secret") {
				t.Fatal("leave decision exposed body")
			}
			m, _ = coordinationKey(m, "r")
			if m.coordination.form.requestID != id || !reflect.DeepEqual(m.coordination.form.positions, positions) {
				t.Fatal("Return changed editor")
			}
			m, _ = coordinationKey(m, "esc")
			m, _ = coordinationKey(m, "k")
			if m.coordination.form != nil || len(m.coordination.drafts) != 1 {
				t.Fatal("Keep did not suspend in memory")
			}
			// An arrival and resize must not replace the selected parent or draft.
			m.coordination.messages = append(m.coordination.messages, c.Message{ID: "arrival"})
			out, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
			m = out.(fleetModel)
			m, _ = coordinationKey(m, "p")
			f = m.coordination.form
			if f.requestID != id || f.parent != "selected-parent" || !reflect.DeepEqual(f.values, values) || !reflect.DeepEqual(f.positions, positions) || !f.reveal || m.scroll != scroll || len(m.coordination.drafts) != 0 {
				t.Fatal("resume lost original buffer/context/focus/viewport")
			}
			m, _ = composeKey(m, tea.KeyF1)
			if !m.help || strings.Contains(m.View(), "synthetic-draft-secret") {
				t.Fatal("help exposed private body")
			}
			m, _ = coordinationKey(m, "esc")
			m, _ = composeKey(m, tea.KeyCtrlC)
			if m.coordination.leaving != "quit" || strings.Contains(m.View(), "synthetic-draft-secret") {
				t.Fatal("quit bypassed draft warning")
			}
			assertComposeBounds(t, m)
			m, _ = coordinationKey(m, "r")
			m, _ = coordinationKey(m, "esc")
			m, _ = coordinationKey(m, "d")
			if m.coordination.form != nil || len(m.coordination.drafts) != 0 || strings.Contains(m.View(), "synthetic-draft-secret") {
				t.Fatal("discard retained active private buffer")
			}
		})
	}
}

func TestComposeFrozenDraftResumesIdenticalRetry(t *testing.T) {
	m, s := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "original", Members: []string{"owner", "hawk"}}
	m.coordination.messages = []c.Message{{ID: "parent"}}
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "synthetic-kept-root")
	m, _ = coordinationKey(m, "esc")
	m, _ = coordinationKey(m, "k")
	var sent []c.SendMessage
	s.send = func(in c.SendMessage) (c.MessageReceipt, error) {
		sent = append(sent, in)
		if len(sent) == 1 {
			return c.MessageReceipt{}, c.ErrUnavailable
		}
		return c.MessageReceipt{MessageID: "committed", MasterID: s.MasterID(), Sequence: 4}, nil
	}
	m, _ = coordinationKey(m, "p")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "synthetic-uncertain-secret")
	m = coordinationSubmit(t, m)
	id := m.coordination.form.requestID
	m, _ = coordinationKey(m, "esc")
	m, _ = coordinationKey(m, "k")
	// Even root-compose must reopen uncertainty rather than allocate a new send.
	m, cmd := coordinationKey(m, "c")
	if cmd != nil || !m.coordination.form.frozen || m.coordination.form.requestID != id {
		t.Fatal("uncertain intent was replaced or automatically retried")
	}
	m, _ = coordinationKey(m, "attempted edit")
	m, cmd = coordinationKey(m, "ctrl+s")
	if cmd != nil || len(sent) != 1 {
		t.Fatal("frozen submit made a new write")
	}
	m, cmd = coordinationKey(m, "ctrl+r")
	m = coordinationRun(t, m, cmd)
	if len(sent) != 2 || !reflect.DeepEqual(sent[0], sent[1]) || m.coordination.form != nil || m.coordination.receipt.MessageID != "committed" {
		t.Fatal("retry changed captured payload or failed to clear buffer")
	}
	if !reflect.DeepEqual(sent[0].ExpectedMembers, []string{"owner", "hawk"}) || sent[0].ParentMessageID != "parent" {
		t.Fatal("send omitted captured authority/parent")
	}
}

func TestComposeCreationFailsClosedOnIdentityAndPermissionChange(t *testing.T) {
	for _, change := range []string{"principal", "kind", "epoch", "master", "permission"} {
		t.Run(change, func(t *testing.T) {
			m, s := coordinationFixture()
			m, _ = coordinationKey(m, "n")
			m.coordination.form.values = []string{"synthetic", "coop", "owner,hawk"}
			m, _ = coordinationKey(m, "ctrl+s")
			m, cmd := coordinationKey(m, "ctrl+s")
			switch change {
			case "principal":
				s.self.Principal.ID = "replacement"
			case "kind":
				s.self.Principal.Kind = "agent"
			case "epoch":
				s.self.RestoreEpoch++
			case "master":
				s.master = "replacement-master"
			case "permission":
				s.self.Principal.Capabilities = []string{"read"}
			}
			// An unexpected create panics through the embedded interface mock.
			m = coordinationRun(t, m, cmd)
			if !m.coordination.failed || !m.coordination.form.frozen {
				t.Fatal("changed authority did not fail closed")
			}
		})
	}
}

func TestComposeDiagnosticsAndQuitProtectKeptDrafts(t *testing.T) {
	privateHome := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, privateHome)
	}
	m, _ := coordinationFixture()
	m, _ = coordinationKey(m, "n")
	m, _ = coordinationKey(m, "synthetic-diagnostic-secret")
	for _, value := range []any{m, m.coordination, m.coordination.form} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "synthetic-diagnostic-secret") {
				t.Fatal("diagnostic formatting exposed body")
			}
		}
	}
	var diagnostics bytes.Buffer
	slog.New(slog.NewTextHandler(&diagnostics, nil)).Info("synthetic diagnostics", "model", m, "coordination", m.coordination, "form", m.coordination.form)
	if strings.Contains(diagnostics.String(), "synthetic-diagnostic-secret") || strings.Contains(m.coordination.status, "synthetic-diagnostic-secret") {
		t.Fatal("logs or status exposed private input")
	}
	m, _ = coordinationKey(m, "esc")
	m, _ = coordinationKey(m, "k")
	m.workspace = 0
	m, cmd := coordinationKey(m, "q")
	if cmd != nil || m.coordination.leaving != "quit" {
		t.Fatal("quit from Fleet lost kept draft without warning")
	}
	m, cmd = coordinationKey(m, "d")
	if cmd == nil || cmd() != tea.Quit() || m.coordination.form != nil || len(m.coordination.drafts) != 0 {
		t.Fatal("explicit quit did not clear owned buffers")
	}
	entries, err := os.ReadDir(privateHome)
	if err != nil || len(entries) != 0 {
		t.Fatal("draft lifecycle wrote user config/cache/state files")
	}
}

func TestComposeRechecksAuthorityWithoutSending(t *testing.T) {
	m, s := coordinationFixture()
	m.coordination.thread = &c.Thread{ID: "thread"}
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "synthetic-resume-secret")
	m, _ = coordinationKey(m, "ctrl+v")
	m, _ = coordinationKey(m, "esc")
	m, _ = coordinationKey(m, "k")
	original := s.self
	s.self.Principal.ID = "replacement"
	m.coordination.self = s.self
	m, _ = coordinationKey(m, "c")
	if strings.Contains(m.View(), "synthetic-resume-secret") {
		t.Fatal("changed principal exposed kept draft")
	}
	m, cmd := composeKey(m, tea.KeyCtrlL)
	m = coordinationRun(t, m, cmd)
	m, _ = coordinationKey(m, "ctrl+v")
	if !m.coordination.failed || strings.Contains(m.View(), "synthetic-resume-secret") {
		t.Fatal("identity recheck rebound captured intent")
	}
	s.self = original
	m, cmd = composeKey(m, tea.KeyCtrlL)
	m = coordinationRun(t, m, cmd)
	m, _ = coordinationKey(m, "ctrl+v")
	if m.coordination.failed || !strings.Contains(m.View(), "synthetic-resume-secret") || m.coordination.form.self.Principal.ID != original.Principal.ID {
		t.Fatal("original identity could not recover its unchanged buffer")
	}
}

func TestComposeHTTPMembershipChangeFailsClosed(t *testing.T) {
	owner, _ := coordinationMasterFixture(t)
	ctx := context.Background()
	thread, err := owner.CreateThread(ctx, c.CreateThread{Title: "synthetic membership", Engagement: "coop", Members: []string{"owner", "hawk", "robin"}, RequestID: "membership-thread"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := coordinationFixture()
	m.services.Coordination = owner
	m.coordination.self, err = owner.Self(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.coordination.thread = &thread
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "hawk")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, "synthetic-membership-secret")
	_, err = owner.SetMembers(ctx, c.SetMembers{ThreadID: thread.ID, ExpectedMembers: thread.Members, Members: []string{"owner", "hawk"}, RequestID: "change-unaddressed-member"})
	if err != nil {
		t.Fatal(err)
	}
	m = coordinationSubmit(t, m)
	if !m.coordination.failed || m.coordination.form == nil || !m.coordination.form.frozen || !strings.HasPrefix(m.coordination.status, c.ErrConflict.Error()) {
		t.Fatal("membership change did not fail closed")
	}
	page, err := owner.Messages(ctx, thread.ID, 0)
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("stale-member send committed a message")
	}
}

func TestComposeResizeKeepsVisibleCursorAndPinnedContext(t *testing.T) {
	m, _ := coordinationFixture()
	m.width, m.height = 120, 40
	m.coordination.thread = &c.Thread{ID: "thread"}
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, strings.Repeat("界e\u0301 synthetic literal ", 80))
	m, _ = coordinationKey(m, "ctrl+v")
	value, pos := m.coordination.form.values[1], m.coordination.form.positions[1]
	for _, size := range [][2]int{{40, 20}, {80, 24}, {120, 40}} {
		out, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = out.(fleetModel)
		probe := m
		probe.revealComposeCursor()
		if m.scroll != probe.scroll || !strings.Contains(m.View(), "Master: test-master") || !strings.Contains(m.View(), "Principal: owner") || m.coordination.form.values[1] != value || m.coordination.form.positions[1] != pos {
			t.Fatal("resize lost visible cursor, pinned context or literal text")
		}
		assertComposeBounds(t, m)
	}
}

func TestComposePreviewRequiresCompleteReviewAndReceiptIdentity(t *testing.T) {
	m, s := coordinationFixture()
	m.width, m.height = 40, 20
	m.coordination.thread = &c.Thread{ID: "thread"}
	m, _ = coordinationKey(m, "c")
	m, _ = coordinationKey(m, "tab")
	m, _ = coordinationKey(m, strings.Repeat("synthetic line\n", 30))
	m, _ = coordinationKey(m, "ctrl+v")
	m, _ = coordinationKey(m, "ctrl+s")
	m, cmd := coordinationKey(m, "ctrl+s")
	if cmd != nil || m.coordination.form.frozen {
		t.Fatal("long preview submitted without complete review")
	}
	assertComposeBounds(t, m)
	m = composeReview(m)
	s.send = func(c.SendMessage) (c.MessageReceipt, error) {
		return c.MessageReceipt{MessageID: "committed", MasterID: s.MasterID(), RestoreEpoch: 1, Sequence: 42, Backend: "master", Deliveries: []c.Delivery{{AgentID: "hawk", State: "queued"}}}, nil
	}
	m, cmd = coordinationKey(m, "ctrl+s")
	m = coordinationRun(t, m, cmd)
	if m.coordination.receipt == nil {
		t.Fatal("reviewed preview did not submit")
	}
	assertComposeBounds(t, m)
	for _, want := range []string{"committed", "epoch: 1", "sequence: 42", "queued", "not acknowledgment, execution or wake"} {
		if !strings.Contains(strings.Join(m.composeLines(), "\n"), want) {
			t.Fatalf("receipt omitted %s", want)
		}
	}
	s.status = func(string) (c.MessageReceipt, error) { return c.MessageReceipt{MessageID: "wrong-message"}, nil }
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	if !m.coordination.failed || m.coordination.receipt.MessageID != "committed" {
		t.Fatal("status retargeted committed receipt")
	}
}

func TestComposeHTTPExplicitEmptyMembershipFailsClosed(t *testing.T) {
	owner, _ := coordinationMasterFixture(t)
	ctx := context.Background()
	thread, err := owner.CreateThread(ctx, c.CreateThread{Title: "synthetic empty guard", Engagement: "coop", Members: []string{"owner", "hawk"}, RequestID: "empty-guard-thread"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.SendMessage{ThreadID: thread.ID, ExpectedMembers: []string{}, Recipients: []string{"hawk"}, Body: "synthetic empty-guard body", RequestID: "empty-guard-send"}
	if _, err := owner.Send(ctx, in); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("HTTP empty guard: got %v, want conflict", err)
	}
	page, err := owner.Messages(ctx, thread.ID, 0)
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("explicit-empty membership committed a message")
	}
	// A legacy absent guard must retain its previous HTTP contract.
	in.ExpectedMembers, in.RequestID = nil, "legacy-unguarded-send"
	receipt, err := owner.Send(ctx, in)
	if err != nil || receipt.MessageID == "" {
		t.Fatalf("legacy nil guard failed: %v", err)
	}
	page, err = owner.Messages(ctx, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != receipt.MessageID {
		t.Fatal("legacy send did not commit exactly one message")
	}
}

func TestComposeQuitWarningOwnsOverlayAndRestoresReturn(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		for _, overlay := range []string{"active-help", "kept-fleet-help", "kept-servers-services"} {
			t.Run(fmt.Sprint(size)+"/"+overlay, func(t *testing.T) {
				m, _ := coordinationFixture()
				m.width, m.height = size[0], size[1]
				m, _ = coordinationKey(m, "n")
				m, _ = coordinationKey(m, "synthetic-overlay-secret")
				id := m.coordination.form.requestID
				if overlay == "active-help" {
					m, _ = composeKey(m, tea.KeyF1)
				} else {
					m, _ = coordinationKey(m, "esc")
					m, _ = coordinationKey(m, "k")
					if overlay == "kept-fleet-help" {
						m, _ = coordinationKey(m, "1")
						m, _ = coordinationKey(m, "?")
					} else {
						m, _ = coordinationKey(m, "3")
						m, _ = coordinationKey(m, "v")
					}
				}
				for range 5 {
					m, _ = coordinationKey(m, "pgdown")
				}
				before, workspace, scroll, help, runbook := m.View(), m.workspace, m.scroll, m.help, m.runbook
				m, cmd := composeKey(m, tea.KeyCtrlC)
				if cmd != nil || m.coordination.leaving != "quit" {
					t.Fatal("quit bypassed draft decision")
				}
				view := m.View()
				for _, want := range []string{"Draft protection", "Quit destroys all active and kept drafts.", "Discard all and quit", "Return"} {
					if !strings.Contains(strings.ReplaceAll(view, "\n", ""), want) {
						t.Fatalf("quit warning omitted %q over %s", want, overlay)
					}
				}
				if strings.Contains(view, "synthetic-overlay-secret") || strings.Contains(view, "Composer Help") || strings.Contains(view, "Coop build:") {
					t.Fatal("overlay or private input replaced quit warning")
				}
				assertComposeBounds(t, m)
				m, _ = coordinationKey(m, "r")
				if m.coordination.leaving != "" || m.workspace != workspace || m.scroll != scroll || m.help != help || m.runbook != runbook || m.View() != before {
					t.Fatal("Return lost prior workspace, overlay or viewport")
				}
				if overlay == "active-help" {
					if m.coordination.form == nil || m.coordination.form.requestID != id {
						t.Fatal("Return replaced the active intent")
					}
				} else if len(m.coordination.drafts) != 1 || m.coordination.drafts[0].requestID != id {
					t.Fatal("Return replaced the kept intent")
				}
				m, _ = composeKey(m, tea.KeyCtrlC)
				m, cmd = coordinationKey(m, "d")
				if cmd == nil || cmd() != tea.Quit() || m.coordination.form != nil || len(m.coordination.drafts) != 0 {
					t.Fatal("explicit discard-and-quit did not clear drafts")
				}
			})
		}
	}
}
