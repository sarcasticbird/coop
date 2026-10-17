package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sarcasticbird/coop/internal/worker"
	"io"
	"time"

	"github.com/sarcasticbird/coop/internal/runtime"
)

func (s *Session) Worker(ctx context.Context, operation string) error {
	switch operation {
	case "status", "pause", "resume", "requests":
	default:
		return errors.New("unsupported worker control")
	}
	if _, ok := s.Spec.Profile.Services["worker"]; !ok {
		return errors.New("worker is not configured for this agent")
	}
	info, err := s.Controller.Backend.Inspect(ctx, s.Spec)
	if err != nil {
		return err
	}
	if info.State != runtime.StateRunning {
		return errors.New("agent is stopped; worker controls never start a VM")
	}
	args := []string{"/usr/local/libexec/coop-worker", operation, "--config", "/var/lib/coop/worker/config.json"}
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", args, nil, s.output(), io.Discard); err != nil {
		return errors.New("agent worker is unavailable; check staged installation and worker status")
	}
	return nil
}

func (s *Session) Ping(ctx context.Context, input worker.PingInput) error {
	if err := worker.ValidatePingInput(input); err != nil {
		return err
	}
	if _, ok := s.Spec.Profile.Services["worker"]; !ok {
		return errors.New("worker is not configured for this agent")
	}
	info, err := s.Controller.Backend.Inspect(ctx, s.Spec)
	if err != nil {
		return err
	}
	if info.State != runtime.StateRunning {
		return errors.New("agent is stopped; ping never starts a VM")
	}
	var statusRaw bytes.Buffer
	args := []string{"/usr/local/libexec/coop-worker", "status", "--config", "/var/lib/coop/worker/config.json"}
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", args, nil, &statusRaw, io.Discard); err != nil {
		return errors.New("agent worker status unavailable")
	}
	var status worker.Status
	now := time.Now().UTC()
	if statusRaw.Len() > 64<<10 || json.Unmarshal(statusRaw.Bytes(), &status) != nil || !status.Enabled || !status.WakeEnabled || status.Heartbeat.IsZero() || now.Sub(status.Heartbeat) > 2*time.Minute || status.Heartbeat.After(now.Add(time.Minute)) || status.State == "stale" || status.State == "not_running" {
		return errors.New("agent worker is stopped, stale or wake is disabled")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return errors.New("invalid ping input")
	}
	args[1] = "ping"
	if err := s.Runner.Exec(ctx, s.Spec.RuntimeName, "/", args, bytes.NewReader(raw), s.output(), io.Discard); err != nil {
		return errors.New("ping unavailable; check worker status")
	}
	return nil
}
