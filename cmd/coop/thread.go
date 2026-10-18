package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
	"time"
)

func threadCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "thread", Short: "Read and create master conversation threads"}
	var after string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.Threads(cmd.Context(), c.ThreadQuery{After: after})
		return printMaster(cmd, v, e)
	}}
	list.Flags().StringVar(&after, "after", "", "continuation from the previous thread page")
	var title, engagement string
	var members []string
	create := &cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.CreateThread(cmd.Context(), c.CreateThread{Title: title, Engagement: engagement, Members: members, RequestID: requestID(cmd)})
		return printMaster(cmd, v, e)
	}}
	create.Flags().StringVar(&title, "title", "", "thread title")
	create.Flags().StringVar(&engagement, "engagement", "", "authorized engagement ID")
	create.Flags().StringSliceVar(&members, "member", nil, "stable member IDs")
	create.Flags().String("request-id", "", "retry idempotency key")
	var follow bool
	read := &cobra.Command{Use: "read THREAD", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		after := int64(0)
		for {
			v, e := cl.Messages(cmd.Context(), args[0], after)
			if e != nil {
				return e
			}
			if !follow {
				return printMaster(cmd, v, nil)
			}
			for _, m := range v.Messages {
				if e = printMaster(cmd, m, nil); e != nil {
					return e
				}
				after = m.Sequence
			}
			if v.Next != 0 {
				after = v.Next
				continue
			}
			select {
			case <-cmd.Context().Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
		}
	}}
	read.Flags().BoolVar(&follow, "follow", false, "follow committed master messages")
	var nextMembers, expectedMembers []string
	set := &cobra.Command{Use: "members THREAD", Args: cobra.ExactArgs(1), Short: "Owner replaces membership with an exact prior member set", RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.SetMembers(cmd.Context(), c.SetMembers{ThreadID: args[0], Members: nextMembers, ExpectedMembers: expectedMembers, RequestID: requestID(cmd)})
		return printMaster(cmd, v, e)
	}}
	set.Flags().StringSliceVar(&nextMembers, "member", nil, "complete new stable member IDs")
	set.Flags().StringSliceVar(&expectedMembers, "expected-member", nil, "complete observed current member IDs")
	set.Flags().String("request-id", "", "retry idempotency key")
	_ = set.MarkFlagRequired("member")
	_ = set.MarkFlagRequired("expected-member")
	cmd.AddCommand(list, create, read, set)
	return cmd
}
