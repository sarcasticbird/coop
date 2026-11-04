package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// NativeAbandonment is an operator disposition, never a native completion.
// OriginalSHA binds every original execution field, including the accepted Job.
// Even after fencing, unjournaled effects remain unknown. Action journals retain
// their independent recovery gates and cannot be waived by this disposition.
type NativeAbandonment struct {
	RequestID, ScopeSHA, PriorReceiptID, Reason, OriginalSHA string
	State, Outcome, Effects                                  string
	Fence                                                    *NativeFenceProof `json:",omitempty"`
}

type NativeFenceProof struct {
	ReceiptID, ReceiptSHA, ScopeSHA, SessionID, Disposition string
	VerifiedAt                                              time.Time
}

type NativeAbandonmentInput struct {
	JobID, SessionID, MessageID, RequestID, ScopeSHA, PriorReceiptID, Reason string
}

type nativeScopeFencer interface {
	FenceNativeScope(context.Context, Job, string, string, string) (NativeFenceProof, error)
}

var nativeRecoveryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validNativeAbandonment(x NativeExecution) bool {
	a := x.Abandonment
	if a == nil {
		return true
	}
	if !nativeRecoveryID.MatchString(a.RequestID) || !nativeRecoveryID.MatchString(a.PriorReceiptID) || a.RequestID == a.PriorReceiptID || !hexSHA.MatchString(a.ScopeSHA) || !hexSHA.MatchString(a.OriginalSHA) || strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 512 || strings.ContainsAny(a.Reason, "\r\n") || a.Outcome != "unknown" || a.Effects != "unknown" || (x.Job.Kind != "implement" && x.Job.Kind != "fix") || x.Job.SubmissionState != "accepted" || !validSession(x.Job.SessionID) || !validMessage(x.Job.MessageID) {
		return false
	}
	switch a.State {
	case "intent":
		if a.Fence != nil || x.Phase != "blocked" || x.Reason != "native_observation_unavailable" {
			return false
		}
	case "fenced":
		f := a.Fence
		if f == nil || x.Phase != "cancelled" || x.Reason != "operator_abandoned_missing_native_turn" || !validNativeFence(*f, x.Job, a) {
			return false
		}
	default:
		return false
	}
	original := x
	original.Abandonment = nil
	original.Phase, original.Reason = "blocked", "native_observation_unavailable"
	raw, err := json.Marshal(original)
	return err == nil && BodyHash(string(raw)) == a.OriginalSHA
}

func validNativeFence(f NativeFenceProof, j Job, a *NativeAbandonment) bool {
	return f.SessionID == j.SessionID && f.ScopeSHA == a.ScopeSHA && hexSHA.MatchString(f.ReceiptSHA) && !f.VerifiedAt.IsZero() &&
		(f.Disposition == "interrupted" && f.ReceiptID == a.RequestID || f.Disposition == "already_dispositioned" && f.ReceiptID == a.PriorReceiptID)
}

// AbandonMissingNativeTurn is deliberately separate from ordinary cancellation.
// It requires operator-supplied exact pins, a stopped worker, a producer fence,
// delivery hold, and an authenticated interruption-only native scope fence. It
// cannot recover a result, submit a continuation, or finish an unfinished action.
func AbandonMissingNativeTurn(ctx context.Context, cfg Config, code OpenCode, in NativeAbandonmentInput) (NativeExecution, error) {
	var x NativeExecution
	if cfg.Version != 3 || cfg.Master == nil || !cfg.DeliveryHold || !hexSHA.MatchString(in.JobID) || !validSession(in.SessionID) || !validMessage(in.MessageID) || !nativeRecoveryID.MatchString(in.RequestID) || !nativeRecoveryID.MatchString(in.PriorReceiptID) || in.RequestID == in.PriorReceiptID || !hexSHA.MatchString(in.ScopeSHA) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 512 || strings.ContainsAny(in.Reason, "\r\n") {
		return x, c.ErrInvalid
	}
	fencer, ok := code.(nativeScopeFencer)
	if !ok {
		return x, c.ErrUnsupported
	}
	unlock, err := NewVersionedFileStore(cfg.StateDir, 3).Lock()
	if err != nil {
		return x, err
	}
	defer unlock()
	release, err := lockNativeProducer(cfg)
	if err != nil {
		return x, err
	}
	defer release()
	paused, err := NewVersionedFileStore(cfg.StateDir, 3).Paused()
	if err != nil {
		return x, err
	}
	if !paused {
		return x, c.ErrConflict
	}
	x, err = loadNativeExecution(cfg, in.JobID)
	if err != nil {
		return x, err
	}
	if x.Job.SessionID != in.SessionID || x.Job.MessageID != in.MessageID || x.PendingContinuation != nil || x.Job.SubmissionState != "accepted" || (x.Job.Kind != "implement" && x.Job.Kind != "fix") {
		return x, c.ErrConflict
	}
	a := &NativeAbandonment{RequestID: in.RequestID, ScopeSHA: in.ScopeSHA, PriorReceiptID: in.PriorReceiptID, Reason: in.Reason, State: "intent", Outcome: "unknown", Effects: "unknown"}
	if x.Abandonment != nil {
		previous := *x.Abandonment
		previous.State, previous.Fence, previous.OriginalSHA = "intent", nil, ""
		if previous != *a {
			return x, c.ErrConflict
		}
		if x.Abandonment.State == "fenced" {
			return x, nil
		}
		a = x.Abandonment
	} else {
		if x.Phase != "blocked" || x.Reason != "native_observation_unavailable" {
			return x, c.ErrConflict
		}
		raw, err := json.Marshal(x)
		if err != nil {
			return x, err
		}
		a.OriginalSHA = BodyHash(string(raw))
	}
	// Do not abandon another saved invocation sharing this native root. Prior
	// accepted restore retirements remain authoritative without rewriting them.
	executions, err := os.ReadDir(filepath.Join(masterDir(cfg), "engineering"))
	if err != nil {
		return x, err
	}
	for _, entry := range executions {
		if entry.Name() == in.JobID+".json" {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return x, c.ErrConflict
		}
		other, err := loadNativeExecution(cfg, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return x, err
		}
		if other.Job.SessionID == x.Job.SessionID && other.Phase != "completed" && other.Phase != "cancelled" {
			raw, err := json.Marshal(other)
			if err != nil {
				return x, err
			}
			retired, err := recoveryRetired(cfg, "engineering", entry.Name(), raw)
			if err != nil || !retired {
				return x, c.ErrConflict
			}
		}
	}
	turns, err := masterTurns(cfg)
	if err != nil {
		return x, err
	}
	for _, turn := range turns {
		if turn.Job.SessionID == x.Job.SessionID && turn.Phase != "completed" && turn.Phase != "cancelled" {
			retired, err := retiredTurn(cfg, turn)
			if err != nil || !retired {
				return x, c.ErrConflict
			}
		}
	}
	// Refuse any unfinished managed effect for this job, including an
	// acknowledged checkpoint. This command is only invocation retirement.
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if err != nil {
		return x, err
	}
	for _, entry := range entries {
		if entry.Name() == "producer.lock" {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return x, c.ErrConflict
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return x, err
		}
		var action nativeActionRecord
		if strictTeamJSON(raw, &action) != nil || action.Version != 3 || entry.Name() != BodyHash(action.JobID+"\x00"+action.RequestID)+".json" || action.JobID == in.JobID && action.State != "completed" {
			return x, c.ErrConflict
		}
	}
	x.Abandonment = a
	if err = saveNativeExecution(cfg, x); err != nil {
		return x, err
	}
	fence, err := fencer.FenceNativeScope(ctx, x.Job, in.RequestID, in.ScopeSHA, in.PriorReceiptID)
	if err != nil {
		return x, err // Keep the exact durable intent. Never invent a receipt.
	}
	if !validNativeFence(fence, x.Job, a) {
		return x, c.ErrConflict
	}
	a.State, a.Fence = "fenced", &fence
	x.Phase, x.Reason = "cancelled", "operator_abandoned_missing_native_turn"
	return x, saveNativeExecution(cfg, x)
}
