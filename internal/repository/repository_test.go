package repository

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestDiskCaseAliasesFailBeforeCloning(t *testing.T) {
	for _, kind := range []string{"organization", "repository"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			control := t.TempDir()
			tools := t.TempDir()
			org, repo := "example", "Foo"
			if kind == "organization" {
				org = "Example"
				repo = "foo"
			}
			dir := filepath.Join(root, org, repo)
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			dirty := filepath.Join(dir, "dirty.txt")
			if err := os.WriteFile(dirty, []byte("uncommitted"), 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(control, "clone-attempted")
			for name, body := range map[string]string{"flock": "#!/bin/sh\nexit 0\n", "git": "#!/bin/sh\ntouch \"$MUTATION_MARKER\"\nexit 23\n"} {
				if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", cloneScript, "uat", "example", "foo", "https://github.com/example/foo.git")
			cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "COOP_WORKSPACE_DIR="+root, "COOP_RUNTIME_DIR="+control, "MUTATION_MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "case collision") {
				t.Fatalf("disk alias not diagnosed: %v %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("git reached before disk alias validation")
			}
			data, err := os.ReadFile(dirty)
			if err != nil || string(data) != "uncommitted" {
				t.Fatal("dirty checkout changed")
			}
		})
	}
}

func TestGuestCloneScriptPreservesDirtyCheckoutAndRetriesPartialFailure(t *testing.T) {
	root, local, control, tools := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"flock": "#!/bin/sh\nexit 0\n",                                                  // Single-process fixture, not a replacement for production locking.
		"mv":    "#!/bin/sh\nif [ \"$1\" = -T ]; then shift; fi\nexec /bin/mv \"$@\"\n", // macOS mv has no -T; destinations here are absent.
	} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(control, "gitconfig")
	if err := os.WriteFile(global, []byte("[url \"file://"+filepath.ToSlash(local)+"/\"]\n insteadOf = https://github.com/example/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "COOP_WORKSPACE_DIR="+root, "COOP_RUNTIME_DIR="+control, "GIT_CONFIG_GLOBAL="+global, "GIT_CONFIG_NOSYSTEM=1")
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	provision := func(name string) (string, error) {
		cmd := exec.Command("bash", "-c", cloneScript, "uat", "example", name, "https://github.com/example/"+name+".git")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	git("init", "--bare", filepath.Join(local, "one.git"))
	if out, err := provision("one"); err != nil || !strings.HasSuffix(out, "cloned\n") {
		t.Fatalf("clone: %v %s", err, out)
	}
	dirty := filepath.Join(root, "example", "one", "dirty.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := provision("one"); err != nil || out != "existing\n" {
		t.Fatalf("existing: %v %s", err, out)
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "uncommitted" {
		t.Fatal("dirty checkout changed")
	}
	if _, err := provision("missing"); err == nil {
		t.Fatal("missing remote accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "example", "missing")); !os.IsNotExist(err) {
		t.Fatal("failed clone published")
	}
	leftovers, err := filepath.Glob(filepath.Join(root, "example", ".coop-clone.*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("partial clone left behind")
	}
	git("init", "--bare", filepath.Join(local, "missing.git"))
	if out, err := provision("missing"); err != nil || !strings.HasSuffix(out, "cloned\n") {
		t.Fatalf("retry: %v %s", err, out)
	}
	git("-C", filepath.Dir(dirty), "config", "remote.origin.url", "https://github.com/other/one.git")
	if out, err := provision("one"); err == nil || !strings.Contains(out, "origin conflicts") {
		t.Fatalf("foreign origin accepted: %v %s", err, out)
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "uncommitted" {
		t.Fatal("conflict changed dirty checkout")
	}
}

type repoRunner struct {
	response      string
	ownerResponse string
	apiError      error
	endpoints     []string
	clones        []string
	written       int
}

type appRepoRunner struct {
	calls    [][]string
	response string
}

func (r *appRepoRunner) Exec(_ context.Context, _, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	if strings.Contains(strings.Join(args, " "), "/installation/repositories") {
		_, err := io.WriteString(out, r.response)
		return err
	}
	_, err := io.WriteString(out, "existing\n")
	return err
}
func (*appRepoRunner) Attach(context.Context, string, string, []string) error { return nil }
func TestAppDiscoveryUsesInstallationPagesAndFreshOwnerGit(t *testing.T) {
	r := &appRepoRunner{response: `[{"repositories":[{"name":"one","full_name":"example/one","clone_url":"https://github.com/example/one.git"}]},{"repositories":[{"name":"two","full_name":"example/two","clone_url":"https://github.com/example/two.git"}]}]`}
	result, err := ProvisionWithOptions(context.Background(), r, "robin", []string{"example"}, Options{GitHubApp: true})
	if err != nil || len(result.Existing) != 2 {
		t.Fatalf("App provisioning failed: %+v %v", result, err)
	}
	if len(r.calls) != 3 || strings.Contains(strings.Join(r.calls[0], " "), "/user/repos") || !strings.Contains(strings.Join(r.calls[0], " "), "gh --owner example -- api --paginate --slurp /installation/repositories?per_page=100") {
		t.Fatal("wrong App discovery transport")
	}
	for _, args := range r.calls[1:] {
		if !strings.Contains(strings.Join(args, " "), "coop-github env -- bash") {
			t.Fatal("clone bypasses fresh credential environment")
		}
	}
}
func TestAppDiscoveryRejectsForeignRepositoryBeforeCloning(t *testing.T) {
	r := &appRepoRunner{response: `[{"repositories":[{"name":"one","full_name":"other/one","clone_url":"https://github.com/other/one.git"}]}]`}
	if _, err := ProvisionWithOptions(context.Background(), r, "robin", []string{"example"}, Options{GitHubApp: true}); err == nil || len(r.calls) != 1 {
		t.Fatal("foreign App scope accepted")
	}
}

func (r *repoRunner) Exec(_ context.Context, _, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
	if len(args) > 1 && args[1] == "gh" {
		endpoint := args[len(args)-1]
		r.endpoints = append(r.endpoints, endpoint)
		if strings.HasPrefix(endpoint, "/users/") {
			response := r.ownerResponse
			if response == "" {
				response = `{"login":"example","type":"Organization"}`
			}
			_, err := io.WriteString(out, response)
			return err
		}
		if r.apiError != nil {
			return r.apiError
		}
		n, err := io.WriteString(out, r.response)
		r.written = n
		return err
	}
	name := args[len(args)-1]
	r.clones = append(r.clones, name)
	_, err := io.WriteString(out, "cloned\n")
	return err
}

func TestDiscoveryIsBoundedDuringTransport(t *testing.T) {
	r := &repoRunner{response: strings.Repeat("x", (16<<20)+1)}
	if _, err := Provision(context.Background(), r, "robin", []string{"example"}); err == nil {
		t.Fatal("oversized discovery accepted")
	}
	if r.written > 16<<20 {
		t.Fatalf("buffered %d bytes before rejecting discovery", r.written)
	}
}
func (r *repoRunner) Attach(context.Context, string, string, []string) error { return nil }

func TestProvisionConsumesAllPages(t *testing.T) {
	r := &repoRunner{response: `[[{"name":"one","full_name":"example/one","clone_url":"https://github.com/example/one.git"}],[{"name":"two","full_name":"example/two","clone_url":"https://github.com/example/two.git"}]]`}
	result, err := Provision(context.Background(), r, "robin", []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cloned) != 2 || len(r.clones) != 2 {
		t.Fatalf("result=%+v calls=%q", result, r.clones)
	}
}

func TestPersonalAccountDiscoveryIncludesPrivateRepositoriesAndFiltersOtherOwners(t *testing.T) {
	r := &repoRunner{
		ownerResponse: `{"login":"example","type":"User"}`,
		response:      `[[{"name":"public","full_name":"example/public","clone_url":"https://github.com/example/public.git"},{"name":"foreign","full_name":"other/foreign","clone_url":"https://github.com/other/foreign.git"}],[{"name":"private","private":true,"full_name":"example/private","clone_url":"https://github.com/example/private.git"}]]`,
	}
	result, err := Provision(context.Background(), r, "robin", []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cloned) != 2 || result.Cloned[0].Name != "public" || result.Cloned[1].Name != "private" {
		t.Fatalf("wrong personal account checkouts: %+v", result)
	}
	if len(r.endpoints) != 2 || r.endpoints[1] != "/user/repos?affiliation=owner,collaborator&visibility=all&per_page=100" {
		t.Fatalf("private repository discovery not used: %q", r.endpoints)
	}
}

func TestPersonalAccountDiscoveryDoesNotFallBackOnAuthenticationFailure(t *testing.T) {
	r := &repoRunner{ownerResponse: `{"login":"example","type":"User"}`, apiError: fmt.Errorf("HTTP 401")}
	result, err := Provision(context.Background(), r, "robin", []string{"example"})
	if err == nil || len(result.Failed) != 1 || len(r.clones) != 0 || len(r.endpoints) != 2 {
		t.Fatalf("authentication failure not preserved: result=%+v err=%v endpoints=%q", result, err, r.endpoints)
	}
}

func TestDiscoveryRejectsInvalidOwnerMetadata(t *testing.T) {
	for _, response := range []string{`{"login":"other","type":"User"}`, `{"login":"example","type":"Bot"}`, `null`, `{`} {
		r := &repoRunner{ownerResponse: response, response: `[]`}
		if _, err := Provision(context.Background(), r, "robin", []string{"example"}); err == nil || len(r.clones) != 0 {
			t.Fatalf("invalid owner accepted: %s err=%v", response, err)
		}
	}
}
func TestProvisionRejectsTraversalAndForeignURLs(t *testing.T) {
	for _, entry := range []string{
		`{"name":"../other","full_name":"example/../other","clone_url":"https://github.com/example/../other.git"}`,
		`{"name":"one","full_name":"example/one","clone_url":"https://evil.example/example/one.git"}`,
		`{"name":"one","full_name":"other/one","clone_url":"https://github.com/other/one.git"}`,
	} {
		r := &repoRunner{response: "[[" + entry + "]]"}
		if _, err := Provision(context.Background(), r, "robin", []string{"example"}); err == nil {
			t.Fatal("unsafe discovery accepted")
		}
		if len(r.clones) != 0 {
			t.Fatal("cloned unsafe destination")
		}
	}
}
func TestProvisionRejectsCaseCollisionsBeforeCloning(t *testing.T) {
	r := &repoRunner{response: `[[{"name":"One","full_name":"example/One","clone_url":"https://github.com/example/One.git"},{"name":"one","full_name":"example/one","clone_url":"https://github.com/example/one.git"}]]`}
	_, err := Provision(context.Background(), r, "robin", []string{"example"})
	if err == nil || !strings.Contains(err.Error(), "collision") || len(r.clones) != 0 {
		t.Fatalf("err=%v clones=%q", err, r.clones)
	}
}
