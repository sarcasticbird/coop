package main

import (
	"io"
	"os"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

func goalCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "goal", Short: "Propose master goals and record exact owner decisions"}
	var file string
	propose := &cobra.Command{Use: "propose THREAD", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		f, e := os.Open(file)
		if e != nil {
			return e
		}
		defer func() { _ = f.Close() }()
		raw, e := io.ReadAll(io.LimitReader(f, c.MaxJSON+1))
		if e != nil {
			return e
		}
		if len(raw) > c.MaxJSON {
			return c.ErrSize
		}
		var in c.GoalProposal
		if err := c.DecodeJSON(raw, &in); err != nil {
			return err
		}
		in.ThreadID = args[0]
		in.RequestID = requestID(cmd)
		v, e := cl.ProposeGoal(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	propose.Flags().StringVar(&file, "file", "", "proposal JSON file")
	propose.Flags().String("request-id", "", "retry idempotency key")
	cmd.AddCommand(propose)
	var engagement, after string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List a bounded page of active approved goals", RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.ApprovedGoals(cmd.Context(), engagement, after)
		return printMaster(cmd, v, e)
	}}
	list.Flags().StringVar(&engagement, "engagement", "", "authorized engagement ID")
	list.Flags().StringVar(&after, "after", "", "next page cursor")
	var inspectRevision string
	inspect := &cobra.Command{Use: "authority GOAL", Args: cobra.ExactArgs(1), Short: "Read exact revision authority and current decision sequence", RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.ResolveGoal(cmd.Context(), c.GoalRef{Source: "master", MasterID: cl.MasterID(), GoalID: args[0], RevisionSHA256: inspectRevision})
		return printMaster(cmd, v, e)
	}}
	inspect.Flags().StringVar(&inspectRevision, "revision", "", "exact revision SHA256")
	_ = inspect.MarkFlagRequired("revision")
	cmd.AddCommand(list, inspect)
	for _, action := range []string{"approve", "reject", "pause", "resume", "complete", "reopen"} {
		var revision string
		var seq int64
		decision := &cobra.Command{Use: action + " GOAL", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			cl, e := masterClient(cmd)
			if e != nil {
				return e
			}
			v, e := cl.DecideGoal(cmd.Context(), c.GoalDecisionInput{GoalID: args[0], RevisionSHA256: revision, ExpectedDecisionSequence: seq, Action: action, RequestID: requestID(cmd)})
			return printMaster(cmd, v, e)
		}}
		decision.Flags().StringVar(&revision, "revision", "", "exact revision SHA256")
		decision.Flags().Int64Var(&seq, "expected-decision-seq", 0, "observed goal decision sequence")
		decision.Flags().String("request-id", "", "retry idempotency key")
		_ = decision.MarkFlagRequired("revision")
		_ = decision.MarkFlagRequired("expected-decision-seq")
		cmd.AddCommand(decision)
	}
	return cmd
}
