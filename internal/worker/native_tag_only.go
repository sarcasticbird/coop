package worker

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type tagOnlyIntent struct {
	Version                                 int
	Binding                                 releaseBinding
	JobID, RequestID, InputSHA              string
	Ref, Object, Message, Name, Email, Date string
	Phase                                   string
	Result                                  NativeActionResult
}

func tagOnlyPath(cfg Config, id string) string {
	return filepath.Join(masterDir(cfg), "tag-only", BodyHash(id)+".json")
}

func newTagOnlyIntent(cfg Config, h c.Handoff, target releaseTarget, record nativeActionRecord) tagOnlyIntent {
	j := tagOnlyIntent{Version: 1, Binding: releaseBinding{MasterID: cfg.Master.MasterID, Placement: configuredPlacement(cfg), ExecutorID: cfg.Self.ID, Handoff: h, Target: target}, JobID: record.JobID, RequestID: record.RequestID, InputSHA: record.InputSHA, Ref: "refs/tags/" + h.Input.Tag, Message: "Owner-approved Coop tag-only " + h.ID + "\n", Name: cfg.Self.Login, Email: strconv.FormatInt(cfg.Self.ID, 10) + "+" + cfg.Self.Login + "@users.noreply.github.com", Date: h.CreatedAt.UTC().Format(time.RFC3339)}
	// Git's canonical object framing makes the expected object independently
	// discoverable if GitHub accepts a create but its response is lost.
	body := fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger %s <%s> %d +0000\n\n%s", target.SHA, h.Input.Tag, j.Name, j.Email, h.CreatedAt.Unix(), j.Message)
	sum := sha1.Sum([]byte(fmt.Sprintf("tag %d\x00%s", len(body), body)))
	j.Object = hex.EncodeToString(sum[:])
	return j
}

func saveTagOnlyIntent(cfg Config, j tagOnlyIntent) error {
	path := tagOnlyPath(cfg, j.Binding.Handoff.ID)
	if err := ensurePrivateDir(filepath.Dir(path), true); err != nil {
		return err
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicPrivate(path, raw)
}

func readTagOnlyIntent(cfg Config, id string) (tagOnlyIntent, error) {
	var j tagOnlyIntent
	raw, err := readPrivate(tagOnlyPath(cfg, id), c.MaxJSON)
	if err != nil {
		return j, err
	}
	if strictTeamJSON(raw, &j) != nil || j.Version != 1 || j.Binding.Handoff.ID != id || j.Binding.MasterID != cfg.Master.MasterID || j.Binding.ExecutorID != cfg.Self.ID || j.Binding.Placement != configuredPlacement(cfg) || j.JobID == "" || j.RequestID == "" || !hexSHA.MatchString(j.InputSHA) || c.ValidateHandoff(j.Binding.Handoff.Input) != nil || j.Binding.Handoff.Input.Kind != "tag-only" {
		return j, c.ErrConflict
	}
	expected := newTagOnlyIntent(cfg, j.Binding.Handoff, j.Binding.Target, nativeActionRecord{JobID: j.JobID, RequestID: j.RequestID, InputSHA: j.InputSHA})
	if j.Ref != expected.Ref || j.Object != expected.Object || j.Message != expected.Message || j.Name != expected.Name || j.Email != expected.Email || j.Date != expected.Date || j.Binding.Target.SHA != c.TagOnlyTargetSHA(j.Binding.Handoff.Input) {
		return j, c.ErrConflict
	}
	switch j.Phase {
	case "object-intent", "object-created", "ref-intent", "ref-rejected", "completed":
	default:
		return j, c.ErrConflict
	}
	if j.Phase == "completed" && (j.Result.Phase != "completed" || j.Result.URL != "https://github.com/"+j.Binding.Target.Repository+"/tree/"+j.Binding.Handoff.Input.Tag) {
		return j, c.ErrConflict
	}
	owner := strings.Split(j.Binding.Target.Repository, "/")[0]
	if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, j.Binding.Target.Repository) {
		return j, c.ErrForbidden
	}
	return j, nil
}

// Caller owns the producer lock. A persisted intent is never replayed. In
// particular an object accepted without an acknowledgment cannot start a ref.
func publishNativeTagOnly(ctx context.Context, cfg Config, api releaseAPI, h c.Handoff, target releaseTarget, record nativeActionRecord, guard func() error) (NativeActionResult, error) {
	j := newTagOnlyIntent(cfg, h, target, record)
	if saved, err := readTagOnlyIntent(cfg, h.ID); err == nil {
		if saved.Binding != j.Binding || saved.JobID != j.JobID || saved.RequestID != j.RequestID || saved.InputSHA != j.InputSHA {
			return NativeActionResult{}, c.ErrConflict
		}
		return confirmTagOnlyIntent(ctx, cfg, api, saved, guard)
	} else if !os.IsNotExist(err) {
		return NativeActionResult{}, err
	}
	owner := strings.Split(target.Repository, "/")[0]
	base := "repos/" + target.Repository
	// Equal commit targets do not confer ownership of an existing tag.
	if _, _, err := api.api(ctx, owner, "GET", base+"/git/ref/tags/"+h.Input.Tag, nil); err == nil {
		return NativeActionResult{}, c.ErrConflict
	} else if !githubMissing(err) {
		return NativeActionResult{}, err
	}
	// An existing equal object is also foreign unless our durable intent exists.
	if _, _, err := api.api(ctx, owner, "GET", base+"/git/tags/"+j.Object, nil); err == nil {
		return NativeActionResult{}, c.ErrConflict
	} else if !githubMissing(err) {
		return NativeActionResult{}, err
	}
	if err := guard(); err != nil {
		return NativeActionResult{}, err
	}
	j.Phase = "object-intent"
	if err := saveTagOnlyIntent(cfg, j); err != nil {
		return NativeActionResult{}, err
	}
	record.State, record.Step = "uncertain", "tag-object"
	if err := saveNativeAction(cfg, record); err != nil {
		return NativeActionResult{}, err
	}
	raw, _, err := api.api(ctx, owner, "POST", base+"/git/tags", map[string]any{"tag": h.Input.Tag, "message": j.Message, "object": target.SHA, "type": "commit", "tagger": map[string]string{"name": j.Name, "email": j.Email, "date": j.Date}})
	if err != nil {
		return NativeActionResult{}, err
	}
	var object struct{ SHA string }
	if json.Unmarshal(raw, &object) != nil || object.SHA != j.Object {
		return NativeActionResult{}, c.ErrConflict
	}
	if err = verifyTagOnlyObject(ctx, api, j); err != nil {
		return NativeActionResult{}, err
	}
	j.Phase = "object-created"
	if err = saveTagOnlyIntent(cfg, j); err != nil {
		return NativeActionResult{}, err
	}
	if err = guard(); err != nil {
		return NativeActionResult{}, err
	}
	if _, _, err = api.api(ctx, owner, "GET", base+"/git/ref/tags/"+h.Input.Tag, nil); err == nil {
		return NativeActionResult{}, c.ErrConflict
	} else if !githubMissing(err) {
		return NativeActionResult{}, err
	}
	j.Phase = "ref-intent"
	if err = saveTagOnlyIntent(cfg, j); err != nil {
		return NativeActionResult{}, err
	}
	record.Step = "tag-ref"
	if err = saveNativeAction(cfg, record); err != nil {
		return NativeActionResult{}, err
	}
	if _, _, err = api.api(ctx, owner, "POST", base+"/git/refs", map[string]string{"ref": j.Ref, "sha": j.Object}); err != nil {
		var response *GitHubError
		if errors.As(err, &response) && response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 {
			// A conclusive refusal is not a lost acknowledgment. A concurrent
			// foreign ref must not later become our successful receipt.
			j.Phase = "ref-rejected"
			return NativeActionResult{}, errors.Join(err, saveTagOnlyIntent(cfg, j))
		}
		return NativeActionResult{}, err
	}
	return confirmTagOnlyIntent(ctx, cfg, api, j, guard)
}

func verifyTagOnlyObject(ctx context.Context, api releaseAPI, j tagOnlyIntent) error {
	raw, _, err := api.api(ctx, strings.Split(j.Binding.Target.Repository, "/")[0], "GET", "repos/"+j.Binding.Target.Repository+"/git/tags/"+j.Object, nil)
	if err != nil {
		return err
	}
	var got struct {
		SHA, Tag, Message string
		Object            struct{ SHA, Type string }
		Tagger            struct{ Name, Email, Date string }
	}
	if json.Unmarshal(raw, &got) != nil || got.SHA != j.Object || got.Tag != j.Binding.Handoff.Input.Tag || got.Message != j.Message || got.Object.SHA != j.Binding.Target.SHA || got.Object.Type != "commit" || got.Tagger.Name != j.Name || got.Tagger.Email != j.Email {
		return c.ErrConflict
	}
	date, e := time.Parse(time.RFC3339, got.Tagger.Date)
	if e != nil || date.UTC().Format(time.RFC3339) != j.Date {
		return c.ErrConflict
	}
	return nil
}

// Observation reports remote facts only, never a native completion receipt.
func observeTagOnlyIntent(ctx context.Context, api releaseAPI, j tagOnlyIntent) (NativeActionResult, error) {
	if j.Phase == "ref-rejected" {
		return NativeActionResult{}, c.ErrConflict
	}
	if err := verifyTagOnlyObject(ctx, api, j); err != nil {
		return NativeActionResult{}, err
	}
	if j.Phase == "object-intent" || j.Phase == "object-created" {
		return NativeActionResult{Phase: "checkpoint"}, nil
	}
	raw, _, err := api.api(ctx, strings.Split(j.Binding.Target.Repository, "/")[0], "GET", "repos/"+j.Binding.Target.Repository+"/git/ref/tags/"+j.Binding.Handoff.Input.Tag, nil)
	if err != nil {
		return NativeActionResult{}, err
	}
	var ref struct {
		Ref    string
		Object struct{ SHA, Type string }
	}
	if json.Unmarshal(raw, &ref) != nil || ref.Ref != j.Ref || ref.Object.SHA != j.Object || ref.Object.Type != "tag" {
		return NativeActionResult{}, c.ErrConflict
	}
	return NativeActionResult{Phase: "observed", URL: "https://github.com/" + j.Binding.Target.Repository + "/tree/" + j.Binding.Handoff.Input.Tag}, nil
}

// Caller owns the producer lock. Confirmation may finish only the original
// action; an uncertain intent can never start another GitHub mutation.
func confirmTagOnlyIntent(ctx context.Context, cfg Config, api releaseAPI, j tagOnlyIntent, guard func() error) (NativeActionResult, error) {
	raw, err := readPrivate(nativeActionPath(cfg, j.JobID, j.RequestID), c.MaxJSON)
	if err != nil {
		return NativeActionResult{}, err
	}
	var record nativeActionRecord
	if strictTeamJSON(raw, &record) != nil || record.Version != 3 || record.JobID != j.JobID || record.RequestID != j.RequestID || record.InputSHA != j.InputSHA || record.Operation != "tag-only" {
		return NativeActionResult{}, c.ErrConflict
	}
	if record.State == "completed" {
		if j.Phase != "completed" || record.Result != j.Result {
			return NativeActionResult{}, c.ErrConflict
		}
		return record.Result, nil
	}
	if record.State != "uncertain" {
		return NativeActionResult{}, c.ErrConflict
	}
	if err = guard(); err != nil {
		return NativeActionResult{}, err
	}
	result, err := observeTagOnlyIntent(ctx, api, j)
	if err != nil || result.Phase != "observed" {
		return result, err
	}
	// The object/ref alone do not prove that the integrated target still matches
	// the approved tree. Read that exact commit again before terminal persistence.
	raw, _, err = api.api(ctx, strings.Split(j.Binding.Target.Repository, "/")[0], "GET", "repos/"+j.Binding.Target.Repository+"/git/commits/"+j.Binding.Target.SHA, nil)
	if err != nil {
		return NativeActionResult{}, err
	}
	var commit struct {
		SHA  string
		Tree struct{ SHA string }
	}
	tree := j.Binding.Handoff.Input.TagOnly.IntegratedTreeSHA
	if j.Binding.Handoff.Input.TagOnly.VersionPRURL != "" {
		tree = j.Binding.Handoff.Input.TagOnly.VersionTreeSHA
	}
	if json.Unmarshal(raw, &commit) != nil || commit.SHA != j.Binding.Target.SHA || commit.Tree.SHA != tree {
		return NativeActionResult{}, c.ErrConflict
	}
	if err = guard(); err != nil {
		return NativeActionResult{}, err
	}
	result.Phase = "completed"
	if j.Phase != "completed" {
		j.Phase, j.Result = "completed", result
		if err = saveTagOnlyIntent(cfg, j); err != nil {
			return NativeActionResult{}, err
		}
	}
	record.State, record.Result = "completed", result
	if err = saveNativeAction(cfg, record); err != nil {
		return NativeActionResult{}, err
	}
	return result, nil
}

// RunNativeAction should route tag-only here while holding its producer lock,
// before the ordinary closed-task and uncertain-action guards. Saved intents
// may confirm under current authority, but never replay a GitHub write.
func runNativeTagOnlyActionLocked(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, jobID string, in NativeAction) (NativeActionResult, error) {
	if !nativeDeliveryActor(cfg) || in.Operation != "tag-only" || in.RequestID == "" || len(in.RequestID) > 128 {
		return NativeActionResult{}, c.ErrForbidden
	}
	// No caller-provided artifact, target, prose or alternate task is authority.
	input, _ := json.Marshal(in)
	minimal, _ := json.Marshal(NativeAction{Operation: "tag-only", RequestID: in.RequestID})
	if string(input) != string(minimal) {
		return NativeActionResult{}, c.ErrInvalid
	}
	x, err := loadNativeExecution(cfg, jobID)
	if err != nil {
		return NativeActionResult{}, err
	}
	if !nativeTagOnlyJob(cfg, x.Job) || x.Binding == nil {
		return NativeActionResult{}, c.ErrForbidden
	}
	raw, _ := json.Marshal(in)
	record := nativeActionRecord{Version: 3, JobID: jobID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: "tag-only", State: "prepared"}
	if saved, e := readPrivate(nativeActionPath(cfg, jobID, in.RequestID), c.MaxJSON); e == nil {
		var existing nativeActionRecord
		if strictTeamJSON(saved, &existing) != nil || existing.Version != 3 || existing.JobID != jobID || existing.RequestID != in.RequestID || existing.InputSHA != record.InputSHA || existing.Operation != "tag-only" {
			return NativeActionResult{}, c.ErrConflict
		}
		record = existing
	} else if !os.IsNotExist(e) {
		return NativeActionResult{}, e
	}
	var h c.Handoff
	var target releaseTarget
	var savedIntent *tagOnlyIntent
	if j, e := readTagOnlyIntent(cfg, x.Job.Master.DeliveryHandoffID); e == nil {
		if j.JobID != jobID || j.RequestID != in.RequestID || j.InputSHA != record.InputSHA || j.Binding.Handoff.Input.Goal != x.Goal || j.Binding.Handoff.Input.AssignmentSHA != NativeAssignmentSHA(*x.Binding) {
			return NativeActionResult{}, c.ErrConflict
		}
		action, e := loadNativeActionIntent(cfg, x, record)
		if e != nil {
			return NativeActionResult{}, e
		}
		encoded, _ := json.Marshal(action)
		if string(encoded) != string(input) {
			return NativeActionResult{}, c.ErrConflict
		}
		if err = verifyTagOnlyObserver(ctx, cfg, m); err != nil {
			return NativeActionResult{}, err
		}
		h, target, savedIntent = j.Binding.Handoff, j.Binding.Target, &j
	} else if !os.IsNotExist(e) {
		return NativeActionResult{}, e
	}
	if savedIntent == nil {
		if record.State != "prepared" {
			return NativeActionResult{}, c.ErrConflict
		}
		if uncertain, e := nativeJobActionUncertain(cfg, jobID); e != nil {
			return NativeActionResult{}, e
		} else if uncertain {
			return NativeActionResult{}, c.ErrConflict
		}
	}
	guard := func() error {
		if cfg.DeliveryHold {
			return c.ErrForbidden
		}
		if err := verifyNativePlacement(ctx, cfg, m); err != nil {
			return err
		}
		for _, paused := range []func() (bool, error){NewVersionedFileStore(cfg.StateDir, 3).Paused, NewVersionedFileStore(cfg.StateDir, 3).AdmissionPaused} {
			if yes, e := paused(); e != nil {
				return e
			} else if yes {
				return c.ErrForbidden
			}
		}
		current, e := loadNativeExecution(cfg, jobID)
		if e != nil {
			return e
		}
		if current.Phase != "active" || current.Job.ID != x.Job.ID || !nativeTagOnlyJob(cfg, current.Job) {
			return c.ErrForbidden
		}
		encoded, _ := json.Marshal(current)
		if retired, e := recoveryRetired(cfg, "engineering", jobID+".json", encoded); e != nil {
			return e
		} else if retired {
			return c.ErrEpoch
		}
		if e = nativeSourceTaskSettled(cfg, x.Job.TaskURL, jobID); e != nil {
			return e
		}
		if _, e = nativeCurrentGoal(ctx, cfg, m, x.Goal); e != nil {
			return e
		}
		cat, e := nativeTagOnlySnapshot(ctx, cfg, m, g, x.Job)
		if e != nil {
			return e
		}
		loadNativeTagOnlyEvidence(ctx, cfg, m, &cat)
		for _, candidate := range NativeTagOnlyCandidates(cfg, cat, nil, time.Now()) {
			if candidate.Job.ID != x.Job.ID || candidate.Handoff.ID != x.Job.Master.DeliveryHandoffID || NativeAssignmentSHA(candidate.Task.Binding) != NativeAssignmentSHA(*x.Binding) {
				continue
			}
			validator, ok := m.(interface {
				ValidateTagOnlyHandoff(context.Context, c.TagOnlyValidation) (c.Handoff, error)
			})
			if !ok {
				return c.ErrUnsupported
			}
			approved, e := validator.ValidateTagOnlyHandoff(ctx, c.TagOnlyValidation{Goal: x.Goal, HandoffID: candidate.Handoff.ID})
			if e != nil {
				return e
			}
			if approved != *candidate.Handoff {
				return c.ErrConflict
			}
			if e = nativeRegisteredBinding(ctx, m, candidate.Task.Binding); e != nil {
				return e
			}
			got, e := nativeTagOnlyTarget(ctx, g, candidate.Task.Task.Repo, *candidate.Handoff)
			if e != nil {
				return e
			}
			if h.ID != "" && (h != *candidate.Handoff || target != got) {
				return c.ErrConflict
			}
			h, target = *candidate.Handoff, got
			return nil
		}
		return c.ErrForbidden
	}
	if savedIntent != nil {
		return confirmTagOnlyIntent(ctx, cfg, g, *savedIntent, guard)
	}
	if err = guard(); err != nil {
		return NativeActionResult{}, err
	}
	if err = persistNativeActionIntent(cfg, x, in); err != nil {
		return NativeActionResult{}, err
	}
	return publishNativeTagOnly(ctx, cfg, g, h, target, record, guard)
}

func verifyTagOnlyObserver(ctx context.Context, cfg Config, m NativeEngineeringMaster) error {
	// Observation requires authenticated Rook identity, not current deliver
	// capability: disabling execution must not prevent reading remote facts.
	if cfg.Version != 3 || cfg.Master == nil || cfg.Name != c.DeliveryPrincipalID || cfg.Master.AgentID != c.DeliveryPrincipalID || cfg.Self.ID != c.DeliveryGitHubBotID || cfg.Self.Kind != "Bot" {
		return c.ErrForbidden
	}
	self, err := m.Self(ctx)
	if err != nil {
		return err
	}
	if self.Principal.ID != c.DeliveryPrincipalID || self.Principal.Kind != "agent" || self.Principal.GitHubBotID != c.DeliveryGitHubBotID || self.Placement != configuredPlacement(cfg) || !containsExact(self.Principal.Engagements, cfg.Master.Engagement) {
		return c.ErrForbidden
	}
	return nil
}

// ObserveNativeTagOnly never writes local or remote state or confirms completion.
// Placement changes fail closed rather than adopting an unverified old intent.
func ObserveNativeTagOnly(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, goal c.GoalRef, id string) (NativeActionResult, error) {
	if err := verifyTagOnlyObserver(ctx, cfg, m); err != nil {
		return NativeActionResult{}, err
	}
	j, err := readTagOnlyIntent(cfg, id)
	if err != nil {
		return NativeActionResult{}, err
	}
	if j.Binding.Handoff.Input.Goal != goal {
		return NativeActionResult{}, c.ErrConflict
	}
	owner := strings.Split(j.Binding.Target.Repository, "/")[0]
	if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, j.Binding.Target.Repository) {
		return NativeActionResult{}, c.ErrForbidden
	}
	return observeTagOnlyIntent(ctx, g, j)
}
