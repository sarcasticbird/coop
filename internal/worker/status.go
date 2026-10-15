package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Status struct {
	RecoveryIntent     *Job             `json:"recovery_intent,omitempty"`
	PromptFailure      *PromptSizeError `json:"prompt_failure,omitempty"`
	WakeIntakeDeferred bool             `json:"wake_intake_deferred,omitempty"`
	APINotBefore       time.Time        `json:"api_not_before,omitempty"`
	WakeEnabled        bool             `json:"wake_enabled,omitempty"`
	Wakes              []WakeSummary    `json:"wakes,omitempty"`
	Name               string           `json:"name,omitempty"`
	Enabled            bool             `json:"enabled"`
	State              string           `json:"state"`
	Paused             bool             `json:"paused"`
	ServerHealthy      bool             `json:"server_healthy"`
	ServerVersion      string           `json:"server_version,omitempty"`
	Heartbeat          time.Time        `json:"heartbeat"`
	LastPoll           time.Time        `json:"last_poll"`
	ActiveTask         string           `json:"active_task,omitempty"`
	SessionID          string           `json:"session_id,omitempty"`
	SubmissionState    string           `json:"submission_state,omitempty"`
	WaitReason         string           `json:"wait_reason,omitempty"`
	LastError          string           `json:"last_error,omitempty"`
	NextRetry          time.Time        `json:"next_retry,omitempty"`
}

func (s *FileStore) WriteStatus(status Status) error {
	if err := validateStatusPromptFailures(status); err != nil {
		return err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return atomicPrivate(filepath.Join(s.Dir, "status.json"), raw)
}
func ReadStatus(dir string, now time.Time) (Status, error) {
	raw, err := readPrivate(filepath.Join(dir, "status.json"), 64<<10)
	if os.IsNotExist(err) {
		return Status{State: "not_running"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := strictJSON(raw, &status); err != nil {
		return status, err
	}
	if err := validateStatusPromptFailures(status); err != nil {
		return status, err
	}
	if now.Sub(status.Heartbeat) > 2*time.Minute {
		status.State = "stale"
		status.ServerHealthy = false
	}
	return status, nil
}

func validateStatusPromptFailures(status Status) error {
	if err := validatePromptFailure(status.PromptFailure); err != nil {
		return err
	}
	for _, wake := range status.Wakes {
		if err := validatePromptFailure(wake.PromptFailure); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) idleStatus() {
	if e.status.RecoveryIntent != nil {
		return
	}
	if e.status.Paused || e.status.WaitReason == "server_unavailable" {
		return
	}
	for _, j := range e.state.Jobs {
		if j.Phase == "waiting" && j.WaitReason == "session_busy" {
			e.status.State, e.status.WaitReason = "waiting", "session_busy"
			e.status.NextRetry = j.NextAttemptAt
			if e.nextPoll.After(e.status.NextRetry) {
				e.status.NextRetry = e.nextPoll
			}
			return
		}
		if j.Phase == "waiting" && e.Clock.Now().Before(j.NextAttemptAt) {
			if e.status.NextRetry.IsZero() || j.NextAttemptAt.Before(e.status.NextRetry) {
				e.status.State = "waiting"
				e.status.WaitReason = "retry"
				e.status.NextRetry = j.NextAttemptAt
			}
		}
	}
	if e.status.WaitReason != "" {
		return
	}
	for _, j := range e.state.Jobs {
		if j.Phase == "blocked" {
			e.status.State = "blocked"
			e.status.WaitReason = j.WaitReason
			e.status.PromptFailure = j.PromptFailure
			return
		}
	}
	for _, j := range e.state.Jobs {
		if j.Phase == "waiting" && j.WaitReason == "review_handoff" {
			e.status.State, e.status.WaitReason = "waiting", "review_handoff"
			return
		}
	}
	for _, p := range e.state.SnapshotCache.PRs {
		if !p.Closed && !p.Merged && sameActor(p.Author, e.Config.Implementer) && missingPRMetadata(p) {
			e.status.State, e.status.WaitReason = "blocked", "pr_metadata"
			return
		}
	}
	for _, t := range e.state.SnapshotCache.Tasks {
		if t.Ready && !t.Completed {
			g, ok := goalFor(e.state.SnapshotCache, t.GoalURL)
			if !ok || EvaluateGoal(e.Config, g, e.Clock.Now()).State != "approved" {
				e.status.State = "waiting"
				e.status.WaitReason = "owner_approval"
				return
			}
		}
	}
	if e.Config.Role == "implementer" {
		for _, p := range e.state.SnapshotCache.PRs {
			if !p.Merged && !p.Closed && sameActor(p.Author, e.Config.Implementer) {
				e.status.State = "waiting"
				e.status.WaitReason = "review"
				if latestReview(e.Config, p).State == "APPROVED" {
					e.status.WaitReason = "checks"
				}
				return
			}
		}
	}
	if e.status.State == "working" {
		e.status.State = "idle"
	}
}
