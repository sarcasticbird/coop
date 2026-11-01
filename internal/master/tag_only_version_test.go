package master

import (
	"context"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type tagOnlyTasks struct {
	*retainedTasks
	version c.ProviderTask
}

func (b *tagOnlyTasks) Read(ctx context.Context, ref string) (c.ProviderTask, error) {
	if ref == b.version.Reference {
		return b.version, nil
	}
	return b.retainedTasks.Read(ctx, ref)
}

func (b *tagOnlyTasks) Complete(ctx context.Context, in c.TaskCompleteInput, actor c.Principal) (c.ProviderTask, error) {
	if in.Reference == b.version.Reference {
		b.version.State, b.version.ClosedReason = "closed", "done"
		return b.version, nil
	}
	return b.retainedTasks.Complete(ctx, in, actor)
}

func masterTagVersionFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.HandoffInput, *tagOnlyTasks) {
	t.Helper()
	s, owner, rook, in, sourceTasks := masterTagOnlyFixture(t)
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	version := sourceTasks.task
	version.Reference, err = c.KataTaskReference(sourceTasks.Capabilities().ProjectUID, "01M5MSA8FZCCD7DWMTP6RWM3ME", "example/coop")
	if err != nil {
		t.Fatal(err)
	}
	version.Title, version.Body = "Version source", "Set the exact tag version, preserving executable and library payload"
	binding := *version.Binding
	binding.TaskURL, binding.PolicySHA = version.Reference, sourceTasks.policy
	binding.TaskScopeSHA = digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{version.Title, version.Body})
	version.Binding = &binding
	backend := &tagOnlyTasks{retainedTasks: sourceTasks, version: version}
	s.Tasks = backend
	for _, link := range []c.ArtifactLink{{Goal: in.Goal, Kind: "task", URL: version.Reference, AssignmentSHA: digest(binding), RequestID: "version-task"}, {Goal: in.Goal, Kind: "pull_request", TaskURL: version.Reference, URL: "https://github.com/example/coop/pull/10", AssignmentSHA: digest(binding), RequestID: "version-pr"}} {
		actor := owner
		if link.Kind == "pull_request" {
			actor = c.Principal{ID: a.Revision.Proposal.AuthorID}
		}
		if _, err := s.RegisterArtifact(t.Context(), actor, link); err != nil {
			t.Fatal(err)
		}
	}
	v := in
	v.Kind, v.Tag, v.TagOnly, v.RequestID = "deliver", "", c.TagOnlyAuthority{}, "version-deliver"
	v.TaskURL, v.PRURL, v.AssignmentSHA, v.TaskScopeSHA = version.Reference, "https://github.com/example/coop/pull/10", digest(binding), binding.TaskScopeSHA
	v.HeadSHA, v.ArtifactSHA, v.PRScopeRevision, v.BaseSHA = strings.Repeat("c", 40), strings.Repeat("c", 64), strings.Repeat("d", 64), in.TagOnly.IntegratedCommitSHA
	delivery, err := s.CreateHandoff(t.Context(), owner, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TaskComplete(t.Context(), rook, c.TaskCompleteInput{Goal: v.Goal, Reference: v.TaskURL, AssignmentSHA: v.AssignmentSHA, PullRequestURL: v.PRURL, DeliveryHandoffID: delivery.ID, RequestID: "version-complete"}); err != nil {
		t.Fatal(err)
	}
	r := &in.TagOnly
	r.VersionGoal, r.VersionTaskURL, r.VersionPRURL, r.VersionHeadSHA, r.VersionBaseSHA, r.VersionCommitSHA, r.VersionTreeSHA = in.Goal, version.Reference, v.PRURL, v.HeadSHA, v.BaseSHA, strings.Repeat("d", 40), strings.Repeat("e", 40)
	r.VersionDeliveryHandoffID, r.VersionDeliveryInputSHA = delivery.ID, digest(delivery.Input)
	r.VersionCompletionRequestID = "version-complete"
	r.PayloadSchema, r.PayloadSHA, r.SourceBundleSHA, r.VersionBundleSHA = c.TagOnlyPayloadV1, strings.Repeat("e", 64), strings.Repeat("e", 64), v.ArtifactSHA
	for _, actor := range []string{"hawk", "wren"} {
		body := string(encode(c.TagOnlyReport{Binding: c.TagOnlyReportBinding(in), Result: "ready"}))
		out, err := s.Store.Send(t.Context(), c.Principal{ID: actor}, c.SendMessage{ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: body, RequestID: "version-tag-report-" + actor})
		if err != nil {
			t.Fatal(err)
		}
		if actor == "hawk" {
			r.EngineeringReportID, r.EngineeringReportBodySHA = out.MessageID, retainedBodySHA(body)
		} else {
			r.QAReportID, r.QAReportBodySHA = out.MessageID, retainedBodySHA(body)
		}
	}
	return s, owner, rook, in, backend
}

func TestMasterTagOnlyVersionNativeProvenance(t *testing.T) {
	for _, change := range []string{"none", "missing-registration", "registration-assignment", "version-binding", "version-open", "version-policy", "version-goal", "version-body", "version-revoked", "source-revoked", "original-policy", "report-imported", "source-completion", "version-completion", "version-completion-actor"} {
		t.Run(change, func(t *testing.T) {
			s, owner, rook, in, tasks := masterTagVersionFixture(t)
			h, err := s.CreateHandoff(t.Context(), owner, in)
			if err != nil {
				t.Fatal("tag authorization", err)
			}
			if change == "none" {
				if _, err = s.ValidateTagOnlyHandoff(t.Context(), rook, c.TagOnlyValidation{Goal: in.Goal, HandoffID: h.ID}); err != nil {
					t.Fatal(err)
				}
				return
			}
			switch change {
			case "source-completion", "version-completion":
				id := in.TagOnly.SourceCompletionRequestID
				if change == "version-completion" {
					id = in.TagOnly.VersionCompletionRequestID
				}
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM requests WHERE actor='rook' AND request_id=?", id)
			case "version-completion-actor":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE requests SET actor=? WHERE actor='rook' AND request_id=?", owner.ID, in.TagOnly.VersionCompletionRequestID)
			case "missing-registration":
				_, err = s.Store.db.ExecContext(t.Context(), "DELETE FROM artifacts WHERE url=?", in.TagOnly.VersionPRURL)
			case "registration-assignment":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE artifacts SET assignment_sha=? WHERE url=?", strings.Repeat("f", 64), in.TagOnly.VersionPRURL)
			case "version-binding":
				tasks.version.Binding.TaskScopeSHA = strings.Repeat("f", 64)
			case "version-open":
				tasks.version.State = "open"
			case "version-policy":
				tasks.version.Binding.PolicySHA = strings.Repeat("f", 64)
			case "version-goal":
				tasks.version.Goal.RevisionSHA256 = strings.Repeat("f", 64)
			case "version-body":
				tasks.version.Body += "drift"
			case "original-policy":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE handoffs SET body=json_set(body,'$.input.retained.execution_policy_sha256',?) WHERE id=?", strings.Repeat("f", 64), in.TagOnly.SourceDeliveryHandoffID)
			case "report-imported":
				_, err = s.Store.db.ExecContext(t.Context(), "UPDATE messages SET body=json_set(body,'$.imported',json('true')) WHERE id=?", in.TagOnly.QAReportID)
			case "version-revoked", "source-revoked":
				id := in.TagOnly.VersionDeliveryHandoffID
				if change == "source-revoked" {
					id = in.TagOnly.SourceDeliveryHandoffID
				}
				proof, e := s.tagOnlyDeliveryProof(t.Context(), s.Store.db, owner, in.Goal, id, map[bool]string{true: in.TagOnly.SourceDeliveryInputSHA, false: in.TagOnly.VersionDeliveryInputSHA}[change == "source-revoked"])
				if e != nil {
					t.Fatal(e)
				}
				withdraw := proof.Input
				withdraw.Revoked, withdraw.RequestID = true, change
				_, err = s.CreateHandoff(t.Context(), owner, withdraw)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ValidateTagOnlyHandoff(t.Context(), rook, c.TagOnlyValidation{Goal: in.Goal, HandoffID: h.ID}); err == nil {
				t.Fatal("drifted version/source provenance accepted")
			}
		})
	}
}
