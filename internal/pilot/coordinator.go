// Package pilot is a throwaway, fixture-only orchestration spike. It is not a worker.
package pilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

var ErrHeld = errors.New("pilot_held_inspect_receipt")

const MasterID = "coop-maestro-pilot"
const BirdID = "fixture-bird"

type Request struct {
	RequestID string    `json:"request_id"`
	Task      string    `json:"task"`
	Goal      c.GoalRef `json:"goal"`
}
type Task struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status"`
}
type Evidence struct {
	SessionID       string `json:"session_id"`
	NativeMessageID string `json:"native_message_id,omitempty"`
	Model           string `json:"model,omitempty"`
	SHA256          string `json:"sha256"`
	ReplyID         string `json:"reply_id"`
	ExitCode        int    `json:"exit_code"`
}
type Result struct {
	Request          Request          `json:"request"`
	Fingerprint      string           `json:"fingerprint"`
	TaskSHA256       string           `json:"task_sha256"`
	ApprovalSequence int64            `json:"approval_sequence"`
	Phase            string           `json:"phase"`
	Delivery         c.MessageReceipt `json:"delivery"`
	Evidence         Evidence         `json:"evidence"`
}
type Backend interface {
	Authority(context.Context, c.GoalRef) (c.GoalAuthority, error)
	Send(context.Context, c.GoalAuthority, Request) (c.MessageReceipt, error)
}
type Ledger interface {
	ReadTask(context.Context, string) (Task, error)
	Claim(context.Context, string) error
	Record(context.Context, Request, Evidence) error
}
type Bird interface {
	Execute(context.Context, c.MessageReceipt) (Evidence, error)
}

// EvidenceRecovery may inspect an already submitted turn; it must never submit.
type EvidenceRecovery interface {
	RecoverEvidence(context.Context, c.MessageReceipt) (Evidence, error)
}
type Coordinator struct {
	StateDir, ConfigSHA, BirdID string
	Backend                     Backend
	Ledger                      Ledger
	Bird                        Bird
}

func digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("pilot fingerprint value: %w", err))
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TaskDigest excludes mutable lifecycle and evidence fields.
func TaskDigest(t Task) string               { return digest([]string{t.UID, t.Title, t.Body}) }
func (p *Coordinator) path(id string) string { return filepath.Join(p.StateDir, digest(id)+".json") }
func (p *Coordinator) Status(id string) (Result, error) {
	var r Result
	b, e := os.ReadFile(p.path(id))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func (p *Coordinator) save(r Result) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(p.StateDir, ".receipt-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, e = f.Write(b); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, p.path(r.Request.RequestID))
}
func (p *Coordinator) authority(ctx context.Context, r Result) (c.GoalAuthority, error) {
	a, e := p.Backend.Authority(ctx, r.Request.Goal)
	if e != nil {
		return a, e
	}
	if !a.Valid || a.ApprovalSequence < 1 || a.Actors.Author.PrincipalID != p.BirdID || a.Revision.Proposal.Scope != "kata-fixture-sha256:"+r.TaskSHA256 {
		return a, c.ErrForbidden
	}
	if r.ApprovalSequence != 0 && a.ApprovalSequence != r.ApprovalSequence {
		return a, c.ErrConflict
	}
	return a, nil
}
func (p *Coordinator) Run(ctx context.Context, in Request) (Result, error) {
	var r Result
	if in.RequestID == "" || len(in.RequestID) > 128 || in.Task == "" || in.Goal.Source != "master" || in.Goal.MasterID != MasterID || in.Goal.GoalID == "" || in.Goal.RevisionSHA256 == "" || p.BirdID != BirdID || !filepath.IsAbs(p.StateDir) {
		return r, c.ErrInvalid
	}
	if e := os.MkdirAll(p.StateDir, 0700); e != nil {
		return r, e
	}
	info, e := os.Lstat(p.StateDir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return r, c.ErrForbidden
	}
	lock, e := os.OpenFile(filepath.Join(p.StateDir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return r, e
	}
	defer func() { _ = lock.Close() }()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return r, c.ErrConflict
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	fingerprint := digest([]any{in, p.ConfigSHA})
	r, e = p.Status(in.RequestID)
	if e == nil {
		if r.Fingerprint != fingerprint {
			return r, c.ErrConflict
		}
		if r.Phase == "completed" {
			return r, nil
		}
		if r.Phase != "claimed" && r.Phase != "running" {
			return r, ErrHeld
		}
		if r.Phase == "running" {
			if _, ok := p.Bird.(EvidenceRecovery); !ok {
				return r, ErrHeld
			}
		}
	} else if !os.IsNotExist(e) {
		return r, e
	}
	t, e := p.Ledger.ReadTask(ctx, in.Task)
	if e != nil {
		return r, e
	}
	if r.Phase == "" {
		r = Result{Request: in, Fingerprint: fingerprint, TaskSHA256: TaskDigest(t)}
	}
	if TaskDigest(t) != r.TaskSHA256 || t.Status != "open" {
		return r, c.ErrConflict
	}
	a, e := p.authority(ctx, r)
	if e != nil {
		return r, e
	}
	r.ApprovalSequence = a.ApprovalSequence
	if r.Phase == "running" {
		r.Evidence, e = p.Bird.(EvidenceRecovery).RecoverEvidence(ctx, r.Delivery)
		if e != nil {
			return r, fmt.Errorf("%w: %v", ErrHeld, e)
		}
	} else {
		if r.Phase == "" {
			r.Phase = "claiming"
			if e = p.save(r); e != nil {
				return r, e
			}
			if e = p.Ledger.Claim(ctx, in.Task); e != nil {
				return r, fmt.Errorf("claim uncertain, inspect receipt: %w", e)
			}
			r.Phase = "claimed"
			if e = p.save(r); e != nil {
				return r, e
			}
		}
		// Re-read both independent authorities after the claim and before dispatch.
		current, e := p.Ledger.ReadTask(ctx, in.Task)
		if e != nil {
			return r, e
		}
		if TaskDigest(current) != r.TaskSHA256 || current.Status != "open" {
			return r, c.ErrConflict
		}
		a, e = p.authority(ctx, r)
		if e != nil {
			return r, e
		}
		r.Delivery, e = p.Backend.Send(ctx, a, in)
		if e != nil {
			return r, e
		} // stable master request ID permits send reconciliation
		if len(r.Delivery.Deliveries) != 1 || r.Delivery.Deliveries[0].AgentID != p.BirdID {
			return r, c.ErrConflict
		}
		r.Phase = "running"
		if e = p.save(r); e != nil {
			return r, e
		}
		r.Evidence, e = p.Bird.Execute(ctx, r.Delivery)
		if e != nil {
			return r, fmt.Errorf("execution uncertain, inspect receipt: %w", e)
		}

	}
	r.Phase = "recording"
	if e = p.save(r); e != nil {
		return r, e
	}
	if _, e = p.authority(ctx, r); e != nil {
		return r, e
	}
	current, e := p.Ledger.ReadTask(ctx, in.Task)
	if e != nil {
		return r, e
	}
	if TaskDigest(current) != r.TaskSHA256 || current.Status != "open" {
		return r, c.ErrConflict
	}
	if e = p.Ledger.Record(ctx, in, r.Evidence); e != nil {
		return r, e
	}
	r.Phase = "completed"
	return r, p.save(r)
}
