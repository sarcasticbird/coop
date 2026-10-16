package hostcontrol

import (
	"context"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/workspace"
	"os"
	"path/filepath"
	"time"
)

// RegisterInteractive is a host-owner CLI action. Remote tools cannot supply a
// project path or adopt legacy inventory. It records evidence without booting.
func (c *Control) RegisterInteractive(ctx context.Context, projectRoot string) (Workspace, error) {
	if !filepath.IsAbs(projectRoot) {
		return Workspace{}, fail("absolute_project_required")
	}
	s, err := session.New(c.rt, projectRoot)
	if err != nil {
		return Workspace{}, err
	}
	if s.Project != projectRoot {
		return Workspace{}, fail("canonical_project_required")
	}
	release, err := lock.AcquireContext(ctx, s.Name, 30*time.Second)
	if err != nil {
		return Workspace{}, err
	}
	defer release()
	rows, err := c.rt.Containers()
	if err != nil {
		return Workspace{}, fail("runtime_unavailable")
	}
	for _, row := range rows {
		if row.Name != s.Name {
			continue
		}
		if (row.State != "running" && row.State != "stopped") || row.ProjectMount() != s.Project || row.Labels[runtime.ProjectLabel] != s.Project || row.Labels[session.SpecLabel] != s.SpecFingerprint() {
			return Workspace{}, fail("registration_ownership_mismatch")
		}
		image, err := c.rt.ContainerImage(s.Name)
		if err != nil || image != s.DesiredImageName() {
			return Workspace{}, fail("registration_image_mismatch")
		}
		spec := workspace.Spec{Mode: workspace.Interactive, Identity: s.Name, RuntimeName: s.Name, Project: s.Project, Home: s.GuestHome, Workdir: s.Project, Image: s.DesiredImageName(), Fingerprint: s.SpecFingerprint(), Config: s.Cfg}
		if old, err := c.store.Load(workspace.Interactive, s.Name); err == nil {
			if old.RuntimeName != s.Name || old.Applied.Project != s.Project || old.Fingerprint != spec.Fingerprint || old.Image != spec.Image {
				return Workspace{}, fail("existing_ownership_requires_maintenance")
			}
		} else if !os.IsNotExist(err) {
			return Workspace{}, err
		} else if err = c.store.Save(spec.Record()); err != nil {
			return Workspace{}, err
		}
		return Workspace{Ref: c.reference("interactive:" + s.Name), Mode: "interactive", Name: s.Name, State: row.State, Fingerprint: spec.Fingerprint, Owned: true}, nil
	}
	return Workspace{}, fail("workspace_absent")
}
