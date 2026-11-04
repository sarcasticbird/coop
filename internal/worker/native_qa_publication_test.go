package worker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Private messaging is deliberately unavailable. Routine QA publication,
// discovery and delivery selection must still use the actual GitHub review.
type qaWithoutPrivateMessages struct {
	*releaseMasterFixture
	reads, sends int
}

func (m *qaWithoutPrivateMessages) Message(context.Context, string) (c.Message, error) {
	m.reads++
	return c.Message{}, c.ErrForbidden
}

func (m *qaWithoutPrivateMessages) Send(context.Context, c.SendMessage) (c.MessageReceipt, error) {
	m.sends++
	return c.MessageReceipt{}, c.ErrForbidden
}

func TestNativeQAReviewDoesNotRequirePrivatePublication(t *testing.T) {
	f := newPostMergeQAActionFixture(t)
	f.transport.merged, f.transport.closed = false, false
	// Select an ordinary pre-merge QA job, rather than the fixture's postmerge one.
	cat, err := f.github.NativeSnapshot(t.Context(), f.cfg, f.master)
	if err != nil {
		t.Fatal(err)
	}
	jobs := NativeCandidates(f.cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "qa" || nativeIsPostMergeQA(jobs[0].Job) {
		t.Fatalf("missing ordinary QA job: %+v", jobs)
	}
	v := jobs[0]
	if err := os.Remove(nativeExecutionPath(f.cfg, f.execution.Job.ID)); err != nil {
		t.Fatal(err)
	}
	f.execution = NativeExecution{Version: 3, MasterID: f.cfg.Master.MasterID, InstanceID: f.cfg.Master.InstanceID, Generation: f.cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	if err := saveNativeExecution(f.cfg, f.execution); err != nil {
		t.Fatal(err)
	}
	m := &qaWithoutPrivateMessages{releaseMasterFixture: f.master}
	result, err := RunNativeAction(t.Context(), f.cfg, m, f.github, f.execution.Job.ID, f.action)
	if err != nil || result.Phase != "completed" || result.EvidenceMessageID != "" || result.EvidenceBodySHA != "" {
		t.Fatal("QA depended on private publication", result, err)
	}
	intent, err := readPrivate(nativeActionIntentPath(f.cfg, f.execution.Job.ID, f.action.RequestID), c.MaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	var saved nativeActionIntent
	if err := strictTeamJSON(intent, &saved); err != nil || saved.Action.ReportBody != f.action.ReportBody {
		t.Fatal("private report was not retained in its original intent", err)
	}
	cat, err = f.github.NativeSnapshot(t.Context(), f.cfg, m)
	if err != nil {
		t.Fatal("discovery fetched a private report", err)
	}
	task := cat.Tasks[0]
	pr := task.PRs[0]
	review := nativeLatestReview(pr, task.Binding.QAID)
	if strings.Contains(review.Body, f.action.ReportBody) || strings.Contains(review.Body, "Private evidence is retained") {
		t.Fatal("private report leaked or nonexistent publication claimed")
	}
	selectNativeRook(&f.cfg, nil)
	jobs = NativeCandidates(f.cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" {
		t.Fatalf("actual QA review did not clear Rook: %+v", jobs)
	}
	for _, legacy := range []bool{false, true} {
		// Old review references remain data, not an obligation to fetch a message.
		if legacy {
			var qa NativeQAResult
			if err := nativeMetadata(review.Body, "qa", &qa); err != nil {
				t.Fatal(err)
			}
			qa.EvidenceMessageID, qa.EvidenceBodySHA = "historical-unavailable-report", BodyHash("private")
			for i := range pr.Reviews {
				if pr.Reviews[i].ID == review.ID {
					pr.Reviews[i].Body = NativeQAMetadata(qa)
				}
			}
		}
		cache := &fakeCacheClient{data: map[string][]byte{}, page: c.GitHubCatalogPage{Version: 1, Mode: "on", MasterID: f.cfg.Master.MasterID, RestoreEpoch: 1, Manifest: BodyHash("manifest"), Goals: cat.Goals}}
		addCachedTask(t, cache, task)
		raw, err := json.Marshal(pr)
		if err != nil {
			t.Fatal(err)
		}
		ref := c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: pr.URL, Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}
		digest := c.GitHubDigest(raw)
		cache.data[digest] = raw
		cache.page.Objects = append(cache.page.Objects, c.GitHubCoverage{Object: ref, DueAt: time.Now().Add(time.Minute), Header: c.GitHubHeader{Revision: digest, HeadSHA: pr.HeadSHA, BaseSHA: pr.BaseSHA, ScopeRevision: pr.ScopeRevision}, Datasets: []c.GitHubDatasetRef{{Object: ref, Kind: "pr", SHA256: digest, HeaderRevision: digest, Total: len(raw), Version: 1, DueAt: time.Now().Add(time.Minute)}}})
		cached, err := NewCachedNativeReader(f.cfg, f.github, cache, RealClock{}).NativeSnapshot(t.Context(), f.cfg, m)
		if err != nil || len(NativeCandidates(f.cfg, cached, nil, time.Now())) != 1 {
			t.Fatal("cached QA clearance depended on private messaging", legacy, cached.Cache, err)
		}
	}
	if m.reads != 0 || m.sends != 0 || f.sends != 0 || len(f.events) != 1 || f.events[0] != "APPROVE" {
		t.Fatal("expected one PR review and no private-message effects", m.reads, m.sends, f.events)
	}
}
