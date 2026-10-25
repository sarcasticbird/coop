package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type releaseAPI interface {
	api(context.Context, string, string, string, any) ([]byte, http.Header, error)
}
type releaseTarget struct {
	Repository string
	SHA        string
}
type NativeReleaseResult struct {
	Phase     string `json:"phase"`
	Reason    string `json:"reason,omitempty"`
	Tag       string `json:"tag"`
	SourceSHA string `json:"source_sha"`
	URL       string `json:"url,omitempty"`
}
type releaseJournal struct {
	Version   int
	Binding   releaseBinding
	Identity  string
	Phase     string
	TagObject string
	Result    NativeReleaseResult
}

// RunNativeRelease consumes an owner-created handoff, not conversational prose.
// Every call revalidates source authority; the producer journal never retries an
// uncertain write or replaces an existing tag. It does not upgrade live shares.
func RunNativeRelease(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, goal c.GoalRef, id string) (NativeReleaseResult, error) {
	var empty NativeReleaseResult
	if cfg.Version != 3 || cfg.Master == nil || id == "" {
		return empty, c.ErrInvalid
	}
	unlock, err := lockNativeProducer(cfg)
	if err != nil {
		return empty, err
	}
	defer unlock()
	if err = verifyNativePlacement(ctx, cfg, m); err != nil {
		return empty, err
	}
	if _, ok := m.(handoffSource); !ok {
		return empty, c.ErrUnsupported
	}
	cat := NativeCatalog{Goals: []c.GoalAuthority{{Revision: c.GoalRevision{Ref: goal}}}}
	// Read the exact request through the authenticated master, never trust an
	// owner-supplied-looking JSON file in the checkout.
	if err = loadCatalogHandoffs(ctx, m, &cat); err != nil {
		return empty, err
	}
	var h c.Handoff
	for _, v := range cat.Handoffs {
		if v.ID == id {
			h = v
			break
		}
	}
	if h.ID == "" || h.Input.Kind != "release" {
		return empty, c.ErrForbidden
	}
	// Once the tag is acknowledged, observation has no remote write. Permit
	// it while paused/quiescent so recovery can obtain terminal evidence.
	if saved, _, e := readReleaseJournal(cfg, id); e == nil && (saved.Phase == "tagged" || saved.Phase == "published") {
		return publishNativeRelease(ctx, cfg, g, h, saved.Binding.Target, func() error { return c.ErrForbidden })
	} else if e != nil && !os.IsNotExist(e) {
		return empty, e
	}
	store := NewVersionedFileStore(cfg.StateDir, 3)
	target, err := nativeReleaseTarget(ctx, cfg, m, g, h)
	if err != nil {
		return empty, err
	}
	guard := func() error {
		if e := verifyNativePlacement(ctx, cfg, m); e != nil {
			return e
		}
		for _, paused := range []func() (bool, error){store.Paused, store.AdmissionPaused} {
			if yes, e := paused(); e != nil {
				return e
			} else if yes {
				return c.ErrForbidden
			}
		}
		current, e := nativeReleaseTarget(ctx, cfg, m, g, h)
		if e != nil {
			return e
		}
		if current != target {
			return c.ErrConflict
		}
		if e = releaseSourceSettled(cfg, h.Input.TaskURL); e != nil {
			return e
		}
		return nil
	}
	return publishNativeRelease(ctx, cfg, g, h, target, guard)
}

func nativeReleaseTarget(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, h c.Handoff) (releaseTarget, error) {
	var empty releaseTarget
	g.config = cfg
	t, err := g.ReadNativeTask(ctx, cfg, h.Input.TaskURL)
	if err != nil {
		return empty, err
	}
	if _, err = nativeCurrentGoal(ctx, cfg, m, t.Binding.Goal); err != nil {
		return empty, err
	}
	if err = validateNativeRoster(cfg, t.Binding); err != nil {
		return empty, err
	}
	a, err := ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: m}, t.Binding)
	if err != nil {
		return empty, err
	}
	if cfg.Self.ID != t.Binding.AuthorID || cfg.Master.AgentID != a.Actors.Author.PrincipalID || h.Input.Goal != t.Binding.Goal || h.Input.AssignmentSHA != NativeAssignmentSHA(t.Binding) {
		return empty, c.ErrForbidden
	}
	if err = nativeRegisteredBinding(ctx, m, t.Binding); err != nil {
		return empty, err
	}
	p, err := g.ReadNativeReleasePR(ctx, cfg, t, a, h.Input.PRURL)
	if err != nil {
		return empty, err
	}
	if !p.Merged || p.HeadSHA != h.Input.HeadSHA || p.QAArtifact == nil || p.QAArtifact.SHA256 != h.Input.ArtifactSHA || p.ChecksState != "success" && p.ChecksState != "none" {
		return empty, c.ErrForbidden
	}
	if _, cleared := nativePeerCleared(t.Binding, p); !cleared {
		return empty, c.ErrForbidden
	}
	if a.Revision.Proposal.QA.Required {
		review := nativeLatestReview(p, t.Binding.QAID)
		var qa NativeQAResult
		if nativeMetadata(review.Body, "qa", &qa) != nil {
			return empty, c.ErrForbidden
		}
		report, e := m.Message(ctx, qa.EvidenceMessageID)
		if e != nil {
			return empty, e
		}
		if _, clear := nativeQAState(t.Binding, a, p, map[string]c.Message{report.ID: report}); !clear {
			return empty, c.ErrForbidden
		}
	}
	owner := strings.Split(t.Task.Repo, "/")[0]
	endpoint := "repos/" + t.Task.Repo
	raw, _, err := g.api(ctx, owner, "GET", endpoint+"/pulls/"+strings.TrimPrefix(p.URL, "https://github.com/"+t.Task.Repo+"/pull/"), nil)
	if err != nil {
		return empty, err
	}
	var merged struct {
		Merged bool `json:"merged"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		SHA string `json:"merge_commit_sha"`
	}
	if json.Unmarshal(raw, &merged) != nil || !merged.Merged || merged.Head.SHA != h.Input.HeadSHA || !qaHeadSHA.MatchString(merged.SHA) {
		return empty, c.ErrConflict
	}
	raw, _, err = g.api(ctx, owner, "GET", endpoint+"/compare/"+merged.SHA+"..."+url.PathEscape(t.Binding.BaseBranch), nil)
	if err != nil {
		return empty, err
	}
	var ancestry struct {
		Status string `json:"status"`
		Behind int    `json:"behind_by"`
	}
	if json.Unmarshal(raw, &ancestry) != nil || ancestry.Behind != 0 || ancestry.Status != "ahead" && ancestry.Status != "identical" {
		return empty, c.ErrConflict
	}
	// The source commit must actually contain the existing workflow. The executor
	// creates a tag only; repository CI owns packaging, attestations and release.
	if _, _, err = g.api(ctx, owner, "GET", endpoint+"/contents/.github/workflows/"+h.Input.Workflow+"?ref="+merged.SHA, nil); err != nil {
		return empty, err
	}
	return releaseTarget{Repository: t.Task.Repo, SHA: merged.SHA}, nil
}

func githubMissing(err error) bool {
	var e *GitHubError
	return errors.As(err, &e) && e.StatusCode == 404
}

func publishNativeRelease(ctx context.Context, cfg Config, api releaseAPI, h c.Handoff, target releaseTarget, guards ...func() error) (NativeReleaseResult, error) {
	binding := releaseBinding{MasterID: cfg.Master.MasterID, Placement: configuredPlacement(cfg), ExecutorID: cfg.Self.ID, Handoff: h, Target: target}
	identity, marshalErr := json.Marshal(binding)
	if marshalErr != nil {
		return NativeReleaseResult{}, marshalErr
	}
	path := releaseJournalPath(cfg, h.ID)
	if err := ensurePrivateDir(filepath.Dir(path), true); err != nil {
		return NativeReleaseResult{}, err
	}
	j := releaseJournal{Version: 1, Binding: binding, Identity: BodyHash(string(identity)), Result: NativeReleaseResult{Tag: h.Input.Tag, SourceSHA: target.SHA}}
	if saved, raw, err := readReleaseJournal(cfg, h.ID); err == nil {
		if saved.Identity != j.Identity {
			if saved.Phase == "published" && saved.Binding.Handoff.ID == h.ID && saved.Binding.Handoff.Input.Goal == h.Input.Goal && saved.Binding.Target == target {
				return saved.Result, nil
			}
			if saved.Phase != "tagged" || saved.Binding.Handoff.ID != h.ID || saved.Binding.Handoff.Input.Goal != h.Input.Goal || saved.Binding.Target != target {
				return j.Result, c.ErrConflict
			}
		}
		if saved.Phase == "object-created" {
			retired, e := recoveryRetired(cfg, "releases", filepath.Base(path), raw)
			if e != nil {
				return j.Result, e
			}
			if retired {
				return j.Result, c.ErrEpoch
			}
		}
		j = saved
	} else if !os.IsNotExist(err) {
		return j.Result, err
	}
	save := func() error {
		raw, err := json.Marshal(j)
		if err != nil {
			return err
		}
		return atomicPrivate(path, raw)
	}
	if j.Phase == "uncertain" {
		return j.Result, c.ErrConflict
	}
	owner := strings.Split(target.Repository, "/")[0]
	base := "repos/" + target.Repository
	if j.Phase == "" {
		if _, _, err := api.api(ctx, owner, "GET", base+"/git/ref/tags/"+h.Input.Tag, nil); err == nil {
			return j.Result, c.ErrConflict
		} else if !githubMissing(err) {
			return j.Result, err
		}
		for _, guard := range guards {
			if err := guard(); err != nil {
				return j.Result, err
			}
		}
		j.Phase = "uncertain"
		if err := save(); err != nil {
			return j.Result, err
		}
		raw, _, err := api.api(ctx, owner, "POST", base+"/git/tags", map[string]any{"tag": h.Input.Tag, "message": "Owner-approved Coop release " + h.ID, "object": target.SHA, "type": "commit", "tagger": map[string]string{"name": cfg.Self.Login, "email": strconv.FormatInt(cfg.Self.ID, 10) + "+" + cfg.Self.Login + "@users.noreply.github.com", "date": h.CreatedAt.UTC().Format(time.RFC3339)}})
		if err != nil {
			return j.Result, err
		}
		var object struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(raw, &object) != nil || !qaHeadSHA.MatchString(object.SHA) {
			return j.Result, c.ErrConflict
		}
		j.TagObject = object.SHA
		j.Phase = "object-created"
		if err = save(); err != nil {
			return j.Result, err
		}
	}
	if j.Phase == "object-created" {
		for _, guard := range guards {
			if err := guard(); err != nil {
				return j.Result, err
			}
		}
		j.Phase = "uncertain"
		if err := save(); err != nil {
			return j.Result, err
		}
		raw, _, err := api.api(ctx, owner, "POST", base+"/git/refs", map[string]string{"ref": "refs/tags/" + h.Input.Tag, "sha": j.TagObject})
		if err != nil {
			return j.Result, err
		}
		var ref struct {
			Ref    string `json:"ref"`
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if json.Unmarshal(raw, &ref) != nil || ref.Ref != "refs/tags/"+h.Input.Tag || ref.Object.SHA != j.TagObject || ref.Object.Type != "tag" {
			return j.Result, c.ErrConflict
		}
		j.Phase = "tagged"
		j.Result.Phase = "waiting"
		if err = save(); err != nil {
			return j.Result, err
		}
	}
	raw, _, err := api.api(ctx, owner, "GET", base+"/git/ref/tags/"+h.Input.Tag, nil)
	if err != nil {
		return j.Result, err
	}
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if json.Unmarshal(raw, &ref) != nil || ref.Object.SHA != j.TagObject || ref.Object.Type != "tag" {
		return j.Result, c.ErrConflict
	}
	raw, _, err = api.api(ctx, owner, "GET", base+"/actions/workflows/"+h.Input.Workflow+"/runs?head_sha="+target.SHA+"&event=push&per_page=100", nil)
	if err != nil {
		return j.Result, err
	}
	var runs struct {
		Runs []struct {
			Head       string `json:"head_sha"`
			Branch     string `json:"head_branch"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if json.Unmarshal(raw, &runs) != nil {
		return j.Result, c.ErrConflict
	}
	success := false
	j.Result.Phase = "waiting"
	j.Result.Reason = ""
	for _, r := range runs.Runs {
		if r.Head == target.SHA && r.Branch == h.Input.Tag {
			success = r.Conclusion == "success"
			switch r.Conclusion {
			case "failure", "cancelled", "timed_out", "action_required", "startup_failure", "stale", "skipped", "neutral":
				j.Result.Phase = "blocked"
				j.Result.Reason = "workflow_" + r.Conclusion
				return j.Result, save()
			}
			break
		}
	}
	if !success {
		return j.Result, nil
	}
	raw, _, err = api.api(ctx, owner, "GET", base+"/releases/tags/"+h.Input.Tag, nil)
	if githubMissing(err) {
		return j.Result, nil
	}
	if err != nil {
		return j.Result, err
	}
	var released struct {
		Tag       string            `json:"tag_name"`
		URL       string            `json:"html_url"`
		Draft     bool              `json:"draft"`
		Published string            `json:"published_at"`
		Assets    []json.RawMessage `json:"assets"`
	}
	expectedURL := "https://github.com/" + target.Repository + "/releases/tag/" + h.Input.Tag
	if json.Unmarshal(raw, &released) != nil || released.Tag != h.Input.Tag || released.URL != expectedURL || released.Draft || released.Published == "" || len(released.Assets) == 0 {
		return j.Result, c.ErrConflict
	}
	j.Result.Phase = "published"
	j.Result.URL = released.URL
	j.Phase = "published"
	return j.Result, save()
}
