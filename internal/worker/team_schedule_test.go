package worker

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestTeamFairnessContinuousHandoffs(t *testing.T) {
	for _, kind := range []string{"review", "fix", "deliver", "qa"} {
		for _, oldest := range []string{"plan", "wake"} {
			t.Run(kind+"/oldest-"+oldest, func(t *testing.T) {
				clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				other := "wake"
				if oldest == "wake" {
					other = "plan"
				}
				jobs := []Job{
					{ID: "handoff", Kind: kind, FirstEligibleAt: clock.Now()},
					{ID: oldest, Kind: oldest, FirstEligibleAt: clock.Now().Add(-2 * time.Hour), SchedulePriority: 10},
					{ID: other, Kind: other, FirstEligibleAt: clock.Now().Add(-time.Hour)},
					{ID: "coding", Kind: "implement", FirstEligibleAt: clock.Now().Add(-3 * time.Hour)},
				}
				var counters ScheduleCounters
				// Replace every completed candidate so all classes remain continuously runnable.
				for cycle := 0; cycle < 4; cycle++ {
					foreground := oldest
					if cycle%2 != 0 {
						foreground = other
					}
					wantIDs := []string{"handoff", "handoff", foreground, "coding"}
					wantCounters := []ScheduleCounters{{1, 1}, {2, 2}, {0, 3}, {0, 0}}
					for step, wantID := range wantIDs {
						before := append([]Job(nil), jobs...)
						beforeCounters := counters
						got, ok := ChooseTeamJob(jobs, counters)
						if !ok || got.ID != wantID {
							t.Fatalf("cycle %d step %d: chose %q, %v; want %q", cycle, step, got.ID, ok, wantID)
						}
						if !reflect.DeepEqual(jobs, before) || counters != beforeCounters {
							t.Fatal("selection mutated candidates or counters")
						}
						// Repeated selection without completion must not consume service.
						if again, ok := ChooseTeamJob(jobs, counters); !ok || !reflect.DeepEqual(again, got) {
							t.Fatalf("selection without completion changed: %+v, %v", again, ok)
						}
						counters = completedService(counters, got)
						if counters != wantCounters[step] {
							t.Fatalf("cycle %d step %d: counters = %+v, want %+v", cycle, step, counters, wantCounters[step])
						}
						clock.now = clock.Now().Add(time.Minute)
						for i := range jobs {
							if jobs[i].ID == got.ID {
								jobs[i].FirstEligibleAt = clock.Now()
							}
						}
					}
				}
			})
		}
	}
}

func TestTeamFairnessClassBoundsAndFallbacks(t *testing.T) {
	// Each row covers every nonempty combination of runnable classes.
	for _, tc := range []struct {
		name     string
		counters ScheduleCounters
		want     [7]string
	}{
		{"initial", ScheduleCounters{}, [7]string{"review", "plan", "review", "implement", "review", "plan", "review"}},
		{"below bounds", ScheduleCounters{1, 2}, [7]string{"review", "plan", "review", "implement", "review", "plan", "review"}},
		{"handoff bound", ScheduleCounters{2, 2}, [7]string{"review", "plan", "plan", "implement", "review", "plan", "plan"}},
		{"foreground bound", ScheduleCounters{0, 3}, [7]string{"review", "plan", "review", "implement", "implement", "implement", "implement"}},
		{"both bounds coding first", ScheduleCounters{2, 3}, [7]string{"review", "plan", "plan", "implement", "implement", "implement", "implement"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for mask := 1; mask < 8; mask++ {
				t.Run(fmt.Sprint(mask), func(t *testing.T) {
					var jobs []Job
					for bit, kind := range []string{"review", "plan", "implement"} {
						if mask&(1<<bit) != 0 {
							jobs = append(jobs, Job{ID: kind, Kind: kind})
						}
					}
					got, ok := ChooseTeamJob(jobs, tc.counters)
					if !ok || got.Kind != tc.want[mask-1] {
						t.Fatalf("chose %+v, %v; want %s", got, ok, tc.want[mask-1])
					}
				})
			}
		})
	}
	for _, jobs := range [][]Job{nil, {}, {{ID: "unsupported", Kind: "unknown"}}} {
		if got, ok := ChooseTeamJob(jobs, ScheduleCounters{2, 3}); ok || !reflect.DeepEqual(got, Job{}) {
			t.Fatalf("no runnable class: got %+v, %v", got, ok)
		}
	}
}

func TestTeamFairnessDeterministicOrdering(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, kinds := range [][]string{{"review", "fix", "deliver", "qa"}, {"plan", "wake"}, {"implement"}} {
		for _, key := range []string{"age", "priority", "ordinal", "ID"} {
			t.Run(kinds[0]+"/"+key, func(t *testing.T) {
				jobs := make([]Job, 4)
				for i := range jobs {
					jobs[i] = Job{ID: fmt.Sprintf("job-%d", i), Kind: kinds[i%len(kinds)], FirstEligibleAt: now}
				}
				// The winning key must dominate all later keys, not just input order.
				switch key {
				case "age":
					jobs[0].FirstEligibleAt = now.Add(-time.Nanosecond)
					jobs[0].SchedulePriority, jobs[0].ScheduleOrdinal, jobs[0].ID = 99, 99, "z"
				case "priority":
					jobs[0].SchedulePriority = -1
					jobs[0].ScheduleOrdinal, jobs[0].ID = 99, "z"
				case "ordinal":
					jobs[0].ScheduleOrdinal, jobs[0].ID = -1, "z"
				}
				want := jobs[0]
				var permute func(int)
				permute = func(start int) {
					if start == len(jobs) {
						before := append([]Job(nil), jobs...)
						counters := ScheduleCounters{2, 3}
						got, ok := ChooseTeamJob(jobs, counters)
						if !ok || !reflect.DeepEqual(got, want) {
							t.Fatalf("input %+v: chose %+v, %v; want %+v", jobs, got, ok, want)
						}
						if !reflect.DeepEqual(jobs, before) || counters != (ScheduleCounters{2, 3}) {
							t.Fatal("selection mutated candidates or counters")
						}
						return
					}
					for i := start; i < len(jobs); i++ {
						jobs[start], jobs[i] = jobs[i], jobs[start]
						permute(start + 1)
						jobs[start], jobs[i] = jobs[i], jobs[start]
					}
				}
				permute(0)
			})
		}
	}
}

func TestTeamFairnessCompletedService(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ScheduleCounters
		hand ScheduleCounters
		fore ScheduleCounters
	}{
		{"zero", ScheduleCounters{}, ScheduleCounters{1, 1}, ScheduleCounters{0, 1}},
		{"below caps", ScheduleCounters{1, 2}, ScheduleCounters{2, 3}, ScheduleCounters{0, 3}},
		{"saturated", ScheduleCounters{2, 3}, ScheduleCounters{2, 3}, ScheduleCounters{0, 3}},
	} {
		for _, kind := range []string{"review", "fix", "deliver", "qa", "plan", "wake", "implement"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				in := tc.in
				job := Job{ID: "completed", Kind: kind}
				before := job
				want := tc.fore
				switch kind {
				case "review", "fix", "deliver", "qa":
					want = tc.hand
				case "implement":
					want = ScheduleCounters{}
				}
				if got := completedService(in, job); got != want {
					t.Fatalf("completedService(%+v, %s) = %+v, want %+v", in, kind, got, want)
				}
				if in != tc.in || !reflect.DeepEqual(job, before) {
					t.Fatal("completion accounting mutated its inputs")
				}
			})
		}
	}
	var counters ScheduleCounters
	for i := 0; i < 100; i++ {
		counters = completedService(counters, Job{Kind: "review"})
	}
	if counters != (ScheduleCounters{2, 3}) {
		t.Fatalf("continuous handoffs did not saturate: %+v", counters)
	}
	for i := 0; i < 100; i++ {
		counters = completedService(counters, Job{Kind: "wake"})
	}
	if counters != (ScheduleCounters{0, 3}) {
		t.Fatalf("continuous foreground did not saturate: %+v", counters)
	}
}
