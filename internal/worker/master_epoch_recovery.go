package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
)

type MasterRecoveryTransport interface {
	MasterID() string
	RecoveryStatus(context.Context) (c.RecoveryStatus, error)
	ReconcileGuest(context.Context, c.GuestRecovery) (c.RecoveryStatus, error)
}
type masterRecoverySnapshot struct {
	Version              int
	MasterID, InstanceID string
	Generation, Epoch    int64
	FilesSHA             string
	Records              []c.RecoveryEvidence
}

func recoverySnapshotPath(cfg Config, epoch int64) string {
	return filepath.Join(masterDir(cfg), "recovery", jsonNumberEpoch(epoch)+"-"+jsonNumberEpoch(cfg.Master.Generation)+"-"+BodyHash(cfg.Master.InstanceID)+".json")
}
func jsonNumberEpoch(epoch int64) string { b, _ := json.Marshal(epoch); return string(b) }

// A view is local to one producer-locked operation. Recovery and placement
// writers own that same lock, so evidence cannot change during its lifetime.
// Never retain a view across lock releases or worker ticks.
type masterRecoveryView struct {
	cfg            Config
	adoption       masterPlacementAdoption
	adoptionLoaded bool
	adoptionErr    error
	accepted       masterRecoverySnapshot
	acceptedLoaded bool
	acceptedErr    error
}

func (v *masterRecoveryView) loadAdoption() (masterPlacementAdoption, error) {
	if !v.adoptionLoaded {
		v.adoption, v.adoptionErr = loadPlacementAdoption(v.cfg)
		v.adoptionLoaded = true
	}
	return v.adoption, v.adoptionErr
}

func recoveryRetired(cfg Config, kind, key string, raw []byte) (bool, error) {
	return (&masterRecoveryView{cfg: cfg}).retired(kind, key, raw)
}

func (view *masterRecoveryView) retired(kind, key string, raw []byte) (bool, error) {
	if _, ok, e := view.adoptedRecord(kind, key, raw); e != nil || ok {
		return ok, e
	}
	if !view.acceptedLoaded {
		b, err := readPrivate(filepath.Join(masterDir(view.cfg), "recovery", "accepted.json"), MaxSnapshot)
		if err == nil && (strictTeamJSON(b, &view.accepted) != nil || view.accepted.Version != 1) {
			err = c.ErrConflict
		}
		view.acceptedErr = err
		view.acceptedLoaded = true
	}
	err := view.acceptedErr
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	v, cfg := view.accepted, view.cfg
	if v.MasterID != cfg.Master.MasterID || v.InstanceID != cfg.Master.InstanceID || v.Generation != cfg.Master.Generation {
		adoption, e := view.loadAdoption()
		if e != nil || string(mustRecoveryJSON(v)) != string(mustRecoveryJSON(adoption.Accepted)) {
			return false, c.ErrConflict
		}
		return false, nil
	}
	canonical, err := canonicalRecoveryRaw(raw)
	if err != nil {
		return false, err
	}
	for _, r := range v.Records {
		if r.Kind == kind && r.RecordKey == kind+"/"+key && BodyHash(string(r.Data)) == BodyHash(string(canonical)) {
			return true, nil
		}
	}
	return false, nil
}
func retiredTurn(cfg Config, turn MasterTurn) (bool, error) {
	b, e := json.Marshal(turn)
	if e != nil {
		return false, e
	}
	return recoveryRetired(cfg, "inbox", BodyHash(turn.Delivery.ID)+".json", b)
}
func gatherMasterRecovery(ctx context.Context, cfg Config, code OpenCode) ([]c.RecoveryEvidence, string, error) {
	out := []c.RecoveryEvidence{}
	files := []string{}
	for _, kind := range []string{"inbox", "outbox", "published", "engineering", "actions", "releases"} {
		dir := filepath.Join(masterDir(cfg), kind)
		entries, e := os.ReadDir(dir)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, "", e
		}
		for _, entry := range entries {
			if kind == "actions" && entry.Name() == "producer.lock" {
				continue
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				return nil, "", c.ErrConflict
			}
			raw, e := readPrivate(filepath.Join(dir, entry.Name()), c.MaxJSON-8192)
			if e != nil {
				return nil, "", e
			}
			r := c.RecoveryEvidence{Kind: kind, RecordKey: kind + "/" + entry.Name(), Data: json.RawMessage(raw)}
			// Canonicalize JSON for stable comparisons with snapshots written by helpers.
			canonical, e := canonicalRecoveryRaw(raw)
			if e != nil {
				return nil, "", e
			}
			r.Data = canonical
			files = append(files, r.RecordKey+":"+BodyHash(string(r.Data)))
			if adopted, ok, e := adoptedRecoveryRecord(cfg, kind, entry.Name(), raw); e != nil {
				return nil, "", e
			} else if ok {
				out = append(out, adopted)
				continue
			}
			var job *Job
			switch kind {
			case "inbox":
				var v MasterTurn
				if strictTeamJSON(raw, &v) != nil {
					return nil, "", c.ErrConflict
				}
				v, e = loadMasterTurn(cfg, v.Delivery.ID)
				if e != nil {
					return nil, "", e
				}
				r.MessageID = v.Message.ID
				r.DeliveryID = v.Delivery.ID
				job = &v.Job
			case "outbox":
				var v masterOutbox
				if strictTeamJSON(raw, &v) != nil || v.Version != 1 || v.MasterID != cfg.Master.MasterID || v.InstanceID != cfg.Master.InstanceID || v.Reply.Generation != cfg.Master.Generation || entry.Name() != v.Reply.RequestID+".json" {
					return nil, "", c.ErrConflict
				}
				r.DeliveryID = v.Reply.DeliveryID
				r.Intent = &v.Reply.Intent
				turn, e := loadMasterTurn(cfg, r.DeliveryID)
				if e != nil {
					return nil, "", e
				}
				job = &turn.Job
			case "published":
				var v c.MessageReceipt
				if strictTeamJSON(raw, &v) != nil || v.MasterID != cfg.Master.MasterID || v.MessageID == "" {
					return nil, "", c.ErrConflict
				}
				r.MessageID = v.MessageID
			case "engineering":
				v, e := loadNativeExecution(cfg, strings.TrimSuffix(entry.Name(), ".json"))
				if e != nil {
					return nil, "", e
				}
				job = &v.Job
			case "actions":
				var v nativeActionRecord
				if strictTeamJSON(raw, &v) != nil || v.Version != 3 || entry.Name() != BodyHash(v.JobID+"\x00"+v.RequestID)+".json" {
					return nil, "", c.ErrConflict
				}
				// The producer lock excludes an in-flight helper. Prepared is
				// before a write or an acknowledged checkpoint; each remote
				// effect persists uncertain before it starts. Retiring a known
				// checkpoint does not complete or replay the original action.
				r.Terminal = v.State == "completed" || v.State == "prepared"
				r.Uncertain = !r.Terminal
			case "releases":
				r.Terminal, e = releaseRecoveryRecord(cfg, entry.Name(), raw)
				if e != nil {
					return nil, "", e
				}
				// Unknown writes need proof. Acknowledged checkpoints can be
				// explicitly retired; a tagged release can still be observed
				// through its accepted placement proof, never retagged.
				r.Uncertain = !r.Terminal
			}
			if job != nil {
				r.Terminal = job.SessionID == "" || job.MessageID == "" || job.SubmissionState == "prepared" || job.SubmissionState == "completed"
				if job.SessionID != "" && job.MessageID != "" {
					r.Intent = &c.NativeIntent{SessionID: job.SessionID, MessageID: job.MessageID}
				}
				if !r.Terminal && code != nil {
					observed, e := code.Observe(ctx, *job)
					if e != nil {
						return nil, "", e
					}
					r.Terminal = observed.Terminal
					r.Observation = mustRecoveryJSON(observed)
				}
				r.Uncertain = !r.Terminal
			}
			r.ID = BodyHash(string(mustRecoveryJSON(r)))
			out = append(out, r)
			if len(out) > c.MaxMessages {
				return nil, "", c.ErrCapacity
			}
		}
	}
	raw, e := readPrivate(filepath.Join(masterDir(cfg), "schedule.json"), MaxSnapshot)
	if e == nil {
		if _, e = decodeNativeSchedule(cfg, raw); e != nil {
			return nil, "", e
		}
		r := scheduleEvidence(raw)
		out = append(out, r)
		files = append(files, r.RecordKey+":"+BodyHash(string(r.Data)))
		if len(out) > c.MaxMessages {
			return nil, "", c.ErrCapacity
		}
	} else if !os.IsNotExist(e) {
		return nil, "", e
	}
	// The master retains accepted schedule and release-observation digests for
	// this epoch. Carry old observations alongside current bytes; never replace
	// accepted evidence when a read-only publication check advances its result.
	if adoption, e := loadPlacementAdoption(cfg); e == nil {
		for _, r := range adoption.Accepted.Records {
			if r.Kind != "schedule" && r.Kind != "releases" {
				continue
			}
			seen := false
			for _, current := range out {
				if current.ID == r.ID {
					seen = true
					break
				}
			}
			if !seen {
				out = append(out, r)
			}
		}
	} else if !os.IsNotExist(e) {
		return nil, "", e
	}
	if len(out) > c.MaxMessages {
		return nil, "", c.ErrCapacity
	}
	sort.Strings(files)
	return out, BodyHash(strings.Join(files, "\n")), nil
}
func mustRecoveryJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// ReconcileMaster is an explicit stopped-worker operation. Reporting does not
// accept an epoch; apply requires the operator's resolved remote manifest.
func ReconcileMaster(ctx context.Context, cfg Config, cl MasterRecoveryTransport, code OpenCode, apply bool) (c.RecoveryStatus, error) {
	var status c.RecoveryStatus
	if cfg.Version != 3 || cfg.Master == nil || cl.MasterID() != cfg.Master.MasterID {
		return status, c.ErrInvalid
	}
	release, e := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if e != nil {
		return status, e
	}
	defer release()
	dir := filepath.Join(masterDir(cfg), "actions")
	if e = ensurePrivateDir(dir, true); e != nil {
		return status, e
	}
	lockPath := filepath.Join(dir, "producer.lock")
	if e = safePath(lockPath); e != nil {
		return status, e
	}
	lock, e := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return status, e
	}
	defer func() { _ = lock.Close() }()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return status, c.ErrConflict
	}
	status, e = cl.RecoveryStatus(ctx)
	if e != nil {
		return status, e
	}
	if !status.Quiescent {
		return status, c.ErrConflict
	}
	records, files, e := gatherMasterRecovery(ctx, cfg, code)
	if e != nil {
		return status, e
	}
	path := recoverySnapshotPath(cfg, status.RestoreEpoch)
	snap := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: status.RestoreEpoch, FilesSHA: files}
	if raw, e := readPrivate(path, MaxSnapshot); e == nil {
		if strictTeamJSON(raw, &snap) != nil || snap.MasterID != cfg.Master.MasterID || snap.InstanceID != cfg.Master.InstanceID || snap.Generation != cfg.Master.Generation || snap.Epoch != status.RestoreEpoch {
			return status, c.ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return status, e
	}
	if apply {
		if snap.FilesSHA != files || !status.Reconciled || status.ManifestSHA256 != c.RecoveryDigest(snap.Records) {
			return status, c.ErrEpoch
		}
		// Admission uses connection epoch only after the immutable acceptance record.
		accepted := filepath.Join(masterDir(cfg), "recovery", "accepted.json")
		if e = atomicPrivate(accepted, mustRecoveryJSON(snap)); e != nil {
			return status, e
		}
		var conn masterConnectionState
		b, e := readPrivate(filepath.Join(masterDir(cfg), "connection.json"), 4096)
		if os.IsNotExist(e) {
			conn = masterConnectionState{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}
		} else if e != nil {
			return status, e
		} else if strictTeamJSON(b, &conn) != nil || conn.MasterID != cfg.Master.MasterID || conn.InstanceID != cfg.Master.InstanceID || conn.Generation != cfg.Master.Generation {
			return status, c.ErrConflict
		}
		conn.Epoch = status.RestoreEpoch
		return status, atomicPrivate(filepath.Join(masterDir(cfg), "connection.json"), mustRecoveryJSON(conn))
	}
	existing := map[string]bool{}
	for _, r := range snap.Records {
		existing[r.ID] = true
	}
	for _, r := range records {
		if !existing[r.ID] {
			snap.Records = append(snap.Records, r)
		}
	}
	snap.FilesSHA = files
	if len(mustRecoveryJSON(snap)) > MaxSnapshot {
		return status, c.ErrSize
	}
	if e = atomicPrivate(path, mustRecoveryJSON(snap)); e != nil {
		return status, e
	}
	placement := c.Placement{AgentID: cfg.Master.AgentID, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}
	for _, r := range snap.Records {
		if _, e = cl.ReconcileGuest(ctx, c.GuestRecovery{Placement: placement, RestoreEpoch: status.RestoreEpoch, Records: []c.RecoveryEvidence{r}}); e != nil {
			return status, e
		}
	}
	return cl.ReconcileGuest(ctx, c.GuestRecovery{Placement: placement, RestoreEpoch: status.RestoreEpoch, Complete: true, RecordCount: len(snap.Records), ManifestSHA256: c.RecoveryDigest(snap.Records)})
}

// Canonical JSON avoids field order differences between saved typed records and
// the immutable recovery receipt.
func canonicalRecoveryRaw(raw []byte) ([]byte, error) {
	var v any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if e := decoder.Decode(&v); e != nil {
		return nil, e
	}
	b, e := json.Marshal(v)
	return b, e
}
