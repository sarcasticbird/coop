package main

import (
	"github.com/sarcasticbird/coop/internal/birdmemory"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/spf13/cobra"
)

func memoryCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Read and save bird memory in Coop's SQLite database"}
	var agent string
	cmd.PersistentFlags().StringVar(&agent, "bird", "", "stable bird identity (defaults to the authenticated bird)")
	var after, prefix string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		value, err := cl.ListMemory(cmd.Context(), c.MemoryList{AgentID: agent, After: after, Prefix: prefix})
		return printMaster(cmd, value, err)
	}}
	list.Flags().StringVar(&after, "after", "", "continue from the previous page's next key")
	list.Flags().StringVar(&prefix, "prefix", "", "literal key prefix")
	read := &cobra.Command{Use: "read KEY", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !c.ValidMemoryKey(args[0]) {
			return c.ErrInvalid
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		value, err := cl.ReadMemory(cmd.Context(), c.MemoryQuery{AgentID: agent, Key: args[0]})
		return printMaster(cmd, value, err)
	}}
	var file, source string
	var revision int64
	write := &cobra.Command{Use: "write KEY --file PATH --revision N", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := privateBody(file)
		if err != nil {
			return err
		}
		in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{AgentID: agent, Key: args[0]}, Body: body, Source: source, ExpectedRevision: revision}
		if err = c.ValidateMemoryWrite(in); err != nil {
			return err
		}
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		value, err := cl.WriteMemory(cmd.Context(), in)
		return printMaster(cmd, value, err)
	}}
	write.Flags().StringVar(&file, "file", "", "UTF-8 memory body file")
	write.Flags().StringVar(&source, "source", "", "optional provenance label, not authority")
	write.Flags().Int64Var(&revision, "revision", 0, "expected current revision; 0 creates a new key")
	_ = write.MarkFlagRequired("file")
	_ = write.MarkFlagRequired("revision")
	importCmd := &cobra.Command{Use: "import-markdown PATH", Args: cobra.ExactArgs(1), Short: "Import a Markdown file or directory without overwriting memory or deleting sources", RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := masterClient(cmd)
		if err != nil {
			return err
		}
		value, err := birdmemory.ImportMarkdown(cmd.Context(), cl, agent, args[0])
		if err != nil {
			return err
		}
		return printMaster(cmd, value, nil)
	}}
	cmd.AddCommand(list, read, write, importCmd)
	return cmd
}
