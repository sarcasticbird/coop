// Package runtime abstracts the container runtime. The interface exists
// so the core can be unit-tested without Apple's `container` (hosted CI
// has no nested virtualization).
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sarcasticbird/coop/internal/jobcontrol"
)

// RunSpec describes a session container to create.
type RunSpec struct {
	Name    string
	Image   string
	CPUs    int
	Memory  string
	SSH     bool
	Env     map[string]string
	Labels  map[string]string
	Mounts  []Mount  // bind mounts (source resolved host-side)
	Volumes []Volume // named volumes
}

type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type Volume struct {
	Name   string
	Target string
}

// State is a container's lifecycle state. Runtime errors are errors —
// never collapsed into a state — so "absent" is always a fact, not a
// guess (destroy/create decisions ride on this).
type State int

const (
	StateAbsent State = iota
	StateStopped
	StateRunning
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	default:
		return "absent"
	}
}

// ExitError reports the exact status returned by an interactive guest
// command. Runtime setup and transport failures use ordinary errors.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("guest command exited %d", e.Code) }
func (e *ExitError) ExitCode() int { return e.Code }

// SignalError reports a lifecycle signal captured so callers can unwind and
// run cleanup rather than terminating immediately.
type SignalError struct {
	Signal syscall.Signal
}

func (e *SignalError) Error() string { return fmt.Sprintf("interrupted by signal %s", e.Signal) }
func (e *SignalError) ExitCode() int { return 128 + int(e.Signal) }

// Runtime is the minimal surface coop needs from a container engine.
type Runtime interface {
	State(name string) (State, error)
	Start(name string) error
	Run(spec RunSpec) error
	Stop(name string) error
	Remove(name string) error
	List() (string, error)
	Containers() ([]ContainerInfo, error)
	EnsureVolume(name string) error
	RemoveVolume(name string) error
	ListVolumes() ([]string, error)
	// Exec runs argv inside the container's live mount namespace.
	Exec(name string, argv []string, stdin io.Reader) error
	ExecContext(context.Context, string, []string, io.Reader) error
	// ExecInteractive attaches the terminal and waits for the guest process.
	ExecInteractive(context.Context, string, string, []string) error
	// GuestFileExists reports whether path exists inside the container.
	// Failures are errors, never "absent" — seed policies make
	// overwrite decisions on this answer.
	GuestFileExists(name, path string) (bool, error)
	ImageExists(name string) (bool, error)
	// ContainerImage reports the image reference a container was
	// created from.
	ContainerImage(name string) (string, error)
	// ContainerLabel reads a label from a container ("" when unset —
	// legacy containers predate labeling).
	ContainerLabel(name, key string) (string, error)
}

// Apple shells out to Apple's `container` CLI.
//
// Known platform quirks this implementation encodes:
//   - `container cp` writes to the rootfs snapshot and BYPASSES volume
//     mounts — never use it; all writes go through `exec` stdin.
//   - single-file bind mounts are unsupported (directories only).
type Apple struct {
	Bin string // defaults to "container"
}

func NewApple() *Apple { return &Apple{Bin: "container"} }

// Every non-interactive runtime command carries a deadline so a wedged
// apiserver cannot hang coop indefinitely. Interactive exec follows the guest
// process lifetime.
const (
	opTimeout            = 60 * time.Second
	execTimeout          = 10 * time.Minute // seed tar streams
	interactiveKillGrace = 2 * time.Second
)

func (a *Apple) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", a.Bin, strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

func (a *Apple) output(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", a.Bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// State resolves via the full listing — absence is the container
// missing from a SUCCESSFUL list, never an error collapsed to false.
func (a *Apple) State(name string) (State, error) {
	out, err := a.output("list", "--all", "--format", "json")
	if err != nil {
		return StateAbsent, fmt.Errorf("container list: %w", err)
	}
	return stateFromList(name, out)
}

// stateFromList parses the listing. Schema drift is an error: silently
// ignoring malformed entries could report a live container as absent,
// and volume destruction rides on absence.
func stateFromList(name string, out []byte) (State, error) {
	var entries []struct {
		Configuration struct {
			ID string `json:"id"`
		} `json:"configuration"`
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return StateAbsent, fmt.Errorf("container list parse: %w", err)
	}
	for _, e := range entries {
		if e.Configuration.ID == "" {
			return StateAbsent, fmt.Errorf("container list: entry without id (schema drift?)")
		}
		if e.Configuration.ID != name {
			continue
		}
		switch e.Status.State {
		case "running":
			return StateRunning, nil
		case "":
			return StateAbsent, fmt.Errorf("container %s: empty state (schema drift?)", name)
		default: // stopped, stopping, created, ...
			return StateStopped, nil
		}
	}
	return StateAbsent, nil
}

func (a *Apple) Start(name string) error { return a.run("start", name) }
func (a *Apple) Stop(name string) error  { return a.run("stop", name) }
func (a *Apple) Remove(name string) error {
	return a.run("rm", name)
}

func (a *Apple) List() (string, error) {
	out, err := a.output("list", "--all")
	return string(out), err
}

func (a *Apple) EnsureVolume(name string) error {
	volumes, err := a.ListVolumes()
	if err != nil {
		return fmt.Errorf("inspect volumes: %w", err)
	}
	for _, volume := range volumes {
		if volume == name {
			return nil
		}
	}
	return a.run("volume", "create", name)
}

func (a *Apple) RemoveVolume(name string) error {
	return a.run("volume", "rm", name)
}

func (a *Apple) ListVolumes() ([]string, error) {
	out, err := a.output("volume", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	return volumeNamesFromList(out)
}

func volumeNamesFromList(out []byte) ([]string, error) {
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" {
			return nil, fmt.Errorf("volume list: entry without id (schema drift?)")
		}
		names = append(names, e.ID)
	}
	return names, nil
}

// ValidateMountField rejects strings that could escape the CLI's
// comma-delimited --mount grammar (a crafted path containing
// ",source=/Users,target=/host" must never reach the parser — later
// duplicate directives win and can remount arbitrary host paths).
func ValidateMountField(s string) error {
	if strings.ContainsAny(s, ",=") {
		return fmt.Errorf("path %q contains mount-grammar characters (,=) — refusing", s)
	}
	if s == "" {
		return fmt.Errorf("empty mount field")
	}
	return nil
}

func (a *Apple) Run(spec RunSpec) error {
	for _, m := range spec.Mounts {
		if err := ValidateMountField(m.Source); err != nil {
			return err
		}
		if err := ValidateMountField(m.Target); err != nil {
			return err
		}
	}
	for _, v := range spec.Volumes {
		if err := ValidateMountField(v.Name); err != nil {
			return err
		}
		if err := ValidateMountField(v.Target); err != nil {
			return err
		}
		if strings.Contains(v.Name, ":") || strings.Contains(v.Target, ":") {
			return fmt.Errorf("volume field contains ':' (reserved by -v grammar): %q:%q", v.Name, v.Target)
		}
	}
	args := []string{"run", "-d", "--name", spec.Name,
		"--cpus", fmt.Sprint(spec.CPUs), "--memory", spec.Memory}
	if spec.SSH {
		args = append(args, "--ssh")
	}
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range spec.Labels {
		args = append(args, "-l", k+"="+v)
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", v.Name+":"+v.Target)
	}
	for _, m := range spec.Mounts {
		s := fmt.Sprintf("type=virtiofs,source=%s,target=%s", m.Source, m.Target)
		if m.ReadOnly {
			s += ",readonly"
		}
		args = append(args, "--mount", s)
	}
	args = append(args, spec.Image, "sleep", "infinity")
	return a.run(args...)
}

func (a *Apple) Exec(name string, argv []string, stdin io.Reader) error {
	return a.ExecContext(context.Background(), name, argv, stdin)
}

func (a *Apple) ExecContext(parent context.Context, name string, argv []string, stdin io.Reader) error {
	ctx, cancel := context.WithTimeout(parent, execTimeout)
	defer cancel()
	args := append([]string{"exec"}, ifStdin(stdin)...)
	args = append(args, name)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec %v: %w: %s", argv, err, stderr.String())
	}
	return nil
}

func (a *Apple) ExecInteractive(ctx context.Context, name, workdir string, argv []string) (retErr error) {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	bin, err := exec.LookPath(a.Bin)
	if err != nil {
		return fmt.Errorf("find container runtime: %w", err)
	}
	args := []string{"exec", "-it", "-w", workdir, name}
	args = append(args, argv...)
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	terminal, err := jobcontrol.Configure(cmd, os.Stdin)
	if err != nil {
		return fmt.Errorf("configure interactive job control: %w", err)
	}
	defer func() {
		// Start failures can happen after child-side foreground handoff.
		retErr = appendError(retErr, terminal.Restore())
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start interactive container exec: %w", err)
	}
	done := make(chan struct{})
	monitorDone := terminal.Monitor(cmd.Process.Pid, done)
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayInteractiveCancellation(ctx, cmd.Process.Pid, done, syscall.Kill, cmd.Process.Kill)
	}()

	waitErr := cmd.Wait()
	close(done)
	monitorErr := <-monitorDone
	relayErr := <-relayDone

	var result error
	if waitErr != nil {
		var processExit *exec.ExitError
		if errors.As(waitErr, &processExit) {
			result = &ExitError{Code: processExitCode(processExit)}
		} else {
			result = fmt.Errorf("wait for interactive container exec: %w", waitErr)
		}
	}
	return appendError(appendError(result, relayErr), monitorErr)
}

// appendError preserves a lone error's concrete type. errors.Join(err, nil)
// returns a wrapper, which would hide ordinary guest exits from callers that
// intentionally distinguish a bare exit from an exit plus cleanup failures.
func appendError(current, additional error) error {
	if additional == nil {
		return current
	}
	if current == nil {
		return additional
	}
	return errors.Join(current, additional)
}

func interactiveSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// NotifyContext converts lifecycle signals into cancellation causes so callers
// can unwind through revocation and cleanup defers.
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, len(interactiveSignals()))
	signal.Notify(signals, interactiveSignals()...)
	stopped := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(signals)
			close(stopped)
			cancel(context.Canceled)
		})
	}
	go func() {
		select {
		case received := <-signals:
			if systemSignal, ok := received.(syscall.Signal); ok {
				cancel(&SignalError{Signal: systemSignal})
			} else {
				cancel(fmt.Errorf("interrupted by signal %v", received))
			}
			select {
			case received = <-signals:
				signal.Stop(signals)
				if systemSignal, ok := received.(syscall.Signal); ok {
					signal.Reset(systemSignal)
					_ = syscall.Kill(os.Getpid(), systemSignal)
				}
			case <-stopped:
			}
		case <-ctx.Done():
		case <-stopped:
		}
	}()
	return ctx, stop
}

func relayInteractiveCancellation(
	ctx context.Context,
	processGroup int,
	done <-chan struct{},
	signalGroup func(int, syscall.Signal) error,
	killProcess func() error,
) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	sig := syscall.SIGTERM
	var signalErr *SignalError
	if errors.As(context.Cause(ctx), &signalErr) {
		sig = signalErr.Signal
	}
	if err := signalGroup(-processGroup, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		relayErr := fmt.Errorf("relay %s to interactive child group: %w", sig, err)
		fallbackErr := killProcess()
		if errors.Is(fallbackErr, os.ErrProcessDone) {
			fallbackErr = nil
		}
		if fallbackErr != nil {
			fallbackErr = fmt.Errorf("kill interactive child after relay failure: %w", fallbackErr)
		}
		return errors.Join(relayErr, fallbackErr)
	}

	timer := time.NewTimer(interactiveKillGrace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	if err := signalGroup(-processGroup, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		groupErr := fmt.Errorf("kill unresponsive interactive child group: %w", err)
		fallbackErr := killProcess()
		if errors.Is(fallbackErr, os.ErrProcessDone) {
			fallbackErr = nil
		}
		if fallbackErr != nil {
			fallbackErr = fmt.Errorf("kill unresponsive interactive child: %w", fallbackErr)
		}
		return errors.Join(groupErr, fallbackErr)
	}
	return nil
}

func processExitCode(processExit *exec.ExitError) int {
	if status, ok := processExit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	if code := processExit.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

// GuestFileExists answers via stdout so exec failure (container gone,
// apiserver down) is distinguishable from a plain "no".
func (a *Apple) GuestFileExists(name, path string) (bool, error) {
	out, err := a.output("exec", name,
		"sh", "-c", `test -f "$1" && echo yes || echo no`, "coop-test", path)
	if err != nil {
		return false, fmt.Errorf("exists check %s: %w", path, err)
	}
	switch strings.TrimSpace(string(out)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("exists check %s: unexpected output %q", path, out)
	}
}

// ImageExists uses quiet output because Apple denormalizes references there;
// JSON configuration names retain registry prefixes such as
// docker.io/library/, which do not compare directly with coop:latest.
func (a *Apple) ImageExists(name string) (bool, error) {
	out, err := a.output("image", "ls", "--quiet")
	if err != nil {
		return false, fmt.Errorf("image ls: %w", err)
	}
	for _, ref := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(ref) == name {
			return true, nil
		}
	}
	return false, nil
}

func (a *Apple) ContainerImage(name string) (string, error) {
	out, err := a.output("inspect", name)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	ref, err := parseContainerImage(out)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	return ref, nil
}

// parseContainerImage extracts the image reference from `container
// inspect` JSON. Callers make TEARDOWN decisions on this value, so
// schema drift (missing/empty reference) is an error, never "".
func parseContainerImage(out []byte) (string, error) {
	var entries []struct {
		Configuration struct {
			Image struct {
				Reference string `json:"reference"`
			} `json:"image"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no entries")
	}
	if entries[0].Configuration.Image.Reference == "" {
		return "", fmt.Errorf("empty image reference (schema drift?)")
	}
	return entries[0].Configuration.Image.Reference, nil
}

func (a *Apple) ContainerLabel(name, key string) (string, error) {
	out, err := a.output("inspect", name)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	var entries []struct {
		Configuration struct {
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", fmt.Errorf("inspect %s: parse: %w", name, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("inspect %s: no entries", name)
	}
	return entries[0].Configuration.Labels[key], nil
}

func ifStdin(r io.Reader) []string {
	if r != nil {
		return []string{"-i"}
	}
	return nil
}
