package hostcontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
)

type grant struct {
	config CallerConfig
	digest [32]byte
}
type Policy struct {
	mu     sync.RWMutex
	grants map[string]grant
}

func NewPolicy(c Config) (*Policy, error) {
	p := &Policy{}
	if e := p.Reload(c); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *Policy) Reload(c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	b, e := readPrivate(c.VerifierFile, 1<<20)
	if e != nil {
		return e
	}
	var verifiers map[string]string
	if e = strictJSON(b, &verifiers); e != nil {
		return e
	}
	next := map[string]grant{}
	seen := map[[32]byte]bool{}
	for _, v := range c.Callers {
		raw, e := hex.DecodeString(verifiers[v.ID])
		if e != nil || len(raw) != 32 {
			return fail("invalid_verifier")
		}
		var d [32]byte
		copy(d[:], raw)
		if seen[d] {
			return fail("ambiguous_verifier")
		}
		seen[d] = true
		v.Workspaces = append([]string(nil), v.Workspaces...)
		v.Capabilities = append([]string(nil), v.Capabilities...)
		next[v.ID] = grant{v, d}
	}
	p.mu.Lock()
	p.grants = next
	p.mu.Unlock()
	return nil
}
func (p *Policy) Authenticate(token string) (Caller, error) {
	if len(token) == 0 || len(token) > 512 {
		return Caller{}, fail("unauthorized")
	}
	d := sha256.Sum256([]byte(token))
	p.mu.RLock()
	defer p.mu.RUnlock()
	var found Caller
	for id, v := range p.grants {
		if subtle.ConstantTimeCompare(d[:], v.digest[:]) == 1 {
			found = Caller{ID: id, digest: d}
		}
	}
	if found.ID == "" {
		return Caller{}, fail("unauthorized")
	}
	return found, nil
}
func (p *Policy) Authorize(c Caller, selector, cap string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	g, ok := p.grants[c.ID]
	if !ok || subtle.ConstantTimeCompare(c.digest[:], g.digest[:]) != 1 {
		return fail("forbidden")
	}
	hasCap := false
	for _, s := range g.config.Capabilities {
		hasCap = hasCap || s == cap
	}
	if !hasCap {
		return fail("forbidden")
	}
	if selector == "" {
		return nil
	}
	for _, s := range g.config.Workspaces {
		if s == selector {
			return nil
		}
	}
	return fail("forbidden")
}
