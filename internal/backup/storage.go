package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"golang.org/x/sys/unix"
)

type AppleStorage struct {
	Machine           runtime.AppleMachine
	Store             workspace.Store
	Target            *workspace.Spec
	CreateInteractive func(context.Context, workspace.Record, string) error
	Maintenance       bool
	Archive           string
	ArchiveSHA256     string
}
type volumeRecord struct {
	Name    string `json:"name"`
	Payload string `json:"payload"`
}
type diskMetadata struct {
	Source         workspace.Record `json:"source"`
	RuntimeVersion string           `json:"runtime_version"`
	Volumes        []volumeRecord   `json:"volumes,omitempty"`
	Initialized    bool             `json:"initialized"`
}

var archiveVolumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

type containerInspection struct {
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
		Labels map[string]string `json:"labels"`
		Mounts []struct {
			Source string `json:"source"`
			Type   struct {
				Volume *struct {
					Name string `json:"name"`
				} `json:"volume"`
			} `json:"type"`
		} `json:"mounts"`
	} `json:"configuration"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

// Source ownership is checked against the original manifest before Restore
// substitutes the explicitly selected destination identity.
func (s AppleStorage) ValidateArchive(m Manifest) error {
	var meta diskMetadata
	if err := json.Unmarshal(m.Metadata, &meta); err != nil {
		return err
	}
	if !reflect.DeepEqual(meta.Source, m.Record) || meta.RuntimeVersion != m.RuntimeVersion {
		return fmt.Errorf("archive source metadata does not match its manifest")
	}
	if !strings.HasPrefix(m.RuntimeVersion, "1.5.") {
		return fmt.Errorf("%w: archive runtime layout is incompatible", workspace.ErrUnsupported)
	}
	return nil
}

func (s AppleStorage) root(ctx context.Context) (string, string, error) {
	data, err := s.Machine.Output(ctx, "system", "status", "--format", "json")
	if err != nil {
		return "", "", err
	}
	var info struct {
		Paths struct {
			AppRoot string `json:"appRoot"`
		} `json:"paths"`
		Server struct {
			Version string `json:"version"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(info.Server.Version, "1.5.") {
		return "", "", fmt.Errorf("%w: cold disk layout is verified only for Apple Container 1.5.x", workspace.ErrUnsupported)
	}
	if !filepath.IsAbs(info.Paths.AppRoot) {
		return "", "", fmt.Errorf("runtime did not report an absolute storage root")
	}
	root, err := filepath.EvalSymlinks(info.Paths.AppRoot)
	return root, info.Server.Version, err
}
func readMetadata(path string, v any) error {
	f, err := openRegular(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 1<<20 {
		return fmt.Errorf("oversized runtime metadata")
	}
	return json.NewDecoder(io.LimitReader(f, (1<<20)+1)).Decode(v)
}
func openRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("runtime payload is not a regular file")
	}
	return f, nil
}
func ownedPath(root string, parts ...string) (string, error) {
	path := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\") {
			return "", fmt.Errorf("invalid owned storage component")
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("owned storage contains a symlink")
		}
	}
	return path, nil
}
func appendDisk(snapshot *Snapshot, name, path string) error {
	f, err := openRegular(path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	_ = f.Close()
	if err != nil {
		return err
	}
	snapshot.Payloads = append(snapshot.Payloads, Payload{Name: name, Size: info.Size(), Open: func() (io.ReadCloser, error) { return openRegular(path) }})
	return nil
}
func (s AppleStorage) container(ctx context.Context, name string) (containerInspection, error) {
	data, err := s.Machine.Output(ctx, "inspect", name)
	if err != nil {
		return containerInspection{}, err
	}
	var rows []containerInspection
	if err := json.Unmarshal(data, &rows); err != nil {
		return containerInspection{}, err
	}
	if len(rows) != 1 || rows[0].Configuration.ID != name || rows[0].Status.State == "" {
		return containerInspection{}, fmt.Errorf("container inspection identity/schema mismatch")
	}
	return rows[0], nil
}
func (s AppleStorage) volumeSource(ctx context.Context, root, name string) (string, error) {
	data, err := s.Machine.Output(ctx, "volume", "inspect", name)
	if err != nil {
		return "", err
	}
	var rows []struct {
		Configuration struct{ Name, Source string } `json:"configuration"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return "", err
	}
	if len(rows) != 1 || rows[0].Configuration.Name != name {
		return "", fmt.Errorf("volume inspection identity mismatch")
	}
	path, err := ownedPath(root, "volumes", name, "volume.img")
	if err != nil {
		return "", err
	}
	if filepath.Clean(rows[0].Configuration.Source) != path {
		return "", fmt.Errorf("volume source is outside owned storage")
	}
	return path, nil
}
func (s AppleStorage) Snapshot(ctx context.Context, r workspace.Record) (Snapshot, error) {
	if err := workspace.ValidateRecord(r); err != nil {
		return Snapshot{}, err
	}
	if pending, err := s.pending(r.Mode, r.Identity); err != nil {
		return Snapshot{}, err
	} else if pending != nil {
		return Snapshot{}, fmt.Errorf("incomplete maintenance; preserve and resume original archive %s before taking another backup", pending.Archive)
	}
	root, version, err := s.root(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Record: r, RuntimeVersion: version}
	meta := diskMetadata{Source: r, RuntimeVersion: version}
	var bundle string
	if r.Mode == workspace.Agent {
		info, state, err := s.Machine.Inspect(ctx, r.RuntimeName)
		if err != nil {
			return snapshot, err
		}
		if state != runtime.StateStopped {
			return snapshot, fmt.Errorf("stop the machine before backup")
		}
		if strings.TrimPrefix(info.Image.Reference, "docker.io/library/") != strings.TrimPrefix(r.Image, "docker.io/library/") {
			return snapshot, fmt.Errorf("machine image differs from its ownership record")
		}
		bundle, err = ownedPath(root, "plugin-state", "machine-apiserver", "machines", r.RuntimeName)
		if err != nil {
			return snapshot, err
		}
		var config struct {
			Configuration struct {
				ID string `json:"id"`
			} `json:"configuration"`
		}
		if err := readMetadata(filepath.Join(bundle, "config.json"), &config); err != nil {
			return snapshot, err
		}
		if config.Configuration.ID != r.RuntimeName {
			return snapshot, fmt.Errorf("machine bundle ownership mismatch")
		}
		marker := filepath.Join(bundle, "machine.initialized")
		if data, err := os.ReadFile(marker); err == nil {
			meta.Initialized = strings.HasPrefix(string(data), "1")
		} else if !os.IsNotExist(err) {
			return snapshot, err
		}
	} else {
		info, err := s.container(ctx, r.RuntimeName)
		if err != nil {
			return snapshot, err
		}
		if info.Status.State != "stopped" {
			return snapshot, fmt.Errorf("stop the interactive Coop before backup")
		}
		if info.Configuration.Labels["coop.spec"] == "" {
			return snapshot, fmt.Errorf("cannot establish legacy workspace specification")
		}
		snapshot.Record.Fingerprint = info.Configuration.Labels["coop.spec"]
		snapshot.Record.Image = info.Configuration.Image.Reference
		meta.Source = snapshot.Record
		bundle, err = ownedPath(root, "containers", r.RuntimeName)
		if err != nil {
			return snapshot, err
		}
		volumes, err := runtime.NewApple().ListVolumes()
		if err != nil {
			return snapshot, err
		}
		// Keep volume-to-payload identities stable across runtime enumeration order.
		sort.Strings(volumes)
		// Any running container using an owned volume makes a cold backup unsafe.
		data, err := s.Machine.Output(ctx, "list", "--all", "--format", "json")
		if err != nil {
			return snapshot, err
		}
		var containers []containerInspection
		if err := json.Unmarshal(data, &containers); err != nil {
			return snapshot, err
		}
		for _, name := range volumes {
			if !strings.HasPrefix(name, r.RuntimeName+project.VolumeSep) {
				continue
			}
			for _, c := range containers {
				if c.Status.State == "running" {
					for _, m := range c.Configuration.Mounts {
						if m.Type.Volume != nil && m.Type.Volume.Name == name {
							return snapshot, fmt.Errorf("owned volume %s is in use", name)
						}
					}
				}
			}
			path, err := s.volumeSource(ctx, root, name)
			if err != nil {
				return snapshot, err
			}
			payload := "volume-" + strconv.Itoa(len(meta.Volumes))
			meta.Volumes = append(meta.Volumes, volumeRecord{Name: name, Payload: payload})
			if err := appendDisk(&snapshot, payload, path); err != nil {
				return snapshot, err
			}
		}
	}
	var fs struct{ Source, Destination string }
	if err := readMetadata(filepath.Join(bundle, "rootfs.json"), &fs); err != nil {
		return snapshot, err
	}
	disk, err := ownedPath(bundle, "rootfs.ext4")
	if err != nil {
		return snapshot, err
	}
	if filepath.Clean(fs.Source) != disk || fs.Destination != "/" {
		return snapshot, fmt.Errorf("root filesystem source is outside its owned bundle")
	}
	if err := appendDisk(&snapshot, "rootfs", disk); err != nil {
		return snapshot, err
	}
	metadata, err := json.Marshal(meta)
	snapshot.Metadata = metadata
	return snapshot, err
}

// replaceColdDisk publishes only into a freshly created, stopped runtime object.
// Hard-linking from private restore staging preserves sparse allocation. The
// fallback handles staging on another filesystem without expanding holes.
func replaceColdDisk(ctx context.Context, source, destination string) error {
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".coop-disk-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName)
	defer func() { _ = os.Remove(tmpName) }()
	if err := os.Link(source, tmpName); err != nil {
		in, err := openRegular(source)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		info, err := in.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		pipeR, pipeW := io.Pipe()
		go func() { err := writeSparse(ctx, pipeW, in, info.Size()); _ = pipeW.CloseWithError(err) }()
		copyErr := readSparse(ctx, pipeR, out, info.Size())
		_ = pipeR.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return os.Rename(tmpName, destination)
}

func (s AppleStorage) Publish(ctx context.Context, r workspace.Record, dir string) error {
	if s.Store == nil {
		return fmt.Errorf("recovery ownership store is not configured")
	}
	var meta diskMetadata
	if err := readMetadata(filepath.Join(dir, ".runtime-metadata.json"), &meta); err != nil {
		return err
	}
	if err := workspace.ValidateRecord(meta.Source); err != nil {
		return err
	}
	if meta.Source.Mode != r.Mode || meta.Source.Image != r.Image || meta.Source.Fingerprint != r.Fingerprint {
		return fmt.Errorf("archive metadata ownership mismatch")
	}
	if err := validatePayloadSet(dir, meta); err != nil {
		return err
	}
	pending, err := s.pending(r.Mode, r.Identity)
	if err != nil {
		return err
	}
	if _, err := s.Store.Load(r.Mode, r.Identity); err == nil && !s.Maintenance && pending == nil {
		return fmt.Errorf("destination ownership already exists")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	root, _, err := s.root(ctx)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(meta.RuntimeVersion, "1.5.") {
		return fmt.Errorf("%w: archive runtime layout is incompatible", workspace.ErrUnsupported)
	}
	if s.Target != nil {
		if s.Target.Mode != r.Mode || s.Target.Identity != r.Identity {
			return fmt.Errorf("selected recovery identity mismatch")
		}
		if s.Target.Image != r.Image {
			return fmt.Errorf("%w: configure the destination with the archive's image before restoring", workspace.ErrUnsupported)
		}
		r = s.Target.Record()
	}
	if s.Maintenance || pending != nil {
		return s.publishMaintenance(ctx, r, meta, root, dir, pending)
	}
	var bundle string
	if r.Mode == workspace.Agent {
		_, state, err := s.Machine.Inspect(ctx, r.RuntimeName)
		if err != nil {
			return err
		}
		if state != runtime.StateAbsent {
			return fmt.Errorf("destination machine already exists")
		}
		if len(meta.Volumes) != 0 {
			return fmt.Errorf("machine archive contains unexpected volumes")
		}
		if err := s.Machine.Create(ctx, r.RuntimeName, r.Image, r.Applied.Resources.CPUs, r.Applied.Resources.Memory); err != nil {
			return err
		}
		bundle, err = ownedPath(root, "plugin-state", "machine-apiserver", "machines", r.RuntimeName)
		if err != nil {
			return err
		}
	} else {
		if s.CreateInteractive == nil {
			return fmt.Errorf("interactive recovery creation is not configured")
		}
		// Prove absence using a successful full listing, not a failed inspect.
		state, err := runtime.NewApple().State(r.RuntimeName)
		if err != nil {
			return err
		}
		if state != runtime.StateAbsent {
			return fmt.Errorf("destination interactive Coop already exists")
		}
		for _, v := range meta.Volumes {
			if !strings.HasPrefix(v.Name, meta.Source.RuntimeName+project.VolumeSep) || !payloadName.MatchString(v.Payload) {
				return fmt.Errorf("archive volume ownership mismatch")
			}
			name := r.RuntimeName + strings.TrimPrefix(v.Name, meta.Source.RuntimeName)
			volumes, err := runtime.NewApple().ListVolumes()
			if err != nil {
				return err
			}
			for _, existing := range volumes {
				if existing == name {
					return fmt.Errorf("destination volume %s already exists", name)
				}
			}
		}
		if err := s.CreateInteractive(ctx, r, ""); err != nil {
			return err
		}
		bundle, err = ownedPath(root, "containers", r.RuntimeName)
		if err != nil {
			return err
		}
		for _, v := range meta.Volumes {
			name := r.RuntimeName + strings.TrimPrefix(v.Name, meta.Source.RuntimeName)
			if err := runtime.NewApple().EnsureVolume(name); err != nil {
				return err
			}
			dest, err := s.volumeSource(ctx, root, name)
			if err != nil {
				return err
			}
			if err := replaceColdDisk(ctx, filepath.Join(dir, v.Payload), dest); err != nil {
				return fmt.Errorf("incomplete restore of %s: %w", r.RuntimeName, err)
			}
		}
	}
	var fs struct{ Source, Destination string }
	if err := readMetadata(filepath.Join(bundle, "rootfs.json"), &fs); err != nil {
		return err
	}
	destination, err := ownedPath(bundle, "rootfs.ext4")
	if err != nil {
		return err
	}
	if filepath.Clean(fs.Source) != destination || fs.Destination != "/" {
		return fmt.Errorf("new runtime rootfs ownership mismatch")
	}
	if err := replaceColdDisk(ctx, filepath.Join(dir, "rootfs"), destination); err != nil {
		return fmt.Errorf("incomplete restore of %s: %w", r.RuntimeName, err)
	}
	if r.Mode == workspace.Agent && meta.Initialized {
		marker, err := ownedPath(bundle, "machine.initialized")
		if err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte("1"), 0600); err != nil {
			return err
		}
	}
	return s.Store.Save(r)
}

func validatePayloadSet(dir string, meta diskMetadata) error {
	want := map[string]bool{"rootfs": true, ".runtime-metadata.json": true}
	names := map[string]bool{}
	for _, v := range meta.Volumes {
		if meta.Source.Mode != workspace.Interactive || !strings.HasPrefix(v.Name, meta.Source.RuntimeName+project.VolumeSep) || !archiveVolumeName.MatchString(v.Name) || !strings.HasPrefix(v.Payload, "volume-") || !payloadName.MatchString(v.Payload) || want[v.Payload] || names[v.Name] {
			return fmt.Errorf("invalid archive volume ownership or payload mapping")
		}
		want[v.Payload] = true
		names[v.Name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != len(want) {
		return fmt.Errorf("archive payload set is incomplete or unexpected")
	}
	for _, entry := range entries {
		if !want[entry.Name()] {
			return fmt.Errorf("unexpected archive payload %q", entry.Name())
		}
		f, err := openRegular(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
