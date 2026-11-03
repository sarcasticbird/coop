package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// NativeContainer keeps named agents on the public container/volume API rather
// than depending on the machine plugin's private root-disk layout.
type NativeContainer struct {
	Configuration struct {
		ID           string            `json:"id"`
		CreationDate string            `json:"creationDate"`
		Labels       map[string]string `json:"labels"`
		Image        struct {
			Reference string `json:"reference"`
		} `json:"image"`
		Resources struct {
			CPUs          int    `json:"cpus"`
			MemoryInBytes uint64 `json:"memoryInBytes"`
		} `json:"resources"`
		Mounts []struct {
			Destination string `json:"destination"`
			Type        struct {
				Volume *struct {
					Name string `json:"name"`
				} `json:"volume"`
			} `json:"type"`
		} `json:"mounts"`
	} `json:"configuration"`
	Status struct {
		State       string `json:"state"`
		StartedDate string `json:"startedDate"`
	} `json:"status"`
}

func (m AppleMachine) NativeContainers(ctx context.Context) ([]NativeContainer, error) {
	raw, err := m.Output(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var rows []NativeContainer
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, fmt.Errorf("container listing is not an array")
	}
	for _, row := range rows {
		if row.Configuration.ID == "" || row.Status.State != "stopped" && row.Status.State != "running" {
			return nil, fmt.Errorf("container listing identity/state schema mismatch")
		}
	}
	return rows, nil
}

func (m AppleMachine) NativeCreate(ctx context.Context, spec RunSpec) error {
	args, err := runArgs(spec)
	if err != nil {
		return err
	}
	args[0] = "create"
	args = append(args[:1], args[2:]...) // create has no detached-run flag
	return m.command(ctx, args, nil, io.Discard, io.Discard)
}

func (m AppleMachine) NativeExec(ctx context.Context, name, workdir string, argv []string, input io.Reader, stdout, stderr io.Writer) error {
	args := append([]string{"exec"}, ifStdin(input)...)
	args = append(args, "--workdir", workdir, name)
	return m.command(ctx, append(args, argv...), input, stdout, stderr)
}

func (m AppleMachine) NativeAttach(ctx context.Context, name, workdir string, argv []string) error {
	if m.Command != nil {
		return m.NativeExec(ctx, name, workdir, argv, nil, io.Discard, io.Discard)
	}
	a := NewApple()
	a.Bin = m.bin()
	return a.ExecInteractive(ctx, name, workdir, argv)
}

func (m AppleMachine) NativeVolumes(ctx context.Context) ([]string, error) {
	raw, err := m.Output(ctx, "volume", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("volume listing is not an array")
	}
	return volumeNamesFromList(raw)
}
