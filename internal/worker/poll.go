package worker

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

var errPollPending = errors.New("GitHub snapshot pending")

type snapshotPoll struct {
	snapshot       Snapshot
	health         ServerHealth
	err, healthErr error
	started        time.Time
}

func (e *Engine) refresh(ctx context.Context) error {
	if e.poll == nil && e.Clock.Now().Before(e.apiNotBefore) {
		return errPollPending
	}
	if e.poll == nil && e.Clock.Now().Before(e.nextPoll) {
		return errPollPending
	}
	if !e.asynchronous {
		e.lastSnapshotStarted = e.Clock.Now()
		return e.refreshSynchronous(ctx)
	}
	if e.poll != nil {
		select {
		case result := <-e.poll:
			e.poll = nil
			if e.pollCancel != nil {
				e.pollCancel()
				e.pollCancel = nil
			}
			if errors.Is(result.err, context.Canceled) && ctx.Err() == nil {
				e.nextPoll = time.Time{}
				return errPollPending
			}
			now := e.Clock.Now()
			e.nextPoll = now.Add(e.Config.Limits.PollInterval + time.Duration(rand.Int64N(int64(e.Config.Limits.PollInterval/10)+1)))
			e.status.ServerHealthy = result.healthErr == nil && result.health.Healthy
			e.status.ServerVersion = result.health.Version
			if result.healthErr != nil {
				e.nextDispatch = now.Add(e.Config.Limits.RetryBase)
			} else {
				e.nextDispatch = time.Time{}
			}
			if result.err != nil {
				e.noteAPIError(result.err, e.nextPoll)
				var gh *GitHubError
				if errors.As(result.err, &gh) && gh.RetryAt.After(e.apiNotBefore) {
					e.apiNotBefore = gh.RetryAt
				}
				if errors.As(result.err, &gh) && gh.RetryAt.After(e.nextPoll) {
					e.nextPoll = gh.RetryAt
				}
				return result.err
			}
			if !result.snapshot.Complete {
				return ghError("incomplete_snapshot")
			}
			if e.Wakes != nil {
				if err := e.Wakes.Ingest(result.snapshot); err != nil {
					return err
				}
			}
			e.state.SnapshotCache = result.snapshot
			e.state.Cursor = result.snapshot.Cursor
			e.lastSnapshotStarted = result.started
			e.status.LastPoll = now
			e.status.LastError = ""
			if result.healthErr != nil {
				e.status.LastError = errorCategory(result.healthErr)
			}
			return e.save()
		default:
			return errPollPending
		}
	}
	queryCtx, cancel := context.WithCancel(ctx)
	e.pollCancel = cancel
	ch := make(chan snapshotPoll, 1)
	e.poll = ch
	previous, cfg, github, code, started := e.state.SnapshotCache, e.Config, e.GitHub, e.OpenCode, e.Clock.Now()
	go func() {
		health, healthErr := code.Health(queryCtx)
		snapshot, err := github.Snapshot(queryCtx, cfg, previous)
		ch <- snapshotPoll{snapshot: snapshot, health: health, healthErr: healthErr, err: err, started: started}
	}()
	return errPollPending
}

func (e *Engine) holdBusy(j Job) error {
	j.Phase, j.WaitReason = "waiting", "session_busy"
	j.MessageID, j.SubmissionState = "", ""
	j.Deadline, j.SubmittedAt, j.TerminalAt = time.Time{}, time.Time{}, time.Time{}
	j.NextAttemptAt = e.Clock.Now().Add(e.Config.OpenCode.ReconcileInterval)
	return e.saveJob(j)
}
func snapshotArtifacts(c Config, j Job, s Snapshot) Artifacts {
	a := Artifacts{PlanResponded: planningResponded(c, j, s)}
	if t, ok := taskFor(s, j.TaskURL); ok {
		a.Task = &t
		a.Claim = t.Claim
		a.IssueCompleted = t.Completed
	}
	for _, p := range s.PRs {
		match := p.URL == j.PRURL
		if j.PRURL == "" {
			match = p.TaskURL == j.TaskURL && !p.Closed && (j.Branch == "" || p.Branch == j.Branch)
		}
		if match {
			copy := p
			a.PR = &copy
			break
		}
	}
	return a
}
func (e *Engine) reconcile(ctx context.Context, j Job) (Artifacts, error) {
	if e.asynchronous {
		return snapshotArtifacts(e.Config, j, e.state.SnapshotCache), nil
	}
	return e.GitHub.Reconcile(ctx, j)
}
func (e *Engine) reportBlock(ctx context.Context, j Job, category string) error {
	if e.poll != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := e.GitHub.ReportBlock(ctx, j, category, ""); err != nil {
		return err
	}
	if saved, ok := e.state.Jobs[j.ID]; ok {
		saved.BlockReported = true
		return e.saveJob(saved)
	}
	return nil
}
