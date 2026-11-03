package worker

import (
	"context"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeDeliveryActor(cfg Config, b NativeTaskBinding) bool {
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || cfg.Name != "rook" || cfg.Master.AgentID != "rook" {
		return false
	}
	member, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
	return ok && member.Name == "rook" && memberCapability(member, "deliver") &&
		!memberCapability(member, "implement") && !memberCapability(member, "engineering-review") && !memberCapability(member, "customer-qa") &&
		cfg.Self.ID != b.AuthorID && cfg.Self.ID != b.PeerID && cfg.Self.ID != b.QAID
}

// Check live principal capabilities as well as local configuration. The same
// guard is used before preparation, each effect, and acknowledged recovery.
func verifyNativeDelivery(ctx context.Context, cfg Config, master NativeEngineeringMaster, binding NativeTaskBinding, authority c.GoalAuthority) error {
	if !nativeDeliveryActor(cfg, binding) {
		return c.ErrForbidden
	}
	self, err := nativeCurrentSelf(ctx, cfg, master)
	if err != nil {
		return err
	}
	if !c.CanDeliver(self.Principal, authority.Actors) {
		return c.ErrForbidden
	}
	return nil
}
