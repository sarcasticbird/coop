package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/worker"
)

// tasksCommand exposes the master's planning API with the worker's existing
// credentials. The server remains responsible for current goal and role grants.
func tasksCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return c.ErrInvalid
	}
	switch args[0] {
	case "capabilities", "read", "list", "create", "adopt":
	default:
		return c.ErrInvalid
	}
	flags := flag.NewFlagSet("tasks "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private v3 worker configuration")
	input := flags.String("input", "", "private structured JSON request file")
	if flags.Parse(args[1:]) != nil || *config == "" || len(flags.Args()) != 0 || args[0] == "capabilities" && *input != "" || args[0] != "capabilities" && *input == "" {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cl, err := worker.TaskPlanningClient(ctx, cfg)
	if err != nil {
		return err
	}
	var raw []byte
	if *input != "" {
		raw, err = credential.ReadPrivateFile(*input)
		if err != nil {
			return err
		}
		if len(raw) > c.MaxJSON {
			return c.ErrSize
		}
	}
	decode := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
			return c.ErrInvalid
		}
		return nil
	}
	var result any
	switch args[0] {
	case "capabilities":
		result, err = cl.TaskCapabilities(ctx)
	case "read":
		var in c.TaskReadInput
		if err = decode(&in); err == nil {
			result, err = cl.TaskRead(ctx, in.Reference)
		}
	case "list":
		var in c.TaskListInput
		if err = decode(&in); err == nil {
			result, err = cl.TaskList(ctx, in)
		}
	case "adopt":
		var in c.TaskAdoptInput
		if err = decode(&in); err == nil {
			if in.RequestID == "" || in.ExpectedApprovalSequence < 1 || in.ExpectedRevision < 1 {
				return c.ErrInvalid
			}
			result, err = cl.TaskAdopt(ctx, in)
		}
	case "create":
		var in c.TaskCreateInput
		if err = decode(&in); err == nil {
			if in.RequestID == "" || in.ExpectedApprovalSequence < 1 {
				return c.ErrInvalid
			}
			result, err = cl.TaskCreate(ctx, in)
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
