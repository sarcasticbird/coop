package credential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestAcquireGitCredentialUsesFillProtocolAndReturnsRedactedMaterial(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	helperDir := t.TempDir()
	requestPath := filepath.Join(t.TempDir(), "request")
	gitPath := filepath.Join(helperDir, "git")
	script := "#!/bin/sh\n" +
		"test \"$1\" = credential || exit 41\n" +
		"test \"$2\" = fill || exit 42\n" +
		"cat > " + shellSingleQuote(requestPath) + "\n" +
		"printf 'protocol=https\\nhost=github.com\\npath=sarcasticbird\\nusername=coop-user\\npassword=test-token\\n'\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr := NewManager(project)
	acquired, err := mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "github",
		Spec: config.Credential{Source: config.CredentialSource{
			Type: "git-credential",
			URL:  "https://github.com/sarcasticbird",
		}},
	}})
	if err != nil {
		t.Fatalf("acquire Git credential: %v", err)
	}
	if len(acquired) != 1 || acquired[0].Metadata().Provider != "git-credential" {
		t.Fatalf("metadata = %+v", acquired)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", acquired[0])); got != "<credential redacted>" {
		t.Fatalf("formatted acquisition = %q", got)
	}
	request, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(request), "protocol=https\nhost=github.com\npath=sarcasticbird\n\n"; got != want {
		t.Fatalf("Git credential request = %q, want %q", got, want)
	}
}

func TestAcquireGitCredentialParsesActualGitFillOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	if err := os.Mkdir(xdg, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	gitConfig := `[credential]
	helper =
	helper = "!f() { printf 'username=coop-user\\npassword=test-token\\n'; }; f"
[credential "https://github.com"]
	useHttpPath = true
`
	// The helper intentionally omits protocol, host, and path. Git merges those
	// request fields into fill output; this verifies the fixed request reaches
	// the helper, not that Git attests the helper's backend record key.
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(t.TempDir())
	acquired, err := mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "github",
		Spec: config.Credential{Source: config.CredentialSource{
			Type: "git-credential",
			URL:  "https://github.com/sarcasticbird",
		}},
	}})
	if err != nil {
		t.Fatalf("parse actual git credential fill output: %v", err)
	}
	if len(acquired) != 1 || acquired[0].userPass == nil {
		t.Fatalf("actual Git acquisition = %v", acquired)
	}
}

func TestParseGitCredentialResponseStrictly(t *testing.T) {
	target := gitCredentialTarget{protocol: "https", host: "github.com", path: "sarcasticbird"}
	valid := "password=test-token\npath=sarcasticbird\nprotocol=https\nusername=coop-user\nhost=github.com\n"
	material, err := parseGitCredentialResponse(target, []byte(valid))
	if err != nil {
		t.Fatalf("valid unordered response rejected: %v", err)
	}
	if material.protocol != "https" || material.host != "github.com" || material.path != "sarcasticbird" || material.username != "coop-user" || material.password != "test-token" {
		t.Fatalf("parsed material = %v", material)
	}
	hostOnlyTarget := gitCredentialTarget{protocol: "https", host: "github.com"}
	withoutPath := "protocol=https\nhost=github.com\nusername=coop-user\npassword=test-token\n"
	material, err = parseGitCredentialResponse(hostOnlyTarget, []byte(withoutPath))
	if err != nil {
		t.Fatalf("host-only response rejected: %v", err)
	}
	if material.path != "" {
		t.Fatalf("host-only material path = %q", material.path)
	}
	withExtensions := "protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token\npassword_expiry_utc=1893456000\noauth_refresh_token=refresh-token\n"
	material, err = parseGitCredentialResponse(target, []byte(withExtensions))
	if err != nil {
		t.Fatalf("response with supported Git credential extensions rejected: %v", err)
	}
	if got := fmt.Sprintf("%v", material); strings.Contains(got, "refresh-token") || strings.Contains(got, "test-token") {
		t.Fatalf("extended credential formatting exposed secret material: %s", got)
	}

	cases := map[string][]byte{
		"missing protocol":     []byte("host=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token\n"),
		"empty username":       []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=\npassword=test-token\n"),
		"duplicate password":   []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=one\npassword=two\n"),
		"malformed line":       []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername\npassword=test-token\n"),
		"NUL byte":             []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test\x00token\n"),
		"unsupported field":    []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token\ntest-token=hidden\n"),
		"protocol mismatch":    []byte("protocol=http\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token\n"),
		"host mismatch":        []byte("protocol=https\nhost=evil.example\npath=sarcasticbird\nusername=coop-user\npassword=test-token\n"),
		"path mismatch":        []byte("protocol=https\nhost=github.com\npath=other\nusername=coop-user\npassword=test-token\n"),
		"missing trusted path": []byte("protocol=https\nhost=github.com\nusername=coop-user\npassword=test-token\n"),
		"missing terminator":   []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token"),
		"multiple terminators": []byte("protocol=https\nhost=github.com\npath=sarcasticbird\nusername=coop-user\npassword=test-token\n\n"),
		"oversized response":   bytes.Repeat([]byte("x"), MaxPayloadBytes+1),
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseGitCredentialResponse(target, response)
			if err == nil {
				t.Fatal("invalid Git credential response accepted")
			}
			if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "coop-user") || strings.Contains(err.Error(), "evil.example") {
				t.Fatalf("error exposed credential response data: %v", err)
			}
			if name == "oversized response" && !errors.Is(err, ErrPayloadTooLarge) {
				t.Fatalf("oversized error = %v", err)
			}
		})
	}
}

func TestAcquireGitCredentialPreservesSafeLimitsAndSanitizesRunnerErrors(t *testing.T) {
	selected := []Selected{{
		Name: "github",
		Spec: config.Credential{Source: config.CredentialSource{
			Type: "git-credential",
			URL:  "https://github.com/acme",
		}},
	}}

	t.Run("payload limit", func(t *testing.T) {
		mgr := NewManager(t.TempDir())
		mgr.GitCredential = func(context.Context, string, []byte) ([]byte, error) {
			return nil, ErrPayloadTooLarge
		}
		_, err := mgr.AcquireAll(context.Background(), t.TempDir(), selected)
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("payload limit error = %v", err)
		}
	})

	t.Run("oversized successful response", func(t *testing.T) {
		mgr := NewManager(t.TempDir())
		mgr.GitCredential = func(context.Context, string, []byte) ([]byte, error) {
			return bytes.Repeat([]byte("x"), MaxPayloadBytes+1), nil
		}
		_, err := mgr.AcquireAll(context.Background(), t.TempDir(), selected)
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("oversized response error = %v", err)
		}
	})

	t.Run("untrusted runner error", func(t *testing.T) {
		mgr := NewManager(t.TempDir())
		mgr.GitCredential = func(context.Context, string, []byte) ([]byte, error) {
			return nil, errors.New("helper printed test-token")
		}
		_, err := mgr.AcquireAll(context.Background(), t.TempDir(), selected)
		if err == nil || strings.Contains(err.Error(), "test-token") {
			t.Fatalf("runner error was not sanitized: %v", err)
		}
	})
}

func TestAcquireGitCredentialCancellationReturnsPromptlyAndRedactsRunnerError(t *testing.T) {
	mgr := NewManager(t.TempDir())
	started := make(chan struct{})
	mgr.GitCredential = func(ctx context.Context, _ string, _ []byte) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, errors.New("helper cancellation exposed test-token")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	home := t.TempDir()
	go func() {
		_, err := mgr.AcquireAll(ctx, home, []Selected{{
			Name: "github",
			Spec: config.Credential{Source: config.CredentialSource{
				Type: "git-credential",
				URL:  "https://github.com/acme",
			}},
		}})
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "test-token") {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Git credential acquisition remained blocked after cancellation")
	}
}

func TestBoundedBufferStopsWriterAtLimit(t *testing.T) {
	buffer := &boundedBuffer{limit: 4}
	written, err := buffer.Write([]byte("abcdef"))
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized write error = %v", err)
	}
	if written != 6 {
		t.Fatalf("reported write length = %d, want 6", written)
	}
	if got := string(buffer.data); got != "abcd" {
		t.Fatalf("bounded data = %q, want %q", got, "abcd")
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
