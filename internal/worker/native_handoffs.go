package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

type handoffSource interface {
	Handoffs(context.Context, c.HandoffQuery) (c.HandoffPage, error)
}

func loadCatalogHandoffs(ctx context.Context, m NativeMaster, out *NativeCatalog) error {
	source, ok := m.(handoffSource)
	if !ok {
		return nil
	}
	for _, a := range out.Goals {
		seen := map[string]bool{}
		for after := ""; ; {
			if seen[after] {
				return c.ErrConflict
			}
			seen[after] = true
			page, err := source.Handoffs(ctx, c.HandoffQuery{Goal: a.Revision.Ref, After: after})
			if err != nil {
				return err
			}
			for _, h := range page.Handoffs {
				if h.ID == "" || h.Input.Goal != a.Revision.Ref || c.ValidateHandoff(h.Input) != nil {
					return c.ErrConflict
				}
				out.Handoffs = append(out.Handoffs, h)
				if len(out.Handoffs) > MaxConnection {
					return c.ErrCapacity
				}
			}
			if page.Next == "" {
				break
			}
			after = page.Next
		}
	}
	return nil
}
func (m *taskSnapshotMaster) Handoffs(ctx context.Context, in c.HandoffQuery) (c.HandoffPage, error) {
	if source, ok := m.NativeMaster.(handoffSource); ok {
		return source.Handoffs(ctx, in)
	}
	return c.HandoffPage{}, nil
}
func qaReassessment(cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR, r Review) *c.Handoff {
	if !nativeChangesRequested("qa", t.Binding, a, p, r) {
		return nil
	}
	var old NativeQAResult
	if nativeMetadata(r.Body, "qa", &old) != nil {
		return nil
	}
	var chosen *c.Handoff
	for i := range cat.Handoffs {
		h := &cat.Handoffs[i]
		in := h.Input
		if in.Kind != "reassess-qa" || in.Goal != t.Binding.Goal || in.TaskURL != t.Task.URL || in.AssignmentSHA != NativeAssignmentSHA(t.Binding) || in.PRURL != p.URL || in.HeadSHA != p.HeadSHA || in.ArtifactSHA != p.QAArtifact.SHA256 || in.ReviewID != r.ID || in.EvidenceMessageID == old.EvidenceMessageID {
			continue
		}
		// Prefer the first immutable request. Later messages must not produce a
		// stream of new model jobs while the same review remains unchanged.
		if chosen == nil || h.CreatedAt.Before(chosen.CreatedAt) || h.CreatedAt.Equal(chosen.CreatedAt) && h.ID < chosen.ID {
			chosen = h
		}
	}
	return chosen
}

func releaseHandoff(cat NativeCatalog, t NativeTask, p PR) *c.Handoff {
	var chosen *c.Handoff
	for i := range cat.Handoffs {
		h := &cat.Handoffs[i]
		in := h.Input
		// QA-optional discovery has no artifact. The owner extension still binds
		// this exact source head; release execution verifies the digest afresh.
		if in.Kind != "release" || in.Goal != t.Binding.Goal || in.TaskURL != t.Task.URL || in.AssignmentSHA != NativeAssignmentSHA(t.Binding) || in.PRURL != p.URL || in.HeadSHA != p.HeadSHA || p.QAArtifact != nil && in.ArtifactSHA != p.QAArtifact.SHA256 {
			continue
		}
		if chosen == nil || h.CreatedAt.Before(chosen.CreatedAt) || h.CreatedAt.Equal(chosen.CreatedAt) && h.ID < chosen.ID {
			chosen = h
		}
	}
	return chosen
}
