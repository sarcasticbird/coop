package main

import "github.com/spf13/cobra"

func cacheCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "cache", Short: "Inspect master GitHub observations"}
	var engagement string
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Read cache freshness without contacting GitHub or waking agents", RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := masterClient(cmd)
		if err != nil {
			return err
		}
		out, err := client.GitHubCacheStatus(cmd.Context(), engagement)
		return printMaster(cmd, out, err)
	}}
	status.Flags().StringVar(&engagement, "engagement", "", "engagement to inspect")
	_ = status.MarkFlagRequired("engagement")
	cmd.AddCommand(status)
	return cmd
}
