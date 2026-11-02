//go:build ignore

// Records public source snapshots and supplemental native identity only. All
// GitHub/master operations here are reads; no private QA body is exported.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/masterclient"
	"github.com/sarcasticbird/coop/internal/worker"
)

func main() {
	out := flag.String("output", "", "existing disposable fixture output directory")
	config := flag.String("master-config", "", "own authenticated read-only master client configuration")
	flag.Parse()
	if *out == "" || *config == "" {
		panic("explicit output and master-config required")
	}
	info, err := os.Stat(*out)
	check(err)
	if !info.IsDir() {
		panic("output is not a directory")
	}
	cl, err := masterclient.LoadConfig(*config)
	check(err)
	heads := []string{"99587865a8133a67311dcb1b65e67a59562eedb5", "d5d8dea1f0f71a81176c4f1eda94dcf8e06b642a", "bc46cae6770733dfd9d55afd0eff8515101d0622", "e55da25979087dfb16ef23226263435c14c45fe9", "26c3cbaeccf26f2f18e1eb8aab5642843590a612"}
	for i, head := range heads {
		n := 158 + i
		pr := run("gh", "api", fmt.Sprintf("repos/sarcasticbird/coop/pulls/%d", n))
		var remote struct {
			Head struct{ SHA string }
			Body string
		}
		check(json.Unmarshal(pr, &remote))
		if remote.Head.SHA != head {
			panic("pinned fixture head drift")
		}
		query := fmt.Sprintf(`query { repository(owner:"sarcasticbird",name:"coop") { pullRequest(number:%d) { commits(first:100) { totalCount nodes { commit { id oid committer { user { __typename databaseId login } } authors(first:100) { totalCount nodes { user { __typename databaseId login } } pageInfo { hasNextPage endCursor } } } } pageInfo { hasNextPage endCursor } } } } }`, n)
		commits := run("gh", "api", "graphql", "-f", "query="+query)
		reviews := run("gh", "api", fmt.Sprintf("repos/sarcasticbird/coop/pulls/%d/reviews?per_page=100", n))
		var binding *c.NativeTaskBinding
		var registration *c.ArtifactLink
		if n >= 161 {
			start := strings.Index(remote.Body, "<!-- coop-pr:v3 ")
			if start < 0 {
				panic("missing actual native source provenance")
			}
			body := remote.Body[start+len("<!-- coop-pr:v3 "):]
			end := strings.Index(body, " -->")
			if end < 0 {
				panic("bad native provenance")
			}
			var metadata worker.NativePRMetadata
			check(json.Unmarshal([]byte(body[:end]), &metadata))
			task, err := cl.TaskRead(context.Background(), metadata.TaskURL)
			check(err)
			binding = task.Binding
			if binding == nil || worker.NativeAssignmentSHA(*binding) != metadata.AssignmentSHA {
				panic("source assignment drift")
			}
			for cursor := ""; ; {
				page, err := cl.Artifacts(context.Background(), c.ArtifactQuery{Goal: metadata.Goal, After: cursor})
				check(err)
				for _, link := range page.Links {
					if link.Kind == "pull_request" && link.URL == fmt.Sprintf("https://github.com/sarcasticbird/coop/pull/%d", n) {
						copy := link
						registration = &copy
					}
				}
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
			if registration == nil || registration.AssignmentSHA != metadata.AssignmentSHA {
				panic("missing exact source registration")
			}
		}
		snapshot := struct {
			Tree                 string
			PR, Reviews, Commits json.RawMessage
			Binding              *c.NativeTaskBinding `json:",omitempty"`
			Registration         *c.ArtifactLink      `json:",omitempty"`
		}{strings.TrimSpace(string(run("git", "rev-parse", head+"^{tree}"))), pr, reviews, commits, binding, registration}
		raw, err := json.MarshalIndent(snapshot, "", "  ")
		check(err)
		archive := run("git", "archive", "--format=tar.gz", "--prefix=source/", head)
		check(os.WriteFile(filepath.Join(*out, fmt.Sprintf("pr%d.json", n)), append(raw, '\n'), 0644))
		check(os.WriteFile(filepath.Join(*out, fmt.Sprintf("pr%d.tar.gz", n)), archive, 0644))
	}
}

func run(name string, args ...string) []byte {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "COOP_GITHUB_OWNER=sarcasticbird")
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	check(err)
	return raw
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
