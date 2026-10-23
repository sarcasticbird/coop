package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type progressOpenCode struct {
	OpenCode
	fingerprint string
}

func (c *progressOpenCode) Observe(ctx context.Context, j Job) (Turn, error) {
	turn, err := c.OpenCode.Observe(ctx, j)
	turn.ProgressFingerprint = c.fingerprint
	return turn, err
}

func diagnosticJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKataDiagnosticReferenceScope(t *testing.T) {
	const project = "01M5MCPEHHP4C98MZSHJEE3M8D"
	const ref = "kata://01m5mcpehhp4c98mzshjee3m8d/01M5MFMZF2MPV5MHF84G9RVGA8/owner/repo"
	cfg := Config{CodeOwners: []string{"owner"}, Repositories: []string{"owner/repo"}, TaskSource: &TaskSourceConfig{Provider: "kata", ProjectUID: project}}
	if got := diagnosticReference(cfg, ref); got != ref {
		t.Fatalf("scoped canonical Kata identity discarded: %q", got)
	}
	for _, bad := range []string{strings.Replace(ref, "01m5mcpehhp4c98mzshjee3m8d", "01m5mfmzf2mpv5mhf84g9rvga8", 1), strings.Replace(ref, "owner/repo", "owner/other", 1), strings.Replace(ref, "owner/repo", "foreign/repo", 1), ref + "?token=secret", ref + "/extra", strings.Replace(ref, "01m5mcpehhp4c98mzshjee3m8d", project, 1)} {
		if diagnosticReference(cfg, bad) != "" {
			t.Fatalf("unscoped Kata reference accepted: %s", bad)
		}
	}
	legacy := "https://github.com/owner/repo/issues/12"
	if diagnosticReference(cfg, legacy) != legacy || diagnosticReference(cfg, "https://github.com/owner/other/pull/1") != "" {
		t.Fatal("GitHub diagnostic scope changed")
	}
	cfg.TaskSource = nil
	if diagnosticReference(cfg, ref) != "" {
		t.Fatal("Kata ref accepted without pinned source")
	}
}

func TestHeartbeatDoesNotAdvanceProgress(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	now := time.Now().UTC()
	s := Status{State: "working", Current: &CurrentWork{Stage: "implement", Phase: "active"}}
	b := newDiagnosticBuilder(now)
	for i := range 3 {
		s.Heartbeat, s.LastPoll, s.NextRetry = now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i)*time.Minute), now.Add(time.Hour)
		d := b.snapshot(cfg, 1, s, nil, c.DiagnosticService{Available: true, Requests: i, LastSuccess: s.LastPoll}, c.DiagnosticService{Available: true, Requests: i}, s.Heartbeat)
		if v := diagnosticJSON(t, d.Work)["progress_at"]; v != nil {
			t.Fatalf("heartbeat invented progress: %v", v)
		}
	}
}

func TestNativeAcceptedProgressSurvivesRestart(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
	v.Task.Task.Title = "Repair worker progress"
	// Update the existing source snapshot; progress must not fetch a second catalog.
	for i := range g.catalog.Tasks {
		if g.catalog.Tasks[i].Task.URL == v.Job.TaskURL {
			g.catalog.Tasks[i].Task.Title = v.Task.Task.Title
		}
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	d.active = &x
	work := diagnosticJSON(t, d.Progress().Current)
	if work["progress_at"] != clock.Now().Format(time.RFC3339Nano) || work["work_title"] != "Repair worker progress" {
		t.Fatalf("accepted native checkpoint absent: %+v", work)
	}
	clock.now = clock.now.Add(time.Minute)
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	if got := diagnosticJSON(t, d.Progress().Current)["progress_at"]; got != work["progress_at"] {
		t.Fatalf("unchanged running poll advanced progress: %v", got)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticJSON(t, restarted.Progress().Current); got["progress_at"] != work["progress_at"] || got["work_title"] != work["work_title"] {
		t.Fatalf("restart lost checkpoint/title: %+v", got)
	}
	// A changed owned tool observation must cross the same durable writer as
	// acceptance, then remain stable across another observation and restart.
	restarted.code = &progressOpenCode{OpenCode: code, fingerprint: strings.Repeat("a", 64)}
	clock.now = clock.now.Add(time.Minute)
	checkpoint := clock.now
	if _, err := restarted.advance(t.Context(), restarted.active); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute)
	if _, err := restarted.advance(t.Context(), restarted.active); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Progress().Current.ProgressAt; got == nil || !got.Equal(checkpoint) {
		t.Fatalf("owned checkpoint replayed or was not persisted: %v", got)
	}
}

func TestNativeOwnedObservationChangesOnce(t *testing.T) {
	j := Job{SessionID: "ses_task", MessageID: "msg_worker"}
	status := "running"
	foreign := false
	cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/ses_task/message/msg_worker":
			writeJSON(w, ownershipMessage(j, j.MessageID, "user", ""))
		case "/session/ses_task/message":
			assistant := ownershipMessage(j, "msg_assistant", "assistant", j.MessageID)
			assistant["parts"] = []any{map[string]any{"id": "prt_tool", "type": "tool", "callID": "tool_1", "state": map[string]any{"status": status, "input": map[string]string{"secret": "never-retain"}, "output": "never-retain"}}}
			rows := []any{ownershipMessage(j, j.MessageID, "user", ""), assistant}
			if foreign {
				rows = append(rows, ownershipMessage(j, "msg_manual", "user", ""))
			}
			writeJSON(w, rows)
		case "/session/status":
			writeJSON(w, map[string]any{j.SessionID: map[string]string{"type": "busy"}})
		case "/permission", "/question":
			writeJSON(w, []any{})
		default:
			t.Errorf("unexpected native read: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	j.Checkout = filepath.Join(cfg.WorkRoot, "task")
	first, err := client.Observe(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := diagnosticJSON(t, first)["ProgressFingerprint"]
	if fingerprint == nil || fingerprint == "" {
		t.Fatal("owned native tool state has no progress fingerprint")
	}
	repeated, err := client.Observe(t.Context(), j)
	if err != nil || diagnosticJSON(t, repeated)["ProgressFingerprint"] != fingerprint {
		t.Fatal("identical response changed fingerprint", err)
	}
	status = "completed"
	changed, err := client.Observe(t.Context(), j)
	if err != nil || diagnosticJSON(t, changed)["ProgressFingerprint"] == fingerprint {
		t.Fatal("owned tool checkpoint not observed", err)
	}
	foreign = true
	if _, err := client.Observe(t.Context(), j); err == nil {
		t.Fatal("foreign native turn treated as owned progress")
	}
	raw, _ := json.Marshal(changed)
	if strings.Contains(string(raw), "never-retain") {
		t.Fatal("native content retained in progress")
	}
}

func TestOwnedCheckpointAdvancesOnlyOnce(t *testing.T) {
	now := time.Now().UTC()
	if got := observedNativeProgress(Job{}, Turn{Accepted: true, ProgressFingerprint: strings.Repeat("a", 64)}, now); got.ProgressAt != nil {
		t.Fatal("unbound observation invented progress")
	}
	j := recordNativeProgress(Job{SessionID: "ses_owned", MessageID: "msg_owned"}, strings.Repeat("f", 64), now.Add(-time.Minute))
	turn := Turn{Accepted: true, SessionID: j.SessionID, MessageID: j.MessageID, ProgressFingerprint: strings.Repeat("a", 64)}
	j = observedNativeProgress(j, turn, now)
	if j.ProgressAt == nil || !j.ProgressAt.Equal(now) {
		t.Fatal("new owned checkpoint not recorded", j)
	}
	turn.ProgressFingerprint = strings.Repeat("b", 64)
	// Two checkpoints in one clock tick still persist the newest fingerprint.
	j = observedNativeProgress(j, turn, now)
	j = observedNativeProgress(j, turn, now.Add(time.Minute))
	if !j.ProgressAt.Equal(now) {
		t.Fatal("repeated checkpoint advanced a second time", j.ProgressAt)
	}
	turn.ProgressFingerprint = strings.Repeat("c", 64)
	j = observedNativeProgress(j, turn, now.Add(2*time.Minute))
	if !j.ProgressAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatal("new checkpoint failed to advance", j.ProgressAt)
	}
	turn.MessageID = "msg_foreign"
	turn.ProgressFingerprint = strings.Repeat("d", 64)
	j = observedNativeProgress(j, turn, now.Add(3*time.Minute))
	if !j.ProgressAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatal("foreign checkpoint advanced progress")
	}
}

func TestOldNativeObservationBaselinesWithoutInventingProgress(t *testing.T) {
	now := time.Now().UTC()
	j := Job{SessionID: "ses_owned", MessageID: "msg_owned"}
	turn := Turn{Accepted: true, SessionID: j.SessionID, MessageID: j.MessageID, ProgressFingerprint: strings.Repeat("a", 64)}
	j = observedNativeProgress(j, turn, now)
	if j.ProgressAt != nil {
		t.Fatalf("old native observation acquired restart time: %v", j.ProgressAt)
	}
	// The baseline itself must persist so repeated polls and another restart
	// cannot invent progress. A later owned tool state change is new evidence.
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var restarted Job
	if err := json.Unmarshal(raw, &restarted); err != nil {
		t.Fatal(err)
	}
	j = observedNativeProgress(restarted, turn, now.Add(time.Minute))
	if j.ProgressAt != nil {
		t.Fatal("unchanged old native observation advanced progress")
	}
	turn.ProgressFingerprint = strings.Repeat("b", 64)
	j = observedNativeProgress(j, turn, now.Add(2*time.Minute))
	if j.ProgressAt == nil || !j.ProgressAt.Equal(now.Add(2*time.Minute)) {
		t.Fatal("new owned checkpoint after baseline was lost")
	}
}

func TestDiagnosticSourceTitleBoundsAndOldProgressUnknown(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	now := time.Now().UTC()
	var old Job
	if err := json.Unmarshal([]byte(`{"TaskURL":"https://github.com/code/repo/issues/1","Phase":"active"}`), &old); err != nil {
		t.Fatal(err)
	}
	source := Snapshot{Tasks: []Task{{URL: old.TaskURL, Title: "  Repair\n worker\tprogress\x00\u202e  "}}}
	w := currentWork(cfg, source, old, now)
	if w.WorkTitle != "Repair worker progress" || w.ProgressAt != nil {
		t.Fatalf("source title or missing evidence mishandled: %+v", w)
	}
	w.WorkTitle = strings.Repeat("界", 200) + "\x1b\n"
	d := newDiagnosticBuilder(now).snapshot(cfg, 1, Status{Current: w}, nil, c.DiagnosticService{}, c.DiagnosticService{}, now)
	if len(d.Work.WorkTitle) > c.MaxDiagnosticTitleBytes || !c.ValidDiagnosticTitle(d.Work.WorkTitle) || d.Work.WorkTitle == "" {
		t.Fatalf("unsafe source title: %q", d.Work.WorkTitle)
	}
	raw, _ := json.Marshal(old)
	if strings.Contains(string(raw), "ProgressAt") || strings.Contains(string(raw), "WorkTitle") || strings.Contains(string(raw), "ProgressFingerprint") {
		t.Fatal("old nil fields changed journal JSON", string(raw))
	}
	var report c.WorkerDiagnostics
	if err := json.Unmarshal([]byte(`{"work":{"observed_at":"2026-10-23T00:00:00Z"}}`), &report); err != nil || report.Work.ProgressAt != nil {
		t.Fatal("old report claims progress", err)
	}
}

func TestBlockedDiagnosticRetainsOwnedTaskCheckpoint(t *testing.T) {
	cfg, m, g, code, x := nativeTerminalWaitFixture(t)
	now := time.Now().UTC()
	x.Job.ProgressAt, x.Job.WorkTitle = &now, "Waiting for review"
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Progress()
	report := newDiagnosticBuilder(now).snapshot(cfg, 1, Status{Native: &p}, nil, c.DiagnosticService{}, c.DiagnosticService{}, now)
	if report.Work.ProgressAt == nil || !report.Work.ProgressAt.Equal(now) || report.Work.WorkTitle != "Waiting for review" || report.Work.TaskURL != x.Job.TaskURL {
		t.Fatalf("blocked task evidence disappeared: %+v", report.Work)
	}
}
