package githubapp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/credential"
)

const GuestHelper = "/usr/local/libexec/coop-github"
const RuntimeDir = "/run/coop"
const CoreGH = "/opt/coop-core/.flox/run/aarch64-linux.coop-core-run/bin/gh"

func ReadCurrentBundle(runtimeDir string, now time.Time) (Bundle, error) {
	return readCurrentBundle(runtimeDir, "/dev/shm/coop-credentials", now)
}
func readCurrentBundle(runtimeDir, leaseRoot string, now time.Time) (Bundle, error) {
	lease, err := filepath.EvalSymlinks(filepath.Join(runtimeDir, "current-credentials"))
	if err != nil {
		return Bundle{}, errors.New("no current GitHub App credentials; refresh on the host")
	}
	root, err := filepath.EvalSymlinks(leaseRoot)
	if err != nil {
		return Bundle{}, errors.New("invalid GitHub App credential storage")
	}
	if filepath.Dir(lease) != root {
		return Bundle{}, errors.New("invalid GitHub App credential generation")
	}
	raw, err := credential.ReadPrivateFile(filepath.Join(lease, "env", "COOP_GITHUB_APP_BUNDLE_FILE"))
	if err != nil {
		return Bundle{}, errors.New("GitHub App credential grant is not staged")
	}
	path := strings.TrimSpace(string(raw))
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	path = filepath.Join(parent, filepath.Base(path))
	if parentErr != nil || !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), lease+string(filepath.Separator)) {
		return Bundle{}, errors.New("GitHub App bundle escapes its current generation")
	}
	raw, err = credential.ReadPrivateFile(path)
	if err != nil {
		return Bundle{}, errors.New("cannot read current GitHub App credentials")
	}
	return ParseBundle(raw, now)
}

func WriteGitCredential(w io.Writer, b Bundle, request io.Reader, now time.Time) error {
	fields := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(request, 16<<10+1))
	scanner.Buffer(make([]byte, 1024), 16<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid Git credential request")
		}
		// Git's capability[], wwwauth[] and other array attributes repeat.
		// They do not select an owner or affect this username/password helper.
		if strings.HasSuffix(key, "[]") {
			continue
		}
		if _, exists := fields[key]; exists {
			return errors.New("duplicate Git credential field")
		}
		fields[key] = value
	}
	if scanner.Err() != nil {
		return errors.New("git credential request exceeds limit")
	}
	if fields["protocol"] != "https" || fields["host"] != "github.com" {
		return errors.New("git credentials are restricted to HTTPS github.com")
	}
	owner, err := repoOwner(fields["path"])
	if err != nil {
		return err
	}
	if _, err = b.Environment(owner, nil, now); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "username=x-access-token\npassword=%s\n\n", b.document.Installations[owner].Value); err != nil {
		return errors.New("cannot write Git credentials")
	}
	return nil
}

func VerifyApp(b Bundle, expected string) error {
	id, err := strconv.ParseInt(expected, 10, 64)
	if err != nil || id <= 0 || b.document.AppID != id {
		return errors.New("GitHub App identity differs from this guest's configured identity")
	}
	return nil
}
func ExpectedAppID() (string, error) {
	raw, err := os.ReadFile("/var/lib/coop/github-app-id")
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	if !os.IsNotExist(err) {
		return "", errors.New("cannot read configured GitHub App identity")
	}
	if value := os.Getenv("COOP_GITHUB_APP_ID"); value != "" {
		return value, nil
	}
	return "", errors.New("GitHub App identity is not configured for this guest")
}
