package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/backup"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/workspace"
	"github.com/spf13/cobra"
)

func recoveryLock(ctx context.Context, name string, op func() error) error {
	release, err := lock.AcquireContext(ctx, name, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	return op()
}
func backupAgent(ctx context.Context, s *agent.Session, dest string) error {
	return recoveryLock(ctx, s.Spec.RuntimeName, func() error {
		r, err := s.Controller.Store.Load(workspace.Agent, s.Spec.Identity)
		if err != nil {
			return err
		}
		storage := backup.AppleStorage{Machine: runtime.AppleMachine{}, Store: s.Controller.Store, Target: &s.Spec}
		return backup.Create(ctx, storage, r, dest)
	})
}
func restoreAgent(ctx context.Context, s *agent.Session, archive string) error {
	return recoveryLock(ctx, s.Spec.RuntimeName, func() error {
		manifest, err := backup.ReadManifest(archive)
		if err != nil {
			return err
		}
		if manifest.Record.Mode != workspace.Agent {
			return fmt.Errorf("backup is not an agent machine")
		}
		storage := backup.AppleStorage{Machine: runtime.AppleMachine{}, Store: s.Controller.Store, Target: &s.Spec}
		return backup.Restore(ctx, storage, archive, s.Spec.Identity)
	})
}
func interactiveSpec(s *session.Session) workspace.Spec {
	return workspace.Spec{Mode: workspace.Interactive, Identity: s.Name, RuntimeName: s.Name, Project: s.Project, Home: s.GuestHome, Workdir: s.Project, Image: s.DesiredImageName(), Fingerprint: s.SpecFingerprint(), Config: s.Cfg}
}
func interactiveStorage(s *session.Session) (backup.AppleStorage, workspace.Spec, error) {
	store, err := workspace.DefaultStore()
	if err != nil {
		return backup.AppleStorage{}, workspace.Spec{}, err
	}
	spec := interactiveSpec(s)
	storage := backup.AppleStorage{Machine: runtime.AppleMachine{}, Store: store, Target: &spec, CreateInteractive: func(ctx context.Context, r workspace.Record, token string, ensureVolume func(string) error) error {
		if ensureVolume == nil {
			ensureVolume = s.RT.EnsureVolume
		}
		runSpec, err := s.RunSpecWithVolumes(ctx, ensureVolume)
		if err != nil {
			return err
		}
		runSpec.Image = r.Image
		runSpec.Labels[session.SpecLabel] = r.Fingerprint
		if token != "" {
			runSpec.Labels[backup.RestoreLabel] = token
		}
		rt := runtime.NewApple()
		return prepareInteractiveTarget(ctx, rt, rt.CreateStopped, runSpec)
	}}
	return storage, spec, nil
}

// Native container create only writes configuration. Bootstrap Coop's trusted
// sleep entrypoint, then stop it before publishing any restored disk data.
func prepareInteractiveTarget(ctx context.Context, rt runtime.Runtime, create func(runtime.RunSpec) error, spec runtime.RunSpec) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := create(spec); err != nil {
		return err
	}
	if err := rt.Start(spec.Name); err != nil {
		return fmt.Errorf("initialize restore target %s: %w", spec.Name, err)
	}
	if err := rt.Stop(spec.Name); err != nil {
		return fmt.Errorf("stop restore target %s before disk publication: %w", spec.Name, err)
	}
	state, err := rt.State(spec.Name)
	if err != nil {
		return err
	}
	if state != runtime.StateStopped {
		return fmt.Errorf("restore target %s is not cold", spec.Name)
	}
	return nil
}
func backupInteractive(cmd *cobra.Command, dest string) error {
	s, _, err := current()
	if err != nil {
		return err
	}
	return recoveryLock(cmd.Context(), s.Name, func() error {
		storage, spec, err := interactiveStorage(s)
		if err != nil {
			return err
		}
		return backup.Create(cmd.Context(), storage, spec.Record(), dest)
	})
}
func restoreInteractive(cmd *cobra.Command, archive string) error {
	s, _, err := current()
	if err != nil {
		return err
	}
	return recoveryLock(cmd.Context(), s.Name, func() error {
		manifest, err := backup.ReadManifest(archive)
		if err != nil {
			return err
		}
		if manifest.Record.Mode != workspace.Interactive {
			return fmt.Errorf("backup is not an interactive Coop")
		}
		storage, spec, err := interactiveStorage(s)
		if err != nil {
			return err
		}
		return backup.Restore(cmd.Context(), storage, archive, spec.Identity)
	})
}
func applyInteractive(cmd *cobra.Command) error {
	s, _, err := current()
	if err != nil {
		return err
	}
	return recoveryLock(cmd.Context(), s.Name, func() error {
		storage, spec, err := interactiveStorage(s)
		if err != nil {
			return err
		}
		archive, err := storage.PendingMaintenance()
		if err != nil {
			return err
		}
		if archive != "" {
			if err := backup.Restore(cmd.Context(), storage, archive, spec.Identity); err != nil {
				return fmt.Errorf("maintenance failed; recovery archive %s: %w", archive, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Maintenance applied. Recovery archive: %s\n", archive)
			return err
		}
		state, err := s.RT.State(s.Name)
		if err != nil {
			return err
		}
		if state != runtime.StateStopped {
			return fmt.Errorf("stop this Coop before applying maintenance")
		}
		image, err := s.RT.ContainerImage(s.Name)
		if err != nil {
			return err
		}
		if image != s.DesiredImageName() {
			return fmt.Errorf("%w: changing the base image cannot preserve arbitrary guest root changes; existing guest preserved", workspace.ErrUnsupported)
		}
		root := filepath.Join(storage.Store.(workspace.FileStore).Root, "..", "recovery")
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		file, err := os.CreateTemp(root, s.Name+"-*.zip")
		if err != nil {
			return err
		}
		archive = file.Name()
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Remove(archive); err != nil {
			return err
		}
		if err := backup.Create(cmd.Context(), storage, spec.Record(), archive); err != nil {
			return err
		}
		storage.Maintenance = true
		if err := backup.Restore(cmd.Context(), storage, archive, spec.Identity); err != nil {
			return fmt.Errorf("maintenance failed; recovery archive %s: %w", archive, err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Maintenance applied. Recovery archive: %s\n", archive)
		return err
	})
}
