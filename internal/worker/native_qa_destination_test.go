package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type goalThreadOnlyMaster struct {
	*dispatchMaster
	reads []string
}

func (m *goalThreadOnlyMaster) Thread(ctx context.Context, id string) (c.Thread, error) {
	m.reads = append(m.reads, id)
	if id != m.authority.Revision.Proposal.ThreadID {
		return c.Thread{}, c.ErrUnavailable
	}
	return m.dispatchMaster.Thread(ctx, id)
}

func TestNativeCurrentAuthorityDoesNotReadPrivateQADestination(t *testing.T) {
	for _, fault := range []string{"none", "missing", "foreign", "goal-membership", "goal-engagement", "paused"} {
		t.Run(fault, func(t *testing.T) {
			cfg, master, catalog, _ := dispatchFixture(t)
			master.authority.Revision.Proposal.QA.EvidenceDestination = c.EvidenceDestination{}
			if fault != "none" {
				master.authority.Revision.Proposal.QA.EvidenceDestination = c.EvidenceDestination{MasterID: cfg.Master.MasterID, ThreadID: "old-private-report", Engagement: cfg.Master.Engagement}
			}
			switch fault {
			case "foreign":
				master.authority.Revision.Proposal.QA.EvidenceDestination.MasterID = "foreign-master"
			case "goal-membership":
				master.threads[0].Members = []string{"author", "peer", "rook"}
			case "goal-engagement":
				master.threads[0].Engagement = "foreign"
			case "paused":
				master.authority.State, master.authority.Valid = "paused", false
			}
			before, _ := json.Marshal(master.authority.Revision)
			m := &goalThreadOnlyMaster{dispatchMaster: master}
			_, err := nativeCurrentGoal(t.Context(), cfg, m, master.authority.Revision.Ref)
			allowed := fault == "none" || fault == "missing" || fault == "foreign"
			if (err == nil) != allowed {
				t.Fatal("incorrect authority decision", fault, err)
			}
			for _, ref := range m.reads {
				if ref != master.authority.Revision.Proposal.ThreadID {
					t.Fatal("read obsolete QA report destination", ref)
				}
			}
			if allowed {
				if len(m.reads) != 1 {
					t.Fatal("current goal thread was not checked exactly once", m.reads)
				}
				catalog.catalog.Goals[0] = master.authority
				candidate := NativeCandidates(cfg, catalog.catalog, nil, time.Now())[0]
				x := NativeExecution{Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Binding: &candidate.Task.Binding}
				dispatcher, err := NewNativeDispatcher(cfg, m, catalog, &fakeOpenCode{}, &fakeCheckouts{}, RealClock{})
				if err != nil {
					t.Fatal(err)
				}
				fresh, err := dispatcher.freshCandidate(t.Context(), x)
				if err != nil || fresh.Job.ID != candidate.Job.ID || fresh.Job.TaskURL != candidate.Job.TaskURL {
					t.Fatal("private destination stranded current engineering job", err)
				}
			}
			after, _ := json.Marshal(master.authority.Revision)
			if string(before) != string(after) {
				t.Fatal("historical destination or revision was rewritten")
			}
		})
	}
}
