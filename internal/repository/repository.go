// Package repository provisions guest-owned clones without altering existing
// branches or working trees.
package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/sarcasticbird/coop/internal/githubapp"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type Entry struct{ Org, Name, Path, Error string }
type Result struct{ Cloned, Existing, Failed []Entry }
type githubRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var organizationName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

type discoveryBuffer struct{ bytes.Buffer }

func (b *discoveryBuffer) WriteString(s string) (int, error) { return b.Write([]byte(s)) }
func (b *discoveryBuffer) Write(p []byte) (int, error) {
	remaining := (16 << 20) - b.Len()
	if len(p) > remaining {
		n, _ := b.Buffer.Write(p[:remaining])
		return n, fmt.Errorf("organization discovery exceeds 16 MiB")
	}
	return b.Buffer.Write(p)
}

const cloneScript = `set -euo pipefail
umask 077
org=$1
name=$2
url=$3
root=${COOP_WORKSPACE_DIR:-/workspace}
control=${COOP_RUNTIME_DIR:-/run/coop}
mkdir -p "$control"
exec 9>"$control/repositories.lock"
flock -x 9
test ! -L "$root"
test ! -L "$root/$org"
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
# API-page validation cannot detect a case-only rename against a checkout
# already on disk. Refuse aliases before creating directories or invoking Git.
for candidate in "$root"/*; do
  spelling=${candidate##*/}
  if [[ "$spelling" != "$org" && $(lower "$spelling") == $(lower "$org") ]]; then
    echo "organization case collision: $candidate and $root/$org" >&2; exit 1
  fi
done
mkdir -p "$root/$org"
for candidate in "$root/$org"/*; do
  spelling=${candidate##*/}
  if [[ "$spelling" != "$name" && $(lower "$spelling") == $(lower "$name") ]]; then
    echo "repository case collision: $candidate and $root/$org/$name" >&2; exit 1
  fi
done
dest=$root/$org/$name
if [[ -e "$dest" || -L "$dest" ]]; then
  test ! -L "$dest" || { echo 'destination is a symlink' >&2; exit 1; }
  test -d "$dest/.git" || { echo 'destination is not a repository' >&2; exit 1; }
  origin=$(git -C "$dest" config --get remote.origin.url)
  canonical=${origin%.git}
  canonical=${canonical#https://github.com/}
  canonical=${canonical#git@github.com:}
  expected=$org/$name
  [[ $(lower "$canonical") == $(lower "$expected") ]] || { echo 'existing repository origin conflicts' >&2; exit 1; }
  printf 'existing\n'
  exit 0
fi
tmp=$(mktemp -d "$root/$org/.coop-clone.XXXXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
git clone -- "$url" "$tmp"
[[ $(git -C "$tmp" config --get remote.origin.url) == "$url" ]]
test ! -e "$dest" && test ! -L "$dest"
mv -T -- "$tmp" "$dest"
trap - EXIT
printf 'cloned\n'`

func validateRepo(org string, r githubRepo) error {
	if !repositoryName.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return fmt.Errorf("invalid repository name %q", r.Name)
	}
	if !strings.EqualFold(r.FullName, org+"/"+r.Name) {
		return fmt.Errorf("repository %q is outside organization %q", r.FullName, org)
	}
	u, err := url.Parse(r.CloneURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Path, "/"+r.FullName+".git") || u.RawPath != "" {
		return fmt.Errorf("repository %q has an unsupported clone URL", r.FullName)
	}
	return nil
}

// GitHub's /users/{owner}/repos endpoint exposes public repositories only.
// Personal accounts need authenticated discovery, then an explicit owner filter.
func discoveryEndpoint(ctx context.Context, runner workspace.Runner, name, owner string) (string, bool, error) {
	var out discoveryBuffer
	args := []string{"/usr/local/libexec/coop-env", "gh", "api", "/users/" + owner}
	if err := runner.Exec(ctx, name, "/workspace", args, nil, &out, io.Discard); err != nil {
		return "", false, fmt.Errorf("resolve GitHub owner: %w", err)
	}
	var account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(out.Bytes(), &account); err != nil {
		return "", false, fmt.Errorf("parse GitHub owner: %w", err)
	}
	if !strings.EqualFold(account.Login, owner) {
		return "", false, fmt.Errorf("GitHub owner response does not match %q", owner)
	}
	switch account.Type {
	case "Organization":
		return "/orgs/" + owner + "/repos?per_page=100", false, nil
	case "User":
		return "/user/repos?affiliation=owner,collaborator&visibility=all&per_page=100", true, nil
	default:
		return "", false, fmt.Errorf("unsupported GitHub owner type %q", account.Type)
	}
}

func Provision(ctx context.Context, runner workspace.Runner, name string, orgs []string) (Result, error) {
	return ProvisionWithOptions(ctx, runner, name, orgs, Options{})
}

type Options struct{ GitHubApp bool }

func ProvisionWithOptions(ctx context.Context, runner workspace.Runner, name string, orgs []string, opts Options) (Result, error) {
	var result Result
	var failures []error
	for _, org := range orgs {
		if !organizationName.MatchString(org) {
			return result, fmt.Errorf("invalid organization %q", org)
		}
		var endpoint string
		var personal bool
		var err error
		if opts.GitHubApp {
			endpoint = "/installation/repositories?per_page=100"
		} else {
			endpoint, personal, err = discoveryEndpoint(ctx, runner, name, org)
		}
		if err != nil {
			result.Failed = append(result.Failed, Entry{Org: org, Error: err.Error()})
			failures = append(failures, fmt.Errorf("discover %s: %w", org, err))
			continue
		}
		var out discoveryBuffer
		args := []string{"/usr/local/libexec/coop-env", "gh", "api", "--paginate", "--slurp", endpoint}
		if opts.GitHubApp {
			args = []string{"/usr/local/libexec/coop-env", githubapp.GuestHelper, "gh", "--owner", org, "--", "api", "--paginate", "--slurp", endpoint}
		}
		if err := runner.Exec(ctx, name, "/workspace", args, nil, &out, io.Discard); err != nil {
			result.Failed = append(result.Failed, Entry{Org: org, Error: err.Error()})
			failures = append(failures, fmt.Errorf("discover %s: %w", org, err))
			continue
		}
		if out.Len() > 16<<20 {
			return result, fmt.Errorf("organization discovery exceeds 16 MiB")
		}
		var pages [][]githubRepo
		var parseErr error
		if opts.GitHubApp {
			var envelopes []struct {
				Repositories []githubRepo `json:"repositories"`
			}
			parseErr = json.Unmarshal(out.Bytes(), &envelopes)
			for _, page := range envelopes {
				pages = append(pages, page.Repositories)
			}
		} else {
			parseErr = json.Unmarshal(out.Bytes(), &pages)
		}
		if parseErr != nil {
			return result, fmt.Errorf("parse repository discovery: %w", parseErr)
		}
		var repos []githubRepo
		seen := map[string]string{}
		for _, page := range pages {
			for _, r := range page {
				owner, _, qualified := strings.Cut(r.FullName, "/")
				if personal && qualified && !strings.EqualFold(owner, org) {
					continue
				}
				if err := validateRepo(org, r); err != nil {
					return result, err
				}
				key := strings.ToLower(r.Name)
				if previous, ok := seen[key]; ok {
					if previous != r.Name {
						return result, fmt.Errorf("case collision between repositories %q and %q", previous, r.Name)
					}
					continue
				}
				seen[key] = r.Name
				repos = append(repos, r)
			}
		}
		for _, r := range repos {
			entry := Entry{Org: org, Name: r.Name, Path: "/workspace/" + org + "/" + r.Name}
			var status bytes.Buffer
			var diagnostic bytes.Buffer
			argv := []string{"/usr/local/libexec/coop-env", "bash", "-c", cloneScript, "coop-clone", org, r.Name, r.CloneURL}
			if opts.GitHubApp {
				argv = append([]string{"/usr/local/libexec/coop-env", githubapp.GuestHelper, "env", "--"}, argv[1:]...)
			}
			err := runner.Exec(ctx, name, "/workspace", argv, nil, &status, &diagnostic)
			if err != nil {
				entry.Error = err.Error()
				result.Failed = append(result.Failed, entry)
				failures = append(failures, fmt.Errorf("clone %s: %w", r.FullName, err))
				continue
			}
			switch strings.TrimSpace(status.String()) {
			case "cloned":
				result.Cloned = append(result.Cloned, entry)
			case "existing":
				result.Existing = append(result.Existing, entry)
			default:
				return result, fmt.Errorf("unexpected clone result for %s", r.FullName)
			}
		}
	}
	return result, errors.Join(failures...)
}
