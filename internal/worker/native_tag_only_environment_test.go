package worker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Change only renewed delivery authority, before readiness and the source merge.
// The approved source QA, its report and all retained source assertions stay put.
func tagOnlyRenewDeliveryEnvironment(t *testing.T, cfg Config, m *dispatchMaster, g *GitHubClient, transport *nativeDeliveryTransport, binding NativeTaskBinding) {
	t.Helper()
	gates := &m.handoffs[0]
	var oldqa NativeQAResult
	if err := nativeMetadata(transport.reviews[1]["body"].(string), "qa", &oldqa); err != nil {
		t.Fatal(err)
	}
	oldqa.Verdict, oldqa.EvidenceMessageID, oldqa.EvidenceBodySHA = "changes-needed", "oldqa-report", BodyHash("old QA needs environment repair")
	m.reports[oldqa.EvidenceMessageID] = c.Message{ID: oldqa.EvidenceMessageID, AuthorID: "qa", AuthorKind: "agent", ThreadID: m.authority.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: "old QA needs environment repair"}
	transport.reviews = append(transport.reviews, map[string]any{"fullDatabaseId": 3, "state": "CHANGES_REQUESTED", "body": NativeQAMetadata(oldqa), "submittedAt": time.Now().Add(-30 * time.Minute).UTC(), "commit": map[string]any{"oid": transport.head}, "author": map[string]any{"__typename": "Bot", "databaseId": binding.QAID, "login": "qa[bot]"}})
	gates.Input.Environment, gates.Input.EnvironmentRevision = "container", "container-v2"
	h := c.Handoff{ID: "renewed-environment", ActorID: "owner", ActorKind: "owner", CreatedAt: time.Now().Add(-20 * time.Minute).UTC(), Input: c.HandoffInput{Kind: "reassess-qa", Goal: binding.Goal, TaskURL: binding.TaskURL, PRURL: gates.Input.PRURL, AssignmentSHA: NativeAssignmentSHA(binding), HeadSHA: oldqa.HeadSHA, ArtifactSHA: oldqa.ArtifactSHA, ReviewID: "3", Environment: "container", EnvironmentRevision: "container-v2", EvidenceMessageID: "renewal-decision", RequestID: "renewal-decision"}}
	m.reports[h.Input.EvidenceMessageID] = c.Message{ID: h.Input.EvidenceMessageID, AuthorID: "owner", AuthorKind: "owner", ThreadID: m.authority.Revision.Proposal.ThreadID, Body: "Renew delivery assertions in container-v2; retain original source QA"}
	m.handoffs = append(m.handoffs, h)
	task := NativeTask{Task: Task{URL: binding.TaskURL, Repo: "code/repo"}, Binding: binding}
	p, err := g.ReadNativePR(t.Context(), cfg, task, m.authority, h.Input.PRURL)
	if err != nil {
		t.Fatal(err)
	}
	if env, err := resolveNativeQAEnvironment(binding, m.authority, p, &h, m.handoffs); err != nil || env.Name != "container" || env.Revision != "container-v2" {
		t.Fatal("fixture lacks exact current environment authority", env, err)
	}
}

func TestTagOnlyProductionEnvironmentJourney(t *testing.T) {
	for _, version := range []bool{false, true} {
		name := "source-only"
		if version {
			name = "version"
		}
		t.Run(name, func(t *testing.T) {
			cfg, m, g, transport, x, api := tagOnlyProductionEnvironmentFixture(t, version, true)
			metadata, prBody, qaBody := transport.metadata, transport.prBody, transport.reviews[1]["body"].(string)
			var qa NativeQAResult
			if err := nativeMetadata(qaBody, "qa", &qa); err != nil {
				t.Fatal(err)
			}
			source := m.handoffs[2]
			r := source.Input.Retained
			if !transport.merged || !transport.closed || qa.Environment != "staging" || qa.EnvironmentRevision != "staging-v1" || r.SourceEnvironment != qa.Environment || r.SourceEnvironmentRevision != qa.EnvironmentRevision || r.SourceQAReviewID != source.Input.QAReviewID || r.SourceQABodySHA != BodyHash(qaBody) || r.SourceReportID != qa.EvidenceMessageID || r.SourceReportBodySHA != qa.EvidenceBodySHA || BodyHash(m.reports[qa.EvidenceMessageID].Body) != qa.EvidenceBodySHA || source.Input.Environment != "container" || source.Input.EnvironmentRevision != "container-v2" {
				t.Fatal("source merge did not preserve historical QA and renewed delivery", source)
			}
			originalReport := m.reports[qa.EvidenceMessageID]
			cat, err := g.NativeSnapshot(t.Context(), cfg, m)
			if err != nil {
				t.Fatal(err)
			}
			jobs := NativeCandidates(cfg, cat, nil, time.Now())
			if len(jobs) != 1 || jobs[0].Job.ID != x.Job.ID {
				t.Fatal("environment transition lost tag candidate", jobs)
			}
			h := jobs[0].Handoff
			if h.Input.TagOnly.TagEnvironment != "tag-device" || h.Input.TagOnly.TagEnvironmentRevision != "tag-device-v3" {
				t.Fatal("tag environment collapsed into delivery environment", h)
			}
			for _, id := range []string{h.Input.TagOnly.EngineeringReportID, h.Input.TagOnly.QAReportID} {
				var report c.TagOnlyReport
				if err := json.Unmarshal([]byte(m.reports[id].Body), &report); err != nil || report.Binding != c.TagOnlyReportBinding(h.Input) {
					t.Fatal("tag report lost distinct environment binding", id, err)
				}
			}
			d := &NativeDispatcher{config: cfg, master: m, github: g, clock: RealClock{}}
			if _, err := d.freshCandidate(t.Context(), x); err != nil {
				t.Fatal("fresh tag environment selection", err)
			}
			api.lost = "ref"
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil {
				t.Fatal("lost tag ref acknowledgement was not uncertain")
			}
			if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
				t.Fatal("uncertain action completed receipt", yes, err)
			}
			m.authority.State = "paused"
			before := tagOnlyDiskSnapshot(t, cfg)
			result, err := ObserveNativeTagOnly(t.Context(), cfg, m, g, x.Goal, x.Job.Master.DeliveryHandoffID)
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			if err != nil || result.Phase != "observed" || len(api.writes) != 2 {
				t.Fatal("read-only tag environment observation", result, err, api.writes)
			}
			if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil {
				t.Fatal("paused confirmation admitted")
			}
			tagOnlyAssertDiskUnchanged(t, cfg, before)
			m.authority.State = "approved"
			result, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"})
			if err != nil || result.Phase != "completed" || len(api.writes) != 2 {
				t.Fatal("authorized tag confirmation", result, err, api.writes)
			}
			intent, err := readTagOnlyIntent(cfg, h.ID)
			if err != nil || intent.Binding.Target.SHA != c.TagOnlyTargetSHA(h.Input) {
				t.Fatal("wrong environment journey tag target", err)
			}
			if yes, err := nativeJobHasResult(cfg, x); !yes || err != nil {
				t.Fatal("missing confirmed receipt", yes, err)
			}
			if transport.metadata != metadata || transport.prBody != prBody || transport.reviews[1]["body"] != qaBody || !reflect.DeepEqual(m.reports[qa.EvidenceMessageID], originalReport) || len(transport.writes) != 0 {
				t.Fatal("tag journey mutated original source proof", transport.writes)
			}
		})
	}
}

func TestTagOnlyProductionEnvironmentDenials(t *testing.T) {
	for _, version := range []bool{false, true} {
		for _, change := range []string{"historical-environment", "historical-revision", "current-environment", "current-revision", "authority-missing", "authority-revoked", "authority-not-owner", "authority-review", "tag-environment", "tag-revision", "tag-report", "source-input-digest"} {
			name := "source-only/" + change
			if version {
				name = "version/" + change
			}
			t.Run(name, func(t *testing.T) {
				cfg, m, g, transport, x, api := tagOnlyProductionEnvironmentFixture(t, version, true)
				tagIndex := len(m.handoffs) - 1
				if version {
					tagIndex--
				}
				h := &m.handoffs[tagIndex]
				source := &m.handoffs[2]
				switch change {
				case "historical-environment":
					source.Input.Retained.SourceEnvironment = "other-source"
				case "historical-revision":
					source.Input.Retained.SourceEnvironmentRevision = "other-source-v1"
				case "current-environment":
					h.Input.Environment = "unauthorized-container"
					source.Input.Environment = h.Input.Environment
				case "current-revision":
					h.Input.EnvironmentRevision = "unauthorized-v3"
					source.Input.EnvironmentRevision = h.Input.EnvironmentRevision
				case "authority-missing":
					// Remove after renewing reports, while h still points at the tag.
				case "authority-revoked":
					m.handoffs[1].Input.Revoked = true
				case "authority-not-owner":
					m.handoffs[1].ActorKind = "agent"
				case "authority-review":
					m.handoffs[1].Input.ReviewID = source.Input.QAReviewID
				case "tag-environment":
					h.Input.TagOnly.TagEnvironment = "other-device"
				case "tag-revision":
					h.Input.TagOnly.TagEnvironmentRevision = "other-device-v4"
				case "tag-report":
					msg := m.reports[h.Input.TagOnly.QAReportID]
					var report c.TagOnlyReport
					if err := json.Unmarshal([]byte(msg.Body), &report); err != nil {
						t.Fatal(err)
					}
					other := h.Input
					other.TagOnly.TagEnvironment = "other-device"
					report.Binding = c.TagOnlyReportBinding(other)
					raw, _ := json.Marshal(report)
					msg.Body = string(raw)
					m.reports[msg.ID] = msg
					h.Input.TagOnly.QAReportBodySHA = BodyHash(msg.Body)
				case "source-input-digest":
					h.Input.TagOnly.SourceDeliveryInputSHA = BodyHash("not the original source input")
				}
				if change == "historical-environment" || change == "historical-revision" || change == "current-environment" || change == "current-revision" {
					h.Input.TagOnly.SourceDeliveryInputSHA = tagOnlyInputSHA(source.Input)
				}
				if change != "tag-environment" && change != "tag-revision" && change != "tag-report" {
					cat := NativeCatalog{Goals: []c.GoalAuthority{m.authority}, Reports: m.reports, Handoffs: []c.Handoff{*h}}
					renewTagOnlyReports(t, &cat, h)
				}
				if change == "authority-missing" {
					m.handoffs = append(m.handoffs[:1], m.handoffs[2:]...)
				}
				cat, err := g.NativeSnapshot(t.Context(), cfg, m)
				if err != nil && change != "authority-revoked" {
					t.Fatal(err)
				}
				if err == nil && len(NativeCandidates(cfg, cat, nil, time.Now())) != 0 {
					t.Fatal("unsafe environment candidate")
				}
				if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "tag-only", RequestID: "tag-request"}); err == nil {
					t.Fatal("unsafe environment tag admitted")
				}
				if len(api.writes) != 0 || len(transport.writes) != 0 {
					t.Fatal("environment denial wrote externally", api.writes, transport.writes)
				}
				if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
					t.Fatal("denied environment completed receipt", yes, err)
				}
			})
		}
	}
}

func TestTagOnlyRetainedEnvironmentBeforeMerge(t *testing.T) {
	for _, change := range []string{"exact", "historical-environment", "current-environment", "authority-missing", "authority-review"} {
		t.Run(change, func(t *testing.T) {
			cfg, m, g, _, transport, binding := retainedProductionFixture(t)
			qaBody := transport.reviews[1]["body"].(string)
			tagOnlyRenewDeliveryEnvironment(t, cfg, m, g, transport, binding)
			retainedReports(t, &cfg, m, g, transport, binding)
			x := retainedExecution(t, cfg, m, g, binding, "deliver")
			switch change {
			case "historical-environment":
				m.handoffs[2].Input.Retained.SourceEnvironment = "not-staging"
			case "current-environment":
				m.handoffs[2].Input.Environment = "not-container"
			case "authority-missing":
				m.handoffs = append(m.handoffs[:1], m.handoffs[2:]...)
			case "authority-review":
				m.handoffs[1].Input.ReviewID = m.handoffs[2].Input.QAReviewID
			}
			result, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "environment-source-complete"})
			if change == "exact" {
				if err != nil || result.Phase != "completed" || !transport.merged || !transport.closed {
					t.Fatal("renewed environment source merge", result, err)
				}
			} else {
				if err == nil || transport.merged || len(transport.writes) != 0 {
					t.Fatal("unsafe environment source merge", err, transport.writes)
				}
				if yes, err := nativeJobHasResult(cfg, x); yes || err != nil {
					t.Fatal("denied source merge completed receipt", yes, err)
				}
			}
			if transport.reviews[1]["body"] != qaBody {
				t.Fatal("renewal rewrote historical source QA")
			}
		})
	}
}
