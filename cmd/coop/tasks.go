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
	addTaskHandoffCommands(group)
	group.AddCommand(&cobra.Command{Use: "capabilities", Short: "Read the configured task provider identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskCapabilities(cmd.Context())
		return printMaster(cmd, v, e)
	}})
	group.AddCommand(&cobra.Command{Use: "read REFERENCE", Short: "Read a Kata issue UID or assigned canonical task reference", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskRead(cmd.Context(), args[0])
		return printMaster(cmd, v, e)
	}})
	var goalID, revision string
	var status, after string
	list := &cobra.Command{Use: "list", Short: "List team Kata tasks, optionally for an exact goal revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if (goalID == "") != (revision == "") {
			return c.ErrInvalid
		}
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		in := c.TaskListInput{Status: status, After: after}
		if goalID != "" {
			in.Goal = c.GoalRef{Source: "master", MasterID: cl.MasterID(), GoalID: goalID, RevisionSHA256: revision}
		}
		v, e := cl.TaskList(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	list.Flags().StringVar(&goalID, "goal", "", "goal ID")
	list.Flags().StringVar(&revision, "revision", "", "exact approved goal revision SHA256")
	list.Flags().StringVar(&status, "status", "", "open or closed; empty lists both")
	list.Flags().StringVar(&after, "after", "", "next cursor from the preceding task page")
	group.AddCommand(list)
	var file string
	create := &cobra.Command{Use: "create", Short: "Create a team Kata task without a routine approval handoff", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		if in.RequestID == "" || in.Goal != (c.GoalRef{}) && in.ExpectedApprovalSequence < 1 {
			return c.ErrInvalid
		}
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.TaskCreate(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	create.Flags().StringVar(&file, "file", "", "task JSON: title, body, optional owner and stable request_id; goal fields are only for engineering registration")
	group.AddCommand(create)
	for _, operation := range []string{"edit", "assign", "claim", "close", "reopen", "comment"} {
		var updateFile string
		command := &cobra.Command{Use: operation, Short: operation + " a team Kata task under your own identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if updateFile == "" {
				return c.ErrInvalid
			}
			f, err := os.Open(updateFile)
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
			var in c.TaskUpdateInput
			if err = c.DecodeJSON(raw, &in); err != nil {
				return err
			}
			if in.Operation != "" && in.Operation != operation {
				return c.ErrInvalid
			}
			in.Operation = operation
			if err = in.Validate(); err != nil {
				return err
			}
			cl, err := masterClient(cmd)
			if err != nil {
				return err
			}
			v, err := cl.TaskUpdate(cmd.Context(), in)
			return printMaster(cmd, v, err)
		}}
		command.Flags().StringVar(&updateFile, "file", "", "task JSON: reference, request_id, observed expected_revision (except comments), and operation fields")
		group.AddCommand(command)
	}

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
