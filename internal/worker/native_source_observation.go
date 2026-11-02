package worker

import (
	"context"
	"encoding/json"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type SourceActionObservation struct {
	JobID, RequestID, Operation, State, Step, DecisionID, ScopeSHA          string
	ExpectedHead, ExpectedTarget, ObservedHead, ObservedBase, ObservedMerge string
	Merged                                                                  bool
	Disposition                                                             string
}

// ObserveSourceAction does not authorize, adopt, confirm or retry a write. It
// deliberately works after withdrawal/hold: historical facts are not grants.
func ObserveSourceAction(ctx context.Context, cfg Config, g *GitHubClient, job, request string) (out SourceActionObservation, err error) {
	x, err := loadNativeExecution(cfg, job)
	if err != nil {
		return out, err
	}
	if !nativeSourceKind(x.Job.Kind) || x.Job.Master == nil || x.Job.Master.SourceDecisionID == "" || request == "" {
		return out, c.ErrInvalid
	}
	raw, err := readPrivate(nativeActionPath(cfg, job, request), c.MaxJSON)
	if err != nil {
		return out, err
	}
	var record nativeActionRecord
	if strictTeamJSON(raw, &record) != nil || record.JobID != job || record.RequestID != request || record.Version != 3 {
		return out, c.ErrConflict
	}
	out = SourceActionObservation{JobID: job, RequestID: request, Operation: record.Operation, State: record.State, Step: record.Step, DecisionID: x.Job.Master.SourceDecisionID, ScopeSHA: x.Job.ScopeSHA, ExpectedHead: x.Job.HeadSHA, ExpectedTarget: x.Job.Master.SourceTargetSHA, Disposition: "read-only facts; no authority, effect adoption, journal repair or retry permission"}
	repo, n, err := parseRef(x.Job.PRURL)
	if err != nil || !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) {
		return out, c.ErrForbidden
	}
	reader := NewGitHubClient(g.runner)
	reader.config = cfg
	if err = reader.verifyOwner(ctx, strings.Split(repo, "/")[0]); err != nil {
		return out, err
	}
	raw, _, err = reader.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
	if err != nil {
		return out, err
	}
	var pr struct {
		URL      string `json:"html_url"`
		Head     struct{ SHA string }
		Base     struct{ Ref string }
		Merged   bool
		MergeSHA string `json:"merge_commit_sha"`
	}
	if json.Unmarshal(raw, &pr) != nil || pr.URL != x.Job.PRURL {
		return out, c.ErrConflict
	}
	out.ObservedHead, out.ObservedBase, out.ObservedMerge, out.Merged = pr.Head.SHA, pr.Base.Ref, pr.MergeSHA, pr.Merged
	return out, nil
}
