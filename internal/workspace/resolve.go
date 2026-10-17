package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/credential"
)

func ResolveAgent(name string) (Spec, error) {
	cfg, profile, err := config.LoadProfile(name)
	if err != nil {
		return Spec{}, err
	}
	if _, err := credential.Resolve(cfg, nil); err != nil {
		return Spec{}, err
	}
	s := Spec{Mode: Agent, Identity: name, RuntimeName: "coop-agent-" + name, Home: "/root", Workdir: "/workspace", Config: cfg, Profile: profile}
	s.Fingerprint = Fingerprint(s)
	return s, nil
}

func Fingerprint(s Spec) string {
	// Only guest shape and executable inputs belong in identity, never secrets
	// or the host provider configuration that supplies them.
	data, _ := json.Marshal(struct {
		Applied AppliedSpec
		Image   string
		Tools   config.Tools
	}{s.Record().Applied, s.Image, s.Config.Tools})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}
