package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type FileWakeStore struct {
	Dir     string
	Version int
}

var wakeDirectorySync = syncWakeDir

const wakeFenceName = "version.json"

type wakeVersionFence struct {
	Version    int
	InstanceID string
}

func NewWakeStore(dir string) *FileWakeStore { return NewVersionedWakeStore(dir, 1) }
func NewVersionedWakeStore(dir string, version int) *FileWakeStore {
	return &FileWakeStore{Dir: dir, Version: version}
}
func (s *FileWakeStore) compatible() error {
	// Standalone wake fixtures have no parent ledger. Managed inboxes always live
	// under wake/v1, whose record format stays v1 across the ledger conversion.
	if filepath.Base(s.Dir) == "v1" && filepath.Base(filepath.Dir(s.Dir)) == "wake" {
		state, err := NewVersionedFileStore(filepath.Dir(filepath.Dir(s.Dir)), s.Version).Load()
		if err != nil {
			return err
		}
		if state.Version == 2 {
			raw, err := readPrivate(filepath.Join(s.Dir, "inbox", wakeFenceName), 4096)
			if err != nil {
				return errors.New("missing or unreadable wake version fence")
			}
			var fence wakeVersionFence
			if strictTeamJSON(raw, &fence) != nil || fence.Version != 2 || fence.InstanceID != state.InstanceID {
				return errors.New("invalid wake version fence")
			}
		}
	}
	return nil
}
func (s *FileWakeStore) ensure(create bool) error {
	if err := s.compatible(); err != nil {
		return err
	}
	if err := ensurePrivateDir(s.Dir, create); err != nil {
		return err
	}
	for _, dir := range []string{"inbox", "receipts"} {
		if err := ensurePrivateDir(filepath.Join(s.Dir, dir), create); err != nil {
			return err
		}
	}
	return nil
}
func (s *FileWakeStore) producerLock() (func(), error) {
	return s.producerLockMode(false)
}
func (s *FileWakeStore) producerLockMode(nonblocking bool) (func(), error) {
	if err := s.ensure(true); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, "producer.lock")
	if err := safePath(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open wake producer lock")
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = f.Close() // Preserve the primary lock error.
		return nil, errors.New("unsafe wake producer lock")
	}
	mode := unix.LOCK_EX
	if nonblocking {
		mode |= unix.LOCK_NB
	}
	if err := unix.Flock(fd, mode); err != nil {
		_ = f.Close() // Preserve the primary lock error.
		return nil, errors.New("cannot acquire wake producer lock")
	}
	if err := s.compatible(); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
}
func sameWakeRequest(a, b WakeRequest) bool {
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	if a.Kind == "discussion" && b.Kind == "discussion" {
		a.Source = canonicalConversation(a.Source)
		b.Source = canonicalConversation(b.Source)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func (s *FileWakeStore) request(id string) (WakeRequest, error) {
	var r WakeRequest
	if err := s.ensure(false); err != nil {
		return r, err
	}
	if !hexSHA.MatchString(id) {
		return r, errors.New("invalid wake ID")
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "inbox", id+".json"), MaxWakeRecord)
	if err != nil {
		return r, err
	}
	if strictJSON(raw, &r) != nil || r.ID != id {
		return r, errors.New("invalid stored wake request")
	}
	return r, validateWakeRequest(r)
}
func (s *FileWakeStore) Requests() ([]WakeRequest, error) {
	if err := s.ensure(false); os.IsNotExist(err) {
		return []WakeRequest{}, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "inbox"))
	if err != nil {
		return nil, errors.New("cannot read wake inbox")
	}
	var result []WakeRequest
	for _, entry := range entries {
		// This non-request name deliberately makes legacy Requests/Enqueue fail
		// closed. V2 readers validated its instance binding in ensure above.
		if s.Version == 2 && entry.Name() == wakeFenceName {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".request-") {
			continue
		}
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !hexSHA.MatchString(id) {
			return nil, errors.New("invalid wake inbox entry")
		}
		r, err := s.request(id)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
		if len(result) > MaxWakeRecords {
			return nil, errors.New("wake record capacity exceeded")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}
func syncWakeDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
func writeWakeImmutable(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".request-")
	if err != nil {
		return errors.New("cannot stage wake request")
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err := f.Chmod(0600); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err := f.Write(raw); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		return errors.New("cannot publish immutable wake request")
	}
	return wakeDirectorySync(filepath.Dir(path))
}
func (s *FileWakeStore) Enqueue(r WakeRequest) (WakeSummary, error) {
	auto := r.ID == "" && r.Kind == "ping"
	if auto {
		r.ID = randomID()
	}
	if r.Kind == "ping" && r.ChainID == "" {
		r.ChainID = r.ID
	}
	if err := validateWakeRequest(r); err != nil {
		return WakeSummary{}, err
	}
	release, err := s.producerLock()
	if err != nil {
		return WakeSummary{}, err
	}
	defer release()
	requests, err := s.Requests()
	if err != nil {
		return WakeSummary{}, err
	}
	pending := 0
	for _, old := range requests {
		receipt, err := s.Receipt(old.ID)
		if err != nil {
			return WakeSummary{}, err
		}
		if !wakeReceiptTerminal(receipt) {
			pending++
		}
		if old.ID == r.ID {
			if !sameWakeRequest(old, r) {
				return WakeSummary{}, errors.New("wake request ID conflicts with existing input")
			}
			if err := wakeDirectorySync(filepath.Join(s.Dir, "inbox")); err != nil {
				return WakeSummary{}, err
			}
			return WakeSummary{ID: old.ID, Kind: old.Kind, Phase: receipt.Phase, Reason: receipt.Reason, Coalesced: true}, nil
		}
		if auto && receipt.Phase == "queued" && old.Kind == "ping" && old.Target == r.Target && old.Prompt == r.Prompt && old.Source.DiscussionURL == r.Source.DiscussionURL && sameActor(old.Source.Author, r.Source.Author) {
			if err := wakeDirectorySync(filepath.Join(s.Dir, "inbox")); err != nil {
				return WakeSummary{}, err
			}
			return WakeSummary{ID: old.ID, Kind: old.Kind, Phase: receipt.Phase, Reason: receipt.Reason, Coalesced: true}, nil
		}
	}
	if pending >= MaxWakeQueued || len(requests) >= MaxWakeRecords {
		return WakeSummary{}, ErrWakeCapacity
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return WakeSummary{}, errors.New("cannot encode wake request")
	}
	if err := writeWakeImmutable(filepath.Join(s.Dir, "inbox", r.ID+".json"), raw); err != nil {
		return WakeSummary{}, err
	}
	return WakeSummary{ID: r.ID, Kind: r.Kind, Phase: "queued"}, nil
}
func (s *FileWakeStore) Receipt(id string) (WakeReceipt, error) {
	if _, err := s.request(id); err != nil {
		return WakeReceipt{}, err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "receipts", id+".json"), MaxWakeReceipt)
	if os.IsNotExist(err) {
		return WakeReceipt{Version: 1, RequestID: id, Phase: "queued"}, nil
	}
	if err != nil {
		return WakeReceipt{}, err
	}
	var r WakeReceipt
	if strictJSON(raw, &r) != nil || r.Version != 1 || r.RequestID != id || !wakePhase(r.Phase) || validateJobPromptFailure(r.Job) != nil {
		return r, errors.New("invalid wake receipt")
	}
	return r, nil
}
func (s *FileWakeStore) SaveReceipt(r WakeReceipt) error {
	if r.Version != 1 || !hexSHA.MatchString(r.RequestID) || !wakePhase(r.Phase) {
		return errors.New("invalid wake receipt")
	}
	if err := validateJobPromptFailure(r.Job); err != nil {
		return err
	}
	if _, err := s.request(r.RequestID); err != nil {
		return err
	}
	if err := s.ensure(false); err != nil {
		return err
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxWakeReceipt {
		return errors.New("wake receipt exceeds size limit")
	}
	return atomicPrivate(filepath.Join(s.Dir, "receipts", r.RequestID+".json"), raw)
}
func newWakeRouting() WakeRoutingState {
	return WakeRoutingState{Version: 1, Seen: map[string]string{}, Chains: map[string]WakeChain{}, Bindings: map[string]WakeBinding{}}
}
func (s *FileWakeStore) Routing() (WakeRoutingState, error) {
	if err := s.ensure(false); os.IsNotExist(err) {
		return newWakeRouting(), nil
	} else if err != nil {
		return WakeRoutingState{}, err
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "routing.json"), MaxSnapshot)
	if os.IsNotExist(err) {
		return newWakeRouting(), nil
	}
	if err != nil {
		return WakeRoutingState{}, err
	}
	var r WakeRoutingState
	if strictJSON(raw, &r) != nil || r.Version != 1 || r.Seen == nil || r.Chains == nil || r.Bindings == nil || !validWakeContinuity(r) {
		return r, errors.New("invalid wake routing state")
	}
	return r, nil
}
func (s *FileWakeStore) SaveRouting(r WakeRoutingState) error {
	if r.Version != 1 || r.Seen == nil || r.Chains == nil || r.Bindings == nil || !validWakeContinuity(r) {
		return errors.New("invalid wake routing state")
	}
	if err := s.ensure(true); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxSnapshot {
		return errors.New("wake routing state exceeds size limit")
	}
	return atomicPrivate(filepath.Join(s.Dir, "routing.json"), raw)
}
