package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/credential"
)

type Client struct {
	HTTP    *http.Client
	BaseURL string
	Now     func() time.Time
	ReadKey func(string) ([]byte, error)
}

func (c Client) Mint(ctx context.Context, r Registration) (Bundle, error) {
	if err := r.Validate(); err != nil {
		return Bundle{}, err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	readKey := c.ReadKey
	if readKey == nil {
		readKey = credential.ReadPrivateFile
	}
	raw, err := readKey(r.PrivateKeyPath)
	if err != nil {
		return Bundle{}, errors.New("cannot read private App signing key; verify file permissions")
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return Bundle{}, errors.New("invalid App signing key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		anyKey, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return Bundle{}, errors.New("invalid App signing key")
		}
		var ok bool
		key, ok = anyKey.(*rsa.PrivateKey)
		if !ok {
			return Bundle{}, errors.New("app signing key must be RSA")
		}
	}
	if key.N.BitLen() < 2048 {
		return Bundle{}, errors.New("app signing key must be at least 2048 bits")
	}
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(r.AppID, 10)})
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return Bundle{}, errors.New("cannot sign GitHub App request")
	}
	jwt := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	client := http.Client{Timeout: 30 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	request := func(method, path string, body any, out any) error {
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				return errors.New("invalid App API request")
			}
		}
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, e := http.NewRequestWithContext(requestCtx, method, base+path, bytes.NewReader(data))
		if e != nil {
			return errors.New("invalid App API endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			return errors.New("GitHub App API transport failed")
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("GitHub App API %s failed with HTTP %d", method, resp.StatusCode)
		}
		raw, e := readBounded(resp.Body)
		if e != nil {
			return e
		}
		if json.Unmarshal(raw, out) != nil {
			return errors.New("invalid GitHub App API response")
		}
		return nil
	}
	var app struct {
		ID    int64  `json:"id"`
		Slug  string `json:"slug"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := request("GET", "/app", nil, &app); err != nil {
		return Bundle{}, err
	}
	if app.ID != r.AppID || app.Slug != r.Slug || !strings.EqualFold(app.Owner.Login, r.AppOwner) {
		return Bundle{}, errors.New("GitHub App identity does not match registration")
	}
	type installed struct {
		ID      int64 `json:"id"`
		AppID   int64 `json:"app_id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
		Selection   string            `json:"repository_selection"`
		Permissions map[string]string `json:"permissions"`
		SuspendedAt *time.Time        `json:"suspended_at"`
	}
	installations := map[int64]installed{}
	for page := 1; page <= 100; page++ {
		var rows []installed
		path := "/app/installations"
		if page > 1 {
			path += fmt.Sprintf("?per_page=30&page=%d", page)
		}
		if err := request("GET", path, nil, &rows); err != nil {
			return Bundle{}, err
		}
		for _, i := range rows {
			installations[i.ID] = i
		}
		if len(rows) < 30 {
			break
		}
		if page == 100 {
			return Bundle{}, errors.New("too many GitHub App installations")
		}
	}
	d := wireBundle{Version: 1, AppID: r.AppID, BotLogin: r.BotLogin, Installations: map[string]token{}}
	owners := make([]string, 0, len(r.Installations))
	for owner := range r.Installations {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		expected := r.Installations[owner]
		actual, ok := installations[expected.ID]
		if !ok || actual.AppID != r.AppID || !strings.EqualFold(actual.Account.Login, owner) || actual.Selection != "all" || actual.SuspendedAt != nil || !permissionsCover(actual.Permissions, expected.Permissions) {
			return Bundle{}, errors.New("GitHub App installation identity or permissions mismatch")
		}
		var response struct {
			Token       string            `json:"token"`
			ExpiresAt   time.Time         `json:"expires_at"`
			Permissions map[string]string `json:"permissions"`
		}
		if err := request("POST", fmt.Sprintf("/app/installations/%d/access_tokens", expected.ID), map[string]any{"permissions": expected.Permissions}, &response); err != nil {
			return Bundle{}, err
		}
		if !permissionsCover(response.Permissions, expected.Permissions) {
			return Bundle{}, errors.New("GitHub App token has insufficient permissions")
		}
		d.Installations[strings.ToLower(owner)] = token{InstallationID: expected.ID, Value: response.Token, ExpiresAt: response.ExpiresAt, Permissions: response.Permissions}
	}
	encoded, _ := json.Marshal(d)
	return ParseBundle(encoded, now)
}
func permissionsCover(actual, expected map[string]string) bool {
	for key, want := range expected {
		got := actual[key]
		if got != want && (want != "read" || got != "write") {
			return false
		}
	}
	return true
}
