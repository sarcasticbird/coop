package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/spf13/cobra"
)

var newInventoryRouter = func() hostrouter.Router {
	return hostrouter.Router{Local: hostrouter.LocalClient{}, Remote: hostrouter.SSH{}}
}

func installHostCommands(root *cobra.Command) {
	root.PersistentFlags().String("host", "local", "select local or a registered server (inventory only for remote hosts)")
	server := &cobra.Command{Use: "server", Short: "Manage private named SSH server references"}
	server.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectInventorySelectors(cmd); err != nil {
			return err
		}
		r, err := hostrouter.DefaultRegistry()
		if err != nil {
			return err
		}
		hosts, err := r.List()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "NAME ADDRESS"); err != nil {
			return err
		}
		for _, h := range hosts {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", h.Name, h.Address); err != nil {
				return err
			}
		}
		return nil
	}})
	for _, op := range []string{"add", "update", "remove"} {
		use, count := op+" NAME ADDRESS", 2
		if op == "remove" {
			use, count = op+" NAME", 1
		}
		server.AddCommand(&cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectInventorySelectors(cmd); err != nil {
				return err
			}
			r, err := hostrouter.DefaultRegistry()
			if err != nil {
				return err
			}
			address := ""
			if len(args) == 2 {
				address = args[1]
			}
			return r.Change(op, args[0], address)
		}})
	}
	root.AddCommand(server)
	inventory := &cobra.Command{Use: "inventory", Args: cobra.NoArgs, Short: "List local and registered-host agents and interactive Coops", RunE: func(cmd *cobra.Command, _ []string) error {
		return printInventory(cmd)
	}}
	inventory.Flags().Bool("json", false, "print a versioned inventory snapshot")
	inventory.Flags().Duration("timeout", 5*time.Second, "per-host inventory deadline (up to 1m)")
	root.AddCommand(inventory)

	// Install last, outside the --agent dispatch wrapper: a remote selection must
	// never load a local project/profile or run an unsupported local command.
	var wrap func(*cobra.Command)
	wrap = func(c *cobra.Command) {
		if c.RunE != nil {
			original := c.RunE
			c.RunE = func(cmd *cobra.Command, args []string) error {
				if !root.PersistentFlags().Changed("host") {
					return original(cmd, args)
				}
				name, _ := cmd.Flags().GetString("host")
				if name == "" {
					return errors.New("--host requires local or a registered server name")
				}
				if name == "local" {
					return original(cmd, args)
				}
				r, err := hostrouter.DefaultRegistry()
				if err != nil {
					return err
				}
				if _, err := r.Resolve(name); err != nil {
					return err
				}
				if cmd == inventory || (cmd.Parent() == root && cmd.Name() == "ls") {
					return printInventory(cmd)
				}
				return errors.New("this command does not yet support remote --host; no local action was executed")
			}
		}
		for _, child := range c.Commands() {
			wrap(child)
		}
	}
	wrap(root)
}

func rejectInventorySelectors(cmd *cobra.Command) error {
	if cmd.Root().PersistentFlags().Changed("agent") || cmd.Root().PersistentFlags().Changed("credentials") {
		return errors.New("server and inventory commands do not accept --agent or --credentials")
	}
	return nil
}

func printInventory(cmd *cobra.Command) error {
	if err := rejectInventorySelectors(cmd); err != nil {
		return err
	}
	router := newInventoryRouter()
	if cmd.Flags().Lookup("timeout") != nil {
		router.Timeout, _ = cmd.Flags().GetDuration("timeout")
		if router.Timeout <= 0 || router.Timeout > time.Minute {
			return errors.New("--timeout must be greater than zero and at most 1m")
		}
	}
	hosts := []hostrouter.Host{hostrouter.Local}
	registryError := ""
	if cmd.Root().PersistentFlags().Changed("host") {
		name, _ := cmd.Flags().GetString("host")
		if name != "local" {
			r, err := hostrouter.DefaultRegistry()
			if err != nil {
				return err
			}
			h, err := r.Resolve(name)
			if err != nil {
				return err
			}
			hosts = []hostrouter.Host{h}
		}
	} else {
		r, err := hostrouter.DefaultRegistry()
		if err != nil {
			registryError = "registry_unavailable"
		} else {
			registered, err := r.List()
			if err != nil {
				registryError = "registry_unavailable"
			} else {
				hosts = append(hosts, registered...)
			}
		}
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	snapshot := hostrouter.Snapshot{Version: 1, Hosts: []hostrouter.HostInventory{}, RegistryError: registryError}
	if !jsonOutput {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "HOST KIND ID NAME STATE"); err != nil {
			return err
		}
	}
	failed := registryError != ""
	for result := range router.Watch(cmd.Context(), hosts) {
		snapshot.Hosts = append(snapshot.Hosts, result)
		if result.Error != "" {
			failed = true
			if !jsonOutput {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s - - - %s\n", result.Host.Name, result.Error); err != nil {
					return err
				}
			}
			continue
		}
		if !jsonOutput {
			for _, entry := range result.Entries {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s %s %s\n", result.Host.Name, entry.Target.Kind, entry.Target.ID, entry.Name, entry.State); err != nil {
					return err
				}
			}
		}
	}
	if jsonOutput {
		sort.Slice(snapshot.Hosts, func(i, j int) bool { return snapshot.Hosts[i].Host.Name < snapshot.Hosts[j].Host.Name })
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot); err != nil {
			return err
		}
	}
	if registryError != "" {
		return errors.New("remote registry unavailable; local inventory retained (check private registry ownership, permissions and syntax)")
	}
	if failed {
		return errors.New("inventory incomplete; unavailable hosts are not empty or stopped")
	}
	return nil
}
