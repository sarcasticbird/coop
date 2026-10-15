package session

import (
	"context"
	"fmt"
	"sort"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
)

// RunSpec prepares owned volumes and validated mount targets without starting a
// guest. Recovery uses the same shape as ordinary creation.
func (s *Session) RunSpec(ctx context.Context) (runtime.RunSpec, error) {
	return s.RunSpecWithVolumes(ctx, s.RT.EnsureVolume)
}

// RunSpecWithVolumes lets recovery checkpoint ownership before attaching volumes.
func (s *Session) RunSpecWithVolumes(ctx context.Context, ensureVolume func(string) error) (runtime.RunSpec, error) {
	var vols []runtime.Volume
	for name, agent := range s.Cfg.Agents {
		if err := context.Cause(ctx); err != nil {
			return runtime.RunSpec{}, err
		}
		volume := s.Name + project.VolumeSep + name
		if err := ensureVolume(volume); err != nil {
			return runtime.RunSpec{}, fmt.Errorf("volume %s: %w", volume, err)
		}
		vols = append(vols, runtime.Volume{Name: volume, Target: config.ExpandHome(agent.State, s.GuestHome)})
	}
	for _, volume := range s.Cfg.Volumes {
		target, err := ensureProjectVolumeTarget(s.Project, volume.Path)
		if err != nil {
			return runtime.RunSpec{}, fmt.Errorf("prepare project volume %q: %w", volume.Path, err)
		}
		name := projectVolumeName(s.Name, volume.Path)
		if err := ensureVolume(name); err != nil {
			return runtime.RunSpec{}, fmt.Errorf("volume %s for %q: %w", name, volume.Path, err)
		}
		vols = append(vols, runtime.Volume{Name: name, Target: target})
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	mounts := []runtime.Mount{{Source: s.Project, Target: s.Project}}
	for _, m := range s.Cfg.Mounts {
		mounts = append(mounts, runtime.Mount{Source: m.Source, Target: m.Source, ReadOnly: m.ReadOnly()})
	}
	var ports []runtime.Publish
	for _, p := range s.Cfg.Publishes {
		ports = append(ports, runtime.Publish{HostPort: p.HostPort, GuestPort: p.GuestPort})
	}
	return runtime.RunSpec{Name: s.Name, Image: s.DesiredImageName(), CPUs: s.Cfg.Resources.CPUs, Memory: s.Cfg.Resources.Memory, SSH: s.Cfg.SSH, Env: map[string]string{"HOME": s.GuestHome}, Labels: map[string]string{SpecLabel: s.SpecFingerprint(), runtime.ProjectLabel: s.Project}, Mounts: mounts, Volumes: vols, Publishes: ports}, nil
}
