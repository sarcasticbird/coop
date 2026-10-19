package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeSchedule struct {
	Version              int
	MasterID, InstanceID string
	Generation           int64
	Eligible             map[string]time.Time
}

func (d *NativeDispatcher) loadSchedule() error {
	d.eligible = map[string]time.Time{}
	raw, err := readPrivate(filepath.Join(masterDir(d.config), "schedule.json"), MaxSnapshot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s, err := decodeNativeSchedule(d.config, raw)
	if err != nil {
		return err
	}
	d.eligible = s.Eligible
	return nil
}
func decodeNativeSchedule(cfg Config, raw []byte) (nativeSchedule, error) {
	var s nativeSchedule
	if strictTeamJSON(raw, &s) != nil || s.Version != 3 || s.MasterID != cfg.Master.MasterID || s.InstanceID != cfg.Master.InstanceID || s.Generation != cfg.Master.Generation || s.Eligible == nil || len(s.Eligible) > c.MaxMessages {
		return s, c.ErrConflict
	}
	for id, at := range s.Eligible {
		if !hexSHA.MatchString(id) || at.IsZero() {
			return s, c.ErrConflict
		}
	}
	return s, nil
}
func scheduleEvidence(raw []byte) c.RecoveryEvidence {
	r := c.RecoveryEvidence{Kind: "schedule", RecordKey: "schedule/schedule.json", Terminal: true, Data: mustRecoveryJSON(map[string]string{"sha256": BodyHash(string(raw))})}
	r.ID = BodyHash(string(mustRecoveryJSON(r)))
	return r
}

func (d *NativeDispatcher) candidates(catalog NativeCatalog) ([]NativeCandidate, error) {
	jobs := NativeCandidates(d.config, catalog, d.history, d.clock.Now())
	filtered := jobs[:0]
	for _, candidate := range jobs {
		fenced := false
		for _, x := range d.history {
			if x.Phase == "blocked" && (candidate.Job.TaskURL != "" && candidate.Job.TaskURL == x.Job.TaskURL || x.Job.Kind == "plan" && candidate.Job.Kind == "plan" && candidate.Goal.Revision.Ref == x.Goal) {
				fenced = true
				break
			}
		}
		if !fenced {
			filtered = append(filtered, candidate)
		}
	}
	jobs = filtered
	changed := false
	next := map[string]time.Time{}
	for id, at := range d.eligible {
		next[id] = at
	}
	for i := range jobs {
		id := jobs[i].Job.ID
		at, ok := next[id]
		if !ok {
			at = jobs[i].Job.FirstEligibleAt
			next[id] = at
			changed = true
		}
		jobs[i].Job.FirstEligibleAt = at
	}
	if changed {
		if len(next) > c.MaxMessages {
			return nil, c.ErrCapacity
		}
		s := nativeSchedule{Version: 3, MasterID: d.config.Master.MasterID, InstanceID: d.config.Master.InstanceID, Generation: d.config.Master.Generation, Eligible: next}
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		if len(raw) > MaxSnapshot {
			return nil, c.ErrSize
		}
		if err = atomicPrivate(filepath.Join(masterDir(d.config), "schedule.json"), raw); err != nil {
			return nil, err
		}
		d.eligible = next
	}
	return jobs, nil
}
