package credential

import (
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestResolveIncludesDefaultsThenRequested(t *testing.T) {
	cfg := config.Config{
		IncludeCredentials: []string{"git", "shared"},
		Credentials: map[string]config.Credential{
			"git":     fileGrant("git-credential-store", ""),
			"shared":  commandEnvGrant("SHARED_TOKEN"),
			"aws-dev": awsGrant("dev"),
		},
	}
	got, err := Resolve(cfg, []string{"aws-dev", "git"})
	if err != nil {
		t.Fatal(err)
	}
	if names := selectedNames(got); !slices.Equal(names, []string{"git", "shared", "aws-dev"}) {
		t.Fatalf("selection order = %v", names)
	}
}

func TestResolveRejectsUnknownAndExcessiveSelections(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		_, err := Resolve(config.Config{}, []string{"missing"})
		if err == nil || !strings.Contains(err.Error(), `credential "missing": unknown grant`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("selection cap", func(t *testing.T) {
		cfg := config.Config{Credentials: make(map[string]config.Credential)}
		requested := make([]string, 0, config.MaxSelectedCredentials+1)
		for i := 0; i <= config.MaxSelectedCredentials; i++ {
			name := "grant-" + string(rune('a'+i))
			cfg.Credentials[name] = commandEnvGrant("TOKEN_" + string(rune('A'+i)))
			requested = append(requested, name)
		}
		_, err := Resolve(cfg, requested)
		if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestResolveRejectsInjectionConflicts(t *testing.T) {
	tests := map[string]map[string]config.Credential{
		"environment variables": {
			"one": commandEnvGrant("TOKEN"),
			"two": commandEnvGrant("TOKEN"),
		},
		"path environment variables": {
			"one": fileGrant("file", "CONFIG_PATH"),
			"two": fileGrant("file", "CONFIG_PATH"),
		},
		"environment and path variable": {
			"one": commandEnvGrant("CONFIG_PATH"),
			"two": fileGrant("file", "CONFIG_PATH"),
		},
		"git stores": {
			"one": fileGrant("git-credential-store", ""),
			"two": fileGrant("git-credential-store", ""),
		},
		"git adapter variable": {
			"one": fileGrant("git-credential-store", ""),
			"two": commandEnvGrant("GIT_CONFIG_VALUE_1"),
		},
		"aws grants": {
			"one": awsGrant("one"),
			"two": awsGrant("two"),
		},
		"aws standard variable": {
			"one": awsGrant("one"),
			"two": commandEnvGrant("AWS_SECRET_ACCESS_KEY"),
		},
		"aws adapter variable": {
			"one": awsGrant("one"),
			"two": commandEnvGrant("AWS_PROFILE"),
		},
	}

	for name, grants := range tests {
		t.Run(name, func(t *testing.T) {
			requested := make([]string, 0, len(grants))
			for grant := range grants {
				requested = append(requested, grant)
			}
			slices.Sort(requested)
			_, err := Resolve(config.Config{Credentials: grants}, requested)
			if err == nil || !strings.Contains(err.Error(), "both inject") {
				t.Fatalf("conflict accepted: %v", err)
			}
		})
	}
}

func fileGrant(injectType, pathEnv string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "file", Path: "~/.secret"},
		Inject: config.CredentialInjection{Type: injectType, PathEnv: pathEnv},
	}
}

func commandEnvGrant(name string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "command", Argv: []string{"secret-tool"}},
		Inject: config.CredentialInjection{Type: "environment", Name: name},
	}
}

func awsGrant(profile string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "aws-profile", Profile: profile},
		Inject: config.CredentialInjection{Type: "aws"},
	}
}

func selectedNames(selected []Selected) []string {
	names := make([]string, len(selected))
	for i := range selected {
		names[i] = selected[i].Name
	}
	return names
}
