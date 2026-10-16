package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Status struct {
	Actor              Actor            `json:"actor"`
	Current            *CurrentWork     `json:"current,omitempty"`
	HistoricalJobs     int              `json:"historical_jobs,omitempty"`
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

type CurrentWork struct {
	Actor          Actor          `json:"actor"`
	Role           string         `json:"role"`
	Stage          string         `json:"stage"`
	Phase          string         `json:"phase"`
	TaskURL        string         `json:"task,omitempty"`
	GoalURL        string         `json:"goal,omitempty"`
	PRURL          string         `json:"pr,omitempty"`
	HeadSHA        string         `json:"head,omitempty"`
	Author         *Actor         `json:"author,omitempty"`
	Peer           *Actor         `json:"peer,omitempty"`
	QA             *Actor         `json:"qa,omitempty"`
	SubmittedModel *ModelIdentity `json:"submitted_model,omitempty"`
	ExecutedModel  *ModelIdentity `json:"executed_model,omitempty"`
}

func currentWork(c Config, s Snapshot, j Job, now time.Time) *CurrentWork {
	w := &CurrentWork{Actor: c.Self, Role: j.Kind, Stage: j.Kind, Phase: j.Phase, TaskURL: j.TaskURL, GoalURL: j.GoalURL, PRURL: j.PRURL, HeadSHA: j.HeadSHA, SubmittedModel: j.SubmittedModel, ExecutedModel: j.ExecutedModel}
	switch j.Kind {
	case "implement", "fix", "deliver":
		w.Role = "implementer"
	case "review":
		w.Role = "engineering-review"
	case "qa":
		w.Role = "customer-qa"
	case "plan":
		w.Role = "planner"
	case "wake":
		w.Role = "conversation"
	}
	if j.Binding != nil {
		w.Role = j.Binding.Role
	}
	if c.Version == 2 {
		if t, ok := taskFor(s, j.TaskURL); ok {
			if g, ok := goalFor(s, j.GoalURL); ok {
				if a, err := resolveTeamAssignment(c, t, g, now); err == nil && (j.Binding == nil || j.Binding.AssignmentSHA == a.SHA) {
					w.Author, w.Peer = &a.Author, &a.Peer
					if t.QA.Required {
						w.QA = &a.QA
					}
				}
			}
		}
	} else if j.Kind != "plan" && j.Kind != "wake" {
		w.Author, w.Peer = &c.Implementer, &c.Reviewer
	}
	return w
}

// Historical failures remain evidence, not admission or a current blocker.
func currentSavedJob(c Config, s Snapshot, state State, j Job, now time.Time) bool {
	if j.Phase == "completed" {
		return false
	}
	if j.Phase == "active" || j.Phase == "recovery" {
		return true
	}
	if !s.Complete {
		return false
	}
	if j.Kind != "plan" {
		t, ok := taskFor(s, j.TaskURL)
		if !ok || t.Closed || t.Completed {
			return false
		}
		if j.Kind == "implement" {
			for _, p := range s.PRs {
				if p.TaskURL == t.URL && (!p.Closed || p.Merged) {
					return false
				}
			}
		}
	}
	if allowed, _ := Admit(c, j, s, now); allowed {
		return true
	}
	if j.Kind == "implement" {
		state.SnapshotCache = s
		e := Engine{Config: c, state: state, Clock: &statusClock{now: now}}
		allowed, _ := e.continuationAllowed(j)
		return allowed
	}
	return false
}

type statusClock struct {
	RealClock
	now time.Time
}

func (c *statusClock) Now() time.Time { return c.now }

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
	var jobs []Job
	for _, j := range e.state.Jobs {
		if currentSavedJob(e.Config, e.state.SnapshotCache, e.state, j, e.Clock.Now()) {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	for _, j := range jobs {
		if j.Phase == "waiting" && j.WaitReason == "session_busy" {
			e.status.Current = currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
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
				e.status.Current = currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
			}
		}
	}
	if e.status.WaitReason != "" {
		return
	}
	for _, j := range jobs {
		if j.Phase == "blocked" {
			e.status.State = "blocked"
			e.status.WaitReason = j.WaitReason
			e.status.PromptFailure = j.PromptFailure
			e.status.Current = currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
			return
		}
	}
	for _, j := range jobs {
		if j.Phase == "waiting" && j.WaitReason == "review_handoff" {
			e.status.State, e.status.WaitReason = "waiting", "review_handoff"
			return
		}
	}
	for _, p := range e.state.SnapshotCache.PRs {
		if !p.Closed && !p.Merged && configuredImplementer(e.Config, p.Author) && (missingPRMetadata(p) || e.Config.Version == 2 && p.AssignmentSHA == "") {
			e.status.State, e.status.WaitReason = "blocked", "pr_metadata"
			return
		}
	}
	for _, t := range e.state.SnapshotCache.Tasks {
		if t.Ready && !t.Completed && !t.Closed {
			g, ok := goalFor(e.state.SnapshotCache, t.GoalURL)
			if !ok || EvaluateGoal(e.Config, g, e.Clock.Now()).State != "approved" {
				e.status.State = "waiting"
				e.status.WaitReason = "owner_approval"
				return
			}
		}
	}
	if e.Config.Version == 2 {
		for _, p := range e.state.SnapshotCache.PRs {
			t, ok := taskFor(e.state.SnapshotCache, p.TaskURL)
			if !ok || t.Closed || t.Completed || p.Closed || p.Merged {
				continue
			}
			g, ok := goalFor(e.state.SnapshotCache, t.GoalURL)
			if !ok {
				continue
			}
			a, err := resolveTeamAssignment(e.Config, t, g, e.Clock.Now())
			if err != nil || (!sameActor(e.Config.Self, a.Author) && !sameActor(e.Config.Self, a.Peer) && !sameActor(e.Config.Self, a.QA)) {
				continue
			}
			j := Job{Kind: "review", Phase: "waiting", TaskURL: t.URL, GoalURL: g.URL, PRURL: p.URL, HeadSHA: p.HeadSHA}
			e.status.State, e.status.WaitReason = "waiting", "review"
			if teamLatestReview(e.Config, t, p).State == "CHANGES_REQUESTED" {
				j.Kind, e.status.WaitReason = "fix", "changes_requested"
			} else if _, ok := EngineeringReviewFor(e.Config, t, p); ok {
				e.status.WaitReason = "checks"
				if p.ChecksState == "success" || p.ChecksState == "none" {
					if t.QA.Required {
						j.Kind, e.status.WaitReason = "qa", "qa"
						if p.QAArtifact == nil {
							e.status.WaitReason = "qa_artifact"
						} else if _, result, valid := latestTeamQA(e.Config, t, p, t.QA, p.QAArtifact.SHA256); valid {
							e.status.WaitReason = "qa_" + result.Conclusion
							if result.Conclusion == "pass" {
								j.Kind, e.status.WaitReason = "deliver", "merge_eligibility"
							}
							if result.Conclusion == "changes-needed" {
								j.Kind = "fix"
							}
						}
					} else {
						j.Kind, e.status.WaitReason = "deliver", "merge_eligibility"
					}
				}
			}
			e.status.Current = currentWork(e.Config, e.state.SnapshotCache, j, e.Clock.Now())
			switch {
			case sameActor(e.Config.Self, a.Author):
				e.status.Current.Role = "implementer"
			case sameActor(e.Config.Self, a.Peer):
				e.status.Current.Role = "engineering-review"
			case sameActor(e.Config.Self, a.QA):
				e.status.Current.Role = "customer-qa"
			}
			return
		}
	} else if e.Config.Role == "implementer" {
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
	e.status.State = "idle"
}
