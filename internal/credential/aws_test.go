package credential

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestParseAWSProcessRequiresFutureExpiration(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"Version":1,"AccessKeyId":"AKIASECRET","SecretAccessKey":"hidden","SessionToken":"token","Expiration":"2026-07-21T13:00:00Z"}`)
	creds, meta, err := ParseAWSProcess(raw, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if creds.accessKeyID == "" || creds.secretAccessKey == "" || creds.sessionToken == "" {
		t.Fatal("parsed credentials missing")
	}
	if !meta.ExpiresAt.After(now) {
		t.Fatal("expiration missing or expired")
	}
}

func TestParseAWSProcessAllowsOptionalSessionToken(t *testing.T) {
	raw := []byte(`{"Version":1,"AccessKeyId":"key","SecretAccessKey":"secret"}`)
	creds, meta, err := ParseAWSProcess(raw, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if creds.sessionToken != "" || !meta.ExpiresAt.IsZero() {
		t.Fatalf("unexpected optional fields: %+v %+v", creds, meta)
	}
}

func TestParseAWSProcessRejectsInvalidDocumentsWithoutSecrets(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	secrets := []string{"AKIA_DO_NOT_PRINT", "SECRET_DO_NOT_PRINT", "TOKEN_DO_NOT_PRINT"}
	tests := map[string]string{
		"wrong version":  `{"Version":2,"AccessKeyId":"AKIA_DO_NOT_PRINT","SecretAccessKey":"SECRET_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT"}`,
		"missing key":    `{"Version":1,"SecretAccessKey":"SECRET_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT"}`,
		"missing secret": `{"Version":1,"AccessKeyId":"AKIA_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT"}`,
		"missing expiry": `{"Version":1,"AccessKeyId":"AKIA_DO_NOT_PRINT","SecretAccessKey":"SECRET_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT"}`,
		"expired":        `{"Version":1,"AccessKeyId":"AKIA_DO_NOT_PRINT","SecretAccessKey":"SECRET_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT","Expiration":"2026-07-21T11:00:00Z"}`,
		"bad expiry":     `{"Version":1,"AccessKeyId":"AKIA_DO_NOT_PRINT","SecretAccessKey":"SECRET_DO_NOT_PRINT","SessionToken":"TOKEN_DO_NOT_PRINT","Expiration":"tomorrow"}`,
		"malformed json": `{"Version":1,`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseAWSProcess([]byte(raw), now, true)
			if err == nil {
				t.Fatal("invalid AWS process document accepted")
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed secret: %v", err)
				}
			}
		})
	}
}

func TestAcquireAWSProfileUsesExactArgvAndZeroesRawPayload(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	want := []string{"aws", "configure", "export-credentials", "--profile", "dev", "--format", "process"}
	raw := []byte(`{"Version":1,"AccessKeyId":"key","SecretAccessKey":"secret","Expiration":"2026-07-21T13:00:00Z"}`)
	mgr := NewManager(t.TempDir())
	mgr.Now = func() time.Time { return now }
	var got []string
	mgr.Run = func(_ context.Context, _ string, argv []string) ([]byte, error) {
		got = slices.Clone(argv)
		return raw, nil
	}

	acquired, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
		Name: "aws-dev",
		Spec: config.Credential{
			Source:            config.CredentialSource{Type: "aws-profile", Profile: "dev"},
			Inject:            config.CredentialInjection{Type: "aws"},
			RequireExpiration: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v", got)
	}
	if acquired[0].aws == nil || acquired[0].aws.accessKeyID != "key" {
		t.Fatal("structured AWS credentials missing")
	}
	for _, b := range raw {
		if b != 0 {
			t.Fatal("raw AWS payload was not zeroed")
		}
	}
}
