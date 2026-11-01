package worker

import (
	"context"
	"encoding/json"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeTagOnlyVersionTarget(ctx context.Context, api releaseAPI, repo string, h c.Handoff) (releaseTarget, error) {
	r := h.Input.TagOnly
	if c.ValidateHandoff(h.Input) != nil {
		return releaseTarget{}, c.ErrInvalid
	}
	prRepo, n, err := parseRef(r.VersionPRURL)
	if err != nil || prRepo != repo {
		return releaseTarget{}, c.ErrForbidden
	}
	owner := strings.Split(repo, "/")[0]
	base := "repos/" + repo
	raw, _, err := api.api(ctx, owner, "GET", base+"/pulls/"+stringID(n), nil)
	if err != nil {
		return releaseTarget{}, err
	}
	var pr struct {
		Merged bool
		SHA    string `json:"merge_commit_sha"`
		Head   struct{ SHA string }
		Base   struct {
			Ref  string
			Repo struct {
				FullName string `json:"full_name"`
			}
		}
	}
	if json.Unmarshal(raw, &pr) != nil || !pr.Merged || pr.Head.SHA != r.VersionHeadSHA || pr.SHA != r.VersionCommitSHA || pr.Base.Ref != h.Input.BaseBranch || pr.Base.Repo.FullName != repo {
		return releaseTarget{}, c.ErrConflict
	}
	for _, sha := range []string{r.VersionHeadSHA, r.VersionCommitSHA} {
		raw, _, err = api.api(ctx, owner, "GET", base+"/git/commits/"+sha, nil)
		if err != nil {
			return releaseTarget{}, err
		}
		var got struct {
			SHA     string
			Tree    struct{ SHA string }
			Parents []struct{ SHA string }
		}
		if json.Unmarshal(raw, &got) != nil || got.SHA != sha || got.Tree.SHA != r.VersionTreeSHA || sha == r.VersionCommitSHA && (len(got.Parents) != 1 || got.Parents[0].SHA != r.VersionBaseSHA) {
			return releaseTarget{}, c.ErrConflict
		}
	}
	for _, comparison := range []string{r.IntegratedCommitSHA + "..." + r.VersionBaseSHA, r.VersionCommitSHA + "..." + h.Input.BaseBranch} {
		raw, _, err = api.api(ctx, owner, "GET", base+"/compare/"+comparison, nil)
		if err != nil {
			return releaseTarget{}, err
		}
		var ancestry struct {
			Status string
			Behind int `json:"behind_by"`
		}
		if json.Unmarshal(raw, &ancestry) != nil || ancestry.Behind != 0 || ancestry.Status != "ahead" && ancestry.Status != "identical" {
			return releaseTarget{}, c.ErrConflict
		}
	}
	bundles := make([][]byte, 0, 4)
	for _, sha := range []string{r.SourceCommitSHA, r.IntegratedCommitSHA, r.VersionHeadSHA, r.VersionCommitSHA} {
		raw, _, err = api.api(ctx, owner, "GET", base+"/tarball/"+sha, nil)
		if err != nil {
			return releaseTarget{}, err
		}
		bundles = append(bundles, raw)
	}
	if err := VerifyTagOnlyPayload(h.Input, bundles[0], bundles[1], bundles[2], bundles[3]); err != nil {
		return releaseTarget{}, err
	}
	return releaseTarget{Repository: repo, SHA: r.VersionCommitSHA}, nil
}
