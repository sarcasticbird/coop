package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"io"
	"net/http"
	"strings"
	"time"
)

type providerEntry struct {
	Body            []byte    `json:"body"`
	ETag            string    `json:"etag,omitempty"`
	FetchedAt       time.Time `json:"fetched_at"`
	UnconditionalAt time.Time `json:"unconditional_at"`
}
type providerState struct {
	AppID, InstallationID int64
	Fingerprint           string
	Entries               map[string]providerEntry
}
type observationRunner struct {
	base     CommandRunner
	now      time.Time
	state    providerState
	force    bool
	requests int
	seen     map[string]bool
	reused   map[string]time.Time
	clock    Clock
	maxTTL   time.Duration
	yield    func(context.Context) error
}

func newObservationRunner(base CommandRunner, now time.Time, old providerState) *observationRunner {
	if old.Entries == nil {
		old.Entries = map[string]providerEntry{}
	}
	return &observationRunner{base: base, now: now, state: old, seen: map[string]bool{}, reused: map[string]time.Time{}}
}

func (r *observationRunner) observedAt(now time.Time) time.Time {
	for _, at := range r.reused {
		if at.Before(now) {
			now = at
		}
	}
	return now.UTC()
}
func observationTTL(endpoint string, body []byte) time.Duration {
	// These reads bracket native consistency checks. Replaying an older first
	// scan and comparing it with a live second scan would reject a legitimate
	// change every round until its TTL expired. Each scan must validate now.
	if strings.Contains(endpoint, "/issues/") || strings.Contains(string(body), "WorkerScopeHistory") || strings.Contains(string(body), "WorkerTeamBaseScope") || strings.Contains(string(body), "WorkerNativePR") || strings.Contains(string(body), "WorkerNativeBase") || strings.Contains(string(body), "WorkerPRVersion") {
		return 0
	}
	if strings.Contains(string(body), "WorkerTeamReviews") || strings.Contains(endpoint, "/pulls/") {
		return time.Minute
	}
	return 5 * time.Minute
}
func observationWire(body []byte, etag string) []byte {
	var b bytes.Buffer
	_, _ = fmt.Fprint(&b, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n")
	if etag != "" {
		_, _ = fmt.Fprintf(&b, "ETag: %s\r\n", etag)
	}
	_, _ = b.WriteString("\r\n")
	_, _ = b.Write(body)
	return b.Bytes()
}
func (r *observationRunner) Run(ctx context.Context, argv, env []string, in io.Reader) (CommandResult, error) {
	if r.clock != nil {
		r.now = r.clock.Now().UTC()
	}
	if len(argv) < 7 || argv[0] != "/usr/local/libexec/coop-github" || argv[1] != "gh" || argv[5] != "api" {
		return CommandResult{}, c.ErrInvalid
	}
	var body []byte
	if in != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(in, MaxAPIPage+1))
		if err != nil || len(body) > MaxAPIPage {
			return CommandResult{}, c.ErrSize
		}
	}
	endpoint := argv[6]
	method := "GET"
	for i, a := range argv {
		if a == "--method" && i+1 < len(argv) {
			method = argv[i+1]
		}
	}
	if method != "GET" {
		var input struct{ Query string }
		if endpoint != "graphql" || json.Unmarshal(body, &input) != nil || !strings.HasPrefix(strings.TrimSpace(input.Query), "query ") {
			return CommandResult{}, c.ErrForbidden
		}
	}
	key := BodyHash(strings.Join(argv, "\x00") + "\x00" + string(body))
	// Native readers deliberately repeat scans to reject mixed snapshots.
	// Reuse is allowed for the initial read only; verification must reach the
	// provider independently, including when the initial read was retained.
	verification := r.seen[key]
	r.seen[key] = true
	old, found := r.state.Entries[key]
	valid := found && len(old.Body) > 0 && !old.FetchedAt.After(r.now) && len(old.Body) <= MaxAPIPage
	ttl := observationTTL(endpoint, body)
	if r.maxTTL > 0 && ttl > r.maxTTL {
		ttl = r.maxTTL
	}
	if !r.force && !verification && valid && old.FetchedAt.Add(ttl).After(r.now) {
		r.reused[key] = old.FetchedAt
		return CommandResult{Stdout: observationWire(old.Body, old.ETag)}, nil
	}
	if r.requests >= 256 {
		return CommandResult{}, c.ErrCapacity
	}
	if r.yield != nil {
		if err := r.yield(ctx); err != nil {
			return CommandResult{}, err
		}
	}
	args := append([]string{}, argv...)
	conditional := method == "GET" && strings.Contains(endpoint, "/comments?") && !strings.Contains(endpoint, "page=2") && !strings.Contains(endpoint, "page=3") && (!strings.Contains(endpoint, "page=") || strings.HasSuffix(endpoint, "page=1"))
	sentETag := !r.force && conditional && valid && old.ETag != "" && old.UnconditionalAt.Add(30*time.Minute).After(r.now)
	if sentETag {
		args = append(args, "--header", "If-None-Match: "+old.ETag)
	}
	r.requests++
	result, err := r.base.Run(ctx, args, env, bytes.NewReader(body))
	if len(result.Stdout) > MaxAPIPage+(64<<10) {
		return CommandResult{}, c.ErrSize
	}
	res, e := http.ReadResponse(bufio.NewReader(bytes.NewReader(result.Stdout)), nil)
	if e != nil {
		return result, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == 304 {
		if !sentETag {
			return CommandResult{}, c.ErrConflict
		}
		old.FetchedAt = r.now
		r.state.Entries[key] = old
		delete(r.reused, key)
		return CommandResult{Stdout: observationWire(old.Body, old.ETag)}, nil
	}
	if err != nil || result.ExitCode != 0 || res.StatusCode != 200 {
		return result, err
	}
	delete(r.reused, key)
	raw, e := io.ReadAll(io.LimitReader(res.Body, MaxAPIPage+1))
	if e != nil || len(raw) > MaxAPIPage {
		return CommandResult{}, c.ErrSize
	}
	// Archive bytes are verified once and represented by the exact-head digest,
	// never persisted as cached provider output or replayed into an action read.
	if !strings.Contains(endpoint, "/tarball/") && !strings.Contains(string(body), "WorkerCacheProjectOrder") && json.Valid(raw) {
		var graph struct{ Errors []json.RawMessage }
		if endpoint == "graphql" && json.Unmarshal(raw, &graph) == nil && len(graph.Errors) > 0 {
			return result, err
		}
		etag := ""
		if conditional && res.Header.Get("Link") == "" {
			etag = res.Header.Get("ETag")
		}
		if strings.ContainsAny(etag, "\r\n") {
			etag = ""
		}
		r.state.Entries[key] = providerEntry{Body: raw, ETag: etag, FetchedAt: r.now, UnconditionalAt: r.now}
	}
	return result, nil
}
