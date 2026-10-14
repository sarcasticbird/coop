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
	"regexp"
	"strconv"
	"strings"
	"time"
)

type MachineCommand func(context.Context, []string, io.Reader, io.Writer, io.Writer) error
type AppleMachine struct {
	Bin     string
	Command MachineCommand
}
type MachineInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Image  struct {
		Reference string `json:"reference"`
	} `json:"image"`
	CPUs        int    `json:"cpus"`
	Memory      uint64 `json:"memory"`
	HomeMount   string `json:"homeMount"`
	ContainerID string `json:"containerId"`
	IPAddress   string `json:"ipAddress"`
}

func (m AppleMachine) bin() string {
	if m.Bin != "" {
		return m.Bin
	}
	return "container"
}
func (m AppleMachine) command(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) error {
	if m.Command != nil {
		return m.Command(ctx, args, input, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.bin(), args...)
	// Native machines enable SSH forwarding by default. Agent credentials must
	// come only from the explicitly selected host profile.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SSH_AUTH_SOCK=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stdin = input
	cmd.Stdout = stdout
	var diagnostic bytes.Buffer
	cmd.Stderr = io.MultiWriter(stderr, &diagnostic)
	if err := cmd.Run(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return fmt.Errorf("container machine: %s: %w", strings.TrimSpace(diagnostic.String()), &ExitError{Code: processExitCode(exited)})
		}
		return fmt.Errorf("container machine: %w", err)
	}
	return nil
}
func (m AppleMachine) Output(ctx context.Context, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := m.command(ctx, args, nil, &out, io.Discard)
	return out.Bytes(), err
}

func validateMachineVersion(value string) error {
	match := regexp.MustCompile(`\b([0-9]+)\.([0-9]+)\.([0-9]+)\b`).FindStringSubmatch(value)
	if len(match) != 4 {
		return fmt.Errorf("cannot parse Apple Container version")
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major < 1 || (major == 1 && minor < 5) {
		return fmt.Errorf("agent machines require Apple Container 1.5.0 or newer")
	}
	return nil
}
func (m AppleMachine) CheckVersion(ctx context.Context) error {
	data, err := m.Output(ctx, "--version")
	if err != nil {
		return err
	}
	return validateMachineVersion(string(data))
}

func parseMachineInspect(data []byte, name string) (MachineInfo, error) {
	var rows []MachineInfo
	if err := json.Unmarshal(data, &rows); err != nil {
		return MachineInfo{}, fmt.Errorf("machine inspect JSON: %w", err)
	}
	if len(rows) != 1 || rows[0].ID != name {
		return MachineInfo{}, fmt.Errorf("machine inspect identity mismatch")
	}
	i := rows[0]
	if i.Status != "running" && i.Status != "stopped" {
		return i, fmt.Errorf("machine %s has unsupported state %q", name, i.Status)
	}
	if i.HomeMount != "none" {
		return i, fmt.Errorf("refusing agent machine %s with home sharing %q", name, i.HomeMount)
	}
	if i.CPUs < 1 || i.Memory == 0 || i.Image.Reference == "" {
		return i, fmt.Errorf("machine inspect is missing required resources or image")
	}
	return i, nil
}
func (m AppleMachine) Inspect(ctx context.Context, name string) (MachineInfo, State, error) {
	data, err := m.Output(ctx, "machine", "list", "--format", "json")
	if err != nil {
		return MachineInfo{}, StateAbsent, err
	}
	var rows []struct {
		Configuration struct {
			ID string `json:"id"`
		} `json:"configuration"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return MachineInfo{}, StateAbsent, fmt.Errorf("machine list JSON: %w", err)
	}
	found := false
	for _, row := range rows {
		id := row.ID
		if id == "" {
			id = row.Configuration.ID
		}
		if id == "" {
			return MachineInfo{}, StateAbsent, fmt.Errorf("machine list schema missing identity")
		}
		if id == name {
			found = true
		}
	}
	if !found {
		return MachineInfo{}, StateAbsent, nil
	}
	data, err = m.Output(ctx, "machine", "inspect", name)
	if err != nil {
		return MachineInfo{}, StateAbsent, err
	}
	i, err := parseMachineInspect(data, name)
	if err != nil {
		return i, StateAbsent, err
	}
	state := StateStopped
	if i.Status == "running" {
		state = StateRunning
	}
	return i, state, nil
}
func (m AppleMachine) Create(ctx context.Context, name, image string, cpus int, memory string) error {
	return m.command(ctx, []string{"machine", "create", "--name", name, "--home-mount", "none", "--cpus", strconv.Itoa(cpus), "--memory", memory, "--no-boot", image}, nil, io.Discard, io.Discard)
}
func (m AppleMachine) Start(ctx context.Context, name string) error {
	// Apple 1.5's first-boot user setup unconditionally opens a terminal even
	// when machine run was called without -t. Give that setup a private PTY;
	// ordinary commands still use pipes so binary input/output stays intact.
	args := append([]string{"-q", "/dev/null", m.bin()}, machineExecArgs(name, "/", []string{"/bin/true"}, false, false)...)
	m.Bin = "/usr/bin/script"
	return m.command(ctx, args, nil, io.Discard, io.Discard)
}
func (m AppleMachine) Stop(ctx context.Context, name string) error {
	_, err := m.Output(ctx, "machine", "stop", name)
	return err
}
func (m AppleMachine) Remove(ctx context.Context, name string) error {
	_, err := m.Output(ctx, "machine", "delete", name)
	return err
}
func (m AppleMachine) SetResources(ctx context.Context, name string, cpus int, memory string) error {
	_, err := m.Output(ctx, "machine", "set", "--name", name, "cpus="+strconv.Itoa(cpus), "memory="+memory, "home-mount=none")
	return err
}

func machineExecArgs(name, workdir string, argv []string, input, terminal bool) []string {
	args := []string{"machine", "run", "--name", name, "--root", "--workdir", workdir}
	if input {
		args = append(args, "-i")
	}
	if terminal {
		args = append(args, "-t")
	}
	args = append(args, "--")
	// Apple's /sbin.machine/init joins these arguments and evaluates them via
	// the user's shell. Quote each argument before crossing that boundary.
	for _, arg := range argv {
		args = append(args, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return args
}
func (m AppleMachine) Exec(ctx context.Context, name, workdir string, argv []string, input io.Reader, stdout, stderr io.Writer) error {
	args, err := m.childExecArgs(ctx, name, workdir, argv, input != nil, false)
	if err != nil {
		return err
	}
	return m.command(ctx, args, input, stdout, stderr)
}
func (m AppleMachine) ExecContext(ctx context.Context, name string, argv []string, input io.Reader) error {
	return m.Exec(ctx, name, "/", argv, input, io.Discard, io.Discard)
}
func (m AppleMachine) Attach(ctx context.Context, name, workdir string, argv []string) error {
	args, err := m.childExecArgs(ctx, name, workdir, argv, true, true)
	if err != nil {
		return err
	}
	return runInteractive(ctx, m.bin(), args)
}

// Execute directly in the inspected child. Apple 1.5 caches initialized=false
// after first-boot setup and machine run otherwise tries terminal setup again.
// Container exec also preserves argv without Apple's shell wrapper.
func (m AppleMachine) childExecArgs(ctx context.Context, name, workdir string, argv []string, input, terminal bool) ([]string, error) {
	info, state, err := m.Inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if state != StateRunning || info.ContainerID == "" {
		return nil, fmt.Errorf("machine %s has no running child", name)
	}
	args := []string{"exec", "--user", "root", "--workdir", workdir}
	if input {
		args = append(args, "-i")
	}
	if terminal {
		args = append(args, "-t")
	}
	args = append(args, info.ContainerID)
	return append(args, argv...), nil
}
