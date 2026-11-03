package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/runtime"
)

const agentLabel = "coop.agent"
const storageLabel = "coop.storage"
const incarnationLabel = "coop.incarnation"
const portableStorage = "agent-volumes-v1"

// AgentVolumes are guest-only disks. None exposes a host home or checkout.
// Helpers preserve managed worker binaries; image-owned helpers are refreshed
// by the portable entrypoint before systemd starts.
func AgentVolumes(name string) []runtime.Volume {
	return []runtime.Volume{{Name: name + "--home", Target: "/root"}, {Name: name + "--workspace", Target: "/workspace"}, {Name: name + "--state", Target: "/var/lib/coop"}, {Name: name + "--helpers", Target: "/usr/local/libexec"}}
}

type ExecutionGuard func(context.Context, *Record, Spec) error

// ContainerBackend gives refreshed named birds replaceable roots without
// converting or grafting any existing machine-plugin root disk.
type ContainerBackend struct {
	Runtime runtime.AppleMachine
	Store   RecoveryStore
	Guard   ExecutionGuard
}

type containerChange struct {
	Version     int     `json:"version"`
	Source      *Record `json:"source,omitempty"`
	Target      Record  `json:"target"`
	SourceToken string  `json:"source_token,omitempty"`
	Token       string  `json:"token"`
}

func (b ContainerBackend) pending(s Spec) (*containerChange, error) {
	raw, err := b.Store.LoadRecovery(Agent, s.Identity)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var change containerChange
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&change); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF || change.Version != 1 || change.Token == "" || change.Target.Mode != Agent || change.Target.Identity != s.Identity || change.Target.Applied.Runtime != "container" || ValidateRecord(change.Target) != nil {
		return nil, fmt.Errorf("invalid container replacement journal")
	}
	if change.Source != nil && (ValidateRecord(*change.Source) != nil || change.Source.Identity != s.Identity || change.Source.Applied.Runtime != "container" || change.SourceToken == "") {
		return nil, fmt.Errorf("invalid original container ownership")
	}
	return &change, nil
}

func (b ContainerBackend) current(ctx context.Context, s Spec) (*runtime.NativeContainer, error) {
	rows, err := b.Runtime.NativeContainers(ctx)
	if err != nil {
		return nil, err
	}
	var found *runtime.NativeContainer
	for _, row := range rows {
		if row.Configuration.ID == s.RuntimeName {
			if found != nil {
				return nil, fmt.Errorf("duplicate container identity")
			}
			copy := row
			found = &copy
		}
	}
	return found, nil
}

func validateAgentContainer(row runtime.NativeContainer, s Spec) error {
	cfg := row.Configuration
	if cfg.ID != s.RuntimeName || cfg.Labels[agentLabel] != s.Identity || cfg.Labels[storageLabel] != portableStorage || cfg.Labels[incarnationLabel] == "" || cfg.Labels["coop.spec"] == "" || cfg.Image.Reference == "" {
		return fmt.Errorf("unowned or incomplete named container %s", s.RuntimeName)
	}
	want := AgentVolumes(s.RuntimeName)
	if len(cfg.Mounts) != len(want) {
		return fmt.Errorf("agent data mount set changed")
	}
	seen := map[string]bool{}
	for _, mount := range cfg.Mounts {
		if mount.Type.Volume == nil || seen[mount.Destination] {
			return fmt.Errorf("agent has foreign or duplicate mounts")
		}
		seen[mount.Destination] = true
		if !slices.Contains(want, runtime.Volume{Name: mount.Type.Volume.Name, Target: mount.Destination}) {
			return fmt.Errorf("agent volume ownership changed")
		}
	}
	return nil
}

// ValidatePortableContainer is also used by cold backup before reading disks.
func ValidatePortableContainer(row runtime.NativeContainer, record Record) error {
	if record.Mode != Agent || record.Applied.Runtime != "container" || ValidateRecord(record) != nil {
		return fmt.Errorf("invalid portable agent ownership record")
	}
	if err := validateAgentContainer(row, Spec{RuntimeName: record.RuntimeName, Identity: record.Identity}); err != nil {
		return err
	}
	if row.Configuration.Labels["coop.spec"] != record.Fingerprint || normalizeImage(row.Configuration.Image.Reference) != normalizeImage(record.Image) || row.Configuration.Resources.CPUs != record.Applied.Resources.CPUs || row.Configuration.Resources.MemoryInBytes != memoryBytes(record.Applied.Resources.Memory) {
		return fmt.Errorf("portable agent specification drifted")
	}
	return nil
}

func (b ContainerBackend) Inspect(ctx context.Context, s Spec) (Info, error) {
	info := Info{Name: s.RuntimeName, Mode: Agent, State: runtime.StateAbsent}
	row, err := b.current(ctx, s)
	if err != nil || row == nil {
		return info, err
	}
	if err := validateAgentContainer(*row, s); err != nil {
		return info, err
	}
	info.State = runtime.StateStopped
	if row.Status.State == "running" {
		info.State = runtime.StateRunning
	}
	info.Image = row.Configuration.Image.Reference
	info.Fingerprint = row.Configuration.Labels["coop.spec"]
	info.RuntimeInstance = row.Configuration.Labels[incarnationLabel]
	info.ChildContainerID = s.RuntimeName
	if row.Status.StartedDate != "" {
		info.BootIdentity = info.RuntimeInstance + "/" + row.Status.StartedDate
	}
	r, err := b.Store.Load(Agent, s.Identity)
	if err != nil {
		return info, err
	}
	if r.Applied.Runtime != "container" {
		return info, fmt.Errorf("implicit machine conversion is unsupported")
	}
	if normalizeImage(info.Image) != normalizeImage(r.Image) || row.Configuration.Resources.CPUs != r.Applied.Resources.CPUs || row.Configuration.Resources.MemoryInBytes != memoryBytes(r.Applied.Resources.Memory) {
		info.Fingerprint = "runtime-drift"
	}
	if pending, err := b.pending(s); err != nil {
		return info, err
	} else if pending != nil {
		info.Fingerprint = "replacement-pending"
	}
	return info, nil
}

func (b ContainerBackend) guard(ctx context.Context, previous *Record, s Spec) error {
	if s.Mode != Agent || s.Profile.Runtime != "container" || s.Image == "" || s.Fingerprint == "" || ValidateRecord(s.Record()) != nil {
		return fmt.Errorf("invalid portable agent specification")
	}
	if want := s.Profile.Execution; want != nil {
		records, err := b.Store.List()
		if err != nil {
			return err
		}
		for _, record := range records {
			other := record.Applied.Execution
			if record.Mode == Agent && record.Identity != s.Identity && other != nil && other.MasterID == want.MasterID && other.AgentID == want.AgentID && (other.Generation >= want.Generation || other.InstanceID == want.InstanceID) {
				return fmt.Errorf("another owned runtime already pins this worker execution")
			}
		}
	}
	if b.Guard != nil {
		return b.Guard(ctx, previous, s)
	}
	if s.Profile.Execution != nil || s.Profile.Services["worker"].Command != nil {
		return fmt.Errorf("worker execution fencing is not configured")
	}
	return nil
}

func (b ContainerBackend) coldVolumes(ctx context.Context, s Spec, required bool) error {
	volumes, err := b.Runtime.NativeVolumes(ctx)
	if err != nil {
		return err
	}
	want := AgentVolumes(s.RuntimeName)
	for _, v := range want {
		if slices.Contains(volumes, v.Name) != required {
			return fmt.Errorf("agent data volume %s is missing or unexpectedly present", v.Name)
		}
	}
	rows, err := b.Runtime.NativeContainers(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		for _, mount := range row.Configuration.Mounts {
			if mount.Type.Volume == nil {
				continue
			}
			for _, v := range want {
				if mount.Type.Volume.Name == v.Name && (row.Configuration.ID != s.RuntimeName || row.Status.State != "stopped") {
					return fmt.Errorf("agent volume %s is referenced by %s", v.Name, row.Configuration.ID)
				}
			}
		}
	}
	return nil
}

func (b ContainerBackend) create(ctx context.Context, s Spec, token string) error {
	return b.Runtime.NativeCreate(ctx, runtime.RunSpec{Name: s.RuntimeName, Image: s.Image, CPUs: s.Config.Resources.CPUs, Memory: s.Config.Resources.Memory, GuestAdmin: true, Command: []string{"/opt/coop-machine/boot"}, Volumes: AgentVolumes(s.RuntimeName), Labels: map[string]string{agentLabel: s.Identity, storageLabel: portableStorage, incarnationLabel: token, "coop.spec": s.Fingerprint}})
}

func (b ContainerBackend) saveChange(change containerChange) error {
	raw, err := json.Marshal(change)
	if err != nil {
		return err
	}
	return b.Store.SaveRecovery(Agent, change.Target.Identity, raw)
}

func (b ContainerBackend) Create(ctx context.Context, s Spec) error {
	if err := b.guard(ctx, nil, s); err != nil {
		return err
	}
	if pending, err := b.pending(s); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("creation/replacement is pending; run coop apply with unchanged configuration")
	}
	if _, err := b.Store.Load(Agent, s.Identity); err == nil {
		return fmt.Errorf("missing owned container; preserve its volumes and recover explicitly")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Also prove that no old machine-plugin VM owns this stable runtime name.
	if _, state, err := b.Runtime.Inspect(ctx, s.RuntimeName); err != nil {
		return err
	} else if state != runtime.StateAbsent {
		return fmt.Errorf("existing machine must not be implicitly converted")
	}
	if row, err := b.current(ctx, s); err != nil {
		return err
	} else if row != nil {
		return fmt.Errorf("container already exists")
	}
	if err := b.coldVolumes(ctx, s, false); err != nil {
		return err
	}
	change := containerChange{Version: 1, Target: s.Record(), Token: rand.Text()}
	if err := b.saveChange(change); err != nil {
		return err
	}
	return b.finishChange(ctx, s, change)
}

// CreateRestoreTarget leaves ownership unpublished until cold disk restoration
// succeeds. A failed restore cannot accidentally become an ordinary up/start.
func (b ContainerBackend) CreateRestoreTarget(ctx context.Context, s Spec) error {
	if err := b.guard(ctx, nil, s); err != nil {
		return err
	}
	if _, err := b.Store.Load(Agent, s.Identity); err == nil {
		return fmt.Errorf("restore ownership already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if pending, err := b.pending(s); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("pending container replacement")
	}
	if _, state, err := b.Runtime.Inspect(ctx, s.RuntimeName); err != nil {
		return err
	} else if state != runtime.StateAbsent {
		return fmt.Errorf("destination machine already exists")
	}
	if row, err := b.current(ctx, s); err != nil {
		return err
	} else if row != nil {
		return fmt.Errorf("destination container already exists")
	}
	if err := b.coldVolumes(ctx, s, false); err != nil {
		return err
	}
	for _, v := range AgentVolumes(s.RuntimeName) {
		if _, err := b.Runtime.Output(ctx, "volume", "create", v.Name); err != nil {
			return err
		}
	}
	if err := b.create(ctx, s, rand.Text()); err != nil {
		return err
	}
	// Materialize the fresh root/volume disks with only the trusted image boot.
	// Coop services are not enabled automatically and no credentials are staged.
	if _, err := b.Runtime.Output(ctx, "start", s.RuntimeName); err != nil {
		return err
	}
	if err := b.WaitReady(ctx, s); err != nil {
		return err
	}
	if _, err := b.Runtime.Output(ctx, "stop", s.RuntimeName); err != nil {
		return err
	}
	row, err := b.current(ctx, s)
	if err != nil {
		return err
	}
	if row == nil || row.Status.State != "stopped" {
		return fmt.Errorf("restore container is not cold")
	}
	return ValidatePortableContainer(*row, s.Record())
}

// ResumeApply owns interrupted create/remove/create/publication windows. No
// startup path treats an absent container as permission to invent fresh state.
func (b ContainerBackend) ResumeApply(ctx context.Context, s Spec) (bool, error) {
	change, err := b.pending(s)
	if err != nil || change == nil {
		return false, err
	}
	if !reflect.DeepEqual(change.Target, s.Record()) {
		return true, fmt.Errorf("retry replacement with the original unchanged configuration")
	}
	return true, b.finishChange(ctx, s, *change)
}

func (b ContainerBackend) finishChange(ctx context.Context, s Spec, change containerChange) error {
	if err := b.guard(ctx, change.Source, s); err != nil {
		return err
	}
	current, loadErr := b.Store.Load(Agent, s.Identity)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return loadErr
	}
	if change.Source != nil {
		if loadErr != nil || !reflect.DeepEqual(current, *change.Source) && !reflect.DeepEqual(current, change.Target) {
			return fmt.Errorf("replacement ownership record changed")
		}
	} else if loadErr == nil && !reflect.DeepEqual(current, change.Target) {
		return fmt.Errorf("creation ownership record changed")
	}
	row, err := b.current(ctx, s)
	if err != nil {
		return err
	}
	if row != nil {
		if err := validateAgentContainer(*row, s); err != nil {
			return err
		}
		if row.Status.State != "stopped" {
			return fmt.Errorf("replacement target must remain stopped")
		}
		token := row.Configuration.Labels[incarnationLabel]
		if token != change.Token {
			if change.Source == nil || token != change.SourceToken || row.Configuration.Labels["coop.spec"] != change.Source.Fingerprint || normalizeImage(row.Configuration.Image.Reference) != normalizeImage(change.Source.Image) {
				return fmt.Errorf("replacement source identity changed")
			}
			if err := b.coldVolumes(ctx, s, true); err != nil {
				return err
			}
			if err := b.guard(ctx, change.Source, s); err != nil {
				return err
			}
			if _, err := b.Runtime.Output(ctx, "rm", s.RuntimeName); err != nil {
				return err
			}
			row = nil
		}
	}
	if row == nil {
		if change.Source == nil {
			volumes, err := b.Runtime.NativeVolumes(ctx)
			if err != nil {
				return err
			}
			for _, v := range AgentVolumes(s.RuntimeName) {
				if !slices.Contains(volumes, v.Name) {
					if _, err := b.Runtime.Output(ctx, "volume", "create", v.Name); err != nil {
						return err
					}
				}
			}
		}
		if err := b.coldVolumes(ctx, s, true); err != nil {
			return err
		}
		if err := b.guard(ctx, change.Source, s); err != nil {
			return err
		}
		if err := b.create(ctx, s, change.Token); err != nil {
			return err
		}
		row, err = b.current(ctx, s)
		if err != nil || row == nil {
			return fmt.Errorf("new container could not be verified: %w", err)
		}
	}
	if err := validateAgentContainer(*row, s); err != nil {
		return err
	}
	if row.Configuration.Labels[incarnationLabel] != change.Token || row.Configuration.Labels["coop.spec"] != s.Fingerprint || normalizeImage(row.Configuration.Image.Reference) != normalizeImage(s.Image) || row.Configuration.Resources.CPUs != s.Config.Resources.CPUs || row.Configuration.Resources.MemoryInBytes != memoryBytes(s.Config.Resources.Memory) {
		return fmt.Errorf("new container specification mismatch")
	}
	if err := b.coldVolumes(ctx, s, true); err != nil {
		return err
	}
	if err := b.guard(ctx, change.Source, s); err != nil {
		return err
	}
	if err := b.Store.Save(s.Record()); err != nil {
		return err
	}
	return b.Store.ClearRecovery(Agent, s.Identity)
}

func (b ContainerBackend) Apply(ctx context.Context, s Spec) error {
	if handled, err := b.ResumeApply(ctx, s); handled || err != nil {
		return err
	}
	r, err := b.Store.Load(Agent, s.Identity)
	if err != nil {
		return err
	}
	if r.Applied.Runtime != "container" {
		return fmt.Errorf("implicit machine conversion is unsupported")
	}
	row, err := b.current(ctx, s)
	if err != nil {
		return err
	}
	if row == nil || row.Status.State != "stopped" {
		return fmt.Errorf("stop the named container before replacement")
	}
	if err := validateAgentContainer(*row, s); err != nil {
		return err
	}
	if row.Configuration.Labels["coop.spec"] != r.Fingerprint || normalizeImage(row.Configuration.Image.Reference) != normalizeImage(r.Image) {
		return fmt.Errorf("original container specification drifted")
	}
	if err := ValidatePortableContainer(*row, r); err != nil {
		return err
	}
	if reflect.DeepEqual(r, s.Record()) {
		return nil
	}
	if err := b.guard(ctx, &r, s); err != nil {
		return err
	}
	if err := b.coldVolumes(ctx, s, true); err != nil {
		return err
	}
	change := containerChange{Version: 1, Source: &r, Target: s.Record(), SourceToken: row.Configuration.Labels[incarnationLabel], Token: rand.Text()}
	if err := b.saveChange(change); err != nil {
		return err
	}
	return b.finishChange(ctx, s, change)
}

func (b ContainerBackend) Start(ctx context.Context, s Spec) error {
	if change, err := b.pending(s); err != nil {
		return err
	} else if change != nil {
		return fmt.Errorf("replacement is pending; run coop apply")
	}
	if err := b.guard(ctx, nil, s); err != nil {
		return err
	}
	if err := b.coldVolumes(ctx, s, true); err != nil {
		return err
	}
	if err := b.guard(ctx, nil, s); err != nil {
		return err
	}
	_, err := b.Runtime.Output(ctx, "start", s.RuntimeName)
	if err != nil {
		return err
	}
	return b.WaitReady(ctx, s)
}

// A running container process is not yet a usable systemd manager. In
// particular, replacement starts a fresh root and can reach exec before the
// manager socket exists. Keep service callers behind a bounded readiness gate.
// A degraded manager is usable for administrative repair; individual service
// and worker authority/health checks still govern execution.
func (b ContainerBackend) WaitReady(ctx context.Context, s Spec) error {
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var state bytes.Buffer
	err := b.Runtime.NativeExec(readyCtx, s.RuntimeName, "/", []string{"/bin/bash", "-euc", `for ((n=0;n<300;n++)); do
		if [[ -S /run/systemd/private ]]; then
		  state=$(systemctl is-system-running --wait) || true
		  printf '%s\n' "$state"
		  case "$state" in running|degraded) exit 0;; *) exit 1;; esac
		fi
  sleep 0.1
done
echo 'systemd manager socket did not become ready' >&2
exit 1`}, nil, &state, io.Discard)
	if err != nil {
		return fmt.Errorf("portable agent systemd readiness failed (%s): %w", strings.TrimSpace(state.String()), err)
	}
	if status := strings.TrimSpace(state.String()); status != "running" && status != "degraded" {
		return fmt.Errorf("portable agent systemd is not ready: %s", status)
	}
	return nil
}
func (b ContainerBackend) Stop(ctx context.Context, s Spec) error {
	_, err := b.Runtime.Output(ctx, "stop", s.RuntimeName)
	return err
}
func (b ContainerBackend) Remove(ctx context.Context, s Spec) error {
	if pending, err := b.pending(s); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("resolve pending replacement before destruction")
	}
	if err := b.coldVolumes(ctx, s, true); err != nil {
		return err
	}
	if _, err := b.Runtime.Output(ctx, "rm", s.RuntimeName); err != nil {
		return err
	}
	for _, v := range AgentVolumes(s.RuntimeName) {
		if _, err := b.Runtime.Output(ctx, "volume", "rm", v.Name); err != nil {
			return err
		}
	}
	return nil
}
func (b ContainerBackend) Exec(ctx context.Context, name, workdir string, argv []string, input io.Reader, stdout, stderr io.Writer) error {
	return b.Runtime.NativeExec(ctx, name, workdir, argv, input, stdout, stderr)
}
func (b ContainerBackend) Attach(ctx context.Context, name, workdir string, argv []string) error {
	return b.Runtime.NativeAttach(ctx, name, workdir, argv)
}
