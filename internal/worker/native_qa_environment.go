package worker

import (
	"context"
	"errors"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeQAHandoffs(ctx context.Context, m NativeMaster, a c.GoalAuthority) ([]c.Handoff, error) {
	cat := NativeCatalog{Goals: []c.GoalAuthority{a}}
	err := loadCatalogHandoffs(ctx, m, &cat)
	return cat.Handoffs, err
}

func nativeJobQAEnvironment(ctx context.Context, m NativeMaster, x NativeExecution, a c.GoalAuthority, p PR) (nativeQAEnvironment, *c.Handoff, error) {
	if !strings.Contains(x.Job.InputRevision, ":reassess:") {
		env, err := resolveNativeQAEnvironment(*x.Binding, a, p, nil, nil)
		return env, nil, err
	}
	handoffs, err := nativeQAHandoffs(ctx, m, a)
	if err != nil {
		return nativeQAEnvironment{}, nil, err
	}
	for i := range handoffs {
		h := &handoffs[i]
		if p.QAArtifact != nil && x.Job.InputRevision == p.ScopeRevision+":"+p.QAArtifact.SHA256+":"+NativeAcceptanceSHA(a)+":reassess:"+h.ID {
			env, err := resolveNativeQAEnvironment(*x.Binding, a, p, h, handoffs)
			return env, h, err
		}
	}
	return nativeQAEnvironment{}, nil, errors.New("QA reassessment authority missing")
}

// One resolver supplies the prompt, published result and clearance gates. The
// goal remains the default; an immutable owner handoff can change only this
// exact artifact's environment, without rebinding its engineering assignment.
type nativeQAEnvironment struct {
	Name, Revision string
}

func nativeQAScope(b NativeTaskBinding, a c.GoalAuthority, p PR, q NativeQAResult) bool {
	return p.QAArtifact != nil && q.Version == 3 && q.Goal == b.Goal && q.TaskURL == b.TaskURL && q.AssignmentSHA == NativeAssignmentSHA(b) && q.HeadSHA == p.HeadSHA && q.PRScopeRevision == p.ScopeRevision && q.ArtifactSHA == p.QAArtifact.SHA256 && q.AcceptanceSHA == NativeAcceptanceSHA(a)
}

func resolveNativeQAEnvironment(b NativeTaskBinding, a c.GoalAuthority, p PR, h *c.Handoff, handoffs []c.Handoff) (nativeQAEnvironment, error) {
	base := nativeQAEnvironment{a.Revision.Proposal.QA.Environment, a.Revision.Proposal.QA.EnvironmentRevision}
	seen := map[string]bool{}
	var resolve func(*c.Handoff) (nativeQAEnvironment, error)
	resolve = func(h *c.Handoff) (nativeQAEnvironment, error) {
		if h == nil {
			return base, nil
		}
		in := h.Input
		if h.ID == "" || seen[h.ID] || len(seen) >= 32 || c.ValidateHandoff(in) != nil || in.Kind != "reassess-qa" || in.Goal != b.Goal || in.TaskURL != b.TaskURL || in.AssignmentSHA != NativeAssignmentSHA(b) || in.PRURL != p.URL || in.HeadSHA != p.HeadSHA || p.QAArtifact == nil || in.ArtifactSHA != p.QAArtifact.SHA256 {
			return base, errors.New("QA environment handoff scope mismatch")
		}
		seen[h.ID] = true
		var previous *Review
		for i := range p.Reviews {
			if p.Reviews[i].ID == in.ReviewID {
				previous = &p.Reviews[i]
			}
		}
		var q NativeQAResult
		if previous == nil || previous.Author.ID != b.QAID || previous.Author.Kind != "Bot" || previous.HeadSHA != p.HeadSHA || previous.State != "CHANGES_REQUESTED" || !previous.SubmittedAt.After(p.ScopeUpdatedAt) || nativeMetadata(previous.Body, "qa", &q) != nil || !nativeQAScope(b, a, p, q) || q.Verdict != "changes-needed" || in.EvidenceMessageID == q.EvidenceMessageID {
			return base, errors.New("QA environment requires the exact prior assessment")
		}
		if !h.CreatedAt.IsZero() && !h.CreatedAt.After(previous.SubmittedAt) {
			return base, errors.New("QA environment handoff predates its assessment")
		}
		env := base
		if q.Assessment != "" || q.HandoffID != "" {
			prior := nativeQAEnvironmentHandoff(q, handoffs)
			if prior == nil {
				return base, errors.New("QA environment authority missing")
			}
			var err error
			env, err = resolve(prior)
			if err != nil {
				return base, err
			}
		}
		if q.Environment != env.Name || q.EnvironmentRevision != env.Revision {
			return base, errors.New("prior QA environment mismatch")
		}
		if in.Environment != "" {
			// ActorKind is stamped by the master, never accepted in handoff input.
			// Legacy handoffs with unverified environment fields fail closed.
			if h.ActorKind != "owner" || h.CreatedAt.IsZero() {
				return base, errors.New("QA environment requires owner authority")
			}
			env = nativeQAEnvironment{in.Environment, in.EnvironmentRevision}
		}
		return env, nil
	}
	return resolve(h)
}

func nativeQAEnvironmentHandoff(q NativeQAResult, handoffs []c.Handoff) *c.Handoff {
	if q.Assessment != "reassessment" || q.HandoffID == "" {
		return nil
	}
	for i := range handoffs {
		if handoffs[i].ID == q.HandoffID {
			return &handoffs[i]
		}
	}
	return nil
}

func nativeQAResultAuthority(b NativeTaskBinding, a c.GoalAuthority, p PR, q NativeQAResult, handoffs []c.Handoff) (c.GoalAuthority, error) {
	var h *c.Handoff
	if q.Assessment != "" || q.HandoffID != "" {
		h = nativeQAEnvironmentHandoff(q, handoffs)
		if h == nil {
			return a, errors.New("QA reassessment authority missing")
		}
	}
	env, err := resolveNativeQAEnvironment(b, a, p, h, handoffs)
	if err != nil {
		return a, err
	}
	a.Revision.Proposal.QA.Environment, a.Revision.Proposal.QA.EnvironmentRevision = env.Name, env.Revision
	return a, nil
}
