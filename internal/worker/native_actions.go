package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
)

// NativeAction accepts intent and prose, never caller-provided authority,
// review evidence, principal IDs or assignment metadata.
type NativeAction struct {
	Operation     string      `json:"operation"`
	RequestID     string      `json:"request_id"`
	TaskURL       string      `json:"task_url,omitempty"`
	Repository    string      `json:"repository,omitempty"`
	Title         string      `json:"title,omitempty"`
	Body          string      `json:"body,omitempty"`
	BaseBranch    string      `json:"base_branch,omitempty"`
	DependsOn     []string    `json:"depends_on,omitempty"`
	Artifact      *QAArtifact `json:"artifact,omitempty"`
	Verdict       string      `json:"verdict,omitempty"`
	ReportBody    string      `json:"report_body,omitempty"`
	ProjectOwner  string      `json:"project_owner,omitempty"`
	ProjectNumber int         `json:"project_number,omitempty"`
}
type NativeActionResult struct {
	URL               string `json:"url,omitempty"`
	EvidenceMessageID string `json:"evidence_message_id,omitempty"`
	EvidenceBodySHA   string `json:"evidence_body_sha256,omitempty"`
	Phase             string `json:"phase"`
}
type nativeActionRecord struct {
	Version                                                                                                    int
	JobID, RequestID, InputSHA, Operation, State, Step, PublishedHeadSHA, ProjectItemID, ExpectedAssignmentSHA string
	Result                                                                                                     NativeActionResult
}

func LoadNativeAction(path string) (NativeAction, error) {
	var out NativeAction
	raw, err := readPrivate(path, c.MaxJSON)
	if err != nil {
		return out, err
	}
	if strictTeamJSON(raw, &out) != nil {
		return out, c.ErrInvalid
	}
	return out, nil
}
func nativeActionPath(cfg Config, job, request string) string {
	return filepath.Join(masterDir(cfg), "actions", BodyHash(job+"\x00"+request)+".json")
}
func saveNativeAction(cfg Config, r nativeActionRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicPrivate(nativeActionPath(cfg, r.JobID, r.RequestID), raw)
}

// The producer lock covers this scan and every following effect. A changed
// request ID or operation cannot bypass an unresolved remote mutation.
func nativeJobActionUncertain(cfg Config, jobID string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return false, err
		}
		var record nativeActionRecord
		if strictTeamJSON(raw, &record) != nil || record.Version != 3 {
			return false, c.ErrConflict
		}
		if record.JobID == jobID && record.State == "uncertain" {
			return true, nil
		}
	}
	return false, nil
}
func nativeJobHasResult(cfg Config, x NativeExecution) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "actions"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want := map[string]string{"plan": "complete-plan", "implement": "publish-pr", "fix": "publish-pr", "review": "review", "qa": "qa", "deliver": "deliver"}[x.Job.Kind]
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readPrivate(filepath.Join(masterDir(cfg), "actions", entry.Name()), c.MaxJSON)
		if err != nil {
			return false, err
		}
		var r nativeActionRecord
		if strictTeamJSON(raw, &r) != nil {
			return false, c.ErrConflict
		}
		if r.Version == 3 && r.JobID == x.Job.ID && r.Operation == want && r.State == "completed" {
			return true, nil
		}
	}
	return false, nil
}

var errNativeProducerBusy = errors.New("native action already running")

func lockNativeProducer(cfg Config) (func(), error) {
	dir := filepath.Join(masterDir(cfg), "actions")
	if err := ensurePrivateDir(dir, true); err != nil {
		return nil, err
	}
	// Helpers can overlap while the worker observes. Serialize producer effects
	// without holding the worker's native-slot or receipt lock.
	lockPath := filepath.Join(dir, "producer.lock")
	if err := safePath(lockPath); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errNativeProducerBusy
	}
	return func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN); _ = lock.Close() }, nil
}

// RunNativeAction rechecks both authority systems before every external effect.
// These reads cannot make master revocation atomic with GitHub's action. A
// revocation can race the final remote read and write across those services.
func RunNativeAction(ctx context.Context, cfg Config, m NativeEngineeringMaster, g *GitHubClient, jobID string, in NativeAction) (NativeActionResult, error) {
	var empty NativeActionResult
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || in.RequestID == "" || len(in.RequestID) > 128 || len(in.Body) > c.MaxText || len(in.ReportBody) > c.MaxText {
		return empty, c.ErrInvalid
	}
	release, err := lockNativeProducer(cfg)
	if err != nil {
		return empty, err
	}
	defer release()
	x, err := loadNativeExecution(cfg, jobID)
	if err != nil {
		return empty, err
	}
	executionRaw, marshalErr := json.Marshal(x)
	if marshalErr != nil {
		return empty, marshalErr
	}
	retired, retireErr := recoveryRetired(cfg, "engineering", jobID+".json", executionRaw)
	if retireErr != nil {
		return empty, retireErr
	}
	if retired {
		return empty, c.ErrEpoch
	}
	raw, _ := json.Marshal(in)
	digest := BodyHash(string(raw))
	record := nativeActionRecord{Version: 3, JobID: jobID, RequestID: in.RequestID, InputSHA: digest, Operation: in.Operation, State: "prepared"}
	saved, readErr := readPrivate(nativeActionPath(cfg, jobID, in.RequestID), c.MaxJSON)
	if readErr == nil {
		if strictTeamJSON(saved, &record) != nil || record.Version != 3 || record.JobID != jobID || record.RequestID != in.RequestID || record.InputSHA != digest {
			return empty, c.ErrConflict
		}
		if record.State == "completed" {
			return record.Result, nil
		}
		if record.State == "uncertain" {
			return empty, errors.New("native action outcome uncertain; inspect remote evidence before explicit recovery")
		}
	} else if !os.IsNotExist(readErr) {
		return empty, readErr
	}
	if uncertain, err := nativeJobActionUncertain(cfg, jobID); err != nil {
		return empty, err
	} else if uncertain {
		return empty, errors.New("native job has an uncertain action; inspect remote evidence before explicit recovery")
	}
	if x.Phase != "active" {
		return empty, c.ErrForbidden
	}
	a, err := nativeCurrentGoal(ctx, cfg, m, x.Goal)
	if err != nil {
		return empty, err
	}
	reader := NewGitHubClient(g.runner)
	reader.config = cfg
	if in.Operation == "deliver" && record.Step == "task-completed" && record.State == "prepared" {
		if err = nativeConfirmDelivery(ctx, cfg, reader, a, x); err != nil {
			return empty, err
		}
		record.State = "completed"
		record.Result.URL = x.Job.PRURL
		record.Result.Phase = "completed"
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
		return record.Result, nil
	}
	var task NativeTask
	var pr PR
	guard := func() error {
		paused, e := NewVersionedFileStore(cfg.StateDir, 3).Paused()
		if e != nil {
			return e
		}
		if paused {
			return c.ErrForbidden
		}
		saved, e := loadNativeExecution(cfg, jobID)
		if e != nil {
			return e
		}
		if saved.Phase != "active" {
			return c.ErrForbidden
		}
		a, e = nativeCurrentGoal(ctx, cfg, m, x.Goal)
		if e != nil {
			return e
		}
		if x.Binding != nil {
			task, e = reader.ReadNativeTask(ctx, cfg, x.Job.TaskURL)
			if e != nil {
				return e
			}
			if NativeAssignmentSHA(task.Binding) != x.Job.Master.AssignmentSHA || task.Task.Closed || task.Task.Completed {
				return c.ErrForbidden
			}
			a, e = ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: m}, task.Binding)
			if e != nil {
				return e
			}
			if x.Job.PRURL != "" {
				if in.Operation == "publish-pr" && x.Job.Kind == "fix" && record.Step != "" {
					// A push changes the PR head before its artifact metadata can be patched.
					// Validate the exact saved publication head and immutable assignment using
					// native REST fields during only this journaled transition.
					_, n, _ := parseRef(x.Job.PRURL)
					raw, _, err := reader.api(ctx, strings.Split(task.Task.Repo, "/")[0], "GET", "repos/"+task.Task.Repo+"/pulls/"+stringID(n), nil)
					if err != nil {
						return err
					}
					var current struct {
						URL  string `json:"html_url"`
						Body string
						User restActor
						Head struct {
							SHA, Ref string
							Repo     struct {
								FullName string `json:"full_name"`
							}
						}
						Base struct {
							Ref  string
							Repo struct {
								FullName string `json:"full_name"`
							}
						}
					}
					if json.Unmarshal(raw, &current) != nil || current.URL != x.Job.PRURL || current.User.ID != task.Binding.AuthorID || current.User.Kind != "Bot" || current.Head.SHA != record.PublishedHeadSHA || current.Head.Ref != x.Job.Branch || current.Base.Ref != task.Binding.BaseBranch || current.Head.Repo.FullName != task.Task.Repo || current.Base.Repo.FullName != task.Task.Repo {
						return c.ErrForbidden
					}
					if _, err = ParseNativePRMetadata(current.Body, task.Task.Repo, task.Task.URL, x.Goal, NativeAssignmentSHA(task.Binding)); err != nil {
						return err
					}
				} else {
					pr, e = reader.ReadNativePR(ctx, cfg, task, a, x.Job.PRURL)
					if e != nil {
						return e
					}
					if pr.HeadSHA != x.Job.HeadSHA || pr.ScopeRevision != x.Job.PRScopeRevision {
						return errors.New("native exact head or PR scope changed")
					}
				}
			}
		}
		return nil
	}
	// Mark uncertainty before invoking a remote mutation. A lost write response
	// never causes the helper to blindly repeat an external action.
	effect := func(step string, fn func() error) error {
		if err := guard(); err != nil {
			return err
		}
		if step == "merge" || step == "review-published" && in.Operation == "qa" {
			_, peer := nativePeerCleared(task.Binding, pr)
			if !peer || pr.Merged || pr.Closed || pr.ChecksState != "success" && pr.ChecksState != "none" {
				return c.ErrForbidden
			}
			if in.Operation == "qa" && (!qaArtifactEqual(pr.QAArtifact, x.Job.QAArtifact) || pr.QAArtifact == nil) {
				return c.ErrForbidden
			}
			if step == "merge" {
				if pr.MergeState != "CLEAN" {
					return c.ErrForbidden
				}
				if a.Revision.Proposal.QA.Required {
					reports := map[string]c.Message{}
					review := nativeLatestReview(pr, task.Binding.QAID)
					var qa NativeQAResult
					if nativeMetadata(review.Body, "qa", &qa) != nil {
						return c.ErrForbidden
					}
					report, err := m.Message(ctx, qa.EvidenceMessageID)
					if err != nil {
						return err
					}
					reports[report.ID] = report
					if _, ok := nativeQAState(task.Binding, a, pr, reports); !ok {
						return c.ErrForbidden
					}
				}
			}
		}
		if step == "task-completed" && !pr.Merged {
			return c.ErrForbidden
		}
		if _, err := nativeCurrentGoal(ctx, cfg, m, x.Goal); err != nil {
			return err
		}
		record.State, record.Step = "uncertain", step
		if err := saveNativeAction(cfg, record); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
		record.State = "prepared"
		return saveNativeAction(cfg, record)
	}
	if err = guard(); err != nil {
		return empty, err
	}
	if os.IsNotExist(readErr) {
		if in.Operation == "create-task" || in.Operation == "rebind-task" {
			if _, err = selectNativeTaskProject(ctx, cfg, reader, in); err != nil {
				return empty, err
			}
		}
		if err = saveNativeAction(cfg, record); err != nil {
			return empty, err
		}
	}
	switch in.Operation {
	case "rebind-task":
		if x.Job.Kind != "plan" || cfg.Name != cfg.Team.Planner {
			return empty, c.ErrForbidden
		}
		member, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
		if !ok || !memberCapability(member, "plan") {
			return empty, c.ErrForbidden
		}
		existing, e := reader.ReadNativeTask(ctx, cfg, in.TaskURL)
		if e != nil {
			return empty, e
		}
		if existing.Task.Closed || existing.Task.Completed {
			return empty, errors.New("completed native task cannot be rebound")
		}
		if record.Step == "" {
			old, e := m.ResolveGoal(ctx, existing.Binding.Goal)
			if e != nil {
				return empty, e
			}
			for after := ""; ; {
				page, e := m.Artifacts(ctx, c.ArtifactQuery{Goal: existing.Binding.Goal, After: after})
				if e != nil {
					return empty, e
				}
				for _, link := range page.Links {
					if link.Kind == "pull_request" && link.TaskURL == existing.Task.URL {
						pr, e := reader.ReadNativePR(ctx, cfg, existing, old, link.URL)
						if e != nil {
							return empty, e
						}
						if pr.Merged {
							return empty, errors.New("merged native delivery cannot be rebound")
						}
					}
				}
				if page.Next == "" {
					break
				}
				if page.Next == after {
					return empty, c.ErrConflict
				}
				after = page.Next
			}
			record.ExpectedAssignmentSHA = NativeAssignmentSHA(existing.Binding)
		}
		b := NativeTaskBinding{Version: 3, Goal: x.Goal, TaskURL: existing.Task.URL, TaskScopeSHA: existing.Task.BodySHA, ApprovalSequence: a.ApprovalSequence, BaseBranch: in.BaseBranch, AuthorID: a.Actors.Author.GitHubBotID, PeerID: a.Actors.Peer.GitHubBotID, DependsOn: in.DependsOn}
		b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
		if a.Revision.Proposal.QA.Required {
			b.QAID = a.Actors.QA.GitHubBotID
		}
		if err = validateNativeRoster(cfg, b); err != nil {
			return empty, err
		}
		if _, err = ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: m}, b); err != nil {
			return empty, err
		}
		for _, ref := range in.DependsOn {
			dep, e := reader.ReadNativeTask(ctx, cfg, ref)
			if e != nil || dep.Binding.Goal != x.Goal {
				return empty, c.ErrForbidden
			}
		}
		if record.Step == "" {
			if err = reader.verifyOwner(ctx, strings.Split(existing.Task.Repo, "/")[0]); err != nil {
				return empty, err
			}
			if err = effect("task-metadata", func() error {
				_, n, _ := parseRef(b.TaskURL)
				_, _, e := reader.api(ctx, strings.Split(existing.Task.Repo, "/")[0], "POST", "repos/"+existing.Task.Repo+"/issues/"+stringID(n)+"/comments", map[string]string{"body": NativeTaskMetadata(b)})
				return e
			}); err != nil {
				return empty, err
			}
		}
		fresh, e := reader.ReadNativeTask(ctx, cfg, b.TaskURL)
		if e != nil || NativeAssignmentSHA(fresh.Binding) != NativeAssignmentSHA(b) {
			if e != nil {
				return empty, e
			}
			return empty, c.ErrConflict
		}
		if _, err = m.RegisterArtifact(ctx, c.ArtifactLink{Goal: x.Goal, Kind: "task", URL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b), ExpectedAssignmentSHA: record.ExpectedAssignmentSHA, RequestID: "native-rebind-" + BodyHash(jobID+in.RequestID)}); err != nil {
			return empty, err
		}
		record.Result.URL = b.TaskURL
		if err = nativePlaceTask(ctx, cfg, reader, in, b, &record, effect); err != nil {
			return empty, err
		}
	case "create-task":
		if x.Job.Kind != "plan" || cfg.Name != cfg.Team.Planner {
			return empty, c.ErrForbidden
		}
		member, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
		if !ok || !memberCapability(member, "plan") {
			return empty, c.ErrForbidden
		}
		allowed := false
		for _, scope := range a.Revision.Proposal.Repositories {
			if scope.Name == in.Repository && containsExact(scope.BaseBranches, in.BaseBranch) {
				allowed = true
			}
		}
		if !allowed || !validRepo(in.Repository) || !containsFold(cfg.CodeOwners, strings.Split(in.Repository, "/")[0]) || len(cfg.Repositories) > 0 && !containsFold(cfg.Repositories, in.Repository) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || strings.Contains(in.Body, "<!-- coop-") {
			return empty, c.ErrInvalid
		}
		if err = reader.verifyOwner(ctx, strings.Split(in.Repository, "/")[0]); err != nil {
			return empty, err
		}
		b := NativeTaskBinding{Version: 3, Goal: x.Goal, TaskScopeSHA: TaskScopeHash(in.Title, in.Body), ApprovalSequence: a.ApprovalSequence, BaseBranch: in.BaseBranch, AuthorID: a.Actors.Author.GitHubBotID, PeerID: a.Actors.Peer.GitHubBotID, DependsOn: in.DependsOn}
		b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
		if a.Revision.Proposal.QA.Required {
			b.QAID = a.Actors.QA.GitHubBotID
		}
		if err = validateNativeRoster(cfg, b); err != nil {
			return empty, err
		}
		for _, ref := range in.DependsOn {
			dep, e := reader.ReadNativeTask(ctx, cfg, ref)
			if e != nil || dep.Binding.Goal != x.Goal {
				return empty, c.ErrForbidden
			}
		}
		if record.Result.URL == "" {
			err = effect("create-task", func() error {
				raw, _, e := reader.api(ctx, strings.Split(in.Repository, "/")[0], "POST", "repos/"+in.Repository+"/issues", map[string]any{"title": in.Title, "body": in.Body})
				if e != nil {
					return e
				}
				var issue restIssue
				if json.Unmarshal(raw, &issue) != nil || !referenceInRepo(issue.URL, in.Repository, "issues") {
					return c.ErrConflict
				}
				record.Result.URL = issue.URL
				return nil
			})
			if err != nil {
				return empty, err
			}
		}
		b.TaskURL = record.Result.URL
		if record.Step == "create-task" {
			if err = effect("task-metadata", func() error {
				_, n, e := parseRef(b.TaskURL)
				if e != nil {
					return e
				}
				_, _, e = reader.api(ctx, strings.Split(in.Repository, "/")[0], "POST", "repos/"+in.Repository+"/issues/"+stringID(n)+"/comments", map[string]string{"body": NativeTaskMetadata(b)})
				return e
			}); err != nil {
				return empty, err
			}
		}
		fresh, e := reader.ReadNativeTask(ctx, cfg, b.TaskURL)
		if e != nil || NativeAssignmentSHA(fresh.Binding) != NativeAssignmentSHA(b) {
			if e != nil {
				return empty, e
			}
			return empty, c.ErrConflict
		}
		if _, e = ValidateNativeAuthority(ctx, MasterGoalAuthority{Client: m}, b); e != nil {
			return empty, e
		}
		// Master registration is request-idempotent and may be retried safely.
		if _, err = m.RegisterArtifact(ctx, c.ArtifactLink{Goal: x.Goal, Kind: "task", URL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b), RequestID: "native-task-" + BodyHash(jobID+in.RequestID)}); err != nil {
			return empty, err
		}
		if err = nativePlaceTask(ctx, cfg, reader, in, b, &record, effect); err != nil {
			return empty, err
		}
	case "complete-plan":
		if x.Job.Kind != "plan" || cfg.Name != cfg.Team.Planner {
			return empty, c.ErrForbidden
		}
		page, e := m.Artifacts(ctx, c.ArtifactQuery{Goal: x.Goal})
		if e != nil {
			return empty, e
		}
		found := false
		for _, link := range page.Links {
			if link.Kind == "task" {
				found = true
			}
		}
		if !found {
			return empty, errors.New("plan has no registered tasks")
		}
	case "publish-pr":
		if x.Binding == nil || (x.Job.Kind != "implement" && x.Job.Kind != "fix") || cfg.Self.ID != task.Binding.AuthorID || cfg.Master.AgentID != a.Actors.Author.PrincipalID || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || strings.Contains(in.Body, "<!-- coop-") {
			return empty, c.ErrForbidden
		}
		if err = reader.verifyOwner(ctx, strings.Split(task.Task.Repo, "/")[0]); err != nil {
			return empty, err
		}
		if record.Step == "" {
			catalog, e := reader.NativeSnapshot(ctx, cfg, m)
			if e != nil {
				return empty, e
			}
			found := false
			for _, v := range NativeCandidates(cfg, catalog, nil, time.Now().UTC()) {
				if v.Job.ID == jobID {
					found = true
				}
			}
			if !found {
				return empty, c.ErrForbidden
			}
		}
		prepared, e := NewCheckouts(cfg, g.runner).Prepare(ctx, x.Job)
		if e != nil {
			return empty, e
		}
		local := NewCheckouts(cfg, g.runner)
		status, e := local.run(ctx, prepared.Checkout, "git", "status", "--porcelain")
		if e != nil || len(status) != 0 {
			return empty, errors.New("checkpoint local changes before publication")
		}
		head, e := local.run(ctx, prepared.Checkout, "git", "rev-parse", "HEAD")
		if e != nil {
			return empty, e
		}
		headSHA := strings.TrimSpace(string(head))
		if !qaHeadSHA.MatchString(headSHA) {
			return empty, c.ErrInvalid
		}
		meta := NativePRMetadata{Version: 3, Goal: x.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), Artifact: in.Artifact}
		if a.Revision.Proposal.QA.Required && meta.Artifact != nil && (meta.Artifact.HeadSHA != headSHA || !hexSHA.MatchString(meta.Artifact.SHA256)) {
			return empty, errors.New("required immutable exact-head QA artifact missing")
		}
		if record.PublishedHeadSHA != "" && record.PublishedHeadSHA != headSHA {
			return empty, errors.New("local head changed during journaled publication")
		}
		record.PublishedHeadSHA = headSHA
		if record.Step == "" {
			if err = effect("push", func() error {
				_, e := local.run(ctx, prepared.Checkout, "git", "push", "origin", "HEAD:refs/heads/"+prepared.Branch)
				return e
			}); err != nil {
				return empty, err
			}
		}
		if a.Revision.Proposal.QA.Required {
			artifactTask := task.Task
			artifactTask.QA.Required = true
			artifactPR := PR{HeadSHA: headSHA}
			if err = reader.bindQAArtifact(ctx, artifactTask, &artifactPR, meta.Artifact); err != nil {
				return empty, err
			}
			meta.Artifact = artifactPR.QAArtifact
		}
		body := in.Body + "\n\n" + NativePRBody(meta)
		if record.Result.URL == "" {
			if err = effect("publish-pr", func() error {
				endpoint := "repos/" + task.Task.Repo + "/pulls"
				method := "POST"
				input := map[string]string{"title": in.Title, "body": body, "head": prepared.Branch, "base": task.Binding.BaseBranch}
				if x.Job.PRURL != "" {
					_, n, _ := parseRef(x.Job.PRURL)
					endpoint += "/" + stringID(n)
					method = "PATCH"
					delete(input, "head")
					delete(input, "base")
				}
				raw, _, e := reader.api(ctx, strings.Split(task.Task.Repo, "/")[0], method, endpoint, input)
				if e != nil {
					return e
				}
				var result struct {
					URL string `json:"html_url"`
				}
				if json.Unmarshal(raw, &result) != nil || !referenceInRepo(result.URL, task.Task.Repo, "pull") {
					return c.ErrConflict
				}
				record.Result.URL = result.URL
				return nil
			}); err != nil {
				return empty, err
			}
		}
		fresh, e := reader.ReadNativePR(ctx, cfg, task, a, record.Result.URL)
		if e != nil || fresh.HeadSHA != headSHA {
			if e != nil {
				return empty, e
			}
			return empty, c.ErrConflict
		}
		if _, err = nativeCurrentGoal(ctx, cfg, m, x.Goal); err != nil {
			return empty, err
		}
		if _, err = m.RegisterArtifact(ctx, c.ArtifactLink{Goal: x.Goal, Kind: "pull_request", URL: fresh.URL, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), RequestID: "native-pr-" + BodyHash(jobID+in.RequestID)}); err != nil {
			return empty, err
		}
	case "review", "qa":
		if x.Binding == nil || x.Job.PRURL == "" || pr.Merged || pr.Closed || !pr.ReviewsComplete || !pr.ContributorsComplete || nativeContributor(pr, cfg.Self.ID) || cfg.Self.ID == task.Binding.AuthorID || (in.Verdict != "approved" && in.Verdict != "changes-needed") {
			return empty, c.ErrForbidden
		}
		if in.Operation != x.Job.Kind {
			return empty, c.ErrForbidden
		}
		if err = reader.verifyOwner(ctx, strings.Split(task.Task.Repo, "/")[0]); err != nil {
			return empty, err
		}
		public := ""
		event := "APPROVE"
		if in.Verdict == "changes-needed" {
			event = "REQUEST_CHANGES"
		}
		if in.Operation == "review" {
			if cfg.Self.ID != task.Binding.PeerID || cfg.Master.AgentID != a.Actors.Peer.PrincipalID {
				return empty, c.ErrForbidden
			}
			if strings.Contains(in.Body, "<!-- coop-") {
				return empty, c.ErrInvalid
			}
			public = in.Body + "\n\n" + NativePeerMetadata(NativeReviewResult{Version: 3, Goal: x.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: pr.HeadSHA, PRScopeRevision: pr.ScopeRevision, Verdict: in.Verdict})
		} else {
			_, peer := nativePeerCleared(task.Binding, pr)
			if cfg.Self.ID != task.Binding.QAID || cfg.Master.AgentID != a.Actors.QA.PrincipalID || !peer || !a.Revision.Proposal.QA.Required || pr.QAArtifact == nil || pr.ChecksState != "success" && pr.ChecksState != "none" || strings.TrimSpace(in.ReportBody) == "" {
				return empty, c.ErrForbidden
			}
			if record.Result.EvidenceMessageID == "" {
				if err = guard(); err != nil {
					return empty, err
				}
				receipt, e := m.Send(ctx, c.SendMessage{ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: in.ReportBody, RequestID: "native-qa-" + BodyHash(jobID+in.RequestID)})
				if e != nil {
					return empty, e
				}
				record.Result.EvidenceMessageID = receipt.MessageID
				record.Result.EvidenceBodySHA = BodyHash(in.ReportBody)
				if err = saveNativeAction(cfg, record); err != nil {
					return empty, err
				}
			}
			report, e := m.Message(ctx, record.Result.EvidenceMessageID)
			if e != nil {
				return empty, e
			}
			dest := a.Revision.Proposal.QA.EvidenceDestination
			if report.Imported || report.ID != record.Result.EvidenceMessageID || report.AuthorID != cfg.Master.AgentID || report.AuthorKind != "agent" || report.ThreadID != dest.ThreadID || BodyHash(report.Body) != record.Result.EvidenceBodySHA {
				return empty, c.ErrForbidden
			}
			qa := NativeQAResult{Version: 3, Goal: x.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: pr.HeadSHA, PRScopeRevision: pr.ScopeRevision, ArtifactSHA: pr.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(a), Environment: a.Revision.Proposal.QA.Environment, EnvironmentRevision: a.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: report.ID, EvidenceBodySHA: record.Result.EvidenceBodySHA, Verdict: in.Verdict}
			public = "Independent customer QA " + in.Verdict + " for this exact head. Private evidence is retained at the approved master destination.\n\n" + NativeQAMetadata(qa)
		}
		if record.Step != "review-published" {
			if err = effect("review-published", func() error {
				_, n, _ := parseRef(pr.URL)
				_, _, e := reader.api(ctx, strings.Split(task.Task.Repo, "/")[0], "POST", "repos/"+task.Task.Repo+"/pulls/"+stringID(n)+"/reviews", map[string]string{"event": event, "commit_id": pr.HeadSHA, "body": public})
				return e
			}); err != nil {
				return empty, err
			}
		}
		// Re-read actual GitHub reviews. Caller text cannot manufacture a review.
		fresh, e := reader.ReadNativePR(ctx, cfg, task, a, pr.URL)
		if e != nil {
			return empty, e
		}
		actual := nativeLatestReview(fresh, cfg.Self.ID)
		expectedState := "APPROVED"
		if event == "REQUEST_CHANGES" {
			expectedState = "CHANGES_REQUESTED"
		}
		if actual.ID == "" || actual.HeadSHA != x.Job.HeadSHA || actual.State != expectedState || actual.Body != public {
			return empty, c.ErrConflict
		}
		record.Result.URL = pr.URL
	case "deliver":
		if x.Job.Kind != "deliver" || x.Binding == nil || cfg.Self.ID != task.Binding.AuthorID || cfg.Master.AgentID != a.Actors.Author.PrincipalID {
			return empty, c.ErrForbidden
		}
		if !pr.Merged {
			catalog, e := reader.NativeSnapshot(ctx, cfg, m)
			if e != nil {
				return empty, e
			}
			admitted := false
			for _, v := range NativeCandidates(cfg, catalog, nil, time.Now().UTC()) {
				if v.Job.ID == jobID && v.Job.Kind == "deliver" {
					admitted = true
				}
			}
			if !admitted {
				return empty, c.ErrForbidden
			}
			if record.Step == "" {
				if err = effect("merge", func() error {
					_, n, _ := parseRef(pr.URL)
					raw, _, e := reader.api(ctx, strings.Split(task.Task.Repo, "/")[0], "PUT", "repos/"+task.Task.Repo+"/pulls/"+stringID(n)+"/merge", map[string]string{"sha": x.Job.HeadSHA, "merge_method": "squash"})
					if e != nil {
						return e
					}
					var result struct{ Merged bool }
					if json.Unmarshal(raw, &result) != nil || !result.Merged {
						return c.ErrConflict
					}
					return nil
				}); err != nil {
					return empty, err
				}
			}
		}
		fresh, e := reader.ReadNativePR(ctx, cfg, task, a, pr.URL)
		if e != nil || !fresh.Merged || fresh.HeadSHA != x.Job.HeadSHA {
			if e != nil {
				return empty, e
			}
			return empty, c.ErrConflict
		}
		if record.Step != "task-completed" {
			if err = effect("task-completed", func() error {
				_, n, _ := parseRef(task.Task.URL)
				_, _, e := reader.api(ctx, strings.Split(task.Task.Repo, "/")[0], "PATCH", "repos/"+task.Task.Repo+"/issues/"+stringID(n), map[string]string{"state": "closed", "state_reason": "completed"})
				return e
			}); err != nil {
				return empty, err
			}
		}
		record.Result.URL = pr.URL
	default:
		return empty, c.ErrInvalid
	}
	if in.Operation == "deliver" {
		if err = nativeConfirmDelivery(ctx, cfg, reader, a, x); err != nil {
			return empty, err
		}
	}
	record.State = "completed"
	record.Result.Phase = "completed"
	if err = saveNativeAction(cfg, record); err != nil {
		return empty, err
	}
	return record.Result, nil
}

func nativeConfirmDelivery(ctx context.Context, cfg Config, g *GitHubClient, a c.GoalAuthority, x NativeExecution) error {
	if x.Job.Kind != "deliver" || x.Binding == nil || x.Job.Master == nil || cfg.Self.ID != x.Binding.AuthorID || cfg.Master.AgentID != a.Actors.Author.PrincipalID {
		return c.ErrForbidden
	}
	task, err := g.ReadNativeTask(ctx, cfg, x.Job.TaskURL)
	if err != nil {
		return err
	}
	if !task.Task.Completed || !task.Task.Closed || NativeAssignmentSHA(task.Binding) != x.Job.Master.AssignmentSHA {
		return c.ErrConflict
	}
	pr, err := g.ReadNativePR(ctx, cfg, task, a, x.Job.PRURL)
	if err != nil {
		return err
	}
	if !pr.Merged || pr.HeadSHA != x.Job.HeadSHA || pr.ScopeRevision != x.Job.PRScopeRevision {
		return c.ErrConflict
	}
	return nil
}
