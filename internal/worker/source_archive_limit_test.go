package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestSourceArchiveLargerThanJSONPage(t *testing.T) {
	content := make([]byte, MaxAPIPage+4096)
	_, _ = rand.New(rand.NewSource(91)).Read(content)
	archive := qaBundle(t, qaBundleEntry{name: "root/evidence", content: string(content)})
	if len(archive) <= MaxAPIPage {
		t.Fatal("fixture must exceed the JSON page limit")
	}
	want, err := sourceBundleSHA(archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("digest", func(t *testing.T) {
		if got, err := QASourceDigest(bytes.NewReader(archive)); err != nil || got != want {
			t.Fatalf("source digest = %q, %v", got, err)
		}
	})
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "observation"}[cached], func(t *testing.T) {
			var runner CommandRunner = cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
				return response("200 OK", string(archive)), nil
			})
			var observations *observationRunner
			if cached {
				observations = newObservationRunner(runner, time.Now(), providerState{})
				runner = observations
			}
			g := NewGitHubClient(runner)
			pr := PR{HeadSHA: strings.Repeat("a", 40)}
			if err := g.bindQAArtifact(t.Context(), Task{Repo: "code/repo", QA: QAPolicy{Required: true}}, &pr, nil); err != nil {
				t.Fatal(err)
			}
			if pr.QAArtifact == nil || pr.QAArtifact.SHA256 != want {
				t.Fatal("source archive lost its canonical binding")
			}
			if cached && len(observations.state.Entries) != 0 {
				t.Fatal("archive bytes entered the JSON observation cache")
			}
		})
	}
}

func TestLargeResponseLimitRemainsScopedToExactSourceGET(t *testing.T) {
	for _, tc := range []struct{ method, endpoint string }{
		{"GET", "repos/code/repo/pulls/1"},
		{"POST", "repos/code/repo/tarball/" + strings.Repeat("a", 40)},
		{"GET", "repos/code/repo/tarball/main"},
		{"GET", "repos/code/repo/tarball/" + strings.Repeat("a", 40) + "?extra=1"},
	} {
		t.Run(tc.method+tc.endpoint, func(t *testing.T) {
			g := NewGitHubClient(cacheRunFunc(func(context.Context, []string, []string, io.Reader) (CommandResult, error) {
				return response("200 OK", strings.Repeat("x", MaxAPIPage+1)), nil
			}))
			_, _, err := g.api(t.Context(), "code", tc.method, tc.endpoint, nil)
			var gh *GitHubError
			if !errors.As(err, &gh) || gh.Category != "response_too_large" {
				t.Fatalf("oversized ordinary response accepted: %v", err)
			}
		})
	}
}
