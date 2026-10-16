package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"github.com/sarcasticbird/coop/internal/hostmcp"
	"github.com/spf13/cobra"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

func mcpCommand() *cobra.Command {
	var path string
	cmd := &cobra.Command{Use: "mcp", Short: "Serve host-local MCP control or connect through stdio", PersistentPreRunE: func(c *cobra.Command, _ []string) error {
		if c.Root().PersistentFlags().Changed("agent") || c.Root().PersistentFlags().Changed("credentials") {
			return fmt.Errorf("MCP commands do not accept --agent or --credentials")
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("--config requires an absolute private config path")
		}
		return nil
	}}
	cmd.PersistentFlags().StringVar(&path, "config", "", "absolute private MCP configuration file")
	cmd.AddCommand(&cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return serveMCP(c.Context(), path) }})
	cmd.AddCommand(&cobra.Command{Use: "stdio", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := hostmcp.LoadClientConfig(path)
		if err != nil {
			return err
		}
		return hostmcp.RunStdio(c.Context(), cfg, c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr())
	}})
	var id string
	inspect := &cobra.Command{Use: "inspect", Args: cobra.NoArgs, Short: "Read safe operation metadata locally as the host owner", RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := hostcontrol.LoadConfig(path)
		if err != nil {
			return err
		}
		r, err := hostcontrol.InspectOperation(cfg, id)
		if err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"host_id": r.HostID, "operation_id": r.ID, "caller": r.CallerID, "workspace": r.Input.Workspace, "action": r.Input.Action, "state": r.State, "phase": r.Phase, "outcome": r.Outcome, "wake_id": r.WakeID, "wake_phase": r.WakePhase})
	}}
	inspect.Flags().StringVar(&id, "operation", "", "operation ID")
	cmd.AddCommand(inspect)
	var projectRoot string
	register := &cobra.Command{Use: "register", Args: cobra.NoArgs, Short: "Register an existing interactive Coop locally without changing its runtime", RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := hostcontrol.LoadConfig(path)
		if err != nil {
			return err
		}
		control, err := hostcontrol.NewControl(cfg)
		if err != nil {
			return err
		}
		workspace, err := control.RegisterInteractive(c.Context(), projectRoot)
		if err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(workspace)
	}}
	register.Flags().StringVar(&projectRoot, "project", "", "absolute canonical host project root")
	cmd.AddCommand(register)
	return cmd
}
func sameServerConfig(a, b hostcontrol.Config) bool {
	a.Callers = nil
	b.Callers = nil
	a.VerifierFile = ""
	b.VerifierFile = ""
	return reflect.DeepEqual(a, b)
}
func serveMCP(parent context.Context, path string) error {
	cfg, err := hostcontrol.LoadConfig(path)
	if err != nil {
		return err
	}
	host, closeHost, err := hostcontrol.OpenHost(cfg.StateDir, cfg.Label)
	if err != nil {
		return err
	}
	defer func() { _ = closeHost() }()
	policy, err := hostcontrol.NewPolicy(cfg)
	if err != nil {
		return err
	}
	control, err := hostcontrol.NewControl(cfg)
	if err != nil {
		return err
	}
	control.SetVersion(resolvedVersion())
	host.Version = resolvedVersion()
	ledger, err := hostcontrol.OpenLedger(filepath.Join(cfg.StateDir, "operations"), host, cfg.Limits)
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()
	engine, err := hostcontrol.NewEngine(host, ledger, policy, control)
	if err != nil {
		return err
	}
	server, err := hostmcp.NewServer(control, engine, policy)
	if err != nil {
		return err
	}
	handler, err := hostmcp.HTTPHandler(server, cfg, policy)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	engineDone := make(chan error, 1)
	go func() { engineDone <- engine.Run(ctx) }()
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	httpDone := make(chan error, 1)
	go func() { httpDone <- httpServer.Serve(listener) }()
	slog.Info("Coop MCP listening", "host_id", host.ID, "address", listener.Addr().String())
	var serveErr error
running:
	for {
		select {
		case <-ctx.Done():
			break running
		case err := <-httpDone:
			if !errors.Is(err, http.ErrServerClosed) {
				serveErr = err
			}
			break running
		case <-hup:
			next, err := hostcontrol.LoadConfig(path)
			if err == nil && !sameServerConfig(cfg, next) {
				err = fmt.Errorf("only caller policy and verifier sources can reload")
			}
			if err == nil {
				err = policy.Reload(next)
			}
			if err != nil {
				slog.Error("Coop MCP policy reload rejected", "category", "invalid_policy")
			} else {
				slog.Info("Coop MCP policy reloaded", "host_id", host.ID)
			}
		}
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := httpServer.Shutdown(shutdown); err != nil {
		_ = httpServer.Close()
	}
	engineErr := <-engineDone
	return errors.Join(serveErr, engineErr)
}
