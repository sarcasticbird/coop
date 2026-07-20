// coop — sandboxed sessions for coding agents, native to Apple Silicon.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	buildinfo "runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/doctor"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/tui"
)

var version = "dev"

func resolvedVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := buildinfo.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		return version
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified {
		revision += "+dirty"
	}
	return revision
}

var (
	newRuntime = func() runtime.Runtime { return runtime.NewApple() }
	lookPath   = exec.LookPath
)

func main() {
	if err := root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coop:", err)
		os.Exit(1)
	}
}

func current() (*session.Session, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	s, err := session.New(newRuntime(), cwd)
	return s, cwd, err
}

func root() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "coop [agent [args...]]",
		Short: "Sandboxed sessions for coding agents, native to Apple Silicon",
		Long: `coop runs each project's coding-agent sessions inside its own
lightweight Linux VM (Apple container). The project is mounted at its
identical host path, agent configs are seeded in declaratively, and
project toolchains come from the project's own flox manifest.`,
		Args:          cobra.ArbitraryArgs,
		Version:       resolvedVersion(),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, args []string) error {
			// note: flag parsing stops at the first positional (see
			// SetInterspersed below) so agent flags forward verbatim:
			// `coop claude --help` is claude's help, not coop's
			s, cwd, err := current()
			if err != nil {
				return err
			}
			if err := s.Up(); err != nil {
				return err
			}
			return s.Enter(cwd, args)
		},
	}

	// Everything after the agent token belongs to the agent.
	rootCmd.Flags().SetInterspersed(false)

	rootCmd.AddCommand(
		&cobra.Command{Use: "up", Args: cobra.NoArgs, Short: "Start the project's coop without entering",
			RunE: func(_ *cobra.Command, _ []string) error {
				s, _, err := current()
				if err != nil {
					return err
				}
				return s.Up()
			}},
		&cobra.Command{Use: "down", Args: cobra.NoArgs, Short: "Stop the coop (state volumes persist)",
			RunE: func(_ *cobra.Command, _ []string) error {
				s, _, err := current()
				if err != nil {
					return err
				}
				return s.Down()
			}},
		&cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Show this project's coop",
			RunE: func(_ *cobra.Command, _ []string) error {
				s, _, err := current()
				if err != nil {
					return err
				}
				state, err := s.RT.State(s.Name)
				if err != nil {
					// runtime unavailability is an ERROR, not "stopped"
					return fmt.Errorf("runtime unavailable: %w", err)
				}
				fmt.Printf("project:   %s\ncontainer: %s\nstate:     %s\n",
					s.Project, s.Name, state)
				return nil
			}},
		&cobra.Command{Use: "ls", Args: cobra.NoArgs, Short: "List all coops",
			RunE: func(_ *cobra.Command, _ []string) error {
				out, err := newRuntime().List()
				if err != nil {
					return err
				}
				for i, line := range strings.Split(out, "\n") {
					if i == 0 || strings.Contains(line, "coop-") {
						fmt.Println(line)
					}
				}
				return nil
			}},
		&cobra.Command{Use: "doctor", Args: cobra.NoArgs, Short: "Diagnose the host environment",
			RunE: func(_ *cobra.Command, _ []string) error {
				cfg, err := config.Load("")
				if err != nil {
					return err
				}
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve home dir: %w", err)
				}
				fmt.Printf("configuration: global\n\n")
				failed := false
				for _, c := range doctor.Run(newRuntime(), cfg, home, lookPath) {
					mark := map[doctor.Status]string{
						doctor.OK: "ok  ", doctor.Warn: "warn", doctor.Fail: "FAIL",
					}[c.Status]
					fmt.Printf("[%s] %-20s %s\n", mark, c.Name, c.Detail)
					failed = failed || c.Status == doctor.Fail
				}
				if failed {
					return fmt.Errorf("doctor found failures")
				}
				return nil
			}},
		&cobra.Command{Use: "tui", Args: cobra.NoArgs, Short: "Fleet dashboard — every coop on this machine",
			RunE: func(_ *cobra.Command, _ []string) error {
				rt := newRuntime()
				res, err := tui.Run(rt)
				if err != nil {
					return err
				}
				if res.EnterWorkdir != "" {
					// Use the normal session path so seeds, spec
					// reconciliation, and Flox wrapping stay consistent.
					s, err := session.New(rt, res.EnterWorkdir)
					if err != nil {
						return err
					}
					if err := s.Up(); err != nil {
						return err
					}
					return s.Enter(res.EnterWorkdir, nil)
				}
				return nil
			}},
		&cobra.Command{Use: "rebuild", Args: cobra.NoArgs, Short: "Build the sandbox image from the embedded definition",
			RunE: func(_ *cobra.Command, _ []string) error {
				s, _, err := current()
				if err != nil {
					return err
				}
				ctx, err := image.Materialize()
				if err != nil {
					return err
				}
				defer func() { _ = os.RemoveAll(ctx) }()
				args := []string{"build",
					"-t", session.EffectiveImageName(s.Cfg.Image),
					"--build-arg", "GUEST_HOME=" + s.HostHome}
				if pkgs := s.Cfg.Image.ExtraPackages; len(pkgs) > 0 {
					attrs := make([]string, len(pkgs))
					for i, p := range pkgs {
						if strings.Contains(p, "#") {
							attrs[i] = p
						} else {
							attrs[i] = image.NixpkgsRef + "#" + p
						}
					}
					args = append(args, "--build-arg", "EXTRA_PKGS="+strings.Join(attrs, " "))
				}
				args = append(args, ctx)
				build := exec.Command("container", args...)
				build.Stdout, build.Stderr = os.Stdout, os.Stderr
				return build.Run()
			}},
		&cobra.Command{Use: "destroy", Args: cobra.NoArgs, Short: "Remove the coop AND its state volumes",
			RunE: func(_ *cobra.Command, _ []string) error {
				s, _, err := current()
				if err != nil {
					return err
				}
				fmt.Printf("remove %s and its state volumes? [y/N] ", s.Name)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != "y" {
					return fmt.Errorf("aborted")
				}
				return s.Destroy()
			}},
	)
	return rootCmd
}
