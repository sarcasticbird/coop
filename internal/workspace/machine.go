package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/runtime"
)

type MachineBackend struct {
	Runtime runtime.AppleMachine
	Store   Store
}

func (b MachineBackend) Inspect(ctx context.Context, s Spec) (Info, error) {
	i, state, err := b.Runtime.Inspect(ctx, s.RuntimeName)
	info := Info{Name: s.RuntimeName, Mode: Agent, State: state, Image: i.Image.Reference, ChildContainerID: i.ContainerID}
	if i.CreatedDate != "" {
		info.RuntimeInstance = i.ID + "/" + i.CreatedDate
	}
	if i.StartedDate != "" && i.ContainerID != "" {
		info.BootIdentity = i.ContainerID + "/" + i.StartedDate
	}
	if err != nil || state == runtime.StateAbsent {
		return info, err
	}
	r, err := b.Store.Load(Agent, s.Identity)
	if errors.Is(err, os.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Fingerprint = r.Fingerprint
	if i.CPUs != r.Applied.Resources.CPUs || i.Memory != memoryBytes(r.Applied.Resources.Memory) || normalizeImage(i.Image.Reference) != normalizeImage(r.Image) {
		info.Fingerprint = "runtime-drift"
	}
	return info, nil
}
func memoryBytes(value string) uint64 {
	if len(value) < 2 {
		return 0
	}
	n, err := strconv.ParseUint(value[:len(value)-1], 10, 64)
	if err != nil {
		return 0
	}
	switch value[len(value)-1] {
	case 'G':
		return n << 30
	case 'M':
		return n << 20
	}
	return 0
}
func normalizeImage(value string) string { return strings.TrimPrefix(value, "docker.io/library/") }
func (b MachineBackend) Create(ctx context.Context, s Spec) error {
	if err := b.Runtime.CheckVersion(ctx); err != nil {
		return err
	}
	return b.Runtime.Create(ctx, s.RuntimeName, s.Image, s.Config.Resources.CPUs, s.Config.Resources.Memory)
}
func (b MachineBackend) Start(ctx context.Context, s Spec) error {
	return b.Runtime.Start(ctx, s.RuntimeName)
}
func (b MachineBackend) Stop(ctx context.Context, s Spec) error {
	return b.Runtime.Stop(ctx, s.RuntimeName)
}
func (b MachineBackend) Remove(ctx context.Context, s Spec) error {
	return b.Runtime.Remove(ctx, s.RuntimeName)
}
func (b MachineBackend) Apply(ctx context.Context, s Spec) error {
	r, err := b.Store.Load(Agent, s.Identity)
	if err != nil {
		return err
	}
	if normalizeImage(r.Image) != normalizeImage(s.Image) {
		return fmt.Errorf("%w: image replacement requires verified disk recovery; current machine preserved", ErrUnsupported)
	}
	return b.Runtime.SetResources(ctx, s.RuntimeName, s.Config.Resources.CPUs, s.Config.Resources.Memory)
}
func (b MachineBackend) Exec(ctx context.Context, name, workdir string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	return b.Runtime.Exec(ctx, name, workdir, args, input, stdout, stderr)
}
func (b MachineBackend) Attach(ctx context.Context, name, workdir string, args []string) error {
	return b.Runtime.Attach(ctx, name, workdir, args)
}
