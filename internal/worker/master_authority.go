package worker

import (
	"context"
	"errors"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type GoalAuthoritySource interface {
	Resolve(context.Context, c.GoalRef) (c.GoalAuthority, error)
}
type MasterGoalAuthority struct {
	Client interface {
		ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error)
	}
}

func (s MasterGoalAuthority) Resolve(ctx context.Context, ref c.GoalRef) (c.GoalAuthority, error) {
	return s.Client.ResolveGoal(ctx, ref)
}
func nativeTaskRepo(ref string) (string, error) {
	if isKataTask(ref) {
		_, _, repo, err := c.ParseKataTaskReference(ref)
		if err != nil || !validRepo(repo) {
			return "", c.ErrInvalid
		}
		return repo, nil
	}
	parts := strings.Split(strings.TrimPrefix(ref, "https://github.com/"), "/")
	if !validGitHubURL(ref) || len(parts) != 4 || parts[2] != "issues" || !numericID.MatchString(parts[3]) || !validRepo(parts[0]+"/"+parts[1]) {
		return "", errors.New("native task URL is not a canonical GitHub issue")
	}
	return parts[0] + "/" + parts[1], nil
}
func ValidateNativeAuthority(ctx context.Context, source GoalAuthoritySource, b NativeTaskBinding) (c.GoalAuthority, error) {
	var empty c.GoalAuthority
	if b.Version != 3 || b.Goal.Source != "master" || b.Goal.MasterID == "" || b.Goal.GoalID == "" || !hexSHA.MatchString(b.Goal.RevisionSHA256) || !hexSHA.MatchString(b.TaskScopeSHA) || !hexSHA.MatchString(b.PolicySHA) || b.ApprovalSequence < 1 || !validBaseBranch(b.BaseBranch) || b.AuthorID < 1 || b.PeerID < 1 || b.AuthorID == b.PeerID {
		return empty, errors.New("invalid native assignment")
	}
	repo, err := nativeTaskRepo(b.TaskURL)
	if err != nil {
		return empty, err
	}
	a, err := source.Resolve(ctx, b.Goal)
	if err != nil {
		return empty, err
	}
	if !a.Valid || a.State != "approved" || a.Revision.Ref != b.Goal || a.ActiveRevisionSHA256 != b.Goal.RevisionSHA256 || a.ApprovalSequence != b.ApprovalSequence {
		return a, errors.New("native approval no longer current")
	}
	if a.Actors.Author.GitHubBotID != b.AuthorID || a.Actors.Author.PrincipalID != a.Revision.Proposal.AuthorID || a.Actors.Peer.GitHubBotID != b.PeerID || a.Actors.Peer.PrincipalID != a.Revision.Proposal.PeerID {
		return a, errors.New("native assignment actor mismatch")
	}
	if a.Revision.Proposal.QA.Required {
		if b.QAID <= 0 || b.QAID == b.AuthorID || b.QAID == b.PeerID || a.Actors.QA.GitHubBotID != b.QAID || a.Actors.QA.PrincipalID != a.Revision.Proposal.QA.ActorID {
			return a, errors.New("native QA assignment mismatch")
		}
	} else if b.QAID != 0 {
		return a, errors.New("unexpected native QA assignment")
	}
	allowed := false
	for _, scope := range a.Revision.Proposal.Repositories {
		if scope.Name != repo {
			continue
		}
		for _, base := range scope.BaseBranches {
			if base == b.BaseBranch {
				allowed = true
			}
		}
	}
	if !allowed {
		return a, errors.New("native repository or base branch outside approval")
	}
	return a, nil
}
func ValidateNativeQA(b NativeTaskBinding, a c.GoalAuthority, review Review, report c.Message, expected NativeQAResult) error {
	if !a.Valid || a.State != "approved" || a.Revision.Ref != b.Goal || a.ActiveRevisionSHA256 != b.Goal.RevisionSHA256 || a.ApprovalSequence != b.ApprovalSequence || !a.Revision.Proposal.QA.Required || expected.Version != 3 || expected.Goal != b.Goal || expected.AssignmentSHA != NativeAssignmentSHA(b) || expected.TaskURL != b.TaskURL || !qaHeadSHA.MatchString(expected.HeadSHA) || !hexSHA.MatchString(expected.PRScopeRevision) || !hexSHA.MatchString(expected.ArtifactSHA) || expected.AcceptanceSHA != NativeAcceptanceSHA(a) || !hexSHA.MatchString(expected.EvidenceBodySHA) || expected.Environment != a.Revision.Proposal.QA.Environment || expected.EnvironmentRevision == "" || expected.EnvironmentRevision != a.Revision.Proposal.QA.EnvironmentRevision || expected.Verdict != "approved" {
		return errors.New("native QA scope mismatch")
	}
	var actual NativeQAResult
	if nativeMetadata(review.Body, "qa", &actual) != nil || actual != expected || review.ID == "" || review.HeadSHA != expected.HeadSHA || review.State != "APPROVED" || review.Author.Kind != "Bot" || review.Author.ID != b.QAID || b.QAID == b.AuthorID || b.QAID == b.PeerID || a.Actors.QA.GitHubBotID != b.QAID {
		return errors.New("independent native QA review required")
	}
	dest := a.Revision.Proposal.QA.EvidenceDestination
	if dest.MasterID != b.Goal.MasterID || dest.ThreadID != report.ThreadID || report.ID != expected.EvidenceMessageID || report.AuthorID != a.Actors.QA.PrincipalID || report.AuthorKind != "agent" || report.Imported || BodyHash(report.Body) != expected.EvidenceBodySHA {
		return errors.New("native private QA evidence mismatch")
	}
	return nil
}
