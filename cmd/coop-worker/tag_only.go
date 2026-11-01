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

func nativeObserveTagOnlyCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native observe-tag-only", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "private v3 worker config")
	goal := flags.String("goal", "", "exact source goal ID")
	revision := flags.String("revision", "", "exact source goal revision")
	handoff := flags.String("handoff", "", "exact owner tag-only handoff")
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
	m, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ref := c.GoalRef{Source: "master", MasterID: cfg.Master.MasterID, GoalID: *goal, RevisionSHA256: *revision}
	receipt, err := worker.ObserveNativeTagOnly(ctx, cfg, m, worker.NewGitHubClient(worker.ExecRunner{}), ref, *handoff)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
}
