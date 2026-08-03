// coop — sandboxed sessions for coding agents, native to Apple Silicon.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	buildinfo "runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/core"
	"github.com/sarcasticbird/coop/internal/doctor"
	"github.com/sarcasticbird/coop/internal/releasetool"
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
	runSession = func(s *session.Session, cwd string, argv, credentials []string) error {
		return s.Run(cwd, argv, credentials)
	}
	runTUI     = tui.Run
	buildImage = func(args []string, stdout, stderr io.Writer) error {
		build := exec.Command("container", args...)
		build.Stdout, build.Stderr = stdout, stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("run container image build: %w", err)
		}
		return nil
	}
	stdinIsTerminal = func() bool { return isatty.IsTerminal(os.Stdin.Fd()) }
	stopBuilder     = func() error {
		// Bounded: a wedged container service must not hang the rebuild
		// after the build itself has already finished.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "container", "builder", "stop").CombinedOutput(); err != nil {
			return fmt.Errorf("container builder stop: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	resolveReleaseTools = func(ctx context.Context, specs []config.GitHubReleaseTool) ([]config.ResolvedReleaseTool, error) {
		return (releasetool.Resolver{}).Resolve(ctx, specs)
	}
	upgradeCore = func(ctx context.Context, stdout, stderr io.Writer) (core.UpgradeResult, error) {
		stateDir, err := core.StateDir()
		if err != nil {
			return core.UpgradeResult{}, err
		}
		return (core.Upgrader{StateDir: stateDir}).Upgrade(ctx, stdout, stderr)
	}
	saveReleaseToolLock             = releasetool.SaveLock
	pruneReleaseToolCache           = releasetool.PruneCache
	warningOutput         io.Writer = os.Stderr
)

func main() { os.Exit(execute(root())) }

func current() (*session.Session, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	s, err := session.New(newRuntime(), cwd)
	if err != nil {
		return nil, "", err
	}
	writeConfigWarnings(s.Cfg, warningOutput)
	return s, cwd, nil
}

func root() *cobra.Command {
	var requestedCredentials []string
	rootCmd := &cobra.Command{
		Use:   "coop [agent [args...]]",
		Short: "Sandboxed sessions for coding agents, native to Apple Silicon",
		Long: `coop runs each project's coding-agent sessions inside its own
lightweight Linux VM (Apple container). The project is mounted at its
identical host path, agent configs are seeded in declaratively, and local project
settings can add tools through .coop.toml or an optional project flox environment.`,
		Args:          cobra.ArbitraryArgs,
		Version:       resolvedVersion(),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// note: flag parsing stops at the first positional (see
			// SetInterspersed below) so agent flags forward verbatim:
			// `coop claude --help` is claude's help, not coop's
			credentials, err := requestedCredentialNames(cmd, requestedCredentials)
			if err != nil {
				return err
			}
			s, cwd, err := current()
			if err != nil {
				return err
			}
			if err := maybeOfferFirstBuild(cmd, s); err != nil {
				return err
			}
			return runSession(s, cwd, args, credentials)
		},
	}

	rootCmd.PersistentFlags().StringSliceVar(
		&requestedCredentials,
		"credentials",
		nil,
		"include trusted credential grants for this entry (comma-separated, repeatable)",
	)
	// Everything after the agent token belongs to the agent.
	rootCmd.PersistentFlags().SetInterspersed(false)
	rootCmd.Flags().SetInterspersed(false)
	rejectCredentials := func(cmd *cobra.Command) error {
		if cmd.Flags().Changed("credentials") {
			return errors.New("--credentials is only valid when entering a coop or using tui")
		}
		return nil
	}

	rootCmd.AddCommand(
		&cobra.Command{Use: "up", Args: cobra.NoArgs, Short: "Start the project's coop without entering",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				s, _, err := current()
				if err != nil {
					return err
				}
				if err := maybeOfferFirstBuild(cmd, s); err != nil {
					return err
				}
				return s.Up()
			}},
		&cobra.Command{Use: "down", Args: cobra.NoArgs, Short: "Stop the coop (state volumes persist)",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				s, _, err := current()
				if err != nil {
					return err
				}
				return s.Down()
			}},
		&cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Show this project's coop",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				s, _, err := current()
				if err != nil {
					return err
				}
				status, err := s.ImageStatus()
				if err != nil {
					// runtime unavailability is an ERROR, not "stopped"
					return fmt.Errorf("runtime unavailable: %w", err)
				}
				runningImage := status.RunningImage
				if runningImage == "" {
					runningImage = "(none)"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(),
					"project:            %s\ncontainer:          %s\nstate:              %s\nrelease tools:      %s\nrunning image:      %s\ndesired image:      %s\nrebuild required:   %s\nrecreation pending: %s\n",
					s.Project, s.Name, status.State,
					formatReleaseTools(s.Cfg.Tools.GitHubReleases, s.Cfg.Tools.ResolvedReleases),
					runningImage, status.DesiredImage,
					yesNo(status.RebuildRequired), yesNo(status.RecreationPending)); err != nil {
					return fmt.Errorf("write status: %w", err)
				}
				return nil
			}},
		&cobra.Command{Use: "ls", Args: cobra.NoArgs, Short: "List all coops",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
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
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				cfg, err := config.Load("")
				if err != nil {
					return err
				}
				if err := releasetool.HydrateConfig(&cfg); err != nil {
					return fmt.Errorf("load GitHub release tool state: %w", err)
				}
				coreLock, warning, err := core.Active()
				if err != nil {
					return fmt.Errorf("load core upgrade state: %w", err)
				}
				if warning != "" {
					cfg.Warnings = append(cfg.Warnings, warning)
				}
				writeConfigWarnings(cfg, cmd.ErrOrStderr())
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve home dir: %w", err)
				}
				fmt.Printf("configuration: global\n\n")
				failed := false
				for _, c := range doctor.Run(newRuntime(), cfg, home, lookPath, coreLock) {
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
			RunE: func(cmd *cobra.Command, _ []string) error {
				credentials, err := requestedCredentialNames(cmd, requestedCredentials)
				if err != nil {
					return err
				}
				rt := newRuntime()
				res, err := runTUI(rt)
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
					writeConfigWarnings(s.Cfg, warningOutput)
					if err := maybeOfferFirstBuild(cmd, s); err != nil {
						return err
					}
					return runSession(s, res.EnterWorkdir, nil, credentials)
				}
				return nil
			}},
		&cobra.Command{Use: "rebuild", Args: cobra.NoArgs, Short: "Build the sandbox image from the embedded definition",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				s, _, err := current()
				if err != nil {
					return err
				}
				_, err = runRebuild(cmd.Context(), s, cmd.OutOrStdout(), cmd.ErrOrStderr())
				return err
			}},
		&cobra.Command{Use: "upgrade", Args: cobra.NoArgs, Short: "Upgrade Coop's machine-wide locked core",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
				result, err := upgradeCore(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				if err != nil {
					return fmt.Errorf("upgrade core: %w", err)
				}
				if !result.Changed {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Core packages are already up to date."); err != nil {
						return fmt.Errorf("write upgrade summary: %w", err)
					}
					return nil
				}
				if len(result.Changes) == 0 {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Core package revisions changed."); err != nil {
						return fmt.Errorf("write upgrade summary: %w", err)
					}
				} else {
					for _, change := range result.Changes {
						if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s\n", change.Name, change.From, change.To); err != nil {
							return fmt.Errorf("write upgrade summary: %w", err)
						}
					}
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Core lock upgraded."); err != nil {
					return fmt.Errorf("write upgrade summary: %w", err)
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(),
					"Existing coops are stale; run `coop rebuild` in each project when ready."); err != nil {
					return fmt.Errorf("write upgrade summary: %w", err)
				}
				return nil
			}},
		&cobra.Command{Use: "destroy", Args: cobra.NoArgs, Short: "Remove the coop AND its state volumes",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := rejectCredentials(cmd); err != nil {
					return err
				}
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

// builderContainerName is the container Apple's `container build` runs
// builds in; `container builder` subcommands manage it by this fixed name.
const builderContainerName = "buildkit"

const firstBuildPrompt = "No sandbox image for this project yet — the first build takes a few minutes. Build it now? [Y/n] "

const firstBuildNextSteps = `Sandbox image built. Helpful commands:
  coop            open a shell in this project's coop
  coop claude     run Claude Code in the coop
  coop status     show container and image state
  coop tui        fleet dashboard for every coop
  coop doctor     check the host configuration
`

// maybeOfferFirstBuild turns the missing-image hard error into an
// interactive offer when a human is at the terminal. Non-interactive
// callers keep the hard error from the session layer: no hangs, no
// surprise multi-minute builds.
func maybeOfferFirstBuild(cmd *cobra.Command, s *session.Session) error {
	if !stdinIsTerminal() {
		return nil
	}
	status, err := s.ImageStatus()
	if err != nil {
		// Advisory preflight only — session reconciliation reports the
		// real failure with full context if the runtime is broken.
		return nil
	}
	if !status.RebuildRequired {
		return nil
	}
	// Offer only the true first-time experience: no container exists at
	// all. An existing container keeps working without its image tag,
	// and a stale one (pending recreation after tool or core changes)
	// keeps the explicit `coop rebuild` migration workflow — recreation
	// discards root-filesystem changes and deserves a deliberate step.
	if status.State != runtime.StateAbsent {
		return nil
	}
	// The prompt goes to stderr so it stays visible when stdout is
	// redirected; the entry command's own output still owns stdout.
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), firstBuildPrompt); err != nil {
		return fmt.Errorf("write first-build prompt: %w", err)
	}
	line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		// A broken input stream is not consent to a multi-minute
		// build, even if partial input arrived before it failed.
		return fmt.Errorf("read first-build prompt response: %w", readErr)
	}
	if readErr != nil && answer == "" {
		// EOF (Ctrl-D) with no input is a decline, not the default
		// yes; keep the explicit-rebuild error.
		return s.EnsureImage()
	}
	if answer != "" && answer != "y" {
		return s.EnsureImage()
	}
	resolved, err := runRebuild(cmd.Context(), s, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	// The freshly built image embeds the just-resolved release tools;
	// adopt them so this session's desired image is the one just built.
	s.Cfg.Tools.ResolvedReleases = resolved
	if _, err := fmt.Fprint(cmd.OutOrStdout(), firstBuildNextSteps); err != nil {
		return fmt.Errorf("write first-build next steps: %w", err)
	}
	return nil
}

func runRebuild(ctx context.Context, s *session.Session, stdout, stderr io.Writer) ([]config.ResolvedReleaseTool, error) {
	resolved, err := resolveReleaseTools(ctx, s.Cfg.Tools.GitHubReleases)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub release tools: %w", err)
	}
	buildCfg := s.Cfg
	buildCfg.Tools.ResolvedReleases = resolved
	buildDir, err := image.MaterializeWithCoreLock(buildCfg.Tools.Packages, resolved, s.CoreLock)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(buildDir) }()
	desiredImage := session.EffectiveImageNameWithCoreLock(buildCfg, s.CoreLock)
	if _, err := fmt.Fprintf(stdout,
		"core tools:     %d packages\nglobal tools:   %s\nproject tools:  %s\nrelease tools:  %s\nimage:          %s\n",
		len(image.CorePackages()), formatToolList(s.Cfg.Tools.GlobalPackages),
		formatToolList(s.Cfg.Tools.ProjectPackages),
		formatReleaseTools(s.Cfg.Tools.GitHubReleases, resolved), desiredImage); err != nil {
		return nil, fmt.Errorf("write rebuild summary: %w", err)
	}
	args := []string{"build",
		"-t", desiredImage,
		"--build-arg", "GUEST_HOME=" + s.HostHome}
	args = append(args, buildDir)
	// The build starts Apple's buildkit builder VM and leaves it running.
	// Stop it afterward only if this build is what started it — a builder
	// that was already running may be serving someone else's build. When
	// its prior state cannot be determined, leave it alone. This is
	// best-effort: Apple's builder has no ownership API, so a build racing
	// between the state check and the stop can still be affected.
	builderState, builderStateErr := s.RT.State(builderContainerName)
	buildErr := buildImage(args, stdout, stderr)
	if builderStateErr != nil {
		_, _ = fmt.Fprintf(stderr, "coop: warning: builder state unknown, leaving builder VM running: %v\n", builderStateErr)
	} else if builderState != runtime.StateRunning {
		if err := stopBuilder(); err != nil {
			_, _ = fmt.Fprintf(stderr, "coop: warning: stop builder VM: %v\n", err)
		}
	}
	if buildErr != nil {
		return nil, fmt.Errorf("build image %q: %w", desiredImage, buildErr)
	}
	stateDir, err := releasetool.StateDir()
	if err != nil {
		return nil, err
	}
	if err := saveReleaseToolLock(stateDir, s.Project, s.Cfg.Tools.GitHubReleases, resolved); err != nil {
		return nil, fmt.Errorf("save GitHub release tool state: %w", err)
	}
	if err := pruneReleaseToolCache(stateDir, resolved); err != nil {
		_, _ = fmt.Fprintf(stderr, "coop: warning: prune GitHub release tool cache: %v\n", err)
	}
	return resolved, nil
}

func formatToolList(packages []string) string {
	if len(packages) == 0 {
		return "(none)"
	}
	return strings.Join(packages, ", ")
}

func formatReleaseTools(specs []config.GitHubReleaseTool, resolved []config.ResolvedReleaseTool) string {
	if len(specs) == 0 {
		return "(none)"
	}
	resolvedTags := make(map[string]string, len(resolved))
	for _, tool := range resolved {
		resolvedTags[tool.Name] = tool.Tag
	}
	tools := make([]string, 0, len(specs))
	for _, spec := range specs {
		tag := resolvedTags[spec.Name]
		if tag == "" {
			tag = "unresolved"
		}
		tools = append(tools, spec.Name+"@"+tag)
	}
	slices.Sort(tools)
	return strings.Join(tools, ", ")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func writeConfigWarnings(cfg config.Config, output io.Writer) {
	for _, warning := range cfg.Warnings {
		// Deprecation warnings are advisory and must not block commands when
		// stderr is intentionally closed or its consumer exits early.
		_, _ = fmt.Fprintf(output, "coop: warning: %s\n", warning)
	}
}

func requestedCredentialNames(cmd *cobra.Command, values []string) ([]string, error) {
	if cmd.Flags().Changed("credentials") && len(values) == 0 {
		return nil, errors.New("--credentials contains an empty grant name")
	}
	return normalizeCredentialNames(values)
}

func normalizeCredentialNames(values []string) ([]string, error) {
	normalized := make([]string, len(values))
	for i, value := range values {
		normalized[i] = strings.TrimSpace(value)
		if normalized[i] == "" {
			return nil, errors.New("--credentials contains an empty grant name")
		}
	}
	return normalized, nil
}
