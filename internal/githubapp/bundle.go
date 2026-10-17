package githubapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type token struct {
	InstallationID int64             `json:"installation_id"`
	Value          string            `json:"token"`
	ExpiresAt      time.Time         `json:"expires_at"`
	Permissions    map[string]string `json:"permissions"`
}
type wireBundle struct {
	Version       int              `json:"version"`
	AppID         int64            `json:"app_id"`
	BotLogin      string           `json:"bot_login"`
	Installations map[string]token `json:"installations"`
}
type Bundle struct{ document wireBundle }
type InstallationStatus struct {
	ID        int64     `json:"installation_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Status struct {
	AppID         int64                         `json:"app_id"`
	BotLogin      string                        `json:"bot_login"`
	Installations map[string]InstallationStatus `json:"installations"`
}

func (Bundle) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<GitHub App credentials redacted>")
}
func (b Bundle) Encode(w io.Writer) error {
	if err := json.NewEncoder(w).Encode(b.document); err != nil {
		return errors.New("cannot write GitHub App credentials")
	}
	return nil
}
func (b Bundle) Status() Status {
	s := Status{AppID: b.document.AppID, BotLogin: b.document.BotLogin, Installations: map[string]InstallationStatus{}}
	for owner, t := range b.document.Installations {
		s.Installations[owner] = InstallationStatus{ID: t.InstallationID, ExpiresAt: t.ExpiresAt}
	}
	return s
}
func readBounded(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocument+1))
	if err != nil {
		return nil, errors.New("cannot read GitHub App document")
	}
	if len(raw) > MaxDocument {
		return nil, errors.New("GitHub App document exceeds 1 MiB")
	}
	return raw, nil
}
func ParseBundle(raw []byte, now time.Time) (Bundle, error) {
	var d wireBundle
	if len(raw) > MaxDocument || json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.AppID <= 0 || !strings.HasSuffix(d.BotLogin, "[bot]") || len(d.Installations) == 0 || len(d.Installations) > 64 {
		return Bundle{}, errors.New("invalid GitHub App credential document")
	}
	normalized := map[string]token{}
	ids := map[int64]bool{}
	for owner, t := range d.Installations {
		lower := strings.ToLower(owner)
		if !ownerName.MatchString(owner) || t.InstallationID <= 0 || ids[t.InstallationID] || normalized[lower].InstallationID != 0 || t.Value == "" || strings.ContainsAny(t.Value, "\x00\r\n\t ") || len(t.Value) > 64<<10 {
			return Bundle{}, errors.New("invalid GitHub App token scope")
		}
		if !t.ExpiresAt.After(now.Add(30*time.Second)) || t.ExpiresAt.After(now.Add(2*time.Hour)) {
			return Bundle{}, errors.New("GitHub App credentials expired or have invalid expiration")
		}
		ids[t.InstallationID] = true
		normalized[lower] = t
	}
	d.Installations = normalized
	return Bundle{document: d}, nil
}
func (b Bundle) Environment(owner string, base []string, now time.Time) ([]string, error) {
	t, ok := b.document.Installations[strings.ToLower(owner)]
	if !ok {
		return nil, errors.New("GitHub owner is not authorized by this agent")
	}
	if !t.ExpiresAt.After(now.Add(30 * time.Second)) {
		return nil, errors.New("GitHub App credentials expired; wait for host refresh or run coop credentials refresh")
	}
	env := CleanEnvironment(base)
	return append(env, "GH_TOKEN="+t.Value, "GH_HOST=github.com"), nil
}
func CleanEnvironment(base []string) []string {
	env := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if name == "GH_DEBUG" || name == "GH_TRACE" || name == "GIT_CURL_VERBOSE" || strings.HasPrefix(name, "GIT_TRACE") {
			continue
		}
		switch name {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR":
			continue
		}
		env = append(env, entry)
	}
	return env
}
