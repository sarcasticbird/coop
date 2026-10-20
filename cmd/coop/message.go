package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

func messageCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "message", Short: "Send master messages and inspect durable receipts"}
	var to []string
	var parent, bodyfile string
	send := &cobra.Command{Use: "send THREAD", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		body, e := privateBody(bodyfile)
		if e != nil {
			return e
		}
		v, e := cl.Send(cmd.Context(), c.SendMessage{ThreadID: args[0], Recipients: to, ParentMessageID: parent, Body: body, RequestID: requestID(cmd)})
		return printMaster(cmd, v, e)
	}}
	send.Flags().StringSliceVar(&to, "to", nil, "recipient IDs or aliases")
	send.Flags().StringVar(&parent, "reply-to", "", "parent message ID")
	send.Flags().StringVar(&bodyfile, "body-file", "", "message text file")
	send.Flags().String("request-id", "", "retry idempotency key")
	status := &cobra.Command{Use: "status MESSAGE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.MessageStatus(cmd.Context(), args[0])
		return printMaster(cmd, v, e)
	}}
	cmd.AddCommand(send, status, messageContinueCommand())
	return cmd
}

func messageContinueCommand() *cobra.Command {
	var targetFile, to, source, bodyFile string
	cmd := &cobra.Command{Use: "continue THREAD", Short: "Request continuation of an exact approved task; inspect its durable receipt", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		raw, err := privateBody(targetFile)
		if err != nil {
			return err
		}
		var target c.TaskContinuationTarget
		if err = c.DecodeJSON([]byte(raw), &target); err != nil {
			return err
		}
		body, err := privateBody(bodyFile)
		if err != nil {
			return err
		}
		v, err := cl.ContinueTask(cmd.Context(), c.ContinueTaskRequest{ThreadID: args[0], AgentID: to, Target: target, SourceMessageID: source, Body: body, RequestID: requestID(cmd)})
		return printMaster(cmd, v, err)
	}}
	cmd.Flags().StringVar(&to, "to", "", "exact recipient agent ID or alias")
	cmd.Flags().StringVar(&targetFile, "target-file", "", "JSON continuation_target from fresh scoped diagnostics")
	cmd.Flags().StringVar(&source, "source-message", "", "existing same-thread handoff message ID")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "private reason text file")
	cmd.Flags().String("request-id", "", "stable retry idempotency key")
	for _, name := range []string{"to", "target-file", "source-message", "body-file"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
