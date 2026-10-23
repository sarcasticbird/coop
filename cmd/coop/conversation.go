package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

func conversationCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "conversation", Short: "Inspect, map, and recover canonical conversations"}
	cursor := &cobra.Command{Use: "cursor", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		v, err := cl.ConversationCursor(cmd.Context())
		return printMaster(cmd, v, err)
	}}
	var mapping c.MapConversation
	mapping.Source.Provider = "kata"
	mapCmd := &cobra.Command{Use: "map", Short: "Map a drained thread using the recorded native baseline", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		v, err := cl.MapConversation(cmd.Context(), mapping)
		return printMaster(cmd, v, err)
	}}
	mapCmd.Flags().StringVar(&mapping.ThreadID, "thread", "", "existing private thread anchor")
	mapCmd.Flags().StringVar(&mapping.Source.InstanceUID, "instance", "", "pinned native instance UID")
	mapCmd.Flags().StringVar(&mapping.Source.ProjectUID, "project", "", "pinned native project UID")
	mapCmd.Flags().StringVar(&mapping.Source.IssueUID, "issue", "", "canonical native issue UID")
	mapCmd.Flags().Int64Var(&mapping.AfterEventID, "after-event", 0, "recorded native event baseline")
	mapCmd.Flags().StringVar(&mapping.RequestID, "request-id", "", "stable mapping request ID")
	for _, flag := range []string{"thread", "instance", "project", "issue", "after-event", "request-id"} {
		_ = mapCmd.MarkFlagRequired(flag)
	}
	var input string
	reaccept := &cobra.Command{Use: "reaccept", Short: "Reconcile an exact observed cursor and immutable event UID while paused", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		raw, err := privateBody(input)
		if err != nil {
			return err
		}
		var in c.ReacceptConversationCursor
		if err = c.DecodeJSON([]byte(raw), &in); err != nil {
			return err
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		v, err := cl.ReacceptConversationCursor(cmd.Context(), in)
		return printMaster(cmd, v, err)
	}}
	reaccept.Flags().StringVar(&input, "input", "", "private JSON with observed cursor, reason, request_id; legacy anchor repair also requires independently verified reconciled_event_uid")
	_ = reaccept.MarkFlagRequired("input")
	var after int64
	legacy := &cobra.Command{Use: "legacy THREAD", Short: "Read local history without provider access or new deliveries", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		v, err := cl.LegacyMessages(cmd.Context(), args[0], after)
		return printMaster(cmd, v, err)
	}}
	legacy.Flags().Int64Var(&after, "after", 0, "last local message sequence")
	cmd.AddCommand(cursor, mapCmd, reaccept, legacy)
	return cmd
}
