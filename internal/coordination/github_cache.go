package coordination

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	GitHubCacheVersion  = 1
	GitHubChunkBytes    = 128 << 10
	GitHubSnapshotBytes = 16 << 20
	GitHubCacheBytes    = 512 << 20
	GitHubCacheObjects  = 10000
)

type GitHubObjectRef struct {
	RouteID       string  `json:"route_id"`
	Goal          GoalRef `json:"goal"`
	TaskURL       string  `json:"task_url"`
	URL           string  `json:"url"`
	Kind          string  `json:"kind"`
	AssignmentSHA string  `json:"assignment_sha256"`
	RepositoryID  int64   `json:"repository_id,omitempty"`
	ObjectID      string  `json:"object_id,omitempty"`
}
type GitHubHeader struct {
	DependsOn     []string `json:"depends_on,omitempty"`
	Revision      string   `json:"revision"`
	RepositoryID  int64    `json:"repository_id"`
	ObjectID      string   `json:"object_id"`
	HeadSHA       string   `json:"head_sha,omitempty"`
	BaseSHA       string   `json:"base_sha,omitempty"`
	ScopeRevision string   `json:"scope_revision,omitempty"`
	State         string   `json:"state"`
	Completed     bool     `json:"completed"`
}
type GitHubDatasetRef struct {
	Collector      Placement       `json:"collector"`
	RestoreEpoch   int64           `json:"restore_epoch"`
	AppID          int64           `json:"app_id"`
	InstallationID int64           `json:"installation_id"`
	Object         GitHubObjectRef `json:"object"`
	Kind           string          `json:"kind"`
	SHA256         string          `json:"sha256"`
	Version        int             `json:"version"`
	HeaderRevision string          `json:"header_revision"`
	Total          int             `json:"total"`
	ObservedAt     time.Time       `json:"observed_at"`
	DueAt          time.Time       `json:"due_at"`
}
type GitHubCoverage struct {
	ObservedAt time.Time          `json:"observed_at"`
	Collector  Placement          `json:"collector"`
	DueAt      time.Time          `json:"due_at"`
	Object     GitHubObjectRef    `json:"object"`
	Header     GitHubHeader       `json:"header"`
	Datasets   []GitHubDatasetRef `json:"datasets"`
	Dirty      bool               `json:"dirty"`
	Reason     string             `json:"reason,omitempty"`
	NextRetry  time.Time          `json:"next_retry"`
}
type GitHubCatalogQuery struct {
	Engagement string `json:"engagement"`
	Manifest   string `json:"manifest,omitempty"`
	After      string `json:"after,omitempty"`
}
type GitHubCatalogPage struct {
	Version      int              `json:"version"`
	MasterID     string           `json:"master_id"`
	RestoreEpoch int64            `json:"restore_epoch"`
	Manifest     string           `json:"manifest"`
	Mode         string           `json:"mode"`
	Goals        []GoalAuthority  `json:"goals"`
	Objects      []GitHubCoverage `json:"objects"`
	Next         string           `json:"next,omitempty"`
}
type GitHubDatasetQuery struct {
	Ref      GitHubDatasetRef `json:"ref"`
	Manifest string           `json:"manifest"`
	Offset   int              `json:"offset"`
}
type GitHubDatasetChunk struct {
	Ref    GitHubDatasetRef `json:"ref"`
	Data   []byte           `json:"data"`
	Offset int              `json:"offset"`
	Next   int              `json:"next"`
}
type GitHubClaim struct {
	RouteID   string `json:"route_id"`
	RequestID string `json:"request_id"`
}
type GitHubLease struct {
	ID             string             `json:"id"`
	Object         GitHubObjectRef    `json:"object"`
	Authority      GoalAuthority      `json:"authority"`
	Collector      Placement          `json:"collector"`
	RestoreEpoch   int64              `json:"restore_epoch"`
	PolicySHA      string             `json:"policy_sha"`
	Manifest       string             `json:"manifest"`
	Generation     int64              `json:"generation"`
	ExpectedHeader string             `json:"expected_header"`
	StartedAt      time.Time          `json:"started_at"`
	ExpiresAt      time.Time          `json:"expires_at"`
	Retained       []GitHubDatasetRef `json:"retained"`
	AppID          int64              `json:"app_id"`
	InstallationID int64              `json:"installation_id"`
}
type GitHubUpload struct {
	LeaseID string `json:"lease_id"`
	Kind    string `json:"kind"`
	Offset  int    `json:"offset"`
	Data    []byte `json:"data"`
	Total   int    `json:"total"`
	SHA256  string `json:"sha256"`
}
type GitHubCommit struct {
	LeaseID          string             `json:"lease_id"`
	Header           GitHubHeader       `json:"header"`
	HeaderObservedAt time.Time          `json:"header_observed_at,omitempty"`
	Datasets         []GitHubDatasetRef `json:"datasets"`
}
type GitHubFailure struct {
	LeaseID  string    `json:"lease_id"`
	Category string    `json:"category"`
	RetryAt  time.Time `json:"retry_at"`
}
type GitHubDirty struct {
	Object    GitHubObjectRef `json:"object"`
	RequestID string          `json:"request_id"`
}
type GitHubCacheCapabilities struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
}
type GitHubCacheStatus struct {
	Version  int              `json:"version"`
	Mode     string           `json:"mode"`
	Objects  []GitHubCoverage `json:"objects"`
	Manifest string           `json:"manifest"`
}

var githubDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var githubID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func GitHubDigest(raw []byte) string       { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func ValidGitHubDigest(s string) bool      { return githubDigest.MatchString(s) }
func ValidGitHubDatasetKind(s string) bool { return s == "task" || s == "pr" || s == "provider" }
func ValidateGitHubObject(v GitHubObjectRef) error {
	if !githubID.MatchString(v.RouteID) || v.Goal.Source != "master" || v.Goal.MasterID == "" || v.Goal.GoalID == "" || !githubDigest.MatchString(v.Goal.RevisionSHA256) || !githubDigest.MatchString(v.AssignmentSHA) {
		return ErrInvalid
	}
	for _, target := range []string{v.URL, v.TaskURL} {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.ForceQuery {
			return ErrInvalid
		}
		p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(p) != 4 || p[0] == "" || p[1] == "" || (p[2] != "issues" && p[2] != "pull") || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(p[3]) {
			return ErrInvalid
		}
	}
	if v.Kind != "task" && v.Kind != "pull_request" {
		return ErrInvalid
	}
	return nil
}
func ValidateGitHubCacheMessage(v any) error {
	switch x := v.(type) {
	case GitHubUpload:
		if len(x.Data) > GitHubChunkBytes || x.Total > GitHubSnapshotBytes {
			return ErrSize
		}
		if !githubID.MatchString(x.LeaseID) || !ValidGitHubDatasetKind(x.Kind) || !githubDigest.MatchString(x.SHA256) || x.Total < 1 || x.Offset < 0 || len(x.Data) == 0 || x.Offset > x.Total-len(x.Data) {
			return ErrInvalid
		}
	case GitHubCommit:
		if !githubID.MatchString(x.LeaseID) || x.Header.RepositoryID < 1 || x.Header.ObjectID == "" || !githubDigest.MatchString(x.Header.Revision) || len(x.Datasets) == 0 || len(x.Datasets) > 3 {
			return ErrInvalid
		}
		if (x.Header.State != "open" && x.Header.State != "closed") || x.Header.Completed && x.Header.State != "closed" || len(x.Header.ObjectID) > 256 || strings.ContainsAny(x.Header.ObjectID, "\x00\r\n") {
			return ErrInvalid
		}
		for _, d := range x.Datasets {
			if !ValidGitHubDatasetKind(d.Kind) || !githubDigest.MatchString(d.SHA256) || d.Total < 1 || d.Total > GitHubSnapshotBytes || d.Version != GitHubCacheVersion {
				return ErrInvalid
			}
		}
	case GitHubDirty:
		if !githubID.MatchString(x.RequestID) {
			return ErrInvalid
		}
		return ValidateGitHubObject(x.Object)
	case GitHubClaim:
		if !githubID.MatchString(x.RouteID) || !githubID.MatchString(x.RequestID) {
			return ErrInvalid
		}
	case GitHubFailure:
		if !githubID.MatchString(x.LeaseID) || !githubID.MatchString(x.Category) {
			return ErrInvalid
		}
	case GitHubCatalogQuery:
		if x.Engagement == "" || len(x.After) > 128 {
			return ErrInvalid
		}
	case GitHubDatasetQuery:
		if x.Offset < 0 || x.Offset > GitHubSnapshotBytes || !githubDigest.MatchString(x.Ref.SHA256) || !ValidGitHubDatasetKind(x.Ref.Kind) {
			return ErrInvalid
		}
		return ValidateGitHubObject(x.Ref.Object)
	default:
		return ErrInvalid
	}
	return nil
}

// ValidGitHubPayload checks bounded JSON without increasing ordinary API limits.
func ValidGitHubPayload(raw []byte) bool {
	if len(raw) == 0 || len(raw) > GitHubSnapshotBytes {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if scanJSON(d, 0) != nil {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
