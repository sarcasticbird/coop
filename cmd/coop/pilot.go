package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/pilot"
	"github.com/spf13/cobra"
)

func pilotCommand() *cobra.Command {
	var config string
	group := &cobra.Command{Use: "pilot", Short: "Experimental disposable Kata/AgentsView orchestration fixture", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.PersistentFlags().StringVar(&config, "pilot-config", "", "private fixture-only pilot configuration")
	run := &cobra.Command{Use: "run", Short: "Dispatch one approved disposable audit", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, e := pilot.Load(config)
		if e != nil {
			return e
		}
		path, _ := cmd.Flags().GetString("request-file")
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 64<<10))
		d.DisallowUnknownFields()
		var r pilot.Request
		if e = d.Decode(&r); e != nil {
			return e
		}
		var extra any
		if e = d.Decode(&extra); e != io.EOF {
			return errors.New("request must contain exactly one JSON value")
		}
		out, e := p.Run(cmd.Context(), r)
		return printMaster(cmd, out, e)
	}}
	run.Flags().String("request-file", "", "JSON fixture request with exact approved goal and stable request ID")
	status := &cobra.Command{Use: "status REQUEST_ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, e := pilot.Load(config)
		if e != nil {
			return e
		}
		v, e := p.Status(args[0])
		return printMaster(cmd, v, e)
	}}
	server := &cobra.Command{Use: "mcp", Short: "Serve the fixture pilot tools over private stdio", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, e := pilot.Load(config)
		if e != nil {
			return e
		}
		return pilot.NewServer(p).Run(cmd.Context(), &mcp.StdioTransport{})
	}}
	group.AddCommand(run, status, server)
	return group
}
