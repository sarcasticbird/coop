package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeAuthorityFixture struct {
	authority c.GoalAuthority
	err       error
	calls     int
}

func (s *nativeAuthorityFixture) Resolve(ctx context.Context, ref c.GoalRef) (c.GoalAuthority, error) {
	s.calls++
	return s.authority, s.err
}
func nativeBindingFixture() (NativeTaskBinding, *nativeAuthorityFixture) {
	ref := c.GoalRef{Source: "master", MasterID: "master", GoalID: "goal", RevisionSHA256: strings.Repeat("a", 64)}
	b := NativeTaskBinding{Version: 3, Goal: ref, TaskURL: "https://github.com/code/repo/issues/1", TaskScopeSHA: strings.Repeat("b", 64), PolicySHA: strings.Repeat("c", 64), ApprovalSequence: 1, BaseBranch: "main", AuthorID: 2, PeerID: 3, QAID: 4}
	a := c.GoalAuthority{Revision: c.GoalRevision{Ref: ref, Proposal: c.GoalProposal{ThreadID: "private", Scope: "approved", AuthorID: "author", PeerID: "peer", Repositories: []c.RepositoryScope{{Name: "code/repo", BaseBranches: []string{"main"}}}, QA: c.QAPolicy{Required: true, ActorID: "qa", Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceDestination: c.EvidenceDestination{MasterID: "master", ThreadID: "private", Engagement: "coop"}}}}, State: "approved", ActiveRevisionSHA256: ref.RevisionSHA256, DecisionSequence: 1, ApprovalSequence: 1, Valid: true, Actors: c.GoalActors{Author: c.GoalActor{PrincipalID: "author", GitHubBotID: 2}, Peer: c.GoalActor{PrincipalID: "peer", GitHubBotID: 3}, QA: c.GoalActor{PrincipalID: "qa", GitHubBotID: 4}}}
	return b, &nativeAuthorityFixture{authority: a}
}
func TestNativeGoalAdmissionAndEffects(t *testing.T) {
	b, source := nativeBindingFixture()
	if _, err := ValidateNativeAuthority(context.Background(), source, b); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"paused", "completed", "unapproved"} {
		source.authority.State = state
		source.authority.Valid = false
		if _, err := ValidateNativeAuthority(context.Background(), source, b); err == nil {
			t.Fatalf("%s authority accepted", state)
		}
	}
	source.err = errors.New("offline")
	if _, err := ValidateNativeAuthority(context.Background(), source, b); err == nil {
		t.Fatal("unavailable authority accepted")
	}
	if source.calls != 5 {
		t.Fatal("authority reused from cache")
	}
}
func TestNativeSupersessionBinding(t *testing.T) {
	b, s := nativeBindingFixture()
	s.authority.ActiveRevisionSHA256 = strings.Repeat("d", 64)
	if _, err := ValidateNativeAuthority(context.Background(), s, b); err == nil {
		t.Fatal("superseded assignment accepted")
	}
	b, s = nativeBindingFixture()
	s.authority.DecisionSequence = 4
	if _, err := ValidateNativeAuthority(context.Background(), s, b); err != nil {
		t.Fatal("draft-only decision sequence interrupted active approval")
	}
}
func TestNativePrivateQAGate(t *testing.T) {
	b, s := nativeBindingFixture()
	qa := NativeQAResult{Version: 3, Goal: b.Goal, AssignmentSHA: NativeAssignmentSHA(b), TaskURL: b.TaskURL, HeadSHA: strings.Repeat("a", 40), PRScopeRevision: BodyHash("scope"), ArtifactSHA: strings.Repeat("e", 64), AcceptanceSHA: NativeAcceptanceSHA(s.authority), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "report", EvidenceBodySHA: BodyHash("private report"), Verdict: "approved"}
	review := Review{ID: "review", State: "APPROVED", HeadSHA: qa.HeadSHA, Author: Actor{ID: 4, Kind: "Bot", Login: "qa[bot]"}, Body: NativeQAMetadata(qa)}
	report := c.Message{ID: "report", ThreadID: "private", AuthorID: "qa", AuthorKind: "agent", Body: "private report"}
	if err := ValidateNativeQA(b, s.authority, review, report, qa); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Review, *c.Message){func(r *Review, _ *c.Message) { r.ID = "" }, func(r *Review, _ *c.Message) { r.Author.ID = 2 }, func(r *Review, _ *c.Message) { r.HeadSHA = "stale" }, func(_ *Review, m *c.Message) { m.ThreadID = "public" }, func(_ *Review, m *c.Message) { m.Body = "changed" }} {
		r, m := review, report
		mutate(&r, &m)
		if err := ValidateNativeQA(b, s.authority, r, m, qa); err == nil {
			t.Fatal("invalid QA evidence accepted")
		}
	}
	for name, mutate := range map[string]func(*NativeQAResult){"acceptance": func(q *NativeQAResult) { q.AcceptanceSHA = BodyHash("unapproved acceptance") }, "environment revision": func(q *NativeQAResult) { q.EnvironmentRevision = "stale" }, "verdict": func(q *NativeQAResult) { q.Verdict = "" }, "head": func(q *NativeQAResult) { q.HeadSHA = "head" }, "scope": func(q *NativeQAResult) { q.PRScopeRevision = "" }} {
		q, r := qa, review
		mutate(&q)
		r.HeadSHA = q.HeadSHA
		r.Body = NativeQAMetadata(q)
		if err := ValidateNativeQA(b, s.authority, r, report, q); err == nil {
			t.Fatalf("self-consistent out-of-scope %s accepted", name)
		}
	}
}
