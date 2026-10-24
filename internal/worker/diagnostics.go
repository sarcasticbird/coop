package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// serviceObservation is shared by the real client and its scoped readers.
// It records metadata only, never request arguments or response bodies.
type serviceObservation struct {
	mu    sync.Mutex
	value c.DiagnosticService
}

func (o *serviceObservation) record(start time.Time, err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	v := &o.value
	v.ObservedAt = time.Now().UTC()
	v.DurationMS = max(0, time.Since(start).Milliseconds())
	v.Requests++
	v.Available = err == nil
	v.Origin, v.Category = diagnosticError(err)
	v.RetryAt = time.Time{}
	if err == nil {
		v.LastSuccess = v.ObservedAt
	}
	var gh *GitHubError
	if errors.As(err, &gh) {
		v.RetryAt = gh.RetryAt
	}
}

func (o *serviceObservation) snapshot() c.DiagnosticService {
	if o == nil {
		return c.DiagnosticService{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.value
}

func diagnosticError(err error) (origin, category string) {
	if err == nil {
		return "", ""
	}
	origin, category = "worker", "operation_failed"
	var gh *GitHubError
	var oc *OpenCodeError
	if errors.As(err, &gh) {
		origin, category = "github", gh.Category
	} else if errors.As(err, &oc) {
		origin, category = "opencode", oc.Category
	} else if errors.Is(err, c.ErrUnavailable) || errors.Is(err, c.ErrEpoch) {
		origin, category = "master", "unavailable"
	}
	// Categories are an allowlist, not arbitrary error strings from a tool/provider.
	if !slices.Contains([]string{"", "transport_failed", "invalid_response", "request_failed", "rate_limited", "credentials_expired", "permission_denied", "not_found", "response_too_large", "server_auth_unavailable", "unsupported_server_version", "unavailable"}, category) {
		category = "operation_failed"
	}
	return
}

func diagnosticHandling(s Status) string {
	if s.Paused || s.State == "paused" {
		return "waiting"
	}
	switch s.WaitReason {
	case "local_pause", "native_dependency_wait", "native_task_wait", "native_branch_refresh_wait":
		return "waiting"
	case "native_dependency_retry", "native_catalog_unavailable", "cache_refresh_wait":
		return "auto_retry"
	case "native_recovery_required", "native_dependency_recovery_required", "native_admission_stalled", "native_turn_overdue", "native_branch_refresh_limit", "native_branch_refresh_no_progress", "native_checks_repair_limit", "native_checks_repair_no_progress":
		return "action_required"
	case "native_branch_refresh", "native_checks_repair":
		return "working"
	case "":
		if s.State == "working" {
			return "working"
		}
		if s.State == "idle" {
			return "waiting"
		}
	}
	return "unknown"
}

type diagnosticBuilder struct {
	started     time.Time
	transitions []c.DiagnosticTransition
}

func newDiagnosticBuilder(now time.Time) *diagnosticBuilder {
	return &diagnosticBuilder{started: now.UTC()}
}

func diagnosticReference(cfg Config, ref string) string {
	if !c.DiagnosticReference(ref) || ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	var repo string
	if isKataTask(ref) {
		project, _, repository, err := c.ParseKataTaskReference(ref)
		if err != nil || !kataTaskSource(cfg) || project != cfg.TaskSource.ProjectUID {
			return ""
		}
		repo = repository
	} else {
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		repo = strings.Join(parts[:2], "/")
	}
	if !containsFold(cfg.CodeOwners, strings.Split(repo, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, repo) {
		return ""
	}
	return ref
}

func (b *diagnosticBuilder) snapshot(cfg Config, epoch int64, s Status, err error, gh, oc c.DiagnosticService, now time.Time) c.WorkerDiagnostics {
	m := cfg.Master
	d := c.WorkerDiagnostics{Version: 1, Placement: c.Placement{AgentID: m.AgentID, HostID: m.HostID, InstanceID: m.InstanceID, Generation: m.Generation}, RestoreEpoch: epoch, ObservedAt: now.UTC(), StartedAt: b.started, GitHub: gh, OpenCode: oc,
		Wait: c.DiagnosticWait{ObservedAt: now.UTC(), Handling: diagnosticHandling(s), Reason: s.WaitReason, NextRetry: s.NextRetry}, Work: c.DiagnosticWork{ObservedAt: now.UTC(), SubmissionState: s.SubmissionState}}
	if s.Paused || s.State == "paused" {
		d.Wait.Reason, d.Wait.NextRetry = "local_pause", time.Time{}
	}
	if !c.DiagnosticCode(d.Wait.Reason) {
		d.Wait.Reason = "unknown"
	}
	d.GitHub.LastRead = s.LastPoll
	if !c.DiagnosticCode(d.Work.SubmissionState) {
		d.Work.SubmissionState = "unknown"
	}
	if validSession(s.SessionID) {
		d.Work.SessionID = s.SessionID
	}
	if p := s.Native; p != nil {
		if p.ProgressAt != nil && !p.ProgressAt.IsZero() && !p.ProgressAt.After(now) {
			at := p.ProgressAt.UTC()
			d.Work.ProgressAt = &at
		}
		d.Work.WorkTitle = diagnosticTitle(p.WorkTitle)
		d.Wait.Deadline = p.Deadline
		if p.Continuation != nil && c.ValidateContinuationTarget(*p.Continuation) == nil && diagnosticReference(cfg, p.Continuation.TaskURL) != "" {
			target := *p.Continuation
			d.Work.Continuation = &target
		}
		if hexSHA.MatchString(p.PrimaryJobID) {
			d.Work.JobID = p.PrimaryJobID
		}
		for _, blocker := range p.Blocked {
			if s.Current == nil && blocker.JobID == p.PrimaryJobID {
				d.Work.TaskURL = diagnosticReference(cfg, blocker.Task)
			}
			for _, ref := range blocker.Dependencies {
				if safe := diagnosticReference(cfg, ref); safe != "" && len(d.Wait.Dependencies) < c.MaxDiagnosticTransitions && !slices.Contains(d.Wait.Dependencies, safe) {
					d.Wait.Dependencies = append(d.Wait.Dependencies, safe)
				}
			}
		}
	}
	if w := s.Current; w != nil {
		if w.ProgressAt != nil && !w.ProgressAt.IsZero() && !w.ProgressAt.After(now) {
			at := w.ProgressAt.UTC()
			d.Work.ProgressAt = &at
		}
		d.Work.WorkTitle = diagnosticTitle(w.WorkTitle)
		if c.DiagnosticCode(w.Stage) {
			d.Work.Stage = w.Stage
		}
		if c.DiagnosticCode(w.Phase) {
			d.Work.Phase = w.Phase
		}
		d.Work.TaskURL, d.Work.PRURL = diagnosticReference(cfg, w.TaskURL), diagnosticReference(cfg, w.PRURL)
		if qaHeadSHA.MatchString(w.HeadSHA) {
			d.Work.HeadSHA = w.HeadSHA
		}
		model := func(m *ModelIdentity) *c.DiagnosticModel {
			if m == nil {
				return nil
			}
			out := &c.DiagnosticModel{Provider: m.ProviderID, Model: m.ModelID, Variant: m.Variant}
			if !c.ValidDiagnosticModel(out) {
				return nil
			}
			return out
		}
		d.Work.SubmittedModel, d.Work.ExecutedModel = model(w.SubmittedModel), model(w.ExecutedModel)
	}
	origin, category := diagnosticError(err)
	t := c.DiagnosticTransition{At: now.UTC(), Stage: d.Work.Stage, Phase: d.Work.Phase, Reason: d.Wait.Reason, Origin: origin, Category: category}
	changed := len(b.transitions) == 0
	if !changed {
		previous := b.transitions[len(b.transitions)-1]
		previous.At = t.At
		changed = previous != t
	}
	if changed {
		b.transitions = append(b.transitions, t)
		if len(b.transitions) > c.MaxDiagnosticTransitions {
			b.transitions = slices.Clone(b.transitions[len(b.transitions)-c.MaxDiagnosticTransitions:])
		}
	}
	d.Transitions = slices.Clone(b.transitions)
	return d
}

type diagnosticSender interface {
	ReportDiagnostics(context.Context, c.WorkerDiagnostics) error
}
type diagnosticReporter struct {
	done     chan error
	next     time.Time
	disabled bool
}

// tick is called only by the event loop. Neither the sender nor its goroutine
// reads mutable worker state. Reporting can never hold the native slot.
func (r *diagnosticReporter) tick(ctx context.Context, sender diagnosticSender, report c.WorkerDiagnostics, now time.Time) {
	if r.done != nil {
		select {
		case err := <-r.done:
			r.done = nil
			if errors.Is(err, c.ErrUnsupported) {
				r.disabled = true
				slog.Warn("worker diagnostics unsupported by master")
			} else if err != nil {
				slog.Warn("worker diagnostics report failed", "category", "report_unavailable")
			}
		default:
			return
		}
	}
	if r.disabled || now.Before(r.next) || ctx.Err() != nil {
		return
	}
	r.next = now.Add(c.HeartbeatInterval)
	r.done = make(chan error, 1)
	done := r.done
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		done <- sender.ReportDiagnostics(ctx, report)
	}()
}
