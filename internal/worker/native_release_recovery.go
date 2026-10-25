package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Caller owns the producer lock. Remote merged status cannot substitute for
// the source producer's durable action/result and recovery state.
func releaseSourceSettled(cfg Config, task string) error {
	dir := filepath.Join(masterDir(cfg), "engineering")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return c.ErrConflict
		}
		x, err := loadNativeExecution(cfg, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return err
		}
		if x.Job.TaskURL != task {
			continue
		}
		if x.PendingContinuation != nil {
			return c.ErrConflict
		}
		switch x.Phase {
		case "selected", "prepared", "active", "blocked", "completed", "cancelled":
		default:
			return c.ErrConflict
		}
		records, err := loadNativeJobActions(cfg, x)
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.State == "completed" {
				continue
			}
			raw, e := readPrivate(nativeActionPath(cfg, r.JobID, r.RequestID), c.MaxJSON)
			if os.IsNotExist(e) {
				return c.ErrConflict
			}
			if e != nil {
				return e
			}
			retired, e := recoveryRetired(cfg, "actions", filepath.Base(nativeActionPath(cfg, r.JobID, r.RequestID)), raw)
			if e != nil {
				return e
			}
			if !retired {
				return c.ErrConflict
			}
		}
		if x.Phase == "completed" || x.Phase == "cancelled" || nativeTerminalCheckpoint(x) {
			continue
		}
		complete, err := nativeJobHasResult(cfg, x)
		if err != nil {
			return err
		}
		if !complete {
			return c.ErrConflict
		}
	}
	return nil
}

type releaseBinding struct {
	MasterID   string
	Placement  c.Placement
	ExecutorID int64
	Handoff    c.Handoff
	Target     releaseTarget
}

func releaseJournalPath(cfg Config, id string) string {
	return filepath.Join(masterDir(cfg), "releases", BodyHash(id)+".json")
}

func readReleaseJournal(cfg Config, id string) (releaseJournal, []byte, error) {
	var j releaseJournal
	raw, err := readPrivate(releaseJournalPath(cfg, id), c.MaxJSON)
	if err != nil {
		return j, nil, err
	}
	if strictTeamJSON(raw, &j) != nil || j.Version != 1 || j.Binding.Handoff.ID != id || j.Binding.MasterID != cfg.Master.MasterID || j.Binding.ExecutorID != cfg.Self.ID || j.Identity != BodyHash(string(mustRecoveryJSON(j.Binding))) || j.Result.Tag != j.Binding.Handoff.Input.Tag || j.Result.SourceSHA != j.Binding.Target.SHA {
		return j, nil, c.ErrConflict
	}
	switch j.Phase {
	case "uncertain":
	case "object-created", "tagged", "published":
		if !qaHeadSHA.MatchString(j.TagObject) {
			return j, nil, c.ErrConflict
		}
	default:
		return j, nil, c.ErrConflict
	}
	if j.Binding.Placement != configuredPlacement(cfg) {
		if j.Phase != "published" && j.Phase != "tagged" {
			return j, nil, c.ErrConflict
		}
		// A resolved inventory can authorize continued observation of the
		// same acknowledged tag. Only mutable observation/result fields may
		// advance; the original request, placement and tag object stay pinned.
		adoption, e := loadPlacementAdoption(cfg)
		if e != nil {
			return j, nil, e
		}
		accepted := false
		for _, r := range adoption.Accepted.Records {
			if r.Kind != "releases" || r.RecordKey != "releases/"+filepath.Base(releaseJournalPath(cfg, id)) || r.Uncertain {
				continue
			}
			var original releaseJournal
			if strictTeamJSON(r.Data, &original) == nil && (original.Phase == "tagged" || original.Phase == "published") && original.Identity == j.Identity && original.TagObject == j.TagObject && string(mustRecoveryJSON(original.Binding)) == string(mustRecoveryJSON(j.Binding)) {
				accepted = true
				break
			}
		}
		if !accepted {
			return j, nil, c.ErrConflict
		}
	}
	return j, raw, nil
}

func releaseRecoveryRecord(cfg Config, name string, raw []byte) (bool, error) {
	var j releaseJournal
	if json.Unmarshal(raw, &j) != nil || name != filepath.Base(releaseJournalPath(cfg, j.Binding.Handoff.ID)) {
		return false, c.ErrConflict
	}
	owned, _, err := readReleaseJournal(cfg, j.Binding.Handoff.ID)
	if err != nil {
		return false, err
	}
	// As with prepared native actions, an acknowledged checkpoint is safe
	// for explicit retirement. This does not claim publication or permit
	// replay of an unstarted ref write after retirement.
	return owned.Phase != "uncertain", nil
}

// ObserveNativeRelease is a recovery-only read of already acknowledged tags.
// It never submits a GitHub mutation, including while paused or after restore.
func ObserveNativeRelease(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, goal c.GoalRef, id string) (NativeReleaseResult, error) {
	var empty NativeReleaseResult
	if cfg.Version != 3 || cfg.Master == nil || id == "" {
		return empty, c.ErrInvalid
	}
	unlock, err := lockNativeProducer(cfg)
	if err != nil {
		return empty, err
	}
	defer unlock()
	self, err := m.Self(ctx)
	if err != nil {
		return empty, err
	}
	if self.Principal.ID != cfg.Master.AgentID || self.Principal.Kind != "agent" || self.Principal.GitHubBotID != cfg.Self.ID || self.Placement != configuredPlacement(cfg) || !containsExact(self.Principal.Engagements, cfg.Master.Engagement) {
		return empty, c.ErrForbidden
	}
	j, _, err := readReleaseJournal(cfg, id)
	if err != nil {
		return empty, err
	}
	if j.Binding.Handoff.Input.Goal != goal {
		return empty, c.ErrConflict
	}
	if j.Phase == "object-created" {
		r := j.Result
		r.Phase = "checkpoint"
		r.Reason = "tag_reference_not_started"
		return r, nil
	}
	if j.Phase != "tagged" && j.Phase != "published" {
		return empty, c.ErrConflict
	}
	owner := strings.Split(j.Binding.Target.Repository, "/")[0]
	if !containsFold(cfg.CodeOwners, owner) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, j.Binding.Target.Repository) {
		return empty, c.ErrForbidden
	}
	return publishNativeRelease(ctx, cfg, g, j.Binding.Handoff, j.Binding.Target, func() error { return c.ErrForbidden })
}
