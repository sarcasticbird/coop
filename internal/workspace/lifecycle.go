package workspace

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type Controller struct {
	Backend Backend
	Store   Store
}

func (c Controller) owned(ctx context.Context, s Spec) (Info, error) {
	info, err := c.Backend.Inspect(ctx, s)
	if err != nil {
		return info, err
	}
	r, err := c.Store.Load(s.Mode, s.Identity)
	if info.State == runtime.StateAbsent {
		if err != nil && !os.IsNotExist(err) {
			return info, err
		}
		return info, nil
	}
	if err != nil {
		return info, fmt.Errorf("refusing unowned runtime %s: %w", s.RuntimeName, err)
	}
	if r.RuntimeName != s.RuntimeName {
		return info, fmt.Errorf("workspace runtime ownership mismatch")
	}
	if info.Fingerprint == "" {
		info.Fingerprint = r.Fingerprint
	}
	return info, nil
}
func (c Controller) withLock(ctx context.Context, s Spec, op func() error) error {
	release, err := lock.AcquireContext(ctx, s.RuntimeName, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	return op()
}
func (c Controller) Ensure(ctx context.Context, s Spec) error {
	return c.withLock(ctx, s, func() error {
		info, err := c.owned(ctx, s)
		if err != nil {
			return err
		}
		if err := CheckSpecification(info.State, info.Fingerprint, s.Fingerprint); err != nil {
			return err
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		switch info.State {
		case runtime.StateAbsent:
			if err := c.Backend.Create(ctx, s); err != nil {
				return err
			}
			if err := c.Store.Save(s.Record()); err != nil {
				return fmt.Errorf("%s was created but ownership could not be saved; preserve it for recovery: %w", s.RuntimeName, err)
			}
			return c.Backend.Start(ctx, s)
		case runtime.StateStopped:
			return c.Backend.Start(ctx, s)
		default:
			return nil
		}
	})
}
func (c Controller) Down(ctx context.Context, s Spec) error {
	return c.withLock(ctx, s, func() error {
		info, err := c.owned(ctx, s)
		if err != nil {
			return err
		}
		if info.State == runtime.StateAbsent {
			return fmt.Errorf("%s does not exist", s.RuntimeName)
		}
		return c.Backend.Stop(ctx, s)
	})
}
func (c Controller) Apply(ctx context.Context, s Spec) error {
	return c.withLock(ctx, s, func() error {
		info, err := c.owned(ctx, s)
		if err != nil {
			return err
		}
		if info.State != runtime.StateStopped {
			return fmt.Errorf("stop %s before applying maintenance", s.RuntimeName)
		}
		if err := c.Backend.Apply(ctx, s); err != nil {
			return err
		}
		return c.Store.Save(s.Record())
	})
}
func (c Controller) Destroy(ctx context.Context, s Spec) error {
	return c.withLock(ctx, s, func() error {
		info, err := c.owned(ctx, s)
		if err != nil {
			return err
		}
		if info.State == runtime.StateAbsent {
			return fmt.Errorf("%s does not exist", s.RuntimeName)
		}
		if info.State == runtime.StateRunning {
			if err := c.Backend.Stop(ctx, s); err != nil {
				return err
			}
		}
		if err := c.Backend.Remove(ctx, s); err != nil {
			return err
		}
		return c.Store.Remove(s.Mode, s.Identity)
	})
}
