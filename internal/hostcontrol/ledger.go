package hostcontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const retryWindow = 7 * 24 * time.Hour

var requestPattern = regexp.MustCompile(`^r1\.([0-9]{13})\.[0-9a-f]{32}$`)
var operationPattern = regexp.MustCompile(`^o1\.h1_[0-9a-f]{32}\.[0-9a-f]{32}$`)

type streamRecord struct {
	Total    int64 `json:"total"`
	Retained int   `json:"retained"`
}
type OperationRecord struct {
	ResolvesOperation string       `json:"resolves_operation,omitempty"`
	Version           int          `json:"version"`
	Revision          uint64       `json:"revision"`
	ID                string       `json:"id"`
	HostID            string       `json:"host_id"`
	CallerID          string       `json:"caller_id"`
	Verifier          string       `json:"verifier"`
	RequestHash       string       `json:"request_hash"`
	Input             Mutation     `json:"input"`
	Selector          string       `json:"selector,omitempty"`
	Fingerprint       string       `json:"fingerprint"`
	Incarnation       string       `json:"incarnation"`
	RuntimeInstance   string       `json:"runtime_instance,omitempty"`
	PhaseIncarnation  string       `json:"phase_incarnation,omitempty"`
	State             string       `json:"state"`
	Phase             string       `json:"phase,omitempty"`
	CancelIntent      bool         `json:"cancel_intent"`
	Dispatched        bool         `json:"dispatched"`
	HoldResolved      bool         `json:"hold_resolved"`
	CreatedAt         time.Time    `json:"created_at"`
	AcceptedAt        time.Time    `json:"accepted_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
	CompletedAt       time.Time    `json:"completed_at,omitempty"`
	Outcome           Outcome      `json:"outcome"`
	WakeID            string       `json:"wake_id,omitempty"`
	WakePhase         string       `json:"wake_phase,omitempty"`
	Nonce             string       `json:"nonce,omitempty"`
	Pruned            bool         `json:"pruned"`
	Stdout            streamRecord `json:"stdout"`
	Stderr            streamRecord `json:"stderr"`
}
type Ledger struct {
	mu      sync.Mutex
	root    string
	dir     *os.File
	host    Host
	limits  Limits
	records map[string]OperationRecord
}

func OpenLedger(root string, host Host, limits Limits) (*Ledger, error) {
	limits.defaults()
	if !strings.HasPrefix(host.ID, "h1_") || len(host.ID) != 35 {
		return nil, fail("invalid_host_identity")
	}
	d, e := privateDir(root, true)
	if e != nil {
		return nil, e
	}
	l := &Ledger{root: root, dir: d, host: host, limits: limits, records: map[string]OperationRecord{}}
	entries, e := d.ReadDir(3*limits.Records + 8)
	if e != nil && !errors.Is(e, io.EOF) {
		_ = d.Close()
		return nil, e
	}
	if len(entries) >= 3*limits.Records+8 {
		_ = d.Close()
		return nil, fail("ledger_capacity")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !strings.HasPrefix(entry.Name(), "o1.") {
			continue
		}
		raw, e := readAt(d, entry.Name(), 1<<20)
		if e != nil {
			_ = d.Close()
			return nil, e
		}
		var r OperationRecord
		if e = strictJSON(raw, &r); e != nil || r.Version != 1 || !operationPattern.MatchString(r.ID) || r.HostID != host.ID || entry.Name() != r.ID+".json" || !validState(r.State) {
			_ = d.Close()
			return nil, fail("invalid_operation_record")
		}
		l.records[r.ID] = r
	}
	if len(l.records) > limits.Records {
		_ = d.Close()
		return nil, fail("ledger_capacity")
	}
	if e = l.Prune(time.Now().UTC()); e != nil {
		_ = d.Close()
		return nil, e
	}
	return l, nil
}
func (l *Ledger) Close() error { return l.dir.Close() }
func validState(s string) bool {
	switch s {
	case "accepted", "running", "succeeded", "failed", "cancel_requested", "cancelled", "indeterminate":
		return true
	}
	return false
}
func terminal(s string) bool { return s == "succeeded" || s == "failed" || s == "cancelled" }
func validateKey(key RequestKey, now time.Time) error {
	m := requestPattern.FindStringSubmatch(key.ID)
	if m == nil {
		return fail("invalid_request_id")
	}
	ms, e := strconv.ParseInt(m[1], 10, 64)
	if e != nil || !key.CreatedAt.Equal(time.UnixMilli(ms)) {
		return fail("invalid_request_time")
	}
	if now.Sub(key.CreatedAt) > retryWindow || key.CreatedAt.After(now.Add(5*time.Minute)) {
		return fail("expired_or_future_request")
	}
	return nil
}
func (l *Ledger) persist(r OperationRecord) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if len(raw) > 1<<20 {
		return fail("operation_record_too_large")
	}
	return writeAt(l.dir, r.ID+".json", raw)
}
func (l *Ledger) Accept(caller Caller, input Mutation, fingerprint, incarnation string, now time.Time, instance ...string) (OperationRecord, bool, error) {
	return l.accept(caller, input, fingerprint, incarnation, now, "", instance...)
}
func (l *Ledger) AcceptRecovery(caller Caller, input Mutation, fingerprint, incarnation string, now time.Time, instance, target string) (OperationRecord, bool, error) {
	return l.accept(caller, input, fingerprint, incarnation, now, target, instance)
}
func (l *Ledger) accept(caller Caller, input Mutation, fingerprint, incarnation string, now time.Time, resolves string, instance ...string) (OperationRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := validateKey(input.Key, now); e != nil {
		return OperationRecord{}, false, e
	}
	switch input.Action {
	case "exec", "start", "stop", "restart", "ping", "pause", "resume", "cancel":
	default:
		return OperationRecord{}, false, fail("invalid_action")
	}
	canonical, e := json.Marshal(struct {
		Host, Caller string
		Input        Mutation
	}{l.host.ID, caller.ID, input})
	if e != nil {
		return OperationRecord{}, false, e
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	for _, r := range l.records {
		if r.CallerID == caller.ID && r.Input.Key.ID == input.Key.ID {
			if r.RequestHash != hash {
				return OperationRecord{}, false, fail("request_id_conflict")
			}
			return cloneRecord(r), true, nil
		}
	}
	if e = l.prune(now); e != nil {
		return OperationRecord{}, false, e
	}
	if resolves != "" {
		target, ok := l.records[resolves]
		if !ok || target.Input.Action != "exec" || terminal(target.State) || target.HoldResolved || target.Input.Workspace != input.Workspace || !input.InterruptActive || input.Action != "stop" && input.Action != "restart" || target.Incarnation != incarnation {
			return OperationRecord{}, false, fail("invalid_recovery_target")
		}
		for _, r := range l.records {
			if r.ResolvesOperation == resolves && !terminal(r.State) {
				return OperationRecord{}, false, fail("workspace_busy")
			}
		}
	}
	unresolved := 0
	cancellations := map[string]bool{}
	recovery := map[string]bool{}
	for _, r := range l.records {
		if terminal(r.State) || r.HoldResolved || r.Input.Action == "cancel" {
			continue
		}
		if r.ResolvesOperation == "" {
			unresolved++
		}
		if !r.CancelIntent {
			cancellations[r.ID] = true
		}
		if r.Input.Action == "exec" {
			recovery[r.ID] = true
		}
	}
	for _, r := range l.records {
		if !terminal(r.State) {
			if r.Input.Action == "cancel" {
				delete(cancellations, r.Input.TargetOperation)
			}
		}
		// A completed/cancelled resolver has consumed this retry-window budget.
		// Re-reserving it must not steal the original command's cancellation slot.
		if r.ResolvesOperation != "" {
			delete(recovery, r.ResolvesOperation)
		}
	}
	extra := 1 // cancellation slot for a newly admitted work operation
	switch {
	case input.Action == "cancel":
		delete(cancellations, input.TargetOperation)
		extra = 0
	case resolves != "":
		delete(recovery, resolves)
	default:
		if unresolved >= l.limits.Unresolved {
			return OperationRecord{}, false, fail("ledger_capacity")
		}
		if input.Action == "exec" {
			extra += 2
		} // resolving lifecycle operation and its cancellation
	}
	needed := len(cancellations) + 2*len(recovery) + extra
	if len(l.records)+1+needed > l.limits.Records {
		return OperationRecord{}, false, fail("ledger_capacity")
	}
	id, e := randomHex(16)
	if e != nil {
		return OperationRecord{}, false, e
	}
	r := OperationRecord{ResolvesOperation: resolves, Version: 1, Revision: 1, ID: "o1." + l.host.ID + "." + id, HostID: l.host.ID, CallerID: caller.ID, Verifier: hex.EncodeToString(caller.digest[:]), RequestHash: hash, Input: input, Fingerprint: fingerprint, Incarnation: incarnation, State: "accepted", CreatedAt: input.Key.CreatedAt, AcceptedAt: now.UTC(), UpdatedAt: now.UTC()}
	if len(instance) > 0 {
		r.RuntimeInstance = instance[0]
	}
	if input.Action == "ping" {
		sum := sha256.Sum256([]byte("coop-mcp-ping-v1\x00" + l.host.ID + "\x00" + caller.ID + "\x00" + input.Key.ID))
		r.WakeID = hex.EncodeToString(sum[:])
		r.WakePhase = "host_pending"
	}
	if e = l.persist(r); e != nil {
		return OperationRecord{}, false, e
	}
	l.records[r.ID] = cloneRecord(r)
	return cloneRecord(r), false, nil
}
func cloneRecord(r OperationRecord) OperationRecord {
	r.Input.Argv = append([]string(nil), r.Input.Argv...)
	r.Input.Stdin = append([]byte(nil), r.Input.Stdin...)
	if r.Outcome.ExitCode != nil {
		v := *r.Outcome.ExitCode
		r.Outcome.ExitCode = &v
	}
	return r
}
func (l *Ledger) Lookup(caller Caller, id string) (OperationRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || r.CallerID != caller.ID || !strings.HasPrefix(id, "o1."+l.host.ID+".") {
		return OperationRecord{}, fail("operation_not_found")
	}
	return cloneRecord(r), nil
}
func (l *Ledger) all() []OperationRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]OperationRecord, 0, len(l.records))
	for _, r := range l.records {
		out = append(out, cloneRecord(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AcceptedAt.Before(out[j].AcceptedAt) })
	return out
}
func (l *Ledger) Update(r OperationRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	old, ok := l.records[r.ID]
	if !ok || r.Revision != old.Revision {
		return fail("operation_revision_conflict")
	}
	if r.ResolvesOperation != old.ResolvesOperation || r.CallerID != old.CallerID || r.RequestHash != old.RequestHash || r.HostID != old.HostID || r.Input.Key != old.Input.Key || !validState(r.State) {
		return fail("invalid_operation_update")
	}
	if terminal(old.State) && r.State != old.State {
		return fail("terminal_operation")
	}
	if old.State == "indeterminate" && r.State == "running" && old.Input.Action == "exec" {
		return fail("uncertain_exec_no_replay")
	}
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	if terminal(r.State) {
		if r.CompletedAt.IsZero() {
			r.CompletedAt = r.UpdatedAt
		}
		r.Input.Argv = nil
		r.Input.Stdin = nil
		r.Input.Prompt = ""
		r.Nonce = ""
	}
	if e := l.persist(r); e != nil {
		return e
	}
	l.records[r.ID] = cloneRecord(r)
	return nil
}
func (l *Ledger) Prune(now time.Time) error { l.mu.Lock(); defer l.mu.Unlock(); return l.prune(now) }
func (l *Ledger) prune(now time.Time) error {
	var completed []OperationRecord
	for _, r := range l.records {
		if terminal(r.State) {
			completed = append(completed, r)
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].CompletedAt.After(completed[j].CompletedAt) })
	for i, r := range completed {
		expired := now.Sub(r.CreatedAt) > retryWindow
		if !r.Pruned && (i >= l.limits.Terminal || now.Sub(r.CompletedAt) > retryWindow || expired) {
			for _, stream := range []string{"stdout", "stderr"} {
				if e := unix.Unlinkat(int(l.dir.Fd()), r.ID+"."+stream, 0); e != nil && !errors.Is(e, unix.ENOENT) {
					return e
				}
			}
			if e := l.dir.Sync(); e != nil {
				return e
			}
			r.Pruned = true
			r.Input.Argv = nil
			r.Input.Stdin = nil
			r.Input.Prompt = ""
			r.Nonce = ""
			r.Revision++
			if e := l.persist(r); e != nil {
				return e
			}
			l.records[r.ID] = r
		}
		if expired {
			if e := unix.Unlinkat(int(l.dir.Fd()), r.ID+".json", 0); e != nil {
				return e
			}
			if e := l.dir.Sync(); e != nil {
				return e
			}
			delete(l.records, r.ID)
		}
	}
	return nil
}
func recordCaller(r OperationRecord) (Caller, error) {
	b, e := hex.DecodeString(r.Verifier)
	if e != nil || len(b) != 32 {
		return Caller{}, fmt.Errorf("invalid operation caller")
	}
	c := Caller{ID: r.CallerID}
	copy(c.digest[:], b)
	return c, nil
}

// Dispatch is the atomic boundary between cancellable queued work and a side
// effect whose outcome may become uncertain. Cancellation cannot cross it unseen.
func (l *Ledger) Dispatch(caller Caller, id, nonce string) (OperationRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || r.CallerID != caller.ID || r.State != "running" || r.Dispatched {
		return OperationRecord{}, fail("dispatch_not_allowed")
	}
	r.Dispatched = true
	r.Nonce = nonce
	r.Phase = "dispatch_pending"
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	if e := l.persist(r); e != nil {
		return OperationRecord{}, e
	}
	l.records[id] = r
	return cloneRecord(r), nil
}

func (l *Ledger) Checkpoint(caller Caller, id, phase string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || r.CallerID != caller.ID || r.State != "running" || r.CancelIntent {
		return fail("checkpoint_not_allowed")
	}
	r.Dispatched = true
	r.Phase = phase
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	if err := l.persist(r); err != nil {
		return err
	}
	l.records[id] = r
	return nil
}

// InspectOperation is host-owner administration, not remote caller lookup.
func InspectOperation(cfg Config, id string) (OperationRecord, error) {
	if !operationPattern.MatchString(id) {
		return OperationRecord{}, fail("invalid_operation_id")
	}
	raw, err := readPrivate(filepath.Join(cfg.StateDir, "identity.json"), 1024)
	if err != nil {
		return OperationRecord{}, err
	}
	var host Host
	if err = strictJSON(raw, &host); err != nil {
		return OperationRecord{}, err
	}
	if !strings.HasPrefix(id, "o1."+host.ID+".") {
		return OperationRecord{}, fail("operation_not_found")
	}
	raw, err = readPrivate(filepath.Join(cfg.StateDir, "operations", id+".json"), 1<<20)
	if err != nil {
		return OperationRecord{}, err
	}
	var r OperationRecord
	if err = strictJSON(raw, &r); err != nil || r.ID != id || r.HostID != host.ID {
		return OperationRecord{}, fail("invalid_operation_record")
	}
	return r, nil
}

// LifecycleCheckpoint persists phase-specific runtime evidence atomically with
// the cancellation/dispatch guard. Callers hold the runtime's lifecycle lock.
func (l *Ledger) LifecycleCheckpoint(caller Caller, id, phase, instance, incarnation string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || r.CallerID != caller.ID || r.State != "running" || r.CancelIntent {
		return fail("checkpoint_not_allowed")
	}
	r.Dispatched = true
	r.Phase = phase
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	if phase == "started" {
		r.RuntimeInstance = instance
		r.PhaseIncarnation = incarnation
	}
	if err := l.persist(r); err != nil {
		return err
	}
	l.records[id] = r
	return nil
}
