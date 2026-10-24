package worker

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"sort"
	"strings"
	"time"
)

const nativeAdmissionStall = 5 * time.Minute

type NativeBlocker struct {
	JobID        string   `json:"job_id"`
	Task         string   `json:"task,omitempty"`
	Reason       string   `json:"reason"`
	Dependencies []string `json:"dependencies,omitempty"`
}
type NativeProgress struct {
	ProgressAt      *time.Time                `json:"progress_at,omitempty"`
	WorkTitle       string                    `json:"work_title,omitempty"`
	Continuation    *c.TaskContinuationTarget `json:"continuation_target,omitempty"`
	SessionID       string                    `json:"session_id,omitempty"`
	SubmissionState string                    `json:"submission_state,omitempty"`
	Current         *CurrentWork              `json:"current,omitempty"`
	SelectedAt      time.Time                 `json:"selected_at,omitempty"`
	Deadline        time.Time                 `json:"deadline,omitempty"`
	Reason          string                    `json:"reason,omitempty"`
	PrimaryJobID    string                    `json:"primary_job_id,omitempty"`
	Blocked         []NativeBlocker           `json:"blocked,omitempty"`
	LastCatalogRead time.Time                 `json:"last_catalog_read,omitempty"`
	NextCatalogRead time.Time                 `json:"next_catalog_read,omitempty"`
}

// Progress reads only event-loop-owned snapshots. It never touches the model,
// retries an action or mistakes a quiet running turn for a stalled admission.
func (d *NativeDispatcher) Progress() NativeProgress {
	p := NativeProgress{LastCatalogRead: d.catalog.ReadAt, NextCatalogRead: d.nextRefresh}
	if d.catalog.Cache != nil {
		for ref := range d.catalog.Cache.Reasons {
			p.Blocked = append(p.Blocked, NativeBlocker{JobID: BodyHash("cache:" + ref), Task: ref, Reason: "cache_refresh_wait"})
		}
	}
	// Candidate construction is local and uses the same authority/ownership
	// checks as admission. Ignore history here so spent attempts stay visible.
	for _, v := range NativeCandidates(d.config, d.catalog, nil, d.clock.Now()) {
		if !nativeIsBranchRefresh(v.Job) && !nativeIsChecksRepair(v.Job) {
			continue
		}
		reserved := false
		for _, x := range d.history {
			if x.Phase == "blocked" && BindingKey(d.config, x.Job) == BindingKey(d.config, v.Job) {
				reserved = true
				break
			}
		}
		if reserved {
			continue
		}
		prefix := "native_branch_refresh_"
		if nativeIsChecksRepair(v.Job) {
			prefix = "native_checks_repair_"
		}
		reason := prefix + "required"
		if nativeFixCycles(v.Job.TaskURL, d.history) >= Defaults(d.config).Limits.MaxReviewCycles {
			reason = prefix + "limit"
		} else if x, ok := d.history[v.Job.ID]; ok && (x.Phase == "completed" || x.Phase == "cancelled") {
			reason = prefix + "no_progress"
		}
		p.Blocked = append(p.Blocked, NativeBlocker{JobID: v.Job.ID, Task: v.Job.TaskURL, Reason: reason})
	}
	for _, x := range d.history {
		if x.Phase != "blocked" {
			continue
		}
		b := NativeBlocker{JobID: x.Job.ID, Task: x.Job.TaskURL, Reason: nativeRecoveryBlocker(d.config, x)}
		if x.Wait != nil {
			for _, dep := range x.Wait.Dependencies {
				if len(b.Dependencies) < 3 {
					b.Dependencies = append(b.Dependencies, dep.TaskURL)
				}
			}
		}
		p.Blocked = append(p.Blocked, b)
	}
	sort.Slice(p.Blocked, func(i, j int) bool { return p.Blocked[i].JobID < p.Blocked[j].JobID })
	if d.active != nil {
		x := d.active
		p.PrimaryJobID = x.Job.ID
		p.SessionID, p.SubmissionState = x.Job.SessionID, x.Job.SubmissionState
		p.Current = currentWork(d.config, Snapshot{}, x.Job, d.clock.Now())
		if p.Current.WorkTitle == "" {
			for _, task := range d.catalog.Tasks {
				if task.Task.URL == x.Job.TaskURL {
					p.Current.WorkTitle = diagnosticTitle(task.Task.Title)
					break
				}
			}
		}
		p.Current.Phase = x.Phase
		if x.Job.Master != nil {
			p.Current.Role = x.Job.Master.Role
		}
		p.SelectedAt, p.Deadline = x.SelectedAt, x.Job.Deadline
		if nativeIsBranchRefresh(x.Job) {
			p.Reason = "native_branch_refresh"
		}
		if nativeIsChecksRepair(x.Job) {
			p.Reason = "native_checks_repair"
		}
		if (x.Phase == "selected" || x.Phase == "prepared") && !x.SelectedAt.IsZero() && d.clock.Now().Sub(x.SelectedAt) >= nativeAdmissionStall {
			p.Reason = "native_admission_stalled"
		}
		if d.catalog.Cache != nil && x.Phase == "selected" && x.Job.SessionID == "" && x.Job.MessageID == "" && x.Job.Kind == "review" && nativeCacheTaskReady(d.catalog, x.Job.TaskURL, d.clock.Now()) {
			for _, task := range d.catalog.Tasks {
				if task.Task.URL != x.Job.TaskURL {
					continue
				}
				for _, pr := range task.PRs {
					if pr.URL == x.Job.PRURL && pr.HeadSHA == x.Job.HeadSHA && nativeNeedsBranchRefresh(pr) {
						p.Reason = "native_branch_refresh_wait"
					}
				}
			}
		}
		if x.Phase == "active" && !x.Job.Deadline.IsZero() && !d.clock.Now().Before(x.Job.Deadline) {
			p.Reason = "native_turn_overdue"
		}
	}
	if d.held {
		p.Reason = "native_recovery_required"
		p.PrimaryJobID = d.recoveryJob
	} else if p.Current == nil && d.catalogError != "" {
		p.Reason = "native_catalog_unavailable"
	} else if p.Current == nil && len(p.Blocked) > 0 {
		p.Reason = "native_task_wait"
		if p.Blocked[0].Reason == "cache_refresh_wait" {
			p.Reason = "cache_refresh_wait"
		}
		p.PrimaryJobID = p.Blocked[0].JobID
		for _, b := range p.Blocked {
			if b.Reason != "native_dependency_wait" && b.Reason != "native_dependency_retry" && b.Reason != "native_dependency_recovery_required" {
				continue
			}
			if p.Reason != "native_dependency_retry" || b.Reason == "native_dependency_recovery_required" {
				p.Reason, p.PrimaryJobID = b.Reason, b.JobID
			}
			if b.Reason == "native_dependency_recovery_required" {
				break
			}
		}
	}
	if p.Current == nil && !d.held && d.catalogError == "" && p.Reason != "native_dependency_recovery_required" {
		for _, b := range p.Blocked {
			if strings.HasPrefix(b.Reason, "native_branch_refresh_") || strings.HasPrefix(b.Reason, "native_checks_repair_") {
				p.Reason, p.PrimaryJobID = b.Reason, b.JobID
				break
			}
		}
	}
	sort.SliceStable(p.Blocked, func(i, j int) bool {
		return p.Blocked[i].JobID == p.PrimaryJobID && p.Blocked[j].JobID != p.PrimaryJobID
	})
	if len(p.Blocked) > 16 {
		p.Blocked = p.Blocked[:16]
	}
	if x, ok := d.history[p.PrimaryJobID]; ok {
		p.ProgressAt, p.WorkTitle = x.Job.ProgressAt, diagnosticTitle(x.Job.WorkTitle)
	}
	if d.active != nil && d.active.Job.ID == p.PrimaryJobID {
		p.ProgressAt, p.WorkTitle = d.active.Job.ProgressAt, p.Current.WorkTitle
	}

	if x, ok := d.history[p.PrimaryJobID]; ok && x.Phase == "blocked" && (x.Reason == "native_completed_without_result" || x.Reason == "native_dependency_wait") && x.Job.Phase == "completed" && x.Job.SubmissionState == "completed" && !x.Job.TerminalAt.IsZero() {
		target := c.TaskContinuationTarget{JobID: x.Job.ID, TaskURL: x.Job.TaskURL, SessionID: x.Job.SessionID, MessageID: x.Job.MessageID, InstanceID: d.config.Master.InstanceID, Generation: d.config.Master.Generation}
		if c.ValidateContinuationTarget(target) == nil {
			p.Continuation = &target
		}
	}
	return p
}
func (p NativeProgress) heartbeatReason() string {
	reason := p.Reason
	if p.Current != nil {
		reason += " stage=" + p.Current.Stage + " phase=" + p.Current.Phase + " task=" + p.Current.TaskURL
	}
	for _, b := range p.Blocked {
		if strings.Contains(p.Reason, "recovery") || p.Current == nil {
			reason += " blocker=" + b.Reason + " task=" + b.Task
			if len(b.Dependencies) > 0 {
				reason += " depends_on=" + b.Dependencies[0]
			}
			break
		}
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return reason
}
func (m *MasterComms) pendingStall(now time.Time) (string, error) {
	if m.Busy || m.Paused || m.Recovery {
		return "", nil
	}
	records, err := masterTurns(m.Config)
	if err != nil {
		return "", err
	}
	for _, turn := range records {
		retired, e := retiredTurn(m.Config, turn)
		if e != nil {
			return "", e
		}
		if retired {
			continue
		}
		if turn.Phase == "received" && !turn.Message.CreatedAt.IsZero() && now.Sub(turn.Message.CreatedAt) >= nativeAdmissionStall {
			return "native_message_stalled", nil
		}
	}
	return "", nil
}
func (m *MasterComms) engineeringHeartbeat(p NativeProgress) {
	m.EngineeringState, m.EngineeringReason = "", p.heartbeatReason()
	switch p.Reason {
	case "native_recovery_required", "native_dependency_recovery_required", "native_catalog_unavailable", "native_admission_stalled", "native_turn_overdue", "native_branch_refresh_limit", "native_branch_refresh_no_progress", "native_checks_repair_limit", "native_checks_repair_no_progress":
		m.EngineeringState = "recovering"
	}
}
