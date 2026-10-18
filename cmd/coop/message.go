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
	cmd.AddCommand(send, status)
	return cmd
}
