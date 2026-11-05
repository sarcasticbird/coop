package worker

import (
	"sort"
	"time"
)

type ScheduleCounters struct{ Handoffs, Foreground int }

// Retained for decoding historical worker state, not active scheduling.
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

// ChooseTeamJob is the native dispatcher's shared pure fairness selector.
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
