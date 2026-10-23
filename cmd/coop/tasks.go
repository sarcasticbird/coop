package main

import (
	"io"
	"os"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

// taskCommand uses master authorization; task text and Kata labels never grant
// engineering or goal-approval rights.
func taskCommand() *cobra.Command {
	group := &cobra.Command{Use: "task", Short: "Manage work through the master's task provider", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(taskUICommand())
	group.AddCommand(&cobra.Command{Use: "capabilities", Short: "Read the configured task provider identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskCapabilities(cmd.Context())
		return printMaster(cmd, v, e)
	}})
	group.AddCommand(&cobra.Command{Use: "read REFERENCE", Short: "Read one scoped task and its immutable assignment", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskRead(cmd.Context(), args[0])
		return printMaster(cmd, v, e)
	}})
	var goalID, revision string
	list := &cobra.Command{Use: "list", Short: "List tasks for an exact goal revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if goalID == "" || revision == "" {
			return c.ErrInvalid
		}
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskList(cmd.Context(), c.TaskListInput{Goal: c.GoalRef{Source: "master", MasterID: cl.MasterID(), GoalID: goalID, RevisionSHA256: revision}})
		return printMaster(cmd, v, e)
	}}
	list.Flags().StringVar(&goalID, "goal", "", "goal ID")
	list.Flags().StringVar(&revision, "revision", "", "exact approved goal revision SHA256")
	group.AddCommand(list)
	var file string
	create := &cobra.Command{Use: "create", Short: "Plan a task inside an existing approved goal", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if file == "" {
			return c.ErrInvalid
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
		var in c.TaskCreateInput
		if e = c.DecodeJSON(raw, &in); e != nil {
			return e
		}
		if in.RequestID == "" || in.ExpectedApprovalSequence < 1 {
			return c.ErrInvalid
		}
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskCreate(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	create.Flags().StringVar(&file, "file", "", "task JSON including exact goal, observed approval sequence and stable request_id")
	group.AddCommand(create)

	var adoptFile string
	adopt := &cobra.Command{Use: "adopt", Short: "Adopt an existing Kata issue into an exact approved goal", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if adoptFile == "" {
			return c.ErrInvalid
		}
		f, e := os.Open(adoptFile)
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
		var in c.TaskAdoptInput
		if e = c.DecodeJSON(raw, &in); e != nil {
			return e
		}
		if in.RequestID == "" || in.ExpectedRevision < 1 || in.ExpectedApprovalSequence < 1 {
			return c.ErrInvalid
		}
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskAdopt(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	adopt.Flags().StringVar(&adoptFile, "file", "", "adoption JSON with existing reference, exact goal, observed provider revision and approval sequence, and stable request_id")
	group.AddCommand(adopt)
	return group
}
