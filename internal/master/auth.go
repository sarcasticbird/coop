package master

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type Token struct {
	PrincipalID string    `toml:"principal_id"`
	SHA256      string    `toml:"sha256"`
	ExpiresAt   time.Time `toml:"expires_at"`
	InstanceID  string    `toml:"worker_instance_id"`
	Generation  int64     `toml:"generation"`
}
type Policy struct {
	Tokens         []Token  `toml:"tokens"`
	AllowedHosts   []string `toml:"allowed_hosts"`
	AllowedOrigins []string `toml:"allowed_origins"`
}

func (p *Policy) Authenticate(token string) (string, error) {
	entry, err := p.authenticateEntry(token)
	return entry.PrincipalID, err
}
func (p *Policy) authenticateEntry(token string) (Token, error) {
	if len(token) < 12 || len(token) > 4096 {
		return Token{}, c.ErrForbidden
	}
	hash := sha256.Sum256([]byte(token))
	var selected Token
	matches := 0
	for _, entry := range p.Tokens {
		want, err := hex.DecodeString(entry.SHA256)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(hash[:], want) == 1 {
			matches++
			if entry.ExpiresAt.IsZero() || time.Now().Before(entry.ExpiresAt) {
				selected = entry
			}
		}
	}
	if selected.PrincipalID == "" || matches != 1 {
		return Token{}, c.ErrForbidden
	}
	return selected, nil
}
func (p *Policy) CheckRequest(r *http.Request) error {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(p.AllowedHosts, r.Host) && !slices.Contains(p.AllowedHosts, host) {
		return c.ErrForbidden
	}
	if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(p.AllowedOrigins, origin) {
		return c.ErrForbidden
	}
	return nil
}
func bearer(h http.Header) string {
	if len(h.Values("Authorization")) != 1 {
		return ""
	}
	v := h.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}
