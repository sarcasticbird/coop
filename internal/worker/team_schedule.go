package worker

import (
	"context"
	"errors"
	"sort"
	"time"
)

type ScheduleCounters struct{ Handoffs, Foreground int }

type TeamScheduleState struct {
	Counters       ScheduleCounters
	Eligible       map[string]time.Time
	CompletedWakes map[string]bool
}

func handoffJob(j Job) bool {
	return j.Kind == "review" || j.Kind == "fix" || j.Kind == "deliver" || j.Kind == "qa"
}

func completedService(c ScheduleCounters, j Job) ScheduleCounters {
	if handoffJob(j) {
		if c.Handoffs < 2 {
			c.Handoffs++
		}
	} else {
		c.Handoffs = 0
	}
	if j.Kind == "implement" {
		c.Foreground = 0
	} else if c.Foreground < 3 {
		c.Foreground++
	}
	return c
}

// ChooseTeamJob consumes only admitted, runnable candidates. Selection is pure:
// dispatch, failed admission and uncertain submissions never consume service.
func ChooseTeamJob(jobs []Job, counters ScheduleCounters) (Job, bool) {
	var handoffs, foreground, coding []Job
	for _, j := range jobs {
		switch {
		case handoffJob(j):
			handoffs = append(handoffs, j)
		case j.Kind == "plan" || j.Kind == "wake":
			foreground = append(foreground, j)
		case j.Kind == "implement":
			coding = append(coding, j)
		}
	}
	pool := handoffs
	switch {
	case counters.Foreground >= 3 && len(coding) > 0:
		pool = coding
	case counters.Handoffs >= 2 && len(foreground) > 0:
		pool = foreground
	case len(handoffs) > 0:
	case len(foreground) > 0:
		pool = foreground
	default:
		pool = coding
	}
	if len(pool) == 0 {
		return Job{}, false
	}
	sort.Slice(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if !a.FirstEligibleAt.Equal(b.FirstEligibleAt) {
			return a.FirstEligibleAt.Before(b.FirstEligibleAt)
		}
		if a.SchedulePriority != b.SchedulePriority {
			return a.SchedulePriority < b.SchedulePriority
		}
		if a.ScheduleOrdinal != b.ScheduleOrdinal {
			return a.ScheduleOrdinal < b.ScheduleOrdinal
		}
		return a.ID < b.ID
	})
	return pool[0], true
}

func (e *Engine) teamScheduleState() *TeamScheduleState {
	if e.state.Schedule == nil {
		e.state.Schedule = &TeamScheduleState{}
	}
	s := e.state.Schedule
	if s.Eligible == nil {
		s.Eligible = map[string]time.Time{}
	}
	if s.CompletedWakes == nil {
		s.CompletedWakes = map[string]bool{}
	}
	return s
}

// Wake receipts live in a separate durable store. Consume each completed receipt
// once, before another selection. A crash between these writes is safe to retry;
// it never resubmits a turn or repeats an external effect.
func (e *Engine) accountCompletedWakes() error {
	if e.Wakes == nil {
		return nil
	}
	records, err := e.Wakes.Store.Requests()
	if err != nil {
		return err
	}
	var completed []WakeReceipt
	s := e.teamScheduleState()
	for _, r := range records {
		v, err := e.Wakes.Store.Receipt(r.ID)
		if err != nil {
			return err
		}
		if _, admitted := s.Eligible[r.ID]; admitted && v.Phase == "completed" && !v.Job.SubmittedAt.IsZero() && !s.CompletedWakes[r.ID] {
			completed = append(completed, v)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if !completed[i].UpdatedAt.Equal(completed[j].UpdatedAt) {
			return completed[i].UpdatedAt.Before(completed[j].UpdatedAt)
		}
		return completed[i].RequestID < completed[j].RequestID
	})
	for _, v := range completed {
		s.Counters = completedService(s.Counters, Job{Kind: "wake"})
		s.CompletedWakes[v.RequestID] = true
	}
	if len(completed) > 0 {
		return e.save()
	}
	return nil
}

func (e *Engine) teamRunnable(j Job) (bool, string) {
	// The preparing checkpoint precedes the claim. An unsubmitted preparation
	// may retry normal Ready admission; never apply this to uncertain turn intent.
	if j.Kind == "implement" && j.MessageID == "" && j.SubmittedAt.IsZero() && j.SubmissionState == "" {
		if t, ok := taskFor(e.state.SnapshotCache, j.TaskURL); ok && t.Claim == nil {
			return Admit(e.Config, j, e.state.SnapshotCache, e.Clock.Now())
		}
	}
	return e.continuationAllowed(j)
}

func (e *Engine) scheduleTeam(ctx context.Context, fresh bool) error {
	if err := e.accountCompletedWakes(); err != nil {
		return err
	}
	if !fresh {
		if err := e.refresh(ctx); errors.Is(err, errPollPending) {
			return nil
		} else if err != nil {
			return err
		}
	}
	now := e.Clock.Now()
	jobs := Select(e.Config, e.state.SnapshotCache, e.state, now)
	for _, j := range e.state.Jobs {
		if (j.Phase == "preparing" || j.Phase == "waiting" || j.Phase == "paused") && !now.Before(j.NextAttemptAt) && e.savedBindingAllowed(j) {
			if ok, _ := e.teamRunnable(j); ok && !pendingReviewHandoff(e.Config, j, e.state.SnapshotCache, now) {
				jobs = append(jobs, j)
			}
		}
	}
	s := e.teamScheduleState()
	for i := range jobs {
		j := &jobs[i]
		age, ok := s.Eligible[j.ID]
		if !ok {
			age = now
			s.Eligible[j.ID] = age
		}
		j.FirstEligibleAt = age
		if t, ok := taskFor(e.state.SnapshotCache, j.TaskURL); ok {
			j.SchedulePriority, j.ScheduleOrdinal = t.Priority, t.Ordinal
		}
	}
	if e.Wakes != nil {
		r, ok, err := e.Wakes.Next(now)
		if err != nil {
			return err
		}
		if ok && !e.lastSnapshotStarted.Before(r.CreatedAt) {
			s.Eligible[r.ID] = r.CreatedAt
			jobs = append(jobs, Job{ID: r.ID, Kind: "wake", FirstEligibleAt: r.CreatedAt})
		}
	}
	// Persist first eligibility even for candidates that do not win this turn.
	if err := e.save(); err != nil {
		return err
	}
	j, ok := ChooseTeamJob(jobs, s.Counters)
	if !ok {
		e.status.State = "idle"
		return nil
	}
	e.status.State = "working"
	if j.Kind == "wake" {
		return e.Wakes.tick(ctx, e.state.SnapshotCache, j.ID)
	}
	reader, canRead := e.GitHub.(interface {
		Revalidate(context.Context, Job, Snapshot) (Snapshot, error)
	})
	if !canRead {
		return ghError("scope_revalidation_unavailable")
	}
	checkCtx, cancel := context.WithTimeout(ctx, githubRevalidationTimeout)
	snapshot, err := reader.Revalidate(checkCtx, j, e.state.SnapshotCache)
	cancel()
	if err != nil {
		return err
	}
	e.state.SnapshotCache = snapshot
	if !e.savedBindingAllowed(j) {
		return nil
	}
	if _, saved := e.state.Jobs[j.ID]; saved {
		a, err := e.reconcile(ctx, j)
		if err != nil {
			return err
		}
		if artifactCompletes(e.Config, j, a) {
			j.Phase = "completed"
			return e.saveJob(j)
		}
		if allowed, _ := e.teamRunnable(j); !allowed {
			return nil
		}
	} else if allowed, _ := Admit(e.Config, j, e.state.SnapshotCache, e.Clock.Now()); !allowed {
		return nil
	}
	return e.dispatch(ctx, j)
}
