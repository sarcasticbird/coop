package credential

import (
	"fmt"

	"github.com/sarcasticbird/coop/internal/config"
)

// Resolve returns the ordered, deduplicated union of configured defaults and
// credentials requested for one entry.
func Resolve(cfg config.Config, requested []string) ([]Selected, error) {
	names := make([]string, 0, len(cfg.IncludeCredentials)+len(requested))
	seen := make(map[string]struct{}, cap(names))
	for _, group := range [][]string{cfg.IncludeCredentials, requested} {
		for _, name := range group {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	if len(names) > config.MaxSelectedCredentials {
		return nil, fmt.Errorf("selected credential count %d exceeds maximum %d", len(names), config.MaxSelectedCredentials)
	}

	selected := make([]Selected, 0, len(names))
	claims := make(map[string]string)
	for _, name := range names {
		spec, ok := cfg.Credentials[name]
		if !ok {
			return nil, fmt.Errorf("credential %q: unknown grant", name)
		}
		for _, claim := range injectionClaims(spec.Inject) {
			if owner, ok := claims[claim]; ok {
				return nil, fmt.Errorf("credentials %q and %q both inject %s", owner, name, claim)
			}
			claims[claim] = name
		}
		selected = append(selected, Selected{Name: name, Spec: spec})
	}
	return selected, nil
}

func injectionClaims(inject config.CredentialInjection) []string {
	switch inject.Type {
	case "environment":
		return []string{inject.Name}
	case "file":
		return []string{inject.PathEnv}
	case "git-credential-store":
		return []string{
			"Git credential store",
			"GIT_CONFIG_COUNT",
			"GIT_CONFIG_KEY_0",
			"GIT_CONFIG_VALUE_0",
			"GIT_CONFIG_KEY_1",
			"GIT_CONFIG_VALUE_1",
		}
	case "aws":
		return []string{
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_SESSION_TOKEN",
			"AWS_SHARED_CREDENTIALS_FILE",
			"AWS_PROFILE",
			"AWS_EC2_METADATA_DISABLED",
		}
	default:
		// Config validation rejects this before resolution. Keeping resolution
		// side-effect free makes hand-built Config values safe for tests/callers.
		return nil
	}
}
