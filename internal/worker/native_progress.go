package worker

import (
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
	SessionID       string          `json:"session_id,omitempty"`
	SubmissionState string          `json:"submission_state,omitempty"`
	Current         *CurrentWork    `json:"current,omitempty"`
	SelectedAt      time.Time       `json:"selected_at,omitempty"`
	Deadline        time.Time       `json:"deadline,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	PrimaryJobID    string          `json:"primary_job_id,omitempty"`
	Blocked         []NativeBlocker `json:"blocked,omitempty"`
	LastCatalogRead time.Time       `json:"last_catalog_read,omitempty"`
	NextCatalogRead time.Time       `json:"next_catalog_read,omitempty"`
}

// Progress reads only event-loop-owned snapshots. It never touches the model,
// retries an action or mistakes a quiet running turn for a stalled admission.
func (d *NativeDispatcher) Progress() NativeProgress {
	p := NativeProgress{LastCatalogRead: d.catalog.ReadAt, NextCatalogRead: d.nextRefresh}
	for _, x := range d.history {
		if x.Phase != "blocked" {
			continue
		}
		b := NativeBlocker{JobID: x.Job.ID, Task: x.Job.TaskURL, Reason: x.Reason}
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
		p.SessionID, p.SubmissionState = x.Job.SessionID, x.Job.SubmissionState
		p.Current = currentWork(d.config, Snapshot{}, x.Job, d.clock.Now())
		p.Current.Phase = x.Phase
		if x.Job.Master != nil {
			p.Current.Role = x.Job.Master.Role
		}
		p.SelectedAt, p.Deadline = x.SelectedAt, x.Job.Deadline
		if (x.Phase == "selected" || x.Phase == "prepared") && !x.SelectedAt.IsZero() && d.clock.Now().Sub(x.SelectedAt) >= nativeAdmissionStall {
			p.Reason = "native_admission_stalled"
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
		p.PrimaryJobID = p.Blocked[0].JobID
		for _, b := range p.Blocked {
			if b.Reason != "native_dependency_wait" {
				continue
			}
			p.Reason = "native_dependency_wait"
			p.PrimaryJobID = b.JobID
			if d.history[b.JobID].Job.ReconcileFailures >= Defaults(d.config).Limits.MaxAttempts {
				p.Reason = "native_dependency_recovery_required"
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
	case "native_recovery_required", "native_dependency_recovery_required", "native_catalog_unavailable", "native_admission_stalled", "native_turn_overdue":
		m.EngineeringState = "recovering"
	}
}
