package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Source digests use a canonical bundle of sorted paths, modes, link targets and
// file bytes, excluding the archive's generated root and volatile tar headers.
type QAArtifact struct {
	Kind, HeadSHA, SHA256, URL, Revision string
}

func sourceBundleSHA(raw []byte) (string, error) {
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer func() { _ = z.Close() }()
	bounded := &io.LimitedReader{R: z, N: (64 << 20) + 1}
	t := tar.NewReader(bounded)
	entries := map[string]string{}
	var total int64
	root := ""
	count := 0
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		count++
		if count > MaxConnection {
			return "", errors.New("invalid source entry")
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			// GitHub adds a commit comment. Go does not apply global PAX
			// semantics to later files, so ignore only explicit comment metadata.
			if len(h.PAXRecords) != 1 || h.PAXRecords["comment"] == "" {
				return "", errors.New("unsupported source metadata")
			}
			continue
		}
		parts := strings.SplitN(h.Name, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." || strings.ContainsAny(parts[0], "\x00\\") || root != "" && root != parts[0] {
			return "", errors.New("invalid source root")
		}
		root = parts[0]
		name := strings.TrimSuffix(parts[1], "/")
		if name == "" && h.Typeflag != tar.TypeDir || name != "" && (name == "." || name == ".." || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\x00\\")) {
			return "", errors.New("invalid source entry")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Size < 0 || h.Size > (64<<20)-total {
			return "", errors.New("invalid source entry")
		}
		if _, exists := entries[name]; exists {
			return "", errors.New("duplicate source entry")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeSymlink {
			return "", errors.New("unsupported source entry")
		}
		content, err := io.ReadAll(io.LimitReader(t, h.Size+1))
		if err != nil || int64(len(content)) != h.Size {
			return "", errors.New("invalid source bytes")
		}
		total += h.Size
		file := sha256.Sum256(content)
		entries[name] = fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%x", name, h.Mode, h.Typeflag, h.Linkname, file)
	}
	// Tar EOF precedes the gzip trailer; consume it to verify integrity and size.
	if _, err := io.Copy(io.Discard, bounded); err != nil || bounded.N == 0 {
		return "", errors.New("invalid source bytes")
	}
	if len(entries) == 0 {
		return "", errors.New("empty source bundle")
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(entries[key] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (g *GitHubClient) readQAArtifact(ctx context.Context, t Task, p *PR) {
	candidate := p.QAArtifact
	p.QAArtifact, p.QAArtifactError = nil, ""
	if err := g.bindQAArtifact(ctx, t, p, candidate); err != nil {
		// Artifact failure blocks QA without discarding fresh authorization reads.
		p.QAArtifact, p.QAArtifactError = nil, errorCategory(err)
	}
}

func (g *GitHubClient) bindQAArtifact(ctx context.Context, t Task, p *PR, candidate *QAArtifact) error {
	if !t.QA.Required || p.Merged {
		return nil
	}
	if !qaHeadSHA.MatchString(p.HeadSHA) {
		return ghError("invalid_QA_head")
	}
	owner := strings.Split(t.Repo, "/")[0]
	if candidate == nil || candidate.Kind == "source" {
		if reused, err := sourceArtifactReuse(g, t.Repo, p, candidate); reused || err != nil {
			return err
		}
		raw, _, err := g.api(ctx, owner, "GET", "repos/"+t.Repo+"/tarball/"+p.HeadSHA, nil)
		if err != nil {
			return err
		}
		digest, err := sourceBundleSHA(raw)
		if err != nil {
			return ghError("invalid_QA_source_bundle")
		}
		a := QAArtifact{Kind: "source", HeadSHA: p.HeadSHA, SHA256: digest, URL: "https://api.github.com/repos/" + t.Repo + "/tarball/" + p.HeadSHA, Revision: "source-bundle-v1"}
		if candidate != nil && *candidate != a {
			return ghError("QA_artifact_changed")
		}
		p.QAArtifact = &a
		return nil
	}
	// Workflow artifacts are read from native immutable artifact metadata, never
	// a digest copied from a PR body. Expired/deleted artifacts cannot clear QA.
	prefix := "https://api.github.com/repos/" + t.Repo + "/actions/artifacts/"
	if candidate.Kind != "workflow" || candidate.HeadSHA != p.HeadSHA || !strings.HasPrefix(candidate.URL, prefix) || !numericID.MatchString(strings.TrimPrefix(candidate.URL, prefix)) {
		return ghError("invalid_QA_artifact")
	}
	raw, _, err := g.api(ctx, owner, "GET", strings.TrimPrefix(candidate.URL, "https://api.github.com/"), nil)
	if err != nil {
		return err
	}
	var a struct {
		ID          int64
		Digest      string
		Expired     bool
		WorkflowRun struct {
			ID      int64
			HeadSHA string `json:"head_sha"`
		} `json:"workflow_run"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Expired || a.WorkflowRun.HeadSHA != p.HeadSHA || a.ID <= 0 || strconv.FormatInt(a.ID, 10) != strings.TrimPrefix(candidate.URL, prefix) || a.WorkflowRun.ID <= 0 || candidate.Revision != strconv.FormatInt(a.WorkflowRun.ID, 10) || a.Digest != "sha256:"+candidate.SHA256 || !hexSHA.MatchString(candidate.SHA256) {
		return ghError("invalid_QA_artifact")
	}
	copy := *candidate
	p.QAArtifact = &copy
	return nil
}

func qaArtifactEqual(a, b *QAArtifact) bool { return a != nil && b != nil && *a == *b }

func qaRevision(p PR) string {
	if p.QAArtifact == nil {
		return ""
	}
	raw, _ := json.Marshal(p.QAArtifact)
	reviews, _ := json.Marshal(p.Reviews)
	return BodyHash(p.HeadSHA + ":" + p.ScopeRevision + ":" + BodyHash(string(reviews)) + ":" + string(raw))
}
