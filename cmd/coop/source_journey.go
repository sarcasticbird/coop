package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
	"io"
	"os"
)

func addSourceJourneyCommands(group *cobra.Command) {
	var file string
	decision := &cobra.Command{Use: "source-decision", Short: "Record exact owner external intake, target transition, execution or withdrawal", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if file == "" {
			return c.ErrInvalid
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		raw, err := io.ReadAll(io.LimitReader(f, c.MaxJSON+1))
		if err != nil {
			return err
		}
		if len(raw) > c.MaxJSON {
			return c.ErrSize
		}
		var in c.SourceDecisionInput
		if err = c.DecodeJSON(raw, &in); err != nil {
			return err
		}
		if err = c.ValidateSourceDecision(in); err != nil {
			return err
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		out, err := cl.CreateSourceDecision(cmd.Context(), in)
		return printMaster(cmd, out, err)
	}}
	decision.Flags().StringVar(&file, "file", "", "private exact owner decision JSON")
	var goal, revision string
	var after int64
	list := &cobra.Command{Use: "source-decisions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if goal == "" || revision == "" {
			return c.ErrInvalid
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		out, err := cl.SourceDecisions(cmd.Context(), c.SourceDecisionQuery{Goal: c.GoalRef{Source: "master", MasterID: cl.MasterID(), GoalID: goal, RevisionSHA256: revision}, After: after})
		return printMaster(cmd, out, err)
	}}
	list.Flags().StringVar(&goal, "goal", "", "exact goal ID")
	list.Flags().StringVar(&revision, "revision", "", "exact revision")
	list.Flags().Int64Var(&after, "after", 0, "sequence cursor")
	group.AddCommand(decision, list)
}
