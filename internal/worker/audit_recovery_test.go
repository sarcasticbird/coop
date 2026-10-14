package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These audit tests assert the observed defects at 04e6dff, not desired behavior.
func TestAuditMissingSubmissionStaysActiveAfterDeadlineAndRestart(t *testing.T) {
	aborts, missing := 0, 0
	_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/global/health":
			writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
		case strings.Contains(r.URL.Path, "/message/"):
			missing++
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/abort"):
			aborts++
			writeJSON(w, true)
		case r.URL.Path == "/session/status":
			writeJSON(w, map[string]any{})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	e, g, _, clock, store := engineFixture(t)
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	j := Select(e.Config, g.snapshot, e.state, clock.Now())[0]
	if _, err := g.Claim(context.Background(), j, e.state.InstanceID); err != nil {
		t.Fatal(err)
	}
	e.state.SnapshotCache = g.snapshot
	j.Phase, j.SubmissionState = "active", "submitting"
	j.SessionID, j.MessageID = "ses_audit", "msg_neverposted"
	j.Checkout = filepath.Join(e.Config.WorkRoot, "audit")
	j.SubmittedAt = clock.Now()
	j.Deadline = clock.Now().Add(time.Minute)
	if err := e.saveJob(j); err != nil {
		t.Fatal(err)
	}
	clock.now = j.Deadline.Add(time.Minute)
	for range 2 {
		restarted := &Engine{Config: e.Config, GitHub: g, Store: store, Checkouts: e.Checkouts, OpenCode: client, Clock: clock}
		for range 3 {
			tick(t, restarted, clock)
		}
	}
	got := store.state.Jobs[j.ID]
	if got.Phase != "active" || got.Attempts != 0 || aborts != 6 || missing != 6 {
		t.Fatalf("phase=%s attempts=%d aborts=%d missing=%d", got.Phase, got.Attempts, aborts, missing)
	}
	t.Logf("expired unposted intent remains active across two restarts: attempts=%d, aborts=%d, message 404s=%d", got.Attempts, aborts, missing)
}

func TestAuditPauseAbortsLaterManualTurn(t *testing.T) {
	e, _, code, clock, store := engineFixture(t)
	code.running = true
	tick(t, e, clock)
	manualRunning, aborted := true, false
	_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/abort"):
			aborted = manualRunning
			manualRunning = false
			writeJSON(w, true)
		case r.URL.Path == "/session/status":
			writeJSON(w, map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/message"):
			writeJSON(w, []any{map[string]any{"info": map[string]string{"id": "msg_manual", "sessionID": code.last.SessionID, "role": "user"}}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	// The owned turn finished between polls, and a human started another turn
	// in that same session before the worker observed completion.
	e.OpenCode = client
	store.paused = true
	tick(t, e, clock)
	if !aborted || manualRunning || store.state.Jobs[code.last.ID].Phase != "paused" {
		t.Fatal("expected session-wide abort to cancel the later manual turn")
	}
	t.Log("local pause issued session-wide abort without reading latest message ownership; later manual turn was canceled")
}

type auditSwitchFailure struct{ ExecRunner }

func (r auditSwitchFailure) RunIn(ctx context.Context, dir string, argv, env []string, input io.Reader) (CommandResult, error) {
	if len(argv) > 1 && argv[0] == "git" && argv[1] == "switch" {
		return CommandResult{}, errors.New("audit interruption before switch")
	}
	return r.ExecRunner.RunIn(ctx, dir, argv, env, input)
}

func auditLocalCheckout(t *testing.T) (*TaskCheckouts, Job) {
	t.Helper()
	c, s, now := policyFixture(t)
	origin := filepath.Join(filepath.Dir(c.StateDir), "audit-origin")
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "script.sh"), []byte("#!/bin/sh\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "add", ".")
	localGit(t, origin, "commit", "-m", "audit fixture")
	cuts := NewCheckouts(c, ExecRunner{})
	cuts.origin = func(string) string { return origin }
	return cuts, Select(c, s, State{}, now)[0]
}

func TestAuditCloneBeforeSwitchCannotRecover(t *testing.T) {
	cuts, j := auditLocalCheckout(t)
	cuts.runner = auditSwitchFailure{}
	if _, err := cuts.Prepare(context.Background(), j); err == nil {
		t.Fatal("expected injected interruption")
	}
	cuts.runner = ExecRunner{}
	for range 3 {
		if _, err := cuts.Prepare(context.Background(), j); err == nil || err.Error() != "checkout branch changed" {
			t.Fatalf("retry did not reproduce branch mismatch: %v", err)
		}
	}
	t.Log("clone completed on main; every healthy retry rejects checkout branch changed")
}

func TestAuditProgressIgnoresExecutableBitOnlyChange(t *testing.T) {
	cuts, j := auditLocalCheckout(t)
	j, err := cuts.Prepare(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	localGit(t, j.Checkout, "config", "core.filemode", "true")
	before, err := cuts.Progress(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(j.Checkout, "script.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := cuts.Progress(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	diff := localGit(t, j.Checkout, "diff", "--summary")
	if before != after || !strings.Contains(diff, "mode change 100644 => 100755") {
		t.Fatalf("hash equal=%v git diff=%q", before == after, diff)
	}
	t.Logf("progress hashes identical despite Git reporting %s", diff)
}

func TestAuditExecRunnerLeavesChildAfterParentExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := (ExecRunner{}).Run(ctx, []string{"sh", "-c", "sleep 30 & printf '%s' \"$!\""}, nil, nil)
	pid, parseErr := strconv.Atoi(string(result.Stdout))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("invalid fixture child PID: %v", parseErr)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if err == nil || result.ExitCode != 0 {
		t.Fatalf("expected WaitDelay failure after successful parent exit: code=%d err=%v", result.ExitCode, err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child did not survive: %v", err)
	}
	t.Log("parent exited 0, runner returned worker command failed after WaitDelay, background child still alive (killed by test cleanup)")
}
