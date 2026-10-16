package hostcontrol

import (
	"context"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/workspace"
	"sort"
)

func (c *Control) HostInfo(ctx context.Context) (HostInfo, error) {
	i := HostInfo{Host: c.host, Capabilities: []string{"read", "ping", "worker_control", "lifecycle", "exec"}}
	raw, e := c.machine.Output(ctx, "--version")
	if e != nil {
		return i, nil
	}
	i.RuntimeVersion = string(raw)
	if len(i.RuntimeVersion) > 256 {
		i.RuntimeVersion = ""
		return i, nil
	}
	i.RuntimeAvailable = true
	return i, nil
}
func (c *Control) List(ctx context.Context, allow ...func(string) bool) ([]Workspace, error) {
	cfg, e := config.Load("")
	if e != nil {
		return nil, fail("profiles_unavailable")
	}
	keys := map[string]bool{}
	for name := range cfg.Profiles {
		keys["agent:"+name] = true
	}
	scoped, ok := c.store.(interface {
		ListMode(workspace.Mode, func(string) bool) ([]workspace.Record, error)
	})
	if !ok {
		return nil, fail("ownership_unavailable")
	}
	records, e := scoped.ListMode(workspace.Interactive, func(name string) bool { return len(allow) == 0 || allow[0]("interactive:"+name) })
	if e != nil {
		return nil, fail("ownership_unavailable")
	}
	for _, r := range records {
		if r.Mode == workspace.Interactive {
			keys["interactive:"+r.Identity] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	rows := []Workspace{}
	for _, k := range sorted {
		if len(allow) > 0 && !allow[0](k) {
			continue
		}
		b, e := c.resolveSelector(ctx, k)
		if e != nil {
			return nil, e
		}
		if b.Spec.Mode == workspace.Interactive && !b.Workspace.Owned {
			continue
		}
		rows = append(rows, b.Workspace)
	}
	return rows, nil
}
func (c *Control) Status(ctx context.Context, ref string) (WorkspaceStatus, error) {
	b, e := c.Resolve(ctx, ref)
	if e != nil {
		return WorkspaceStatus{}, e
	}
	s := WorkspaceStatus{Workspace: b.Workspace, DesiredFingerprint: b.Spec.Fingerprint}
	if !b.Workspace.Owned && b.Workspace.State != "absent" {
		s.Maintenance = "ownership_required"
	} else if b.Workspace.State != "absent" && b.Workspace.Fingerprint != b.Spec.Fingerprint {
		s.Maintenance = "configuration_changed"
	}
	return s, nil
}
