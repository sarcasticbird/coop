package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
)

func nativeAbandon(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native abandon", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private v3 worker configuration")
	var in worker.NativeAbandonmentInput
	flags.StringVar(&in.JobID, "job", "", "exact saved engineering job")
	flags.StringVar(&in.SessionID, "session", "", "exact saved native session")
	flags.StringVar(&in.MessageID, "message", "", "exact missing native message")
	flags.StringVar(&in.RequestID, "request-id", "", "stable interruption-only native fence identity")
	flags.StringVar(&in.ScopeSHA, "scope-sha", "", "exact currently inspected native scope digest")
	flags.StringVar(&in.PriorReceiptID, "prior-receipt", "", "immutable prior native interruption receipt")
	flags.StringVar(&in.Reason, "reason", "", "operator disposition; effects remain unknown")
	if flags.Parse(args) != nil || *config == "" || len(flags.Args()) != 0 {
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := worker.AbandonMissingNativeTurn(ctx, cfg, code, in)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
