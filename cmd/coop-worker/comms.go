package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/worker"
)

func commsCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "reply" && args[0] != "resolve" && args[0] != "reconcile" && args[0] != "adopt-placement" {
		return errors.New("expected comms reply, resolve, reconcile or adopt-placement")
	}
	flags := flag.NewFlagSet("comms reply", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private v3 worker configuration")
	delivery := flags.String("delivery", "", "addressed master delivery ID")
	bodyFile := flags.String("body-file", "", "answer text file")
	session := flags.String("session-id", "", "exact saved session ID")
	message := flags.String("message-id", "", "exact saved native message ID")
	previous := flags.String("previous-config", "", "previous private placement configuration")
	apply := flags.Bool("apply", false, "accept the explicitly resolved restore epoch")
	action := flags.String("action", "observe", "observe or cancel a stopped worker's saved turn")
	if flags.Parse(args[1:]) != nil || *config == "" || (*delivery == "" && args[0] != "reconcile" && args[0] != "adopt-placement") || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*config)
	if err != nil {
		return err
	}
	if cfg.Version != 3 || cfg.Master == nil {
		return c.ErrInvalid
	}
	if args[0] == "adopt-placement" {
		if *previous == "" || *apply {
			return c.ErrInvalid
		}
		old, e := worker.LoadConfig(*previous)
		if e != nil {
			return e
		}
		cl, e := masterclient.LoadConfig(cfg.Master.ClientConfig)
		if e != nil {
			return e
		}
		proof, e := worker.AdoptMasterPlacement(context.Background(), old, cfg, cl)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(proof)
	}
	if *previous != "" {
		return c.ErrInvalid
	}
	if args[0] == "reconcile" {
		cl, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
		if err != nil {
			return err
		}
		code, err := worker.NewOpenCodeClient(cfg.OpenCode, nil)
		if err != nil {
			return err
		}
		status, err := worker.ReconcileMaster(context.Background(), cfg, cl, code, *apply)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(status)
	}
	if *apply {
		return c.ErrInvalid
	}
	if args[0] == "resolve" {
		if *bodyFile != "" {
			return c.ErrInvalid
		}
		cl, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
		if err != nil {
			return err
		}
		code, err := worker.NewOpenCodeClient(cfg.OpenCode, nil)
		if err != nil {
			return err
		}
		turn, err := worker.ResolveMasterConversation(context.Background(), cfg, cl, code, *delivery, *session, *message, *action)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(turn)
	}
	if *bodyFile == "" || *session != "" || *message != "" || *action != "observe" {
		return c.ErrInvalid
	}
	f, err := os.Open(*bodyFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, c.MaxText+1))
	if err != nil {
		return err
	}
	if len(body) > c.MaxText {
		return c.ErrSize
	}
	if err = worker.QueueMasterReply(cfg, *delivery, string(body)); err != nil {
		return err
	}
	cl, err := masterclient.LoadConfig(cfg.Master.ClientConfig)
	if err == nil {
		var comms *worker.MasterComms
		comms, err = worker.NewMasterComms(cfg, cl, nil, worker.RealClock{})
		if err == nil {
			err = comms.FlushOutbox(context.Background())
		}
	}
	phase := "published"
	reason := ""
	if err != nil {
		phase = "queued"
		reason = "publication_pending"
	}
	return json.NewEncoder(out).Encode(map[string]string{"delivery_id": *delivery, "phase": phase, "reason": reason})
}
