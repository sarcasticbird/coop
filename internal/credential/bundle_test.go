package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestBundleSecretTypesRedactFormattingAndSerialization(t *testing.T) {
	const secret = "do-not-print-this-secret"
	bundle := Bundle{
		files: []SecretFile{{path: "files/001", mode: 0o600, data: []byte(secret)}},
		env:   map[string][]byte{"TOKEN": []byte(secret)},
	}

	for _, value := range []any{bundle, bundle.files[0]} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, value); strings.Contains(got, secret) {
				t.Fatalf("format %s exposed secret: %s", format, got)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("JSON exposed secret: %s", encoded)
		}
	}
}

func TestBuildBundleInjectionAdapters(t *testing.T) {
	lease := GuestLease{Dir: "/dev/shm/coop-credentials/lease"}
	acquired := []Acquired{
		{
			Selected: Selected{Name: "github", Spec: config.Credential{
				Source: config.CredentialSource{Type: "command"},
				Inject: config.CredentialInjection{Type: "environment", Name: "GH_TOKEN"},
			}},
			payload: []byte("github-token"),
		},
		{
			Selected: Selected{Name: "kubernetes", Spec: config.Credential{
				Source: config.CredentialSource{Type: "file"},
				Inject: config.CredentialInjection{Type: "file", PathEnv: "KUBECONFIG"},
			}},
			payload: []byte("kube-config"),
		},
		{
			Selected: Selected{Name: "git", Spec: config.Credential{
				Source: config.CredentialSource{Type: "file"},
				Inject: config.CredentialInjection{Type: "git-credential-store"},
			}},
			payload: []byte("https://user:pass@example.test"),
		},
		{
			Selected: Selected{Name: "aws-dev", Spec: config.Credential{
				Source: config.CredentialSource{Type: "aws-profile", Profile: "dev"},
				Inject: config.CredentialInjection{Type: "aws"},
			}},
			aws: &AWSCredentials{accessKeyID: "key", secretAccessKey: "secret", sessionToken: "session"},
		},
	}

	bundle, err := BuildBundle(acquired, lease)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bundle.env["GH_TOKEN"]); got != "github-token" {
		t.Fatalf("GH_TOKEN = %q", got)
	}
	if got := string(bundle.env["KUBECONFIG"]); got != lease.Dir+"/files/001" {
		t.Fatalf("KUBECONFIG = %q", got)
	}
	if got := string(bundle.env["GIT_CONFIG_COUNT"]); got != "2" {
		t.Fatalf("GIT_CONFIG_COUNT = %q", got)
	}
	if got := string(bundle.env["GIT_CONFIG_KEY_0"]); got != "credential.helper" {
		t.Fatalf("GIT_CONFIG_KEY_0 = %q", got)
	}
	if got := string(bundle.env["GIT_CONFIG_VALUE_0"]); got != "" {
		t.Fatalf("GIT_CONFIG_VALUE_0 = %q", got)
	}
	if got := string(bundle.env["GIT_CONFIG_KEY_1"]); got != "credential.helper" {
		t.Fatalf("GIT_CONFIG_KEY_1 = %q", got)
	}
	if got := string(bundle.env["GIT_CONFIG_VALUE_1"]); got != "store --file "+lease.Dir+"/files/002" {
		t.Fatalf("GIT_CONFIG_VALUE_1 = %q", got)
	}
	if got := string(bundle.env["AWS_SHARED_CREDENTIALS_FILE"]); got != lease.Dir+"/files/003" {
		t.Fatalf("AWS_SHARED_CREDENTIALS_FILE = %q", got)
	}
	if string(bundle.env["AWS_PROFILE"]) != "coop" || string(bundle.env["AWS_EC2_METADATA_DISABLED"]) != "true" {
		t.Fatalf("AWS environment incomplete: %#v", bundle.env)
	}
	wantUnset := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"}
	if !slices.Equal(bundle.unsetEnv, wantUnset) {
		t.Fatalf("AWS environment unsets = %#v", bundle.unsetEnv)
	}
	if len(bundle.files) != 3 {
		t.Fatalf("files = %#v", bundle.files)
	}
	for _, file := range bundle.files {
		if file.mode != fs.FileMode(0o600) {
			t.Fatalf("file %s mode = %o", file.path, file.mode)
		}
	}
	if !bytes.Contains(bundle.files[2].data, []byte("[coop]")) || bytes.Contains(bundle.files[2].data, []byte("[dev]")) {
		t.Fatalf("AWS file uses wrong profile: %s", bundle.files[2].data)
	}
}

func TestBuildBundleRejectsEnvironmentNewlines(t *testing.T) {
	_, err := BuildBundle([]Acquired{{
		Selected: Selected{Name: "token", Spec: config.Credential{
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		}},
		payload: []byte("line-one\nline-two"),
	}}, GuestLease{Dir: "/dev/shm/coop-credentials/lease"})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("embedded newline accepted: %v", err)
	}
}

func TestBuildBundleTrimsOneTerminalEnvironmentNewline(t *testing.T) {
	bundle, err := BuildBundle([]Acquired{{
		Selected: Selected{Name: "token", Spec: config.Credential{
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		}},
		payload: []byte("command-token\n"),
	}}, GuestLease{Dir: "/dev/shm/coop-credentials/lease"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bundle.env["TOKEN"]); got != "command-token" {
		t.Fatalf("TOKEN = %q", got)
	}
}

func TestBuildBundleEnforcesCompleteLimit(t *testing.T) {
	acquired := make([]Acquired, 9)
	for i := range acquired {
		acquired[i] = Acquired{
			Selected: Selected{Name: "grant", Spec: config.Credential{
				Inject: config.CredentialInjection{Type: "file", PathEnv: "CREDENTIAL_FILE"},
			}},
			payload: bytes.Repeat([]byte("x"), MaxPayloadBytes),
		}
	}
	_, err := BuildBundle(acquired, GuestLease{Dir: "/dev/shm/coop-credentials/lease"})
	if !errorsIs(err, ErrBundleTooLarge) {
		t.Fatalf("oversized bundle error = %v", err)
	}
}

func TestBuildBundleEnforcesEnvironmentExecLimits(t *testing.T) {
	lease := GuestLease{Dir: "/dev/shm/coop-credentials/lease"}
	environmentGrant := func(name string, payload []byte) Acquired {
		return Acquired{
			Selected: Selected{Name: strings.ToLower(name), Spec: config.Credential{
				Source: config.CredentialSource{Type: "command"},
				Inject: config.CredentialInjection{Type: "environment", Name: name},
			}},
			payload: payload,
		}
	}

	if _, err := BuildBundle([]Acquired{
		environmentGrant("TOKEN", bytes.Repeat([]byte("x"), maxEnvironmentValueBytes)),
	}, lease); err != nil {
		t.Fatalf("boundary environment value rejected: %v", err)
	}
	if _, err := BuildBundle([]Acquired{
		environmentGrant("TOKEN", bytes.Repeat([]byte("x"), maxEnvironmentValueBytes+1)),
	}, lease); !errors.Is(err, ErrEnvironmentTooLarge) {
		t.Fatalf("oversized environment value error = %v", err)
	}

	var aggregate []Acquired
	for _, name := range []string{"A", "B", "C", "D"} {
		aggregate = append(aggregate, environmentGrant(name, bytes.Repeat([]byte("x"), maxEnvironmentValueBytes)))
	}
	if _, err := BuildBundle(aggregate, lease); !errors.Is(err, ErrEnvironmentTooLarge) {
		t.Fatalf("oversized aggregate environment error = %v", err)
	}
}

func TestSummariesContainOnlySafeMetadata(t *testing.T) {
	expires := time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)
	acquired := []Acquired{
		{
			Selected: Selected{Name: "git", Spec: config.Credential{Source: config.CredentialSource{Type: "file"}}},
			payload:  []byte("https://user:password@example.test"),
			metadata: Metadata{Provider: "file"},
		},
		{
			Selected: Selected{Name: "aws-dev", Spec: config.Credential{Source: config.CredentialSource{Type: "aws-profile", Profile: "dev"}}},
			aws:      &AWSCredentials{accessKeyID: "AKIASECRET", secretAccessKey: "hidden", sessionToken: "token"},
			metadata: Metadata{Provider: "aws-profile", Profile: "dev", ExpiresAt: expires},
		},
	}
	want := []string{
		"git (file; validity: source-managed)",
		"aws-dev (aws-profile dev; expires 2026-07-21T13:00:00Z)",
	}
	got := Summaries(acquired)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("summaries = %#v", got)
	}
	joined := strings.Join(got, "\n")
	for _, secret := range []string{"password", "AKIASECRET", "hidden", "token"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("summary exposed %q: %s", secret, joined)
		}
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
