package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	"github.com/sarcasticbird/coop/internal/birdmemory"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/worker"
)

func memoryCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return c.ErrInvalid
	}
	op := args[0]
	if op != "read" && op != "write" && op != "list" && op != "import-markdown" {
		return c.ErrInvalid
	}
	flags := flag.NewFlagSet("memory "+op, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "private worker config")
	input := flags.String("input", "", "private JSON request file for read/write/list")
	directory := flags.String("path", "", "Markdown file or directory to import")
	if flags.Parse(args[1:]) != nil || *config == "" || len(flags.Args()) != 0 {
		return c.ErrInvalid
	}
	if op == "import-markdown" {
		if *directory == "" || *input != "" {
			return c.ErrInvalid
		}
	} else if *directory != "" || op != "list" && *input == "" {
		return c.ErrInvalid
	}
	cfg, err := worker.LoadConfig(*config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cl, err := worker.MemoryClient(ctx, cfg)
	if err != nil {
		return err
	}
	decode := func(in any) error {
		raw := []byte("{}")
		if *input != "" {
			var e error
			raw, e = credential.ReadPrivateFile(*input)
			if e != nil {
				return e
			}
		}
		return c.DecodeJSON(raw, in)
	}
	var value any
	switch op {
	case "read":
		var in c.MemoryQuery
		if err = decode(&in); err == nil {
			value, err = cl.ReadMemory(ctx, in)
		}
	case "write":
		var in c.MemoryWrite
		if err = decode(&in); err == nil {
			value, err = cl.WriteMemory(ctx, in)
		}
	case "list":
		var in c.MemoryList
		if err = decode(&in); err == nil {
			value, err = cl.ListMemory(ctx, in)
		}
	case "import-markdown":
		value, err = birdmemory.ImportMarkdown(ctx, cl, "", *directory)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(value)
}
