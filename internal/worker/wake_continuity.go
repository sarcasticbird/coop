package worker

import "errors"

// Keep the historical wakeScope format. Continuity excludes only the default
// destination, not any identity, board, roster, alias or peer-hop policy.
func wakeContinuityScope(c Config) string {
	if c.Wake != nil {
		wake := *c.Wake
		wake.CoordinationURL = ""
		c.Wake = &wake
	}
	return wakeScope(c)
}

func validWakeContinuity(r WakeRoutingState) bool {
	return r.ContinuitySHA == "" || r.Baselined && hexSHA.MatchString(r.ScopeSHA) && hexSHA.MatchString(r.ContinuitySHA)
}

func wakeRoutingBaseline(c Config, r WakeRoutingState) (bool, error) {
	scope := wakeScope(c)
	baseline := !r.Baselined || r.ScopeSHA != scope
	if !validWakeContinuity(r) {
		return false, errors.New("invalid wake routing continuity")
	}
	if c.Version != 2 {
		if r.ContinuitySHA != "" {
			return false, errors.New("v2 routing continuity requires matching v2 readers")
		}
		return baseline, nil
	}
	if !r.Baselined {
		return true, nil
	}
	if r.ContinuitySHA == "" {
		if r.ScopeSHA != scope {
			return false, errors.New("wake continuity must first be seeded with the unchanged configuration")
		}
		return false, nil
	}
	continuity := wakeContinuityScope(c)
	if r.ScopeSHA == scope && r.ContinuitySHA != continuity {
		return false, errors.New("wake routing continuity conflicts with the saved scope")
	}
	// Different authority inputs retain the existing fresh-baseline behavior.
	// A destination-only change retains the existing unread and peer-chain state.
	return r.ContinuitySHA != continuity, nil
}
