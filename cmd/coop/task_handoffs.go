package main

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
	"io"
	"os"
)

func addTaskHandoffCommands(group *cobra.Command) {
	for _, spec := range []struct{ name, kind string }{{"first-postmerge-qa", "first-postmerge-qa"}, {"reassess-qa", "reassess-qa"}, {"request-release", "release"}} {
		var file string
		command := &cobra.Command{Use: spec.name, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
			var in c.HandoffInput
			if err = c.DecodeJSON(raw, &in); err != nil {
				return err
			}
			if in.Kind != "" && in.Kind != spec.kind {
				return c.ErrInvalid
			}
			in.Kind = spec.kind
			if err = c.ValidateHandoff(in); err != nil {
				return err
			}
			cl, err := masterClient(cmd)
			if err != nil {
				return err
			}
			out, err := cl.CreateHandoff(cmd.Context(), in)
			return printMaster(cmd, out, err)
		}}
		command.Flags().StringVar(&file, "file", "", "private exact-artifact handoff JSON")
		group.AddCommand(command)
	}
	var goal, revision, after string
	list := &cobra.Command{Use: "handoffs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if goal == "" || revision == "" {
			return c.ErrInvalid
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		out, err := cl.Handoffs(cmd.Context(), c.HandoffQuery{Goal: c.GoalRef{Source: "master", MasterID: cl.MasterID(), GoalID: goal, RevisionSHA256: revision}, After: after})
		return printMaster(cmd, out, err)
	}}
	list.Flags().StringVar(&goal, "goal", "", "exact approved goal ID")
	list.Flags().StringVar(&revision, "revision", "", "exact goal revision")
	list.Flags().StringVar(&after, "after", "", "page cursor")
	group.AddCommand(list)
}
