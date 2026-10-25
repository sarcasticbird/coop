package main

import (
	"context"
	"encoding/json"
	"flag"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/worker"
	"io"
	"time"
)

func nativeReleaseCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "private v3 worker config")
	goal := flags.String("goal", "", "exact source goal ID")
	revision := flags.String("revision", "", "exact source goal revision")
	handoff := flags.String("handoff", "", "owner release request ID")
	observe := flags.Bool("observe", false, "read only an acknowledged tag/publication for recovery")
	if flags.Parse(args) != nil || *path == "" || *goal == "" || *revision == "" || *handoff == "" || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*path)
	if err != nil {
		return err
	}
	if cfg.Version != 3 || cfg.Master == nil {
		return c.ErrInvalid
	}
	master, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ref := c.GoalRef{Source: "master", MasterID: cfg.Master.MasterID, GoalID: *goal, RevisionSHA256: *revision}
	var receipt worker.NativeReleaseResult
	if *observe {
		receipt, err = worker.ObserveNativeRelease(ctx, cfg, master, worker.NewGitHubClient(worker.ExecRunner{}), ref, *handoff)
	} else {
		receipt, err = worker.RunNativeRelease(ctx, cfg, master, worker.NewGitHubClient(worker.ExecRunner{}), ref, *handoff)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
}
