package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testRegistration() Registration {
	return Registration{AppID: 1, Slug: "example", AppOwner: "team", PrivateKeyPath: "/private/key.pem", BotLogin: "example[bot]", BotID: 3, Installations: map[string]Installation{
		"code": {ID: 10, Repositories: "all", Permissions: map[string]string{"contents": "write", "metadata": "read"}},
		"team": {ID: 20, Repositories: "all", Permissions: map[string]string{"organization_projects": "write", "metadata": "read"}},
	}}
}

func TestMintValidatesIdentityPermissionsAndSignedJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, problem := range []string{"", "wrong-app", "wrong-owner", "wrong-install", "permissions", "expired", "error-secret", "redirect"} {
		t.Run(problem, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
				if len(parts) != 3 {
					t.Error("missing JWT")
					http.Error(w, "bad", http.StatusUnauthorized)
					return
				}
				sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
				digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
				if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
					t.Error("invalid signature")
				}
				var claims struct {
					Iat int64  `json:"iat"`
					Exp int64  `json:"exp"`
					Iss string `json:"iss"`
				}
				raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
				if err := json.Unmarshal(raw, &claims); err != nil {
					t.Error(err)
				}
				if claims.Iss != "1" || claims.Iat > now.Unix() || claims.Exp > now.Add(10*time.Minute).Unix() || claims.Exp <= now.Unix() {
					t.Error("invalid JWT claims")
				}
				w.Header().Set("Content-Type", "application/json")
				if problem == "error-secret" {
					http.Error(w, "DO_NOT_PRINT", http.StatusForbidden)
					return
				}
				if problem == "redirect" {
					http.Redirect(w, r, "/stolen", http.StatusFound)
					return
				}
				switch r.URL.Path {
				case "/app":
					id := 1
					if problem == "wrong-app" {
						id = 2
					}
					if _, err := fmt.Fprintf(w, `{"id":%d,"slug":"example","owner":{"login":"team"}}`, id); err != nil {
						t.Fatal(err)
					}
				case "/app/installations":
					owner := "code"
					if problem == "wrong-owner" {
						owner = "other"
					}
					id := 10
					if problem == "wrong-install" {
						id = 99
					}
					if _, err := fmt.Fprintf(w, `[{"id":%d,"app_id":1,"account":{"login":%q},"repository_selection":"all","permissions":{"contents":"write","metadata":"read"}},{"id":20,"app_id":1,"account":{"login":"team"},"repository_selection":"all","permissions":{"organization_projects":"write","metadata":"read"}}]`, id, owner); err != nil {
						t.Fatal(err)
					}
				case "/app/installations/10/access_tokens", "/app/installations/20/access_tokens":
					if r.Method != "POST" {
						t.Error("token request was not POST")
					}
					perms := `{"contents":"write","metadata":"read"}`
					if strings.Contains(r.URL.Path, "/20/") {
						perms = `{"organization_projects":"write","metadata":"read"}`
					}
					if problem == "permissions" {
						perms = `{"metadata":"read"}`
					}
					expiry := now.Add(time.Hour)
					if problem == "expired" {
						expiry = now.Add(-time.Second)
					}
					if _, err := fmt.Fprintf(w, `{"token":%q,"expires_at":%q,"permissions":%s}`, "DO_NOT_PRINT"+strings.Repeat("x", 1500), expiry.Format(time.RFC3339), perms); err != nil {
						t.Fatal(err)
					}
				default:
					t.Error("unexpected endpoint", r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			client := Client{HTTP: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }, ReadKey: func(string) ([]byte, error) { return keyPEM, nil }}
			b, err := client.Mint(context.Background(), testRegistration())
			if problem != "" {
				if err == nil {
					t.Fatal("unsafe mint succeeded")
				}
				if strings.Contains(err.Error(), "DO_NOT_PRINT") {
					t.Fatal("secret in error")
				}
				if problem == "redirect" && calls != 1 {
					t.Fatal("redirect followed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			if err := b.Encode(&output); err != nil {
				t.Fatal(err)
			}
			again, err := ParseBundle([]byte(output.String()), now)
			if err != nil {
				t.Fatal(err)
			}
			if len(again.Status().Installations) != 2 {
				t.Fatal("missing owner credentials")
			}
			for _, format := range []string{"%v", "%+v", "%#v"} {
				if strings.Contains(fmt.Sprintf(format, b), "DO_NOT_PRINT") {
					t.Fatal("secret formatting")
				}
			}
		})
	}
}

func TestRegistrationRejectsCrossAgentAndInvalidScope(t *testing.T) {
	for _, mutate := range []func(*Registration){
		func(r *Registration) { r.AppID = 0 }, func(r *Registration) { r.BotLogin = "human" }, func(r *Registration) { r.PrivateKeyPath = "relative" },
		func(r *Registration) { r.Installations["bad/owner"] = Installation{ID: 30} }, func(r *Registration) { v := r.Installations["team"]; v.ID = 10; r.Installations["team"] = v },
	} {
		r := testRegistration()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
}

func TestBundleRejectsExpiredMalformedAndSecretDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	valid := fmt.Sprintf(`{"version":1,"app_id":1,"bot_login":"example[bot]","installations":{"code":{"installation_id":10,"token":"DO_NOT_PRINT","expires_at":%q,"permissions":{"contents":"read"}}}}`, now.Add(time.Hour).Format(time.RFC3339))
	for _, raw := range []string{"DO_NOT_PRINT", strings.Replace(valid, `"version":1`, `"version":99`, 1), strings.Replace(valid, "code", "bad/owner", 1), strings.Replace(valid, now.Add(time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339), 1), strings.Replace(valid, "DO_NOT_PRINT", "bad\\ntoken", 1)} {
		_, err := ParseBundle([]byte(raw), now)
		if err == nil {
			t.Fatal("invalid bundle accepted")
		}
		if strings.Contains(err.Error(), "DO_NOT_PRINT") {
			t.Fatal("secret diagnostic")
		}
	}
	b, err := ParseBundle([]byte(valid), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Environment("other", []string{"GH_TOKEN=HUMAN_SECRET"}, now)
	if err == nil {
		t.Fatal("unknown owner accepted")
	}
	_, err = b.Environment("code", []string{"GH_TOKEN=HUMAN_SECRET"}, now.Add(2*time.Hour))
	if err == nil {
		t.Fatal("expired token used")
	}
	env, err := b.Environment("code", []string{"GH_TOKEN=HUMAN_SECRET", "GITHUB_TOKEN=HUMAN_SECRET", "PATH=/bin"}, now)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "HUMAN_SECRET") || !strings.Contains(joined, "GH_TOKEN=DO_NOT_PRINT") {
		t.Fatal("credential isolation failed")
	}
	if err := b.Encode(io.Discard); err != nil {
		t.Fatal(err)
	}
}
