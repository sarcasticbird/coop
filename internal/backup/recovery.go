package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sarcasticbird/coop/internal/workspace"
	"golang.org/x/sys/unix"
)

const RestoreLabel = "coop.restore"

type diskIdentity struct{ Device, Inode uint64 }
type maintenanceJournal struct {
	Version                      int
	Archive, SHA256, Root, Token string
	Source, Target               workspace.Record
	Prior                        *workspace.Record
	Bundle                       diskIdentity
	OriginalRoot                 diskIdentity
	Disks                        map[string]diskIdentity
}

func identity(path string) (diskIdentity, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return diskIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG && st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return diskIdentity{}, fmt.Errorf("recovery path is not a regular file or directory")
	}
	return diskIdentity{uint64(st.Dev), uint64(st.Ino)}, nil
}

func (s AppleStorage) pending(mode workspace.Mode, name string) (*maintenanceJournal, error) {
	store, ok := s.Store.(workspace.RecoveryStore)
	if !ok {
		return nil, nil
	}
	data, err := store.LoadRecovery(mode, name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j maintenanceJournal
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing recovery journal data")
	}
	if j.Version != 1 || j.Target.Mode != mode || j.Target.Identity != name || j.Token == "" || !filepath.IsAbs(j.Archive) || len(j.SHA256) != 64 {
		return nil, fmt.Errorf("invalid recovery journal ownership")
	}
	if err := workspace.ValidateRecord(j.Source); err != nil {
		return nil, err
	}
	if err := workspace.ValidateRecord(j.Target); err != nil {
		return nil, err
	}
	return &j, nil
}

// PendingMaintenance returns the original evidence, never a backup of partial disks.
func (s AppleStorage) PendingMaintenance() (string, error) {
	if s.Target == nil {
		return "", fmt.Errorf("recovery target is not configured")
	}
	j, err := s.pending(s.Target.Mode, s.Target.Identity)
	if err != nil || j == nil {
		return "", err
	}
	return j.Archive, nil
}

func saveJournal(store workspace.RecoveryStore, j *maintenanceJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return store.SaveRecovery(j.Target.Mode, j.Target.Identity, data)
}

func (s AppleStorage) maintenanceContainer(ctx context.Context, name string) (*containerInspection, error) {
	data, err := s.Machine.Output(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var rows []containerInspection
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Configuration.ID == "" || row.Status.State == "" {
			return nil, fmt.Errorf("container listing schema mismatch")
		}
		if row.Status.State == "running" {
			for _, mount := range row.Configuration.Mounts {
				if mount.Type.Volume != nil && strings.HasPrefix(mount.Type.Volume.Name, name+"--") {
					return nil, fmt.Errorf("maintenance volume is in use by %s", row.Configuration.ID)
				}
			}
		}
	}
	for _, row := range rows {
		if row.Configuration.ID == name {
			c, err := s.container(ctx, name)
			return &c, err
		}
	}
	return nil, nil
}

func maintenanceRoot(bundle string) (string, error) {
	var fs struct{ Source, Destination string }
	if err := readMetadata(filepath.Join(bundle, "rootfs.json"), &fs); err != nil {
		return "", err
	}
	path, err := ownedPath(bundle, "rootfs.ext4")
	if err != nil {
		return "", err
	}
	if fs.Source != path || fs.Destination != "/" {
		return "", fmt.Errorf("maintenance rootfs ownership mismatch")
	}
	return path, nil
}

func (s AppleStorage) publishMaintenance(ctx context.Context, r workspace.Record, meta diskMetadata, root, dir string, j *maintenanceJournal) (retErr error) {
	store, ok := s.Store.(workspace.RecoveryStore)
	if !ok || r.Mode != workspace.Interactive || s.CreateInteractive == nil || s.Archive == "" || s.ArchiveSHA256 == "" {
		return fmt.Errorf("durable interactive maintenance recovery is not configured")
	}
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("maintenance incomplete; keep the Coop stopped and retry coop apply or coop restore %q with unchanged configuration: %w", s.Archive, retErr)
		}
	}()
	current, err := s.maintenanceContainer(ctx, r.RuntimeName)
	if err != nil {
		return err
	}
	if current != nil && current.Status.State != "stopped" {
		return fmt.Errorf("stop the maintenance destination before retrying")
	}
	if j == nil {
		if current == nil || meta.Source.RuntimeName != r.RuntimeName || current.Configuration.Labels["coop.spec"] != meta.Source.Fingerprint || current.Configuration.Image.Reference != meta.Source.Image {
			return fmt.Errorf("maintenance source ownership changed")
		}
		bundle, err := ownedPath(root, "containers", r.RuntimeName)
		if err != nil {
			return err
		}
		id, err := identity(bundle)
		if err != nil {
			return err
		}
		originalRoot, err := maintenanceRoot(bundle)
		if err != nil {
			return err
		}
		originalID, err := identity(originalRoot)
		if err != nil {
			return err
		}
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		j = &maintenanceJournal{Version: 1, Archive: s.Archive, SHA256: s.ArchiveSHA256, Root: root, Token: hex.EncodeToString(token[:]), Source: meta.Source, Target: r, Bundle: id, OriginalRoot: originalID, Disks: map[string]diskIdentity{}}
		prior, err := s.Store.Load(r.Mode, r.Identity)
		if err == nil {
			j.Prior = &prior
		} else if !os.IsNotExist(err) {
			return err
		}
		for _, v := range meta.Volumes {
			path, err := s.volumeSource(ctx, root, v.Name)
			if err != nil {
				return err
			}
			id, err := identity(path)
			if err != nil {
				return err
			}
			j.Disks[v.Payload] = id
		}
		if err := saveJournal(store, j); err != nil {
			return err
		}
	} else if j.Root != root || j.Archive != s.Archive || j.SHA256 != s.ArchiveSHA256 || !reflect.DeepEqual(j.Source, meta.Source) || !reflect.DeepEqual(j.Target, r) {
		return fmt.Errorf("archive or destination changed since maintenance began; original archive: %s", j.Archive)
	}
	prior, err := s.Store.Load(r.Mode, r.Identity)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	recordMatches := err == nil && (reflect.DeepEqual(prior, r) || j.Prior != nil && reflect.DeepEqual(prior, *j.Prior))
	recordAbsent := os.IsNotExist(err) && j.Prior == nil
	if !recordMatches && !recordAbsent {
		return fmt.Errorf("destination ownership record changed during maintenance")
	}
	// Existing volumes must keep their original identities, even across partial writes.
	for _, v := range meta.Volumes {
		path, err := s.volumeSource(ctx, root, v.Name)
		if err != nil {
			return err
		}
		id, err := identity(path)
		if err != nil {
			return err
		}
		if id != j.Disks[v.Payload] {
			return fmt.Errorf("refusing changed destination volume %s", v.Name)
		}
	}
	if current != nil {
		if current.Configuration.Labels[RestoreLabel] != j.Token {
			bundle, err := ownedPath(root, "containers", r.RuntimeName)
			if err != nil {
				return err
			}
			id, err := identity(bundle)
			if err != nil {
				return err
			}
			if id != j.Bundle || current.Configuration.Labels["coop.spec"] != j.Source.Fingerprint || j.Disks["rootfs"] != (diskIdentity{}) {
				return fmt.Errorf("refusing foreign maintenance destination")
			}
			originalRoot, err := maintenanceRoot(bundle)
			if err != nil {
				return err
			}
			originalID, err := identity(originalRoot)
			if err != nil {
				return err
			}
			if originalID != j.OriginalRoot {
				return fmt.Errorf("refusing changed original rootfs")
			}
		} else if current.Configuration.Labels["coop.spec"] != r.Fingerprint || current.Configuration.Image.Reference != r.Image {
			return fmt.Errorf("maintenance target configuration changed")
		}
		// An interrupted bootstrap has no published rootfs. Recreate only the
		// original bundle or a runtime carrying this transaction's random token.
		if j.Disks["rootfs"] == (diskIdentity{}) {
			if _, err := s.Machine.Output(ctx, "rm", r.RuntimeName); err != nil {
				return err
			}
			current = nil
		}
	}
	if current == nil {
		if j.Disks["rootfs"] != (diskIdentity{}) {
			return fmt.Errorf("maintenance target disappeared after disk publication")
		}
		if err := s.CreateInteractive(ctx, r, j.Token); err != nil {
			return err
		}
	}
	current, err = s.maintenanceContainer(ctx, r.RuntimeName)
	if err != nil {
		return err
	}
	if current == nil || current.Status.State != "stopped" || current.Configuration.Labels[RestoreLabel] != j.Token || current.Configuration.Labels["coop.spec"] != r.Fingerprint || current.Configuration.Image.Reference != r.Image {
		return fmt.Errorf("new maintenance target ownership or stopped-state mismatch")
	}
	bundle, err := ownedPath(root, "containers", r.RuntimeName)
	if err != nil {
		return err
	}
	rootDisk, err := maintenanceRoot(bundle)
	if err != nil {
		return err
	}
	id, err := identity(rootDisk)
	if err != nil {
		return err
	}
	if j.Disks["rootfs"] == (diskIdentity{}) {
		j.Disks["rootfs"] = id
		if err := saveJournal(store, j); err != nil {
			return err
		}
	} else if id != j.Disks["rootfs"] {
		return fmt.Errorf("refusing changed destination rootfs")
	}
	for _, v := range meta.Volumes {
		path, err := s.volumeSource(ctx, root, v.Name)
		if err != nil {
			return err
		}
		if err := replayColdDisk(ctx, filepath.Join(dir, v.Payload), path, j.Disks[v.Payload]); err != nil {
			return err
		}
	}
	if err := replayColdDisk(ctx, filepath.Join(dir, "rootfs"), rootDisk, j.Disks["rootfs"]); err != nil {
		return err
	}
	if err := s.Store.Save(r); err != nil {
		return err
	}
	return store.ClearRecovery(r.Mode, r.Identity)
}

// In-place publication preserves the journaled inode. A short write or crash
// leaves a partial but still owned disk that can be replayed from the archive.
func replayColdDisk(ctx context.Context, source, destination string, want diskIdentity) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	in, err := openRegular(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	fd, err := unix.Open(destination, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	out := os.NewFile(uintptr(fd), destination)
	defer func() { _ = out.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || (diskIdentity{uint64(st.Dev), uint64(st.Ino)}) != want {
		return fmt.Errorf("refusing foreign recovery disk")
	}
	// Clear old extents before replay: sparse zero runs must not retain old bytes.
	if err := out.Truncate(0); err != nil {
		return err
	}
	pipeR, pipeW := io.Pipe()
	go func() { err := writeSparse(ctx, pipeW, in, info.Size()); _ = pipeW.CloseWithError(err) }()
	defer func() { _ = pipeR.Close() }()
	if err := readSparse(ctx, pipeR, out, info.Size()); err != nil {
		return err
	}
	return out.Sync()
}
