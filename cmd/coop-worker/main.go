package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(os.Args[1:], os.Stdout); err != nil {
		slog.Error("worker command failed", "error", err.Error())
		os.Exit(1)
	}
}

var clients = func(c worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) {
	code, err := worker.NewOpenCodeClient(c.OpenCode, nil)
	return worker.NewGitHubClient(worker.ExecRunner{}), code, err
}

func run(args []string, out io.Writer) error { return runInput(args, os.Stdin, out) }
func runInput(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected run, validate, preflight, dry-run, status, pause or resume")
	}
	command := args[0]
	switch command {
	case "run", "validate", "preflight", "dry-run", "status", "pause", "resume", "task-metadata", "ping", "requests", "recover":
	default:
		return errors.New("unsupported worker command")
	}
	flags := flag.NewFlagSet("coop-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "private worker configuration")
	task := flags.String("task", "", "task issue URL")
	goal := flags.String("goal", "", "goal URL")
	requestID := flags.String("request-id", "", "exact wake request ID")
	jobID := flags.String("job-id", "", "exact recovery intent ID")
	sessionID := flags.String("session-id", "", "exact saved session ID")
	messageID := flags.String("message-id", "", "exact saved message ID")
	if flags.Parse(args[1:]) != nil || *path == "" || len(flags.Args()) > 0 {
		return errors.New("worker command requires --config /absolute/private.json")
	}
	if command != "task-metadata" && (*task != "" || *goal != "") {
		return errors.New("task/goal flags require task-metadata")
	}
	if *requestID != "" && command != "requests" {
		return errors.New("request-id requires requests")
	}
	c, err := worker.LoadConfig(*path)
	if err != nil {
		return err
	}
	if command == "recover" {
		if err := worker.RecoveryIntent(c, *jobID, *sessionID, *messageID); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"state": "observation_requested"})
	}
	if *jobID != "" || *sessionID != "" || *messageID != "" {
		return errors.New("intent flags require recover")
	}
	store := worker.NewFileStore(c.StateDir)
	encode := func(value any) error { return json.NewEncoder(out).Encode(value) }
	switch command {
	case "ping":
		if c.Wake == nil || !c.Wake.Enabled {
			return errors.New("wake is not enabled")
		}
		input, err := worker.DecodePingInput(in)
		if err != nil {
			return err
		}
		if err := worker.WorkerAcceptingWake(c.StateDir, time.Now().UTC()); err != nil {
			return err
		}
		summary, err := worker.QueuePing(c, worker.NewWakeStore(filepath.Join(c.StateDir, "wake", "v1")), input, time.Now().UTC())
		if err != nil {
			return err
		}
		return encode(summary)
	case "requests":
		if *requestID != "" {
			if err := worker.ValidatePingInput(worker.PingInput{RequestID: *requestID}); err != nil {
				return err
			}
		}
		summaries, err := worker.WakeSummaries(worker.NewWakeStore(filepath.Join(c.StateDir, "wake", "v1")), 20, *requestID)
		if err != nil {
			return err
		}
		return encode(summaries)
	case "validate":
		return encode(map[string]any{"valid": true, "name": c.Name, "role": c.Role})
	case "status":
		status, err := worker.ReadStatus(c.StateDir, time.Now())
		if err != nil {
			return err
		}
		return encode(status)
	case "pause", "resume":
		paused := command == "pause"
		if err := store.SetPaused(paused); err != nil {
			return err
		}
		return encode(map[string]any{"paused": paused, "state": command + "_requested"})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	github, code, err := clients(c)
	if err != nil {
		return err
	}
	switch command {
	case "preflight":
		if err := code.Preflight(ctx, c); err != nil {
			return err
		}
		snapshot, err := github.Snapshot(ctx, c, worker.Snapshot{})
		if err != nil {
			return err
		}
		return encode(map[string]any{"ready": snapshot.Complete, "name": c.Name, "model": c.Model, "bot": c.Self.Login})
	case "dry-run":
		state, err := store.Load()
		if err != nil {
			return err
		}
		snapshot, err := github.Snapshot(ctx, c, state.SnapshotCache)
		if err != nil {
			return err
		}
		return encode(worker.InspectWork(c, snapshot, state, time.Now()))
	case "task-metadata":
		if *task == "" || *goal == "" || c.Role != "planner" {
			return errors.New("task-metadata requires planner role, --task and --goal")
		}
		snapshot, err := github.Snapshot(ctx, c, worker.Snapshot{})
		if err != nil {
			return err
		}
		text, err := worker.TaskMetadataComment(c, snapshot, *task, *goal, time.Now())
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, text+"\n")
		return err
	case "run":
		engine := &worker.Engine{Config: c, GitHub: github, Store: store, Checkouts: worker.NewCheckouts(c, worker.ExecRunner{}), OpenCode: code, Clock: worker.RealClock{}}
		err := engine.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return errors.New("unsupported worker command")
}
