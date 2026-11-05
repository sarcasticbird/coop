package worker

import (
	"context"
	"errors"
	"time"
)

// Fresh scope validation reads a coherent goal, task dependencies and PR.
const githubRevalidationTimeout = time.Minute

type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }
func (RealClock) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Engine runs only the authenticated native worker. Legacy state remains
// readable, but cannot select work, submit prompts or publish effects.
type Engine struct {
	MasterRuntime *MasterRuntime
	Config        Config
	GitHub        GitHub
	Store         Store
	Checkouts     Checkouts
	OpenCode      OpenCode
	Clock         Clock
}

var errLegacyRuntime = errors.New("legacy worker execution retired; native v3 configuration required")

func (e *Engine) Tick(ctx context.Context) error {
	if e.Config.Version != 3 {
		return errLegacyRuntime
	}
	return e.tickMaster(ctx)
}

func (e *Engine) Run(ctx context.Context) error {
	if e.Config.Version != 3 {
		return errLegacyRuntime
	}
	return e.runMaster(ctx)
}

func (e *Engine) admissionPaused() (bool, error) {
	if control, ok := e.Store.(interface{ AdmissionPaused() (bool, error) }); ok {
		return control.AdmissionPaused()
	}
	return false, nil
}

func (e *Engine) paused() (bool, error) {
	if control, ok := e.Store.(interface{ Paused() (bool, error) }); ok {
		return control.Paused()
	}
	return false, nil
}

func errorCategory(err error) string {
	var size *PromptSizeError
	if errors.As(err, &size) {
		return "prompt_size"
	}
	var gh *GitHubError
	if errors.As(err, &gh) {
		return gh.Category
	}
	var code *OpenCodeError
	if errors.As(err, &code) {
		return code.Category
	}
	return "execution"
}
