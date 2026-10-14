package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/spf13/cobra"
)

func credentialWatchCommand() *cobra.Command {
	var profiles []string
	var interval, retry time.Duration
	cmd := &cobra.Command{Use: "watch", Short: "Renew running agents' GitHub App credentials", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := runtime.NotifyContext(cmd.Context())
		defer stop()
		logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
			ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
				if attr.Key == slog.TimeKey && attr.Value.Kind() == slog.KindTime {
					attr.Value = slog.TimeValue(attr.Value.Time().UTC())
				}
				return attr
			},
		}))
		return agent.WatchCredentials(ctx, agent.RenewConfig{Profiles: profiles, Interval: interval, Retry: retry}, func(ctx context.Context, name string) error {
			s, err := agent.New(name)
			if err == nil {
				found := false
				for _, name := range s.Spec.Config.IncludeCredentials {
					for _, expose := range config.Exposures(s.Spec.Config.Credentials[name]) {
						if expose.Type == "file" && expose.PathEnv == "COOP_GITHUB_APP_BUNDLE_FILE" {
							found = true
						}
					}
				}
				if !found {
					err = errors.New("profile has no GitHub App credential grant")
				} else {
					err = s.RefreshRunning(ctx, nil)
				}
			}
			if errors.Is(err, agent.ErrAgentNotRunning) {
				return err
			}
			if err != nil {
				logger.Error("credential refresh failed", "profile", name, "category", "refresh_failed")
				return err
			}
			logger.Info("credentials refreshed", "profile", name, "refreshed_at", time.Now().UTC().Format(time.RFC3339))
			return nil
		}, nil)
	}}
	cmd.Flags().StringSliceVar(&profiles, "profile", nil, "named profiles to renew")
	cmd.Flags().DurationVar(&interval, "interval", 15*time.Minute, "refresh interval, at most 30m")
	cmd.Flags().DurationVar(&retry, "retry", time.Minute, "retry interval after refresh failure")
	return cmd
}
