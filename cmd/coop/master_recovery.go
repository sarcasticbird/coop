package main

import (
	"os"
	"path/filepath"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/spf13/cobra"
)

func defaultMasterClientPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "coop", "master-client.toml")
}
func installMasterRecovery(masterCmd *cobra.Command) {
	var destination string
	backup := &cobra.Command{Use: "backup", Short: "Create an owner-only consistent snapshot on the master host", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.Backup(cmd.Context(), destination)
		return printMaster(cmd, v, e)
	}}
	backup.Flags().StringVar(&destination, "directory", "", "empty owner-only directory on the master host")
	_ = backup.MarkFlagRequired("directory")
	var source, target, lineage string
	var dry bool
	restore := &cobra.Command{Use: "restore", Short: "Restore offline to an empty directory and reserve a new epoch", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		v, e := master.Restore(cmd.Context(), source, target, lineage, dry)
		return printMaster(cmd, v, e)
	}}
	restore.Flags().StringVar(&source, "backup", "", "private backup directory")
	restore.Flags().StringVar(&target, "directory", "", "empty restore destination")
	restore.Flags().StringVar(&lineage, "epoch-path", "", "existing independent epoch lineage file")
	restore.Flags().BoolVar(&dry, "dry-run", false, "validate and print the proposed restore epoch")
	for _, flag := range []string{"backup", "directory", "epoch-path"} {
		_ = restore.MarkFlagRequired(flag)
	}
	reconcile := &cobra.Command{Use: "reconcile", Short: "Review and resolve guest evidence before explicit resume"}
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.RecoveryStatus(cmd.Context())
		return printMaster(cmd, v, e)
	}}
	for _, op := range []string{"pause", "resume"} {
		var epoch int64
		command := &cobra.Command{Use: op, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			cl, e := masterClient(cmd)
			if e != nil {
				return e
			}
			e = cl.RecoveryControl(cmd.Context(), op, epoch)
			return printMaster(cmd, map[string]bool{"ok": e == nil}, e)
		}}
		command.Flags().Int64Var(&epoch, "epoch", 0, "exact current restore epoch")
		_ = command.MarkFlagRequired("epoch")
		reconcile.AddCommand(command)
	}
	var agent, after string
	evidence := &cobra.Command{Use: "evidence", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.RecoveryEvidence(cmd.Context(), agent, after)
		return printMaster(cmd, v, e)
	}}
	evidence.Flags().StringVar(&agent, "agent", "", "stable agent ID")
	evidence.Flags().StringVar(&after, "after", "", "last evidence ID from the previous page")
	_ = evidence.MarkFlagRequired("agent")
	var resolution c.RecoveryResolution
	resolve := &cobra.Command{Use: "resolve", Short: "Retain evidence and retire its restored delivery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		e = cl.ResolveRecovery(cmd.Context(), resolution)
		return printMaster(cmd, map[string]bool{"ok": e == nil}, e)
	}}
	resolve.Flags().Int64Var(&resolution.RestoreEpoch, "epoch", 0, "current restore epoch")
	resolve.Flags().StringVar(&resolution.AgentID, "agent", "", "stable agent ID")
	resolve.Flags().StringVar(&resolution.EvidenceID, "evidence", "", "immutable evidence ID")
	resolve.Flags().StringVar(&resolution.ProofID, "proof", "", "matching terminal guest evidence ID for uncertain work")
	resolve.Flags().StringVar(&resolution.Reason, "reason", "", "operator explanation")
	for _, flag := range []string{"epoch", "agent", "evidence", "reason"} {
		_ = resolve.MarkFlagRequired(flag)
	}
	reconcile.AddCommand(status, evidence, resolve)
	placement := &cobra.Command{Use: "placement"}
	var change c.PlacementChange
	set := &cobra.Command{Use: "set", Short: "Move a reconciled stopped placement to the next generation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.SetPlacement(cmd.Context(), change)
		return printMaster(cmd, v, e)
	}}
	set.Flags().StringVar(&change.AgentID, "agent", "", "stable agent ID")
	set.Flags().StringVar(&change.HostID, "host", "", "new host ID")
	set.Flags().StringVar(&change.InstanceID, "instance", "", "new worker instance ID")
	set.Flags().Int64Var(&change.Generation, "generation", 0, "new placement generation")
	set.Flags().Int64Var(&change.ExpectedGeneration, "expected-generation", 0, "current placement generation")
	set.Flags().Int64Var(&change.RestoreEpoch, "epoch", 0, "current restore epoch")
	set.Flags().BoolVar(&change.DryRun, "dry-run", false, "validate and print the proposed placement")
	for _, flag := range []string{"agent", "host", "instance", "generation", "expected-generation", "epoch"} {
		_ = set.MarkFlagRequired(flag)
	}
	placement.AddCommand(set)
	history := &cobra.Command{Use: "history"}
	var in c.HistoryImport
	var bodyFile string
	imp := &cobra.Command{Use: "import", Short: "Import read-only history without messages, decisions or wakes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, e := privateBody(bodyFile)
		if e != nil {
			return e
		}
		in.Body = body
		in.RequestID = requestID(cmd)
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.ImportHistory(cmd.Context(), in)
		return printMaster(cmd, v, e)
	}}
	imp.Flags().StringVar(&in.ThreadID, "thread", "", "master thread ID")
	imp.Flags().StringVar(&in.SourceURL, "source-url", "", "original HTTPS source")
	imp.Flags().StringVar(&in.Cutoff, "cutoff", "", "RFC3339 history cutoff")
	imp.Flags().StringVar(&bodyFile, "body-file", "", "history text file")
	imp.Flags().String("request-id", "", "stable import request ID")
	imp.Flags().BoolVar(&in.DryRun, "dry-run", false, "validate and print import digest")
	for _, flag := range []string{"thread", "source-url", "cutoff", "body-file", "request-id"} {
		_ = imp.MarkFlagRequired(flag)
	}
	var histThread, histAfter string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.History(cmd.Context(), histThread, histAfter)
		return printMaster(cmd, v, e)
	}}
	list.Flags().StringVar(&histThread, "thread", "", "master thread ID")
	list.Flags().StringVar(&histAfter, "after", "", "last history digest")
	_ = list.MarkFlagRequired("thread")
	history.AddCommand(imp, list)
	var cut c.SourceCutover
	cutover := &cobra.Command{Use: "cutover", Short: "Record the owner's one-time source retirement for a thread", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cl, e := masterClient(cmd)
		if e != nil {
			return e
		}
		v, e := cl.Cutover(cmd.Context(), cut)
		return printMaster(cmd, v, e)
	}}
	cutover.Flags().StringVar(&cut.ThreadID, "thread", "", "thread ID")
	cutover.Flags().StringVar(&cut.SourceURL, "source-url", "", "retired source URL")
	cutover.Flags().StringVar(&cut.HistorySHA256, "history-sha256", "", "reviewed import digest")
	cutover.Flags().BoolVar(&cut.DryRun, "dry-run", false, "validate and print the cutover")
	for _, flag := range []string{"thread", "source-url", "history-sha256"} {
		_ = cutover.MarkFlagRequired(flag)
	}
	masterCmd.AddCommand(conversationCommand(), backup, restore, reconcile, placement, history, cutover)
}
