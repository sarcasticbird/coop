package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/team"
	"github.com/spf13/cobra"
)

func teamCommand() *cobra.Command {
	var watch bool
	cmd := &cobra.Command{Use: "team", Short: "Read master agent presence and placements", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		if watch {
			return team.Run(cmd.Context(), cl, cl.MasterID(), cmd.InOrStdin(), cmd.OutOrStdout())
		}
		v, e := cl.Team(cmd.Context())
		return printMaster(cmd, v, e)
	}}
	cmd.Flags().BoolVar(&watch, "watch", false, "Open the read-only Team screen (r refresh, ? help, q quit)")
	return cmd
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
