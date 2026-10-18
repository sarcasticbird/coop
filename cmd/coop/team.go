package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

func teamCommand() *cobra.Command {
	return &cobra.Command{Use: "team", Short: "Read master agent presence and placements", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.Team(cmd.Context())
		return printMaster(cmd, v, e)
	}}
}
func inboxCommand() *cobra.Command {
	var after string
	cmd := &cobra.Command{Use: "inbox", Short: "Read this agent's pending master deliveries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.Pending(cmd.Context(), c.PendingQuery{After: after})
		return printMaster(cmd, v, e)
	}}
	cmd.Flags().StringVar(&after, "after", "", "delivery page cursor")
	return cmd
}
