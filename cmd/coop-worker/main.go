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
	if command == "native" {
		return nativeCommand(args[1:], out)
	}
	if command == "comms" {
		return commsCommand(args[1:], out)
	}
	if command == "qa-source-digest" {
		return qaSourceDigest(args[1:], out)
	}
	switch command {
	case "run", "validate", "check-install", "preflight", "dry-run", "status", "pause", "resume", "task-metadata", "ping", "requests", "recover", "migrate-state", "recover-master-migration", "init-state":
	default:
		return errors.New("unsupported worker command")
	}
	flags := flag.NewFlagSet("coop-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	admissionOnly := flags.Bool("admission-only", false, "pause new admissions without aborting active work")
	path := flags.String("config", "", "private worker configuration")
	previous := flags.String("previous-config", "", "installed private configuration for check-install")
	task := flags.String("task", "", "task issue URL")
	goal := flags.String("goal", "", "goal URL")
	assignment := flags.String("assignment", "", "private first v2 assignment JSON file")
	author := flags.String("author", "", "configured author for a first v2 assignment")
	var dependencies []string
	flags.Func("depends-on", "canonical prerequisite issue URL for a first author assignment; repeatable", func(value string) error {
		dependencies = append(dependencies, value)
		return nil
	})
	requestID := flags.String("request-id", "", "exact wake request ID")
	jobID := flags.String("job-id", "", "exact recovery intent ID")
	sessionID := flags.String("session-id", "", "exact saved session ID")
	messageID := flags.String("message-id", "", "exact saved message ID")
	from := flags.Int("from", 0, "source state version")
	to := flags.Int("to", 0, "target state version")
	expected := flags.String("expected-instance", "", "exact saved worker instance")
	if flags.Parse(args[1:]) != nil || *path == "" || len(flags.Args()) > 0 {
		return errors.New("worker command requires --config /absolute/private.json")
	}
	authorSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "author" {
			authorSet = true
		}
	})
	if authorSet && (command != "task-metadata" || *author == "" || *assignment != "") {
		return errors.New("author requires task-metadata, a nonempty member name and no --assignment")
	}
	if len(dependencies) > 0 && !authorSet {
		return errors.New("depends-on requires a first --author assignment")
	}
	if command != "task-metadata" && (*task != "" || *goal != "") {
		return errors.New("task/goal flags require task-metadata")
	}
	if *assignment != "" && command != "task-metadata" {
		return errors.New("assignment requires task-metadata")
	}
	if *requestID != "" && command != "requests" {
		return errors.New("request-id requires requests")
	}
	if command != "migrate-state" && command != "recover-master-migration" && (*from != 0 || *to != 0 || *expected != "") {
		return errors.New("migration flags require migrate-state")
	}
	if *previous != "" && command != "check-install" {
		return errors.New("previous-config requires check-install")
	}
	if *admissionOnly && command != "pause" && command != "resume" {
		return errors.New("admission-only requires pause or resume")
	}
	c, err := worker.LoadConfig(*path)
	if err != nil {
		return err
	}
	if c.Version == 3 {
		switch command {
		case "task-metadata", "recover", "ping", "requests":
			return errors.New("legacy command retired for v3; use native action/resolve or master comms")
		case "preflight", "dry-run":
			return nativeCommand(append([]string{command}, args[1:]...), out)
		}
	}
	if command == "recover-master-migration" {
		if *from != 0 || *to != 0 || *jobID != "" || *sessionID != "" || *messageID != "" {
			return errors.New("recovery requires only config and expected-instance")
		}
		if err := worker.RecoverMasterMigration(c, *expected); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"state": "master_migration_verified"})
	}
	if command == "init-state" {
		if *jobID != "" || *sessionID != "" || *messageID != "" {
			return errors.New("intent flags require recover")
		}
		instance, err := worker.InitializeTeamState(c)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"state": "initialized", "instance_id": instance})
	}
	if command == "migrate-state" {
		if *jobID != "" || *sessionID != "" || *messageID != "" {
			return errors.New("intent flags require recover")
		}
		if err := worker.MigrateState(c, *from, *to, *expected); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"state": "migration_verified"})
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
	if command == "check-install" {
		if err := worker.CheckInstall(c, *previous); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]bool{"compatible": true})
	}
	store := worker.NewVersionedFileStore(c.StateDir, c.Version)
	if command != "validate" && command != "status" {
		if _, err := store.Load(); err != nil {
			return err
		}
	}
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
		summary, err := worker.QueuePing(c, worker.NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), c.Version), input, time.Now().UTC())
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
		summaries, err := worker.WakeSummaries(worker.NewVersionedWakeStore(filepath.Join(c.StateDir, "wake", "v1"), c.Version), 20, *requestID)
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
		if *admissionOnly {
			if err := store.SetAdmissionPaused(paused); err != nil {
				return err
			}
			return encode(map[string]any{"admission_paused": paused, "state": command + "_requested"})
		}
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
		planner := c.Version == 1 && c.Role == "planner" || c.Version == 2 && c.Team != nil && c.Team.Planner == c.Name
		if *task == "" || *goal == "" || !planner {
			return errors.New("task-metadata requires planner role, --task and --goal")
		}
		var snapshot worker.Snapshot
		if authorSet {
			reader, ok := github.(interface {
				InitialAssignmentSnapshot(context.Context, worker.Config, string) (worker.Snapshot, error)
			})
			if !ok {
				return errors.New("initial author assignment requires native PR history reader")
			}
			snapshot, err = reader.InitialAssignmentSnapshot(ctx, c, *task)
		} else {
			snapshot, err = github.Snapshot(ctx, c, worker.Snapshot{})
		}
		if err != nil {
			return err
		}
		var text string
		if authorSet {
			text, err = worker.TaskMetadataForAuthor(c, snapshot, *task, *goal, *author, dependencies, time.Now())
		} else if *assignment != "" {
			text, err = worker.TaskMetadataWithAssignment(c, snapshot, *task, *goal, *assignment, time.Now())
		} else {
			text, err = worker.TaskMetadataComment(c, snapshot, *task, *goal, time.Now())
		}
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
