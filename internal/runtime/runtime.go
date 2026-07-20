// Package runtime abstracts the container runtime. The interface exists
// so the core can be unit-tested without Apple's `container` (hosted CI
// has no nested virtualization).
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
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
	// ExecInteractive replaces the current process (tty passthrough).
	ExecInteractive(name, workdir string, argv []string) error
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

// Every runtime command carries a deadline so a wedged apiserver cannot hang
// coop indefinitely. Interactive exec is exempt because it replaces the
// process.
const (
	opTimeout   = 60 * time.Second
	execTimeout = 10 * time.Minute // seed tar streams
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
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	args := append([]string{"exec"}, ifStdin(stdin)...)
	args = append(args, name)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec %v: %w: %s", argv, err, stderr.String())
	}
	return nil
}

func (a *Apple) ExecInteractive(name, workdir string, argv []string) error {
	bin, err := exec.LookPath(a.Bin)
	if err != nil {
		return err
	}
	args := []string{a.Bin, "exec", "-it", "-w", workdir, name}
	args = append(args, argv...)
	return sysExec(bin, args, os.Environ())
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
