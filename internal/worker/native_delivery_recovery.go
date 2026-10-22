package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// RecoverNativeDelivery retries the original unstarted delivery or confirms an
// acknowledged merge read-only after its model turn ended. Both worker and producer must be exclusively held.
// It never submits a model prompt or retries an uncertain/started remote effect.
func RecoverNativeDelivery(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, code OpenCode, id, session, message string, in NativeAction) (result NativeActionResult, err error) {
	return recoverNativeAction(ctx, cfg, m, g, code, id, session, message, in, false)
}

// RecoverNativePublication finishes only an acknowledged push with its original
// immutable request. It never repeats the push or retries an uncertain effect.
func RecoverNativePublication(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, code OpenCode, id, session, message string, in NativeAction) (NativeActionResult, error) {
	return recoverNativeAction(ctx, cfg, m, g, code, id, session, message, in, true)
}

func recoverNativeAction(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, code OpenCode, id, session, message string, in NativeAction, publication bool) (result NativeActionResult, err error) {
	operation := "deliver"
	if publication {
		operation = "publish-pr"
	}
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || m == nil || g == nil || code == nil || in.Operation != operation || in.RequestID == "" || !validSession(session) || !validMessage(message) {
		return result, c.ErrInvalid
	}
	releaseWorker, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return result, err
	}
	defer releaseWorker()
	releaseProducer, err := lockNativeProducer(cfg)
	if err != nil {
		return result, err
	}
	defer releaseProducer()
	x, err := loadNativeExecution(cfg, id)
	if err != nil {
		return result, err
	}
	kindOK := x.Job.Kind == "deliver"
	if publication {
		kindOK = x.Job.Kind == "fix" || x.Job.Kind == "implement"
	}
	if x.Phase != "blocked" || x.Reason != "native_completed_without_result" || !kindOK || x.Binding == nil || x.Job.SessionID != session || x.Job.MessageID != message || x.Job.SubmissionState != "completed" || x.Job.Phase != "completed" || x.Job.TerminalAt.IsZero() || x.PendingContinuation != nil {
		return result, c.ErrConflict
	}
	raw, err := readPrivate(nativeActionPath(cfg, id, in.RequestID), c.MaxJSON)
	if err != nil {
		return result, err
	}
	var record nativeActionRecord
	input, err := json.Marshal(in)
	if err != nil {
		return result, err
	}
	if strictTeamJSON(raw, &record) != nil || record.Version != 3 || record.JobID != id || record.RequestID != in.RequestID || record.InputSHA != BodyHash(string(input)) || record.Operation != operation || record.State != "prepared" || record.Result != (NativeActionResult{}) {
		return result, errors.New("delivery recovery requires the original unstarted action or acknowledged merge")
	}
	if publication {
		if record.Step != "push" || !qaHeadSHA.MatchString(record.PublishedHeadSHA) {
			return result, errors.New("publication recovery requires an acknowledged exact-head push")
		}
		if _, err = loadNativeActionIntent(cfg, x, record); err != nil {
			return result, err
		}
	} else if record.Step != "" && record.Step != "merge" {
		return result, errors.New("delivery recovery requires the original unstarted action or acknowledged merge")
	}
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(masterDir(cfg), "actions", entry.Name())
		if path == nativeActionPath(cfg, id, in.RequestID) {
			continue
		}
		data, e := readPrivate(path, c.MaxJSON)
		if e != nil {
			return result, e
		}
		var other nativeActionRecord
		if strictTeamJSON(data, &other) != nil || other.Version != 3 {
			return result, c.ErrConflict
		}
		if other.JobID == id && other.State != "completed" {
			return result, errors.New("delivery has another unfinished action; inspect remote evidence")
		}
	}

	d, err := NewNativeDispatcher(cfg, m, g, code, NewCheckouts(cfg, g.runner), RealClock{})
	if err != nil {
		return result, err
	}
	// Accepted restore evidence can retire the original execution without
	// rewriting its retained bytes. Do not reactivate that retired snapshot.
	if d.HasActive() || d.history[id].Phase != "blocked" {
		return result, errors.New("delivery is retired or native engineering still owns the worker slot")
	}
	turns, err := masterTurns(cfg)
	if err != nil {
		return result, err
	}
	for _, turn := range turns {
		retired, e := retiredTurn(cfg, turn)
		if e != nil {
			return result, e
		}
		if !retired && (turn.Phase == "active" || turn.Phase == "prepared" || turn.Phase == "blocked") {
			return result, errors.New("native conversation still owns the worker slot")
		}
	}
	observed, err := code.Observe(ctx, x.Job)
	if err != nil {
		return result, err
	}
	if !observed.Terminal || observed.SessionID != session || observed.MessageID != message {
		return result, errors.New("delivery recovery requires exact terminal native evidence")
	}
	if err = code.CheckContinuation(ctx, x.Job); err != nil {
		return result, err
	}
	if record.Step == "merge" {
		// An acknowledged merge is never replayed. Only confirm the original
		// exact head/assignment is merged and its task is already completed.
		authority, e := nativeCurrentGoal(ctx, cfg, m, x.Goal)
		if e != nil {
			return result, e
		}
		if e = nativeConfirmDelivery(ctx, cfg, g, authority, x); e != nil {
			return result, e
		}
		record.State = "completed"
		record.Result = NativeActionResult{URL: x.Job.PRURL, Phase: "completed"}
		if e = saveNativeAction(cfg, record); e != nil {
			return result, e
		}
		x.Phase, x.Reason = "completed", ""
		if e = saveNativeExecution(cfg, x); e != nil {
			return result, e
		}
		return record.Result, nil
	}
	// Re-enter the ordinary guarded helper under both locks. Keep the original
	// terminal job/session and journal; no new request or model turn is created.
	original := x
	x.Phase, x.Reason = "active", ""
	if err = saveNativeExecution(cfg, x); err != nil {
		return result, err
	}
	defer func() {
		if err == nil && result.Phase == "completed" {
			original.Phase, original.Reason = "completed", ""
		}
		// On refusal or interruption, restore the blocked execution but retain the
		// action journal's actual progress, including any uncertain remote effect.
		err = errors.Join(err, saveNativeExecution(cfg, original))
	}()
	return runNativeActionLocked(ctx, cfg, m, g, id, in)
}
