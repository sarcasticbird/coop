package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/mastermcp"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"github.com/spf13/cobra"
)

func masterClient(cmd *cobra.Command) (*masterclient.Client, error) {
	path, _ := cmd.Flags().GetString("master-config")
	if path == "" {
		path = defaultMasterClientPath()
	}
	return masterclient.LoadConfig(path)
}
func printMaster(cmd *cobra.Command, value any, err error) error {
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
}
func requestID(cmd *cobra.Command) string {
	id, _ := cmd.Flags().GetString("request-id")
	if id == "" {
		id = rand.Text()
	}
	return id
}
func privateBody(path string) (string, error) {
	if path == "" {
		return "", c.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, c.MaxText+1))
	if err != nil {
		return "", err
	}
	if len(b) > c.MaxText {
		return "", c.ErrSize
	}
	return string(b), nil
}
func installMasterCommands(root *cobra.Command) {
	root.PersistentFlags().String("master-config", "", "private master client configuration file")
	masterCmd := &cobra.Command{Use: "master", Short: "Run the Coop master coordination service"}
	var path string
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, Short: "Serve the master API and MCP using private configuration", RunE: func(cmd *cobra.Command, _ []string) (resultErr error) {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		cfg, err := master.LoadConfig(path)
		if err != nil {
			return err
		}
		store, err := master.OpenStore(cmd.Context(), master.StoreConfig{Path: cfg.Database, MasterID: cfg.MasterID, EpochPath: cfg.EpochPath})
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
		if err = store.Configure(cmd.Context(), cfg.Principals, cfg.Placements); err != nil {
			return err
		}
		if err = store.ConfigureGitHubCache(cmd.Context(), cfg.GitHubCache); err != nil {
			return err
		}
		privateConfig, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		service := &master.Service{Store: store, BackupConfig: privateConfig}
		if cfg.Tasks != nil {
			service.Tasks, err = taskstore.New(*cfg.Tasks)
			if err != nil {
				return err
			}
		}
		service.MCPHandler = func(p c.Principal) http.Handler {
			return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mastermcp.NewServer(service, p) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: c.MaxJSON, DisableLocalhostProtection: true})
		}
		server := &http.Server{Addr: cfg.Listen, Handler: master.NewHandler(service, &cfg.Policy), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
		stop := context.AfterFunc(ctx, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		})
		defer stop()
		if cfg.TLSCertFile != "" {
			err = server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = server.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}}
	serve.Flags().StringVar(&path, "config", "", "owner-only master service config")
	_ = serve.MarkFlagRequired("config")
	installMasterRecovery(masterCmd)
	masterCmd.AddCommand(serve)
	root.AddCommand(masterCmd)
	root.AddCommand(threadCommand(), messageCommand(), teamCommand(), inboxCommand(), goalCommand(), cacheCommand())
}
