package worker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
)

type MasterPlacementTransport interface {
	MasterRecoveryTransport
	Self(context.Context) (c.SelfIdentity, error)
	PlacementProof(context.Context) (c.PlacementProof, error)
}
type masterPlacementAdoption struct {
	Version                           int
	Name, Engagement, StateInstanceID string
	GitHubBotID                       int64
	Proof                             c.PlacementProof
	Accepted                          masterRecoverySnapshot
	ConnectionBefore                  masterConnectionState
	ScheduleBefore                    []byte
}

func configuredPlacement(cfg Config) c.Placement {
	return c.Placement{AgentID: cfg.Master.AgentID, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}
}
func placementAdoptionPath(cfg Config) string {
	return filepath.Join(masterDir(cfg), "placements", jsonNumberEpoch(cfg.Master.Generation)+".json")
}
func loadPlacementAdoption(cfg Config) (masterPlacementAdoption, error) {
	var v masterPlacementAdoption
	raw, e := readPrivate(placementAdoptionPath(cfg), MaxSnapshot)
	if e != nil {
		return v, e
	}
	if strictTeamJSON(raw, &v) != nil || v.Version != 1 || v.Name != cfg.Name || v.Engagement != cfg.Master.Engagement || v.GitHubBotID != cfg.Self.ID || v.Proof.MasterID != cfg.Master.MasterID || v.Proof.To != configuredPlacement(cfg) || v.Proof.From.AgentID != cfg.Master.AgentID || v.Proof.From.Generation+1 != v.Proof.To.Generation || v.Proof.RestoreEpoch < 1 || v.Accepted.Version != 1 || v.Accepted.MasterID != v.Proof.MasterID || v.Accepted.InstanceID != v.Proof.From.InstanceID || v.Accepted.Generation != v.Proof.From.Generation || v.Accepted.Epoch != v.Proof.RestoreEpoch || c.RecoveryDigest(v.Accepted.Records) != v.Proof.ManifestSHA256 || v.ConnectionBefore.Version != 1 || v.ConnectionBefore.MasterID != v.Proof.MasterID || v.ConnectionBefore.InstanceID != v.Proof.From.InstanceID || v.ConnectionBefore.Generation != v.Proof.From.Generation || v.ConnectionBefore.Epoch != v.Proof.RestoreEpoch || !hexSHA.MatchString(v.StateInstanceID) {
		return v, c.ErrConflict
	}
	if _, e := placementSchedule(v, cfg); e != nil {
		return v, e
	}
	return v, nil
}

// Historical records can be read only when their exact original bytes belong
// to the inventory accepted before this explicit placement transition.
func adoptedRecoveryRecord(cfg Config, kind, name string, raw []byte) (c.RecoveryEvidence, bool, error) {
	var empty c.RecoveryEvidence
	v, e := loadPlacementAdoption(cfg)
	if os.IsNotExist(e) {
		return empty, false, nil
	}
	if e != nil {
		return empty, false, e
	}
	canonical, e := canonicalRecoveryRaw(raw)
	if e != nil {
		return empty, false, e
	}
	for _, r := range v.Accepted.Records {
		if r.RecordKey == kind+"/"+name && r.Kind == kind && string(r.Data) == string(canonical) && !r.Uncertain {
			return r, true, nil
		}
	}
	return empty, false, nil
}
func masterStateInstanceAllowed(cfg Config, instance string) bool {
	if instance == cfg.Master.InstanceID {
		return true
	}
	v, e := loadPlacementAdoption(cfg)
	return e == nil && v.StateInstanceID == instance
}
func masterInstallTransition(old, next Config) bool {
	if old.Master == nil || old.Master.MasterID != next.Master.MasterID || old.Master.AgentID != next.Master.AgentID || old.Master.Engagement != next.Master.Engagement {
		return false
	}
	v, e := loadPlacementAdoption(next)
	if e != nil || v.Proof.From != configuredPlacement(old) {
		return false
	}
	raw, e := readPrivate(filepath.Join(masterDir(next), "connection.json"), 4096)
	var conn masterConnectionState
	return e == nil && strictTeamJSON(raw, &conn) == nil && conn.Version == 1 && conn.MasterID == next.Master.MasterID && conn.InstanceID == next.Master.InstanceID && conn.Generation == next.Master.Generation
}

// AdoptMasterPlacement is deliberately separate from installation/startup.
// Both workers and their effect producers must be stopped. State and native
// histories retain their original identities; only connection metadata changes.
func AdoptMasterPlacement(ctx context.Context, old, next Config, cl MasterPlacementTransport) (c.PlacementProof, error) {
	var proof c.PlacementProof
	if old.Version != 3 || next.Version != 3 || ValidateConfig(old) != nil || ValidateConfig(next) != nil || old.Name != next.Name || !sameActor(old.Self, next.Self) || old.StateDir != next.StateDir || old.Master.MasterID != next.Master.MasterID || old.Master.AgentID != next.Master.AgentID || old.Master.Engagement != next.Master.Engagement || next.Master.Generation != old.Master.Generation+1 || cl.MasterID() != next.Master.MasterID {
		return proof, c.ErrConflict
	}
	release, e := NewVersionedFileStore(next.StateDir, 3).Lock()
	if e != nil {
		return proof, e
	}
	defer release()
	dir := filepath.Join(masterDir(next), "actions")
	if e = ensurePrivateDir(dir, true); e != nil {
		return proof, e
	}
	lockPath := filepath.Join(dir, "producer.lock")
	if e = safePath(lockPath); e != nil {
		return proof, e
	}
	lock, e := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return proof, e
	}
	defer func() { _ = lock.Close() }()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return proof, c.ErrConflict
	}
	self, e := cl.Self(ctx)
	if e != nil {
		return proof, e
	}
	if self.Placement != configuredPlacement(next) || self.Principal.ID != next.Master.AgentID || self.Principal.Kind != "agent" || self.Principal.GitHubBotID != next.Self.ID {
		return proof, c.ErrForbidden
	}
	status, e := cl.RecoveryStatus(ctx)
	if e != nil {
		return proof, e
	}
	if !status.Quiescent || status.RestoreEpoch != self.RestoreEpoch {
		return proof, c.ErrEpoch
	}
	proof, e = cl.PlacementProof(ctx)
	if e != nil {
		return proof, e
	}
	if proof.MasterID != next.Master.MasterID || proof.From != configuredPlacement(old) || proof.To != self.Placement || proof.RestoreEpoch != self.RestoreEpoch {
		return proof, c.ErrConflict
	}
	adoption, e := loadPlacementAdoption(next)
	if e != nil && !os.IsNotExist(e) {
		return proof, e
	}
	if os.IsNotExist(e) {
		raw, e := readPrivate(filepath.Join(masterDir(old), "recovery", "accepted.json"), MaxSnapshot)
		if e != nil {
			return proof, e
		}
		var accepted masterRecoverySnapshot
		if strictTeamJSON(raw, &accepted) != nil || accepted.MasterID != old.Master.MasterID || accepted.InstanceID != old.Master.InstanceID || accepted.Generation != old.Master.Generation || accepted.Epoch != proof.RestoreEpoch || c.RecoveryDigest(accepted.Records) != proof.ManifestSHA256 {
			return proof, c.ErrConflict
		}
		_, files, e := gatherMasterRecovery(ctx, old, nil)
		if e != nil {
			return proof, e
		}
		if accepted.FilesSHA != files {
			return proof, c.ErrConflict
		}
		state, e := NewVersionedFileStore(next.StateDir, 3).Load()
		if e != nil {
			return proof, e
		}
		if !masterStateInstanceAllowed(old, state.InstanceID) {
			return proof, c.ErrConflict
		}
		for _, job := range state.Jobs {
			if job.Phase != "completed" {
				return proof, c.ErrConflict
			}
		}
		var conn masterConnectionState
		raw, e = readPrivate(filepath.Join(masterDir(old), "connection.json"), 4096)
		if e != nil {
			return proof, e
		}
		if strictTeamJSON(raw, &conn) != nil || conn.MasterID != old.Master.MasterID || conn.InstanceID != old.Master.InstanceID || conn.Generation != old.Master.Generation || conn.Epoch != proof.RestoreEpoch {
			return proof, c.ErrConflict
		}
		schedule, e := readPrivate(filepath.Join(masterDir(old), "schedule.json"), MaxSnapshot)
		if e != nil && !os.IsNotExist(e) {
			return proof, e
		}
		adoption = masterPlacementAdoption{ScheduleBefore: schedule, Version: 1, Name: next.Name, Engagement: next.Master.Engagement, GitHubBotID: next.Self.ID, StateInstanceID: state.InstanceID, Proof: proof, Accepted: accepted, ConnectionBefore: conn}
		if _, e = placementSchedule(adoption, next); e != nil {
			return proof, e
		}
		raw = mustRecoveryJSON(adoption)
		if len(raw) > MaxSnapshot {
			return proof, c.ErrSize
		}
		if e = ensurePrivateDir(filepath.Dir(placementAdoptionPath(next)), true); e != nil {
			return proof, e
		}
		if e = writeWakeImmutable(placementAdoptionPath(next), raw); e != nil {
			return proof, e
		}
	} else if adoption.Proof != proof {
		return proof, c.ErrConflict
	}
	// An interrupted adoption can finish only the exact recorded connection move.
	path := filepath.Join(masterDir(next), "connection.json")
	raw, e := readPrivate(path, 4096)
	if e != nil {
		return proof, e
	}
	var conn masterConnectionState
	if strictTeamJSON(raw, &conn) != nil {
		return proof, c.ErrConflict
	}
	current := conn.Version == 1 && conn.MasterID == next.Master.MasterID && conn.InstanceID == next.Master.InstanceID && conn.Generation == next.Master.Generation
	if !current && conn != adoption.ConnectionBefore {
		return proof, c.ErrConflict
	}
	if e = adoptPlacementSchedule(adoption, next); e != nil {
		return proof, e
	}
	if current {
		return proof, nil
	}
	conn.InstanceID = next.Master.InstanceID
	conn.Generation = next.Master.Generation
	conn.Epoch = 0
	return proof, atomicPrivate(path, mustRecoveryJSON(conn))
}

// The recorded old bytes are retained as evidence; only placement metadata in
// the live schedule changes. Eligibility timestamps and counts stay intact.
func placementSchedule(v masterPlacementAdoption, cfg Config) ([]byte, error) {
	if len(v.ScheduleBefore) == 0 {
		for _, r := range v.Accepted.Records {
			if r.Kind == "schedule" {
				return nil, c.ErrConflict
			}
		}
		return nil, nil
	}
	old := cfg
	binding := *cfg.Master
	old.Master = &binding
	old.Master.InstanceID = v.Proof.From.InstanceID
	old.Master.Generation = v.Proof.From.Generation
	s, e := decodeNativeSchedule(old, v.ScheduleBefore)
	if e != nil {
		return nil, e
	}
	evidence := scheduleEvidence(v.ScheduleBefore)
	found := false
	for _, r := range v.Accepted.Records {
		if r.ID == evidence.ID && bytes.Equal(mustRecoveryJSON(r), mustRecoveryJSON(evidence)) {
			found = true
			break
		}
	}
	if !found {
		return nil, c.ErrConflict
	}
	s.InstanceID = cfg.Master.InstanceID
	s.Generation = cfg.Master.Generation
	return mustRecoveryJSON(s), nil
}
func adoptPlacementSchedule(v masterPlacementAdoption, cfg Config) error {
	after, e := placementSchedule(v, cfg)
	if e != nil {
		return e
	}
	path := filepath.Join(masterDir(cfg), "schedule.json")
	current, e := readPrivate(path, MaxSnapshot)
	if os.IsNotExist(e) && len(after) == 0 {
		return nil
	}
	if e != nil {
		return e
	}
	if len(after) == 0 {
		return c.ErrConflict
	}
	if bytes.Equal(current, after) {
		return nil
	}
	if !bytes.Equal(current, v.ScheduleBefore) {
		return c.ErrConflict
	}
	return atomicPrivate(path, after)
}
