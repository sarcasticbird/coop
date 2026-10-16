package hostmcp

import (
	"context"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Two maximum output pages can expand through JSON escaping and the duplicate
// text/structured envelope. Keep the response bounded above that worst case.
const maxResponseBytes = 4 << 20

type ClientConfig struct {
	Endpoint       string `toml:"endpoint"`
	TokenEnv       string `toml:"token_env"`
	TokenFile      string `toml:"token_file"`
	ExpectedHostID string `toml:"expected_host_id"`
}

func (c ClientConfig) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/mcp" || !regexp.MustCompile(`^h1_[0-9a-f]{32}$`).MatchString(c.ExpectedHostID) || (c.TokenEnv == "") == (c.TokenFile == "") {
		return &hostcontrol.Error{Code: "invalid_client_config"}
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return &hostcontrol.Error{Code: "remote_https_required"}
		}
	}
	return nil
}
func LoadClientConfig(path string) (ClientConfig, error) {
	var c ClientConfig
	raw, err := hostcontrol.ReadPrivateFile(path, 1<<20)
	if err != nil {
		return c, err
	}
	meta, err := toml.Decode(string(raw), &c)
	if err != nil || len(meta.Undecoded()) > 0 {
		return c, &hostcontrol.Error{Code: "invalid_client_config"}
	}
	return c, c.Validate()
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.base.RoundTrip(clone)
	if err == nil {
		resp.Body = &boundedBody{ReadCloser: resp.Body, remaining: maxResponseBytes + 1}
	}
	return resp, err
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("MCP response exceeds limit")
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func newHTTPClient(token string) *http.Client {
	return &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: token}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func connect(ctx context.Context, cfg ClientConfig) (*mcp.ClientSession, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	token := os.Getenv(cfg.TokenEnv)
	if cfg.TokenFile != "" {
		var err error
		token, err = hostcontrol.ReadCredentialFile(cfg.TokenFile)
		if err != nil {
			return nil, err
		}
	}
	if token == "" || len(token) > 512 || strings.ContainsAny(token, "\r\n") {
		return nil, &hostcontrol.Error{Code: "invalid_client_credential"}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "coop-stdio", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: cfg.Endpoint, HTTPClient: newHTTPClient(token), MaxEventSize: maxResponseBytes}, nil)
	if err != nil {
		return nil, &hostcontrol.Error{Code: "host_unavailable"}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_host_info", Arguments: map[string]any{}})
	if err != nil || !matchesHost(result, cfg.ExpectedHostID) {
		_ = session.Close()
		return nil, &hostcontrol.Error{Code: "unexpected_host"}
	}
	return session, nil
}
