package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxAPIPage = 2 << 20
const MaxConnection = 10000
const MaxSnapshot = 16 << 20

type GitHub interface {
	Snapshot(context.Context, Config, Snapshot) (Snapshot, error)
	Claim(context.Context, Job, string) (Claim, error)
	Reconcile(context.Context, Job) (Artifacts, error)
	SyncTask(context.Context, Job, string, string) error
	ReportBlock(context.Context, Job, string, string) error
}
type CommandResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}
type CommandRunner interface {
	Run(context.Context, []string, []string, io.Reader) (CommandResult, error)
}
type GitHubError struct {
	Category string
	RetryAt  time.Time
}

func (e *GitHubError) Error() string { return "GitHub: " + e.Category }
func ghError(category string) error  { return &GitHubError{Category: category} }

type GitHubClient struct {
	runner   CommandRunner
	config   Config
	snapshot Snapshot
	goalIDs  map[string]string
}

func NewGitHubClient(runner CommandRunner) *GitHubClient {
	return &GitHubClient{runner: runner, goalIDs: map[string]string{}}
}

func (g *GitHubClient) api(ctx context.Context, owner, method, endpoint string, body any) ([]byte, http.Header, error) {
	if !slug.MatchString(owner) || strings.ContainsAny(endpoint, "\x00\r\n") || strings.HasPrefix(endpoint, "/") || strings.Contains(endpoint, "://") {
		return nil, nil, ghError("invalid_target")
	}
	argv := []string{"/usr/local/libexec/coop-github", "gh", "--owner", owner, "--", "api", endpoint, "--include", "--method", method}
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > MaxAPIPage {
			return nil, nil, ghError("invalid_request")
		}
		input = bytes.NewReader(raw)
		argv = append(argv, "--input", "-")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, runErr := g.runner.Run(ctx, argv, nil, input)
	if len(result.Stdout) > MaxAPIPage+(64<<10) {
		return nil, nil, ghError("response_too_large")
	}
	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(result.Stdout)), nil)
	if err != nil {
		if runErr != nil || result.ExitCode != 0 {
			return nil, nil, ghError("transport_failed")
		}
		return nil, nil, ghError("invalid_response")
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxAPIPage+1))
	if err != nil || len(raw) > MaxAPIPage {
		return nil, res.Header, ghError("response_too_large")
	}
	if res.StatusCode == 429 || res.StatusCode == 403 && (res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0") {
		retry := time.Now().Add(time.Minute)
		if seconds, e := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 64); e == nil && seconds > 0 && seconds < 86400 {
			retry = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if reset, e := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); e == nil && reset > time.Now().Unix() {
			retry = time.Unix(reset, 0)
		}
		return nil, res.Header, &GitHubError{Category: "rate_limited", RetryAt: retry}
	}
	if runErr != nil || result.ExitCode != 0 || res.StatusCode < 200 || res.StatusCode >= 300 {
		category := "request_failed"
		switch res.StatusCode {
		case 401:
			category = "credentials_expired"
		case 403:
			category = "permission_denied"
		case 404:
			category = "not_found"
		}
		return nil, res.Header, ghError(category)
	}
	return raw, res.Header, nil
}
func (g *GitHubClient) graphql(ctx context.Context, owner, query string, variables map[string]any) (json.RawMessage, error) {
	raw, _, err := g.api(ctx, owner, "POST", "graphql", map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Errors) > 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, ghError("graphql_failed")
	}
	return envelope.Data, nil
}
func at(raw json.RawMessage, path ...string) (json.RawMessage, error) {
	for _, key := range path {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return nil, ghError("invalid_response")
		}
		raw = object[key]
		if len(raw) == 0 || string(raw) == "null" {
			return nil, ghError("unreadable_content")
		}
	}
	return raw, nil
}
func (g *GitHubClient) connection(ctx context.Context, owner, query string, variables map[string]any, path []string) ([]json.RawMessage, error) {
	vars := map[string]any{}
	for k, v := range variables {
		vars[k] = v
	}
	var nodes []json.RawMessage
	seen := map[string]bool{}
	for {
		data, err := g.graphql(ctx, owner, query, vars)
		if err != nil {
			return nil, err
		}
		raw, err := at(data, path...)
		if err != nil {
			return nil, err
		}
		var page struct {
			Nodes    []json.RawMessage `json:"nodes"`
			PageInfo *struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		}
		if json.Unmarshal(raw, &page) != nil || page.PageInfo == nil {
			return nil, ghError("incomplete_connection")
		}
		nodes = append(nodes, page.Nodes...)
		if len(nodes) > MaxConnection {
			return nil, ghError("connection_limit")
		}
		if !page.PageInfo.HasNextPage {
			return nodes, nil
		}
		cursor := page.PageInfo.EndCursor
		if cursor == "" || seen[cursor] {
			return nil, ghError("stalled_cursor")
		}
		seen[cursor] = true
		vars["after"] = cursor
	}
}
func (g *GitHubClient) restList(ctx context.Context, owner, endpoint, field string) ([]json.RawMessage, error) {
	var nodes []json.RawMessage
	for page := 1; page <= MaxConnection/100+1; page++ {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		raw, headers, err := g.api(ctx, owner, "GET", endpoint+separator+"per_page=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, err
		}
		if field != "" {
			raw, err = at(raw, field)
			if err != nil {
				return nil, err
			}
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, ghError("invalid_response")
		}
		nodes = append(nodes, items...)
		if len(nodes) > MaxConnection {
			return nil, ghError("connection_limit")
		}
		if !strings.Contains(headers.Get("Link"), `rel="next"`) && len(items) < 100 {
			return nodes, nil
		}
		if len(items) == 0 {
			return nil, ghError("incomplete_connection")
		}
	}
	return nil, ghError("connection_limit")
}

const actorFields = `__typename login ... on User { databaseId } ... on Bot { databaseId }`

type graphActor struct {
	Kind  string `json:"__typename"`
	Login string `json:"login"`
	ID    int64  `json:"databaseId"`
}

func (a graphActor) actor() Actor { return Actor{ID: a.ID, Login: a.Login, Kind: a.Kind} }
func (g *GitHubClient) verifyOwner(ctx context.Context, owner string) error {
	data, err := g.graphql(ctx, owner, `query WorkerIdentity { viewer { __typename login databaseId } }`, nil)
	if err != nil {
		return err
	}
	raw, err := at(data, "viewer")
	if err != nil {
		return err
	}
	var a graphActor
	// GitHub's viewer field has User shape even for an installation's bot.
	// Verify the immutable configured bot ID plus normalized login instead.
	if json.Unmarshal(raw, &a) != nil || a.ID != g.config.Self.ID || a.ID <= 0 || strings.TrimSuffix(a.Login, "[bot]") != strings.TrimSuffix(g.config.Self.Login, "[bot]") {
		return ghError("unexpected_bot_identity")
	}
	return nil
}

func decode[T any](raw json.RawMessage) (T, error) {
	var value T
	if json.Unmarshal(raw, &value) != nil {
		return value, ghError("invalid_response")
	}
	return value, nil
}
func uniqueOwners(c Config) []string {
	var result []string
	for _, group := range [][]string{c.CodeOwners, c.ProjectOwners} {
		for _, o := range group {
			if !containsFold(result, o) {
				result = append(result, o)
			}
		}
	}
	for _, p := range c.Projects {
		if !containsFold(result, p.Owner) {
			result = append(result, p.Owner)
		}
	}
	for _, r := range c.DiscussionRepositories {
		o := strings.Split(r, "/")[0]
		if !containsFold(result, o) {
			result = append(result, o)
		}
	}
	for _, b := range c.BootstrapGoals {
		if validGitHubURL(b.URL) {
			p := strings.Split(strings.TrimPrefix(b.URL, "https://github.com/"), "/")
			o := p[0]
			if o == "orgs" {
				o = p[1]
			}
			if !containsFold(result, o) {
				result = append(result, o)
			}
		}
	}
	return result
}
func (g *GitHubClient) Snapshot(ctx context.Context, c Config, previous Snapshot) (Snapshot, error) {
	g.config = Defaults(c)
	for _, o := range uniqueOwners(c) {
		if err := g.verifyOwner(ctx, o); err != nil {
			return Snapshot{}, err
		}
	}
	s, err := g.readSnapshot(ctx, previous)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := json.Marshal(s)
	wakeRaw, wakeErr := json.Marshal(s.WakeSources)
	if err != nil || wakeErr != nil || len(raw)+len(wakeRaw) > MaxSnapshot || len(s.WakeSources) > MaxConnection {
		return Snapshot{}, ghError("snapshot_limit")
	}
	s.Complete = true
	g.snapshot = s
	return s, nil
}

func parseRef(raw string) (repo string, number int, err error) {
	if !validGitHubURL(raw) {
		return "", 0, ghError("invalid_target")
	}
	p := strings.Split(strings.TrimPrefix(strings.TrimPrefix(raw, "https://github.com/"), "/"), "/")
	if p[0] == "orgs" {
		return "", 0, ghError("discussion_requires_backing_repository")
	}
	n, e := strconv.Atoi(p[3])
	if e != nil {
		return "", 0, ghError("invalid_target")
	}
	return p[0] + "/" + p[1], n, nil
}

func referenceInRepo(ref, repo, kind string) bool {
	parsedRepo, n, err := parseRef(ref)
	return err == nil && parsedRepo == repo && ref == "https://github.com/"+repo+"/"+kind+"/"+stringID(n)
}

func (g *GitHubClient) issueComments(ctx context.Context, repo string, number int) ([]Comment, error) {
	nodes, err := g.restList(ctx, strings.Split(repo, "/")[0], "repos/"+repo+"/issues/"+stringID(number)+"/comments", "")
	if err != nil {
		return nil, err
	}
	var result []Comment
	for _, raw := range nodes {
		c, err := decode[restComment](raw)
		if err != nil {
			return nil, err
		}
		result = append(result, c.comment())
	}
	return result, nil
}
func (g *GitHubClient) postMarked(ctx context.Context, j Job, phase, body string) (Comment, error) {
	if !hexSHA.MatchString(j.ID) {
		return Comment{}, ghError("invalid_job")
	}
	target := j.TaskURL
	if j.PRURL != "" {
		target = j.PRURL
	}
	if j.Kind == "plan" {
		target = j.GoalURL
	}
	mark := marker(j, phase)
	if strings.Contains(target, "/discussions/") {
		return g.postDiscussion(ctx, j, target, mark, body)
	}
	repo, n, err := parseRef(target)
	if err != nil {
		return Comment{}, err
	}
	comments, err := g.issueComments(ctx, repo, n)
	if err != nil {
		return Comment{}, err
	}
	for _, c := range comments {
		if sameActor(c.Author, g.config.Self) && strings.Contains(c.Body, mark) {
			return c, nil
		}
	}
	raw, _, err := g.api(ctx, strings.Split(repo, "/")[0], "POST", "repos/"+repo+"/issues/"+stringID(n)+"/comments", map[string]string{"body": body + "\n\n" + mark})
	if err != nil {
		return Comment{}, err
	}
	c, err := decode[restComment](raw)
	if err != nil {
		return Comment{}, err
	}
	if !sameActor(c.Author.actor(), g.config.Self) {
		return Comment{}, ghError("unexpected_comment_author")
	}
	return c.comment(), nil
}
func (g *GitHubClient) Claim(ctx context.Context, j Job, instance string) (Claim, error) {
	if !hexSHA.MatchString(instance) {
		return Claim{}, ghError("invalid_instance")
	}
	if g.config.Version == 2 {
		return g.claimTeam(ctx, j, instance)
	}
	c, err := g.postMarked(ctx, j, "claim", "Picked up this approved task.\n"+encodeMetadata("claim", struct {
		Instance string `json:"instance"`
	}{instance}))
	if err != nil {
		return Claim{}, err
	}
	claim := claimFromComment(c)
	if claim == nil {
		return Claim{}, ghError("invalid_claim_response")
	}
	return *claim, nil
}
func (g *GitHubClient) ReportBlock(ctx context.Context, j Job, category, detail string) error {
	allowed := map[string]bool{"scope": true, "permission": true, "execution": true, "review_limit": true, "submission_unknown": true, "server_unavailable": true, "prompt_size": true, "prompt_build": true}
	if !allowed[category] {
		category = "execution"
	}
	body := "Work is blocked (" + category + "). Check the worker's local status for details."
	if category == "prompt_size" && j.PromptFailure != nil && validatePromptFailure(j.PromptFailure) == nil {
		body = "Work is blocked before model submission. " + j.PromptFailure.Error() + " Job/request: " + j.PromptFailure.JobID + ". Source: " + j.PromptFailure.SourceURL
	}
	_, err := g.postMarked(ctx, j, "blocked", body)
	return err
}
func (g *GitHubClient) Reconcile(ctx context.Context, j Job) (Artifacts, error) {
	s, err := g.Snapshot(ctx, g.config, g.snapshot)
	if err != nil {
		return Artifacts{}, err
	}
	var a Artifacts
	a.PlanResponded = planningResponded(g.config, j, s)
	if task, ok := taskFor(s, j.TaskURL); ok {
		a.Task = &task
		a.Claim = task.Claim
		a.IssueCompleted = task.Completed
	}
	for _, p := range s.PRs {
		if p.URL == j.PRURL || p.TaskURL == j.TaskURL {
			copy := p
			a.PR = &copy
			break
		}
	}
	if a.PR != nil {
		a.ProgressHash = BodyHash(a.PR.HeadSHA + ":" + strconv.FormatBool(a.PR.Merged))
	}
	return a, nil
}

func planningResponded(c Config, j Job, s Snapshot) bool {
	if j.Kind != "plan" || !s.Complete {
		return false
	}
	goal, ok := goalFor(s, j.GoalURL)
	if !ok {
		return false
	}
	for _, d := range goal.Decisions {
		if sameActor(d.Author, c.Self) && strings.Contains(d.Command, marker(j, "result")) {
			complete := true
			for _, h := range reviewHandoffs(c, j, s, time.Now()) {
				if h.State == "needs_review" || h.State == "blocked" && !strings.Contains(d.Command, h.BlockerMarker) {
					complete = false
					break
				}
			}
			if complete && deliveryPlanProgress(c, j, s) {
				return true
			}
		}
	}
	return false
}
func (g *GitHubClient) SyncTask(ctx context.Context, j Job, state, detail string) error {
	return g.syncProject(ctx, j, state)
}

var _ GitHub = (*GitHubClient)(nil)
