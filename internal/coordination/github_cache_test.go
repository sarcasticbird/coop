package coordination

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGitHubCacheMessageBounds(t *testing.T) {
	v := GitHubUpload{LeaseID: "lease", Kind: "task", SHA256: strings.Repeat("a", 64), Total: GitHubChunkBytes, Data: make([]byte, GitHubChunkBytes)}
	if err := ValidateGitHubCacheMessage(v); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if len(raw) > MaxJSON {
		t.Fatal("chunk exceeds normal transport")
	}
	v.Data = append(v.Data, 0)
	v.Total++
	if err := ValidateGitHubCacheMessage(v); !errors.Is(err, ErrSize) {
		t.Fatalf("oversize: %v", err)
	}
	v.Data = nil
	v.Total = 1
	v.Kind = "invented"
	if err := ValidateGitHubCacheMessage(v); err == nil {
		t.Fatal("unknown dataset accepted")
	}
}

func TestGitHubCacheRejectsAmbiguousPayload(t *testing.T) {
	if ValidGitHubPayload([]byte(`{"state":"open","state":"closed"}`)) {
		t.Fatal("duplicate JSON fields accepted")
	}
	commit := GitHubCommit{LeaseID: "lease", Header: GitHubHeader{RepositoryID: 1, ObjectID: "object", Revision: strings.Repeat("a", 64), State: "invented"}, Datasets: []GitHubDatasetRef{{Kind: "task", SHA256: strings.Repeat("b", 64), Total: 1, Version: 1}}}
	if ValidateGitHubCacheMessage(commit) == nil {
		t.Fatal("unknown provider state accepted")
	}
}
