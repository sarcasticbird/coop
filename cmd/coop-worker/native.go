package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/worker"
)

func nativeCommand(args []string, out io.Writer) error {
	if len(args) > 0 && (args[0] == "preflight" || args[0] == "dry-run") {
		return nativePreview(args[1:], out)
	}
	if len(args) > 0 && args[0] == "resolve" {
		return nativeResolve(args[1:], out)
	}
	if len(args) == 0 || args[0] != "action" && args[0] != "recover-delivery" && args[0] != "retain-action" {
		return c.ErrInvalid
	}
	recoverDelivery := args[0] == "recover-delivery"
	flags := flag.NewFlagSet("native "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private v3 worker configuration")
	job := flags.String("job", "", "exact saved engineering job")
	input := flags.String("input", "", "private native action JSON file")
	var session, message string
	if recoverDelivery {
		flags.StringVar(&session, "session", "", "exact saved native session")
		flags.StringVar(&message, "message", "", "exact saved native message")
	}
	if flags.Parse(args[1:]) != nil || *config == "" || *job == "" || *input == "" || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*config)
	if err != nil {
		return err
	}
	if cfg.Version != 3 || cfg.Master == nil {
		return c.ErrInvalid
	}
	action, err := worker.LoadNativeAction(*input)
	if err != nil {
		return err
	}
	if args[0] == "retain-action" {
		if err = worker.RetainNativeAction(cfg, *job, action); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]bool{"retained": true})
	}
	master, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var result worker.NativeActionResult
	if recoverDelivery {
		code, e := worker.NewOpenCodeClient(cfg.OpenCode, nil)
		if e != nil {
			return e
		}
		result, err = worker.RecoverNativeDelivery(ctx, cfg, master, worker.NewGitHubClient(worker.ExecRunner{}), code, *job, session, message, action)
	} else {
		result, err = worker.RunNativeAction(ctx, cfg, master, worker.NewGitHubClient(worker.ExecRunner{}), *job, action)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func nativePreview(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "private v3 worker configuration")
	if flags.Parse(args) != nil || *path == "" || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*path)
	if err != nil {
		return err
	}
	if cfg.Version != 3 || cfg.Master == nil {
		return c.ErrInvalid
	}
	if _, err = worker.NewVersionedFileStore(cfg.StateDir, 3).Load(); err != nil {
		return err
	}
	master, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err != nil {
		return err
	}
	code, err := worker.NewOpenCodeClient(cfg.OpenCode, nil)
	if err != nil {
		return err
	}
	github := worker.NewGitHubClient(worker.ExecRunner{})
	dispatcher, err := worker.NewNativeDispatcher(cfg, master, github, code, worker.NewCheckouts(cfg, worker.ExecRunner{}), worker.RealClock{})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	preview, err := dispatcher.Preview(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(preview)
}

func nativeResolve(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native resolve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private v3 worker configuration")
	job := flags.String("job", "", "exact saved engineering job")
	session := flags.String("session", "", "exact saved native session")
	message := flags.String("message", "", "exact saved native message")
	action := flags.String("action", "observe", "observe, cancel or continue after native terminal evidence")
	if flags.Parse(args) != nil || *config == "" || *job == "" || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*config)
	if err != nil {
		return err
	}
	code, err := worker.NewOpenCodeClient(cfg.OpenCode, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var result worker.NativeExecution
	if *action == "continue" {
		if cfg.Master == nil {
			return c.ErrInvalid
		}
		master, e := masterclient.LoadConfig(cfg.Master.ClientConfig)
		if e != nil {
			return e
		}
		result, err = worker.ContinueNativeEngineering(ctx, cfg, master, worker.NewGitHubClient(worker.ExecRunner{}), code, *job, *session, *message)
	} else {
		result, err = worker.ResolveNativeEngineering(ctx, cfg, code, *job, *session, *message, *action)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
