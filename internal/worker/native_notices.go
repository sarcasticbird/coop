package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeNotice struct {
	Message   c.SendMessage
	Hash      string
	Uncertain bool
}

type nativeNotices struct {
	Version                   int
	MasterID, AgentID         string
	Sequence                  uint64
	Active                    map[string]string
	Pending                   *nativeNotice
	NextAttempt, CatalogSince time.Time
	LastThreadID              string
	RejectedThreadID          string
}

// TickBlockerNotices snapshots event-loop state and permits one bounded send
// outside the receipt/model-observation loop. No model work is started here.
func (d *NativeDispatcher) TickBlockerNotices() error {
	if d.noticeDone != nil {
		select {
		case err := <-d.noticeDone:
			d.noticeDone = nil
			d.nextNoticeCheck = d.clock.Now().Add(c.HeartbeatInterval)
			return err
		default:
			return nil
		}
	}
	if d.clock.Now().Before(d.nextNoticeCheck) {
		return nil
	}
	snapshot := &NativeDispatcher{config: d.config, master: d.master, clock: RealClock{}, catalog: d.catalog, catalogError: d.catalogError, held: d.held, recoveryJob: d.recoveryJob, history: map[string]NativeExecution{}}
	for id, x := range d.history {
		snapshot.history[id] = x
	}
	if d.active != nil {
		x := *d.active
		snapshot.active = &x
	}
	done := make(chan error, 1)
	d.noticeDone = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- snapshot.NotifyBlockers(ctx)
	}()
	return nil
}

// NotifyBlockers publishes status without consuming a native/model slot. Its
// durable outbox retries an uncertain send with exactly the same request/body.
func (d *NativeDispatcher) NotifyBlockers(ctx context.Context) error {
	store := NewVersionedFileStore(d.config.StateDir, 3)
	paused, err := store.Paused()
	if err != nil || paused {
		return err
	}
	paused, err = store.AdmissionPaused()
	if err != nil || paused {
		return err
	}
	path := filepath.Join(masterDir(d.config), "blocker-notices.json")
	s := nativeNotices{Version: 1, MasterID: d.config.Master.MasterID, AgentID: d.config.Master.AgentID, Active: map[string]string{}}
	var previous []byte
	if raw, err := readPrivate(path, MaxSnapshot); err == nil {
		if strictTeamJSON(raw, &s) != nil || s.Version != 1 || s.MasterID != d.config.Master.MasterID || s.AgentID != d.config.Master.AgentID || s.Active == nil {
			return c.ErrConflict
		}
		previous = raw
	} else if !os.IsNotExist(err) {
		return err
	}
	save := func() error {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if bytes.Equal(previous, raw) {
			return nil
		}
		if err = atomicPrivate(path, raw); err != nil {
			return err
		}
		previous = raw
		return nil
	}
	now := d.clock.Now()
	if now.Before(s.NextAttempt) {
		return nil
	}
	var peers []c.Presence
	peersRead := false
	validateOwner := func(recipients []string) error {
		if len(recipients) == 0 {
			return nil
		}
		if !peersRead {
			reader, ok := d.master.(interface {
				Team(context.Context) ([]c.Presence, error)
			})
			if !ok {
				return c.ErrUnavailable
			}
			peers, err = reader.Team(ctx)
			if err != nil {
				return err
			}
			peersRead = true
		}
		for _, recipient := range recipients {
			if recipient == d.config.Master.AgentID {
				return c.ErrForbidden
			}
			for _, peer := range peers {
				if recipient == peer.AgentID || recipient == peer.Alias {
					return c.ErrForbidden
				}
			}
		}
		return nil
	}
	groups := map[string][]string{}
	add := func(j Job, reason string) {
		if j.Master == nil || j.Master.ThreadID == "" {
			return
		}
		// Emit diagnostic categories, never arbitrary model/tool error text.
		if reason == "" || len(reason) > 128 {
			reason = "native_recovery_required"
		}
		for _, ch := range reason {
			if ch != '_' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				reason = "native_recovery_required"
				break
			}
		}
		needs := "Inspect the saved run and blocker before using supported recovery; do not replay uncertain actions."
		switch reason {
		case "native_dependency_wait":
			needs = "The worker resumes automatically when its registered prerequisites complete; no owner action is required unless those prerequisites stall."
		case "native_completed_without_result", "native_completed_without_reply":
			needs = "Inspect the saved terminal run, then explicitly continue it or resolve the missing result through supported recovery."
		case "native_branch_refresh_limit", "native_branch_refresh_no_progress":
			needs = "Inspect branch conflicts and the exhausted or unchanged author refresh before deciding how to continue."
		case "native_catalog_unavailable":
			needs = "Restore the GitHub/catalog read; the worker will retry while preserving saved work."
		}
		line := fmt.Sprintf("Task: %s\nPR: %s\nBlocker: %s\nLast action: %s, submission %s. Saved work is retained.\nNeeds: %s", j.TaskURL, j.PRURL, reason, j.Kind, j.SubmissionState, needs)
		groups[j.Master.ThreadID] = append(groups[j.Master.ThreadID], line)
	}
	var latest Job
	for _, x := range d.history {
		if x.Job.Master != nil && x.Job.Master.ThreadID != "" && (latest.Master == nil || x.Job.SubmittedAt.After(latest.SubmittedAt) || x.Job.SubmittedAt.Equal(latest.SubmittedAt) && x.Job.ID > latest.ID) {
			latest = x.Job
		}
		if x.Phase == "blocked" && x.Reason != "goal_paused" && x.Reason != "local_pause" {
			add(x.Job, x.Reason)
			if x.Wait != nil && x.Job.Master != nil {
				deps := []string{}
				for _, dep := range x.Wait.Dependencies {
					if len(deps) < 3 {
						deps = append(deps, dep.TaskURL)
					}
				}
				lines := groups[x.Job.Master.ThreadID]
				if len(lines) > 0 && len(deps) > 0 {
					lines[len(lines)-1] += "\nDepends on: " + strings.Join(deps, ", ")
				}
			}
		}
	}
	turns, err := masterTurns(d.config)
	if err != nil {
		return err
	}
	for _, turn := range turns {
		if turn.Message.ThreadID != "" && (latest.Master == nil || turn.Message.CreatedAt.After(latest.SubmittedAt)) {
			latest = turn.Job
			latest.SubmittedAt = turn.Message.CreatedAt
			latest.Master = &MasterJobBinding{ThreadID: turn.Message.ThreadID}
		}
		if turn.Phase != "blocked" || turn.Reason == "local_pause" || turn.Reason == "goal_paused" {
			continue
		}
		retired, err := retiredTurn(d.config, turn)
		if err != nil {
			return err
		}
		if !retired {
			j := turn.Job
			j.Master = &MasterJobBinding{ThreadID: turn.Message.ThreadID}
			add(j, turn.Reason)
		}
	}
	if latest.Master != nil {
		s.LastThreadID = latest.Master.ThreadID
	} else if s.LastThreadID != "" {
		latest.Master = &MasterJobBinding{ThreadID: s.LastThreadID}
	}
	progress := d.Progress()
	for _, b := range progress.Blocked {
		if b.Reason != "native_branch_refresh_limit" && b.Reason != "native_branch_refresh_no_progress" {
			continue
		}
		for _, task := range d.catalog.Tasks {
			if task.Task.URL != b.Task {
				continue
			}
			for _, goal := range d.catalog.Goals {
				if goal.Revision.Ref == task.Binding.Goal {
					add(Job{Kind: "fix", TaskURL: b.Task, Master: &MasterJobBinding{ThreadID: goal.Revision.Proposal.ThreadID}}, b.Reason)
				}
			}
		}
	}
	if d.active != nil && (progress.Reason == "native_admission_stalled" || progress.Reason == "native_turn_overdue") {
		add(d.active.Job, progress.Reason)
	}
	if d.catalogError != "" {
		if s.CatalogSince.IsZero() {
			s.CatalogSince = now
		}
		if now.Sub(s.CatalogSince) >= nativeAdmissionStall {
			add(latest, "native_catalog_unavailable")
		}
	} else {
		s.CatalogSince = time.Time{}
	}
	current := map[string]string{}
	for thread, lines := range groups {
		sort.Strings(lines)
		current[thread] = BodyHash(strings.Join(lines, "\n\n"))
	}
	// A failed catalog read is not evidence that an earlier blocker cleared.
	if d.catalogError != "" {
		for thread, hash := range s.Active {
			if _, ok := current[thread]; !ok {
				current[thread] = hash
			}
		}
	}
	if s.Pending == nil {
		threads := map[string]bool{}
		for thread := range current {
			threads[thread] = true
		}
		for thread := range s.Active {
			threads[thread] = true
		}
		keys := make([]string, 0, len(threads))
		for thread := range threads {
			keys = append(keys, thread)
		}
		sort.Strings(keys)
		// A definitively rejected thread must not starve other destinations.
		if i := sort.SearchStrings(keys, s.RejectedThreadID); i < len(keys) && keys[i] == s.RejectedThreadID {
			keys = append(keys[i+1:], keys[:i+1]...)
		}
		for _, thread := range keys {
			if current[thread] == s.Active[thread] {
				continue
			}
			body := "[COOP UNBLOCKED]\nAgent: " + d.config.Master.AgentID + "\nThe previously reported blockers are no longer present. Check current status for the next action."
			if current[thread] != "" {
				lines := groups[thread]
				body = "[COOP BLOCKED]\nAgent: " + d.config.Master.AgentID + "\n"
				if len(lines) > 16 {
					body += fmt.Sprintf("Showing 16 of %d blockers; use worker status for details.\n", len(lines))
					lines = lines[:16]
				}
				body += strings.Join(lines, "\n\n")
			}
			recipients := []string{}
			if owner := d.config.Master.BlockerRecipient; owner != "" {
				recipients = append(recipients, owner)
				body = "@" + owner + "\n" + body
			}
			if err := validateOwner(recipients); err != nil {
				return err
			}
			body += "\nObserved: " + now.UTC().Format(time.RFC3339)
			if len(body) > c.MaxText {
				return c.ErrSize
			}
			s.Sequence++
			id := BodyHash(fmt.Sprintf("%s:%s:%d:%d", s.MasterID, d.config.Master.InstanceID, d.config.Master.Generation, s.Sequence))
			s.Pending = &nativeNotice{Hash: current[thread], Message: c.SendMessage{ThreadID: thread, Recipients: recipients, Body: body, RequestID: "blocker-" + id}}
			break
		}
	}
	if err := save(); err != nil {
		return err
	}
	if s.Pending == nil {
		return nil
	}
	err = validateOwner(s.Pending.Message.Recipients)
	if err == nil {
		err = verifyNativePlacement(ctx, d.config, d.master)
	}
	attempted := false
	if err == nil {
		var receipt c.MessageReceipt
		attempted = true
		receipt, err = d.master.Send(ctx, s.Pending.Message)
		if err == nil && receipt.MessageID == "" {
			err = c.ErrConflict
		}
	}
	if err != nil {
		definitive := errors.Is(err, c.ErrForbidden) || errors.Is(err, c.ErrInvalid) || errors.Is(err, c.ErrNotFound)
		if definitive && !s.Pending.Uncertain {
			// These refusals cannot have committed. Rebuild from current routing
			// on retry, retaining round-robin progress across worker restarts.
			s.RejectedThreadID = s.Pending.Message.ThreadID
			s.Pending = nil
		} else if attempted {
			// A lost response may have committed, even if a later attempt is
			// refused. Preserve its original ID, recipients and body exactly.
			s.Pending.Uncertain = true
		}
		s.NextAttempt = now.Add(time.Minute)
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return err
	}
	if s.Pending.Hash == "" {
		delete(s.Active, s.Pending.Message.ThreadID)
	} else {
		s.Active[s.Pending.Message.ThreadID] = s.Pending.Hash
	}
	s.Pending, s.NextAttempt = nil, time.Time{}
	return save()
}
