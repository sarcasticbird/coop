package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestConversationMaskedReaderJourney(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}, {Width: 40, Height: 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, s := coordinationFixture()
			m.width, m.height = size.Width, size.Height
			m.coordination.threads = []c.Thread{{ID: "first"}, {ID: "second"}}
			s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id, Members: []string{"owner", "robin"}}, nil }
			s.messages = func(id string, after int64) (c.MessagePage, error) {
				return c.MessagePage{Messages: []c.Message{{ID: "one", ThreadID: id, Sequence: 1, Body: "synthetic-private-one"}, {ID: "two", ThreadID: id, Sequence: 2, Body: "synthetic-private-two 界e\u0301"}}}, nil
			}
			m, cmd := coordinationKey(m, "enter")
			m = coordinationRun(t, m, cmd)
			if strings.Contains(m.View(), "synthetic-private") || !strings.Contains(m.View(), "body masked") {
				t.Fatal("reader did not open masked")
			}
			m, _ = coordinationKey(m, "ctrl+v")
			if !strings.Contains(m.View(), "synthetic-private-one") {
				t.Fatal("explicit reveal failed")
			}
			m, _ = coordinationKey(m, "j")
			for _, key := range []string{"p", "c"} {
				m, _ = coordinationKey(m, key)
				want := "two"
				if key == "c" {
					want = ""
				}
				if m.coordination.form == nil || m.coordination.form.parent != want || m.coordination.form.reveal {
					t.Fatal("compose/reply inherited reveal or wrong parent")
				}
				m, _ = coordinationKey(m, "esc")
			}
			m, _ = coordinationKey(m, "?")
			if strings.Contains(m.View(), "synthetic-private") || !strings.Contains(m.View(), "Threads Help") {
				t.Fatal("help exposed private content or lost context")
			}
			m, _ = coordinationKey(m, "esc")
			m, _ = coordinationKey(m, "esc")
			if m.coordination.thread != nil || m.coordination.cursor != 0 {
				t.Fatal("return lost thread selection")
			}
			m, _ = coordinationKey(m, "j")
			m, cmd = coordinationKey(m, "enter")
			m = coordinationRun(t, m, cmd)
			if m.coordination.readerReveal || strings.Contains(m.View(), "synthetic-private") || m.coordination.thread.ID != "second" {
				t.Fatal("new buffer inherited prior reveal")
			}
			assertConversationBounds(t, m)
		})
	}
}

func assertConversationBounds(t *testing.T, m fleetModel) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) > m.height {
		t.Fatalf("viewport exceeds height: %d > %d", len(lines), m.height)
	}
	for _, line := range lines {
		if textansi.StringWidth(line) > m.width {
			t.Fatalf("viewport exceeds width: %q", line)
		}
	}
}

func TestConversationFollowSelectionAndViewportAreIndependent(t *testing.T) {
	m, s := coordinationFixture()
	m.width, m.height = 40, 20
	m.coordination.thread = &c.Thread{ID: "thread"}
	m.coordination.readerReveal = true
	for i := 1; i <= 500; i++ {
		m.coordination.messages = append(m.coordination.messages, c.Message{ID: fmt.Sprintf("msg-%03d", i), Sequence: int64(i), Body: strings.Repeat("synthetic 界e\u0301 ", 8)})
	}
	m.coordination.cursor = 100
	m.coordination.messageSelected = "msg-101"
	m.revealConversationSelection()
	m, _ = coordinationKey(m, "pgdown")
	anchor, offset := m.conversationAnchor()
	s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id}, nil }
	s.messages = func(id string, after int64) (c.MessagePage, error) {
		return c.MessagePage{Messages: []c.Message{{ID: "arrival", ThreadID: id, Sequence: 501, Body: "synthetic-arrival"}}}, nil
	}
	m, cmd := coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	gotAnchor, gotOffset := m.conversationAnchor()
	if gotAnchor != anchor || gotOffset != offset || m.coordination.messages[m.coordination.cursor].ID != "msg-101" {
		t.Fatal("follow/eviction changed scroll anchor or selected stable message")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}, {Width: 40, Height: 20}} {
		out, _ := m.Update(size)
		m = out.(fleetModel)
		gotAnchor, _ = m.conversationAnchor()
		if gotAnchor != anchor || m.coordination.messageSelected != "msg-101" {
			t.Fatal("resize stole selection/viewport focus")
		}
		assertConversationBounds(t, m)
	}
	m, _ = coordinationKey(m, "p")
	if m.coordination.form.parent != "msg-101" {
		t.Fatal("reply used visible last line instead of selection")
	}
	m, _ = coordinationKey(m, "esc")
	// Removing the selected message must disable reply rather than retarget it.
	out, _ := m.coordUpdate(coordResult{serial: m.coordination.serial, self: s.self, data: c.MessagePage{Messages: makeSyntheticMessages(502, 1100)}})
	m = out.(fleetModel)
	m, _ = coordinationKey(m, "p")
	if m.coordination.cursor != -1 || m.coordination.form != nil {
		t.Fatal("evicted reply selection silently retargeted")
	}
}

func makeSyntheticMessages(first, last int) []c.Message {
	var messages []c.Message
	for i := first; i <= last; i++ {
		messages = append(messages, c.Message{ID: fmt.Sprint(i), Sequence: int64(i), Body: "synthetic"})
	}
	return messages
}

func TestConversationCancelReadRejectsLateResult(t *testing.T) {
	for _, key := range []string{"esc", "l", "i"} {
		t.Run(key, func(t *testing.T) {
			m, s := coordinationFixture()
			m.coordination.thread = &c.Thread{ID: "original"}
			s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id}, nil }
			s.messages = func(id string, after int64) (c.MessagePage, error) {
				return c.MessagePage{Messages: []c.Message{{ID: "late", ThreadID: id, Body: "synthetic-late"}}}, nil
			}
			m, cmd := coordinationKey(m, "r")
			late := cmd()
			m, _ = coordinationKey(m, key)
			serial := m.coordination.serial
			out, next := m.Update(late)
			m = out.(fleetModel)
			if next != nil || m.coordination.serial != serial || m.coordination.thread != nil || len(m.coordination.messages) != 0 || strings.Contains(m.View(), "synthetic-late") {
				t.Fatal("late read reopened departed reader")
			}
		})
	}
}

func TestConversationAuthorityAndContentFailClosed(t *testing.T) {
	for _, failure := range []string{"denied", "principal", "epoch", "thread", "message", "missing-thread", "offline"} {
		t.Run(failure, func(t *testing.T) {
			m, s := coordinationFixture()
			m.coordination.thread = &c.Thread{ID: "original"}
			m.coordination.readerSelf, m.coordination.readerMaster = s.self, s.MasterID()
			m.coordination.messages = []c.Message{{ID: "cached", Body: "synthetic-cached"}}
			m.coordination.readerReveal = true
			s.thread = func(id string) (c.Thread, error) {
				switch failure {
				case "denied":
					return c.Thread{}, c.ErrForbidden
				case "offline":
					return c.Thread{}, c.ErrUnavailable
				case "thread":
					id = "another"
				}
				return c.Thread{ID: id}, nil
			}
			s.messages = func(id string, after int64) (c.MessagePage, error) {
				if failure == "message" {
					id = "another"
				}
				if failure == "missing-thread" {
					id = ""
				}
				return c.MessagePage{Messages: []c.Message{{ID: "new", ThreadID: id, Body: "synthetic-new"}}}, nil
			}
			if failure == "principal" {
				s.self.Principal.ID = "another-principal"
			}
			if failure == "epoch" {
				s.self.RestoreEpoch++
			}
			m, cmd := coordinationKey(m, "r")
			m = coordinationRun(t, m, cmd)
			m, _ = coordinationKey(m, "ctrl+v")
			if !m.coordination.failed || m.coordination.readerReveal || strings.Contains(m.View(), "synthetic-") || m.coordination.thread.ID != "original" {
				t.Fatal("failed authority exposed or replaced private buffer")
			}
			if failure == "offline" && len(m.coordination.messages) != 1 {
				t.Fatal("transport failure discarded cached buffer")
			}
		})
	}
}

func TestConversationOpenRefusesMismatchedIdentity(t *testing.T) {
	for _, route := range []string{"threads", "inbox"} {
		m, s := coordinationFixture()
		m.coordination.section = route
		m.coordination.threads = []c.Thread{{ID: "selected"}}
		m.coordination.inbox = []c.Delivery{{MessageID: "selected"}}
		s.thread = func(string) (c.Thread, error) { return c.Thread{ID: "another-thread"}, nil }
		s.message = func(string) (c.Message, error) {
			return c.Message{ID: "another-message", ThreadID: "another-thread"}, nil
		}
		m, cmd := coordinationKey(m, "enter")
		m = coordinationRun(t, m, cmd)
		if !m.coordination.failed || m.coordination.thread != nil || strings.Contains(m.View(), "another-") {
			t.Fatalf("%s lookup accepted another captured context", route)
		}
	}
}

func TestConversationThreadListRefreshPaginationAndReturn(t *testing.T) {
	m, s := coordinationFixture()
	m.width, m.height = 40, 20
	for i := 0; i < 30; i++ {
		m.coordination.threads = append(m.coordination.threads, c.Thread{ID: fmt.Sprintf("thread-%02d", i), Title: "duplicate title"})
	}
	m.coordination.cursor = 20
	m.revealConversationSelection()
	m, _ = coordinationKey(m, "pgup") // Intentionally page away from the selected thread.
	scroll := m.scroll
	threads := append([]c.Thread(nil), m.coordination.threads...)
	threads[0], threads[20] = threads[20], threads[0]
	s.threads = func(q c.ThreadQuery) (c.ThreadPage, error) {
		if q.After == "second-page" {
			return c.ThreadPage{Threads: []c.Thread{{ID: "next-page-thread"}}}, nil
		}
		return c.ThreadPage{Threads: threads, Next: "second-page"}, nil
	}
	m, cmd := coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	if m.coordination.cursor != 0 || m.coordination.threads[m.coordination.cursor].ID != "thread-20" || m.scroll != scroll {
		t.Fatal("refresh retargeted duplicate-title thread or moved viewport")
	}
	s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id}, nil }
	s.messages = func(id string, after int64) (c.MessagePage, error) { return c.MessagePage{}, nil }
	m, cmd = coordinationKey(m, "enter")
	m = coordinationRun(t, m, cmd)
	m, _ = coordinationKey(m, "esc")
	if m.scroll != scroll || m.coordination.cursor != 0 || m.coordination.next != "second-page" {
		t.Fatal("reader return lost list context")
	}
	m, cmd = coordinationKey(m, "]")
	m = coordinationRun(t, m, cmd)
	if m.coordination.cursor != 0 || m.coordination.threads[0].ID != "next-page-thread" || m.scroll != 0 {
		t.Fatal("next page did not open at its first thread")
	}
	m.coordination.loadingNext = false
	for range 2 {
		out, _ := m.coordUpdate(coordResult{serial: m.coordination.serial, self: s.self, data: c.ThreadPage{Threads: []c.Thread{{ID: "replacement"}}}})
		m = out.(fleetModel)
	}
	if m.coordination.cursor != -1 {
		t.Fatal("removed thread selection silently retargeted after repeated refresh")
	}
}

func TestConversationResizeRetainsVisibleSelection(t *testing.T) {
	m, _ := coordinationFixture()
	m.width, m.height = 120, 40
	m.coordination.thread = &c.Thread{ID: "thread"}
	m.coordination.messages = makeSyntheticMessages(1, 30)
	m.coordination.readerReveal = true
	m.coordination.cursor = 10
	m.revealConversationSelection()
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 20}, {Width: 80, Height: 24}, {Width: 120, Height: 40}} {
		out, _ := m.Update(size)
		m = out.(fleetModel)
		if !m.conversationSelectionVisible() || !strings.Contains(m.View(), "> [unread this session] 11") {
			t.Fatal("resize hid previously visible selected message")
		}
		assertConversationBounds(t, m)
	}
}

func TestConversationWriteRejectsChangedIdentityBeforeDispatch(t *testing.T) {
	for _, key := range []string{"c", "p"} {
		for _, phase := range []string{"submit", "retry"} {
			for _, change := range []string{"principal", "kind", "epoch", "master"} {
				t.Run(key+"/"+phase+"/"+change, func(t *testing.T) {
					m, s := coordinationFixture()
					s.self.RestoreEpoch = 7
					original := s.self
					m.coordination.self = original
					m.coordination.thread = &c.Thread{ID: "captured-thread"}
					m.coordination.readerSelf, m.coordination.readerMaster = original, s.MasterID()
					m.coordination.messages = []c.Message{{ID: "selected-parent"}}
					var sent []c.SendMessage
					s.send = func(in c.SendMessage) (c.MessageReceipt, error) {
						sent = append(sent, in)
						return c.MessageReceipt{}, c.ErrUnavailable
					}
					m, _ = coordinationKey(m, key)
					m, _ = coordinationKey(m, "tab")
					m, _ = coordinationKey(m, "synthetic-private-body")
					m, _ = coordinationKey(m, "ctrl+s")
					requestID, parent := m.coordination.form.requestID, m.coordination.form.parent
					if phase == "retry" {
						var cmd tea.Cmd
						m, cmd = coordinationKey(m, "ctrl+s")
						m = coordinationRun(t, m, cmd)
						if len(sent) != 1 {
							t.Fatal("initial uncertain send did not dispatch")
						}
					}
					trigger := "ctrl+s"
					if phase == "retry" {
						trigger = "ctrl+r"
					}
					m, cmd := coordinationKey(m, trigger)
					// Change authority after scheduling, so the command must validate fresh Self.
					switch change {
					case "principal":
						s.self.Principal.ID = "another-principal"
					case "kind":
						s.self.Principal.Kind = "agent"
					case "epoch":
						s.self.RestoreEpoch++
					case "master":
						s.master = "another-master"
					}
					before := len(sent)
					m = coordinationRun(t, m, cmd)
					m, cmd = coordinationKey(m, "ctrl+r")
					m = coordinationRun(t, m, cmd)
					f := m.coordination.form
					if len(sent) != before || !m.coordination.failed || !strings.HasPrefix(m.coordination.status, coordError(c.ErrConflict)) || f == nil || !f.frozen || f.requestID != requestID || f.parent != parent || f.thread.ID != "captured-thread" || f.values[1] != "synthetic-private-body" || !reflect.DeepEqual(m.coordination.readerSelf, original) || m.coordination.readerMaster != "test-master" {
						t.Fatal("changed authority dispatched or rebound the frozen request")
					}
					// Only the original authority may retry the original immutable intent.
					s.self, s.master = original, "test-master"
					m, cmd = coordinationKey(m, "ctrl+r")
					m = coordinationRun(t, m, cmd)
					if len(sent) != before+1 || sent[before].RequestID != requestID || sent[before].ThreadID != "captured-thread" || sent[before].ParentMessageID != parent {
						t.Fatal("restored authority could not retry the captured request")
					}
					if before > 0 && !reflect.DeepEqual(sent[0], sent[before]) {
						t.Fatal("retry changed the original uncertain request")
					}
				})
			}
		}
	}
}

func TestConversationRefreshRetainsCurrentThreadPage(t *testing.T) {
	m, s := coordinationFixture()
	m.width, m.height = 40, 20
	var cursors []string
	failNext := false
	s.threads = func(q c.ThreadQuery) (c.ThreadPage, error) {
		cursors = append(cursors, q.After)
		switch q.After {
		case "":
			return c.ThreadPage{Threads: []c.Thread{{ID: "first-page-thread"}}, Next: "page-two"}, nil
		case "page-two":
			return c.ThreadPage{Threads: []c.Thread{{ID: "second-page-thread"}}, Next: "page-three"}, nil
		case "page-three":
			if failNext {
				return c.ThreadPage{}, c.ErrUnavailable
			}
			return c.ThreadPage{Threads: []c.Thread{{ID: "third-page-thread"}}}, nil
		default:
			t.Fatalf("unexpected page: %q", q.After)
			return c.ThreadPage{}, nil
		}
	}
	s.thread = func(id string) (c.Thread, error) { return c.Thread{ID: id}, nil }
	s.messages = func(id string, after int64) (c.MessagePage, error) { return c.MessagePage{}, nil }
	cmd := m.coordLoad(false)
	m = coordinationRun(t, m, cmd)
	m, cmd = coordinationKey(m, "]")
	m = coordinationRun(t, m, cmd)
	assertPageTwo := func() {
		t.Helper()
		if len(m.coordination.threads) != 1 || m.coordination.threads[0].ID != "second-page-thread" || m.coordination.cursor != 0 || m.coordination.next != "page-three" {
			t.Fatalf("lost current thread page: rows=%v cursor=%d next=%q", m.coordination.threads, m.coordination.cursor, m.coordination.next)
		}
	}
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	assertPageTwo()
	m, _ = coordinationKey(m, "f")
	m.refreshing = true // Isolate coordination follow from fleet reads.
	out, cmd := m.Update(fleetTick(time.Now()))
	m = out.(fleetModel)
	batch := cmd().(tea.BatchMsg)
	m = coordinationRun(t, m, batch[0])
	assertPageTwo()
	m, cmd = coordinationKey(m, "enter")
	m = coordinationRun(t, m, cmd)
	m, _ = coordinationKey(m, "esc")
	assertPageTwo()
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	assertPageTwo()
	// A cancelled advance and its late result must leave page two current.
	m, cmd = coordinationKey(m, "]")
	late := cmd()
	m, _ = coordinationKey(m, "esc")
	out, _ = m.Update(late)
	m = out.(fleetModel)
	assertPageTwo()
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	assertPageTwo()
	// A failed page advance must not change the page used by refresh/reconnect.
	failNext = true
	m, cmd = coordinationKey(m, "]")
	m = coordinationRun(t, m, cmd)
	assertPageTwo()
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	assertPageTwo()
	if !reflect.DeepEqual(cursors, []string{"", "page-two", "page-two", "page-two", "page-two", "page-three", "page-two", "page-three", "page-two"}) {
		t.Fatalf("refresh/follow changed page query: %v", cursors)
	}
}
