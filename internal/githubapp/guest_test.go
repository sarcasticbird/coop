package githubapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectOwnerRefusesAmbiguityAndExternalHosts(t *testing.T) {
	cases := []struct {
		args                   []string
		explicit, origin, want string
	}{
		{[]string{"api", "/repos/code/repo/issues"}, "", "", "code"},
		{[]string{"api", "/orgs/team/projects"}, "", "", "team"},
		{[]string{"pr", "list", "-R", "code/repo"}, "", "", "code"},
		{[]string{"project", "list", "--owner=team"}, "", "", "team"},
		{[]string{"issue", "list"}, "", "https://github.com/code/repo.git", "code"},
		{[]string{"api", "graphql", "-f", "query=query {viewer {login}}"}, "team", "", "team"},
		{[]string{"api", "graphql"}, "", "https://github.com/code/repo.git", ""},
		{[]string{"api", "https://evil.test/repos/code/repo"}, "code", "", ""},
		{[]string{"pr", "list", "--repo", "code/repo"}, "team", "", ""},
		{[]string{"api", "/repos/code/one", "/repos/team/two"}, "", "", ""},
		{[]string{"project", "list", "--owner"}, "", "", ""},
		{[]string{"auth", "status", "--show-token=true"}, "code", "", ""},
		{[]string{"auth", "token"}, "code", "", ""},
	}
	for _, c := range cases {
		got, err := SelectOwner(c.args, c.explicit, c.origin)
		if c.want == "" {
			if err == nil {
				t.Errorf("unsafe arguments accepted: %v", c.args)
			}
		} else if err != nil || got != c.want {
			t.Errorf("route %v: %q %v", c.args, got, err)
		}
	}
}

func TestCurrentGenerationSurvivesParentAndRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	leases := filepath.Join(root, "leases")
	runtime := filepath.Join(root, "run")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, generation := range []string{"one", "two"} {
		dir := filepath.Join(leases, generation)
		if err := os.MkdirAll(filepath.Join(dir, "env"), 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "bundle.json")
		raw := fmt.Sprintf(`{"version":1,"app_id":1,"bot_login":"example[bot]","installations":{"code":{"installation_id":10,"token":%q,"expires_at":%q}}}`, generation, now.Add(time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "env", "COOP_GITHUB_APP_BUNDLE_FILE"), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(runtime, "current-credentials")
	if err := os.Symlink(filepath.Join(leases, "one"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_GITHUB_APP_BUNDLE_FILE", filepath.Join(leases, "one", "bundle.json"))
	first, err := readCurrentBundle(runtime, leases, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(leases, "two"), link+".new"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(link+".new", link); err != nil {
		t.Fatal(err)
	}
	second, err := readCurrentBundle(runtime, leases, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.document.Installations["code"].Value != "one" || second.document.Installations["code"].Value != "two" {
		t.Fatal("parent retained stale token")
	}
	if _, err := readCurrentBundle(runtime, leases, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired bundle accepted")
	}
	if err := os.WriteFile(filepath.Join(leases, "two", "env", "COOP_GITHUB_APP_BUNDLE_FILE"), []byte(filepath.Join(leases, "one", "bundle.json")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCurrentBundle(runtime, leases, now); err == nil {
		t.Fatal("cross-generation path accepted")
	}
}

func TestGitCredentialOwnerAndProtocol(t *testing.T) {
	now := time.Now().UTC()
	raw := fmt.Sprintf(`{"version":1,"app_id":1,"bot_login":"example[bot]","installations":{"code":{"installation_id":10,"token":"secret-code","expires_at":%q}}}`, now.Add(time.Hour).Format(time.RFC3339))
	b, err := ParseBundle([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []string{"protocol=https\nhost=github.com\npath=code/repo.git\n\n", "protocol=https\nhost=evil.test\npath=code/repo\n\n", "protocol=https\nhost=github.com\npath=team/repo\n\n", "protocol=https\nhost=github.com\n\n", "protocol=https\nhost=github.com\nhost=evil.test\npath=code/repo\n\n"} {
		var out strings.Builder
		err := WriteGitCredential(&out, b, strings.NewReader(req), now)
		if strings.Contains(req, "path=code/repo.git") {
			if err != nil || out.String() != "username=x-access-token\npassword=secret-code\n\n" {
				t.Fatal("credential protocol failed")
			}
		} else if err == nil || out.Len() != 0 {
			t.Fatal("unsafe Git credential disclosure")
		}
	}
}

func TestGitCredentialAcceptsRepeatedArrayCapabilities(t *testing.T) {
	now := time.Now().UTC()
	raw := fmt.Sprintf(`{"version":1,"app_id":1,"bot_login":"example[bot]","installations":{"code":{"installation_id":10,"token":"fixture","expires_at":%q}}}`, now.Add(time.Hour).Format(time.RFC3339))
	b, err := ParseBundle([]byte(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	request := "capability[]=authtype\ncapability[]=state\nprotocol=https\nhost=github.com\npath=code/repo.git\nwwwauth[]=Basic\nwwwauth[]=Bearer\n\n"
	if err := WriteGitCredential(&output, b, strings.NewReader(request), now); err != nil {
		t.Fatal("valid repeated Git array fields rejected")
	}
	if output.String() != "username=x-access-token\npassword=fixture\n\n" {
		t.Fatal("credential output changed")
	}
}

func TestChildCredentialsDisableInheritedNetworkTracing(t *testing.T) {
	env := CleanEnvironment([]string{"GH_DEBUG=api", "GH_TRACE=1", "GIT_TRACE=1", "GIT_TRACE_CURL=1", "GIT_CURL_VERBOSE=1", "GIT_TRACE2_EVENT=/tmp/leak", "PATH=/bin"})
	if strings.Join(env, "\n") != "PATH=/bin" {
		t.Fatal("secret-bearing network trace configuration inherited")
	}
}

func TestPositionalTargetsAndGHRepoAreOwnerContexts(t *testing.T) {
	for _, args := range [][]string{{"repo", "view", "team/repo"}, {"pr", "view", "https://github.com/team/repo/pull/1"}, {"issue", "view", "https://github.com/team/repo/issues/1"}, {"repo", "list", "team"}, {"repo", "clone", "team/repo"}} {
		got, err := SelectOwner(args, "", "https://github.com/code/repo.git")
		if err != nil || got != "team" {
			t.Fatalf("positional target did not select team: %v %q %v", args, got, err)
		}
		if _, err := SelectOwner(args, "code", ""); err == nil {
			t.Fatal("conflicting explicit and positional owner accepted")
		}
	}
	got, err := SelectOwnerWithRepo([]string{"issue", "list"}, "", "https://github.com/code/repo.git", "team/repo")
	if err != nil || got != "team" {
		t.Fatal("GH_REPO was ignored")
	}
	if _, err := SelectOwnerWithRepo([]string{"pr", "list", "-R", "code/repo"}, "", "", "team/repo"); err == nil {
		t.Fatal("conflicting GH_REPO accepted")
	}
}

func TestMultilineBodiesAreNotPositionalOwnerOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"pr", "create", "--title", "test", "--body", "First line\nSee https://example.test/help", "--repo", "code/repo"},
		{"pr", "comment", "1", "--body", "https://github.com/team/other", "--repo", "code/repo"},
		{"issue", "create", "--body", "line one\nline two", "--repo", "code/repo"},
	} {
		owner, err := SelectOwner(args, "", "")
		if err != nil || owner != "code" {
			t.Fatalf("legitimate body rejected: %q %v", owner, err)
		}
	}
}
