package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

func (svc *Service) TaskCapabilities(ctx context.Context, actor c.Principal) (c.TaskCapabilities, error) {
	if _, err := authorize(ctx, svc.Store.db, actor, "read"); err != nil {
		return c.TaskCapabilities{}, err
	}
	if svc.Tasks == nil {
		return c.TaskCapabilities{}, c.ErrUnsupported
	}
	out := svc.Tasks.Capabilities()
	if svc.Store.ConversationActive(ctx) && svc.Store.conversations.TeamThreadID != "" {
		thread, e := svc.Store.Thread(ctx, actor, svc.Store.conversations.TeamThreadID)
		if e == nil && thread.Source != nil && thread.Source.IssueUID == svc.Store.conversations.TeamIssueUID {
			source := *thread.Source
			out.Conversation = &source
		}
	}
	return out, nil
}
func (svc *Service) taskAuthority(ctx context.Context, actor c.Principal, goal c.GoalRef, mutation bool) (c.Principal, c.GoalAuthority, error) {
	if svc.Tasks == nil {
		return c.Principal{}, c.GoalAuthority{}, c.ErrUnsupported
	}
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return p, c.GoalAuthority{}, err
	}
	a, err := svc.Store.resolveGoal(ctx, svc.Store.db, p, goal)
	if err != nil {
		return p, a, err
	}
	if mutation {
		if err = admission(ctx, svc.Store.db); err != nil {
			return p, a, err
		}
		if !a.Valid {
			return p, a, c.ErrForbidden
		}
	}
	return p, a, nil
}
func (svc *Service) TaskRead(ctx context.Context, actor c.Principal, in c.TaskReadInput) (c.ProviderTask, error) {
	if _, err := svc.TaskCapabilities(ctx, actor); err != nil {
		return c.ProviderTask{}, err
	}
	task, err := svc.Tasks.Read(ctx, in.Reference)
	if err != nil {
		if errors.Is(err, c.ErrInvalid) || errors.Is(err, c.ErrNotFound) {
			return svc.taskIntakeRead(ctx, actor, in.Reference)
		}
		return task, err
	}
	if _, _, err = svc.taskAuthority(ctx, actor, task.Goal, false); err != nil {
		return c.ProviderTask{}, err
	}
	return task, nil
}
func (svc *Service) TaskList(ctx context.Context, actor c.Principal, in c.TaskListInput) (c.ProviderTaskPage, error) {
	out := c.ProviderTaskPage{Tasks: []c.ProviderTask{}}
	if _, _, err := svc.taskAuthority(ctx, actor, in.Goal, false); err != nil {
		return out, err
	}
	after := ""
	for {
		page, err := svc.Artifacts(ctx, actor, c.ArtifactQuery{Goal: in.Goal, After: after})
		if err != nil {
			return out, err
		}
		for _, link := range page.Links {
			if link.Kind != "task" || !strings.HasPrefix(link.URL, "kata:") {
				continue
			}
			task, err := svc.TaskRead(ctx, actor, c.TaskReadInput{Reference: link.URL})
			if err != nil {
				return out, err
			}
			if task.Goal != in.Goal || task.Binding == nil || digest(task.Binding) != link.AssignmentSHA {
				return out, c.ErrConflict
			}
			if len(out.Tasks) >= c.MaxPageRecords {
				return out, c.ErrSize
			}
			out.Tasks = append(out.Tasks, task)
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	return out, nil
}
func (svc *Service) taskBinding(ctx context.Context, p c.Principal, a c.GoalAuthority, task c.ProviderTask, base string, deps []string) (c.NativeTaskBinding, error) {
	allowed := false
	for _, r := range a.Revision.Proposal.Repositories {
		if r.Name == task.Repository && slices.Contains(r.BaseBranches, base) {
			allowed = true
		}
	}
	if !allowed {
		return c.NativeTaskBinding{}, c.ErrForbidden
	}
	if len(deps) > c.MaxPageRecords {
		return c.NativeTaskBinding{}, c.ErrInvalid
	}
	if svc.Tasks.Capabilities().Provider != "kata" {
		return c.NativeTaskBinding{}, c.ErrConflict
	}
	project, _, repo, err := c.ParseKataTaskReference(task.Reference)
	if err != nil || project != svc.Tasks.Capabilities().ProjectUID || repo != task.Repository || task.Goal != a.Revision.Ref {
		return c.NativeTaskBinding{}, c.ErrConflict
	}
	seen := map[string]bool{}
	for _, ref := range deps {
		if ref == task.Reference || seen[ref] {
			return c.NativeTaskBinding{}, c.ErrInvalid
		}
		seen[ref] = true
		dependency, err := svc.TaskRead(ctx, p, c.TaskReadInput{Reference: ref})
		if err != nil {
			return c.NativeTaskBinding{}, err
		}
		if dependency.Goal != task.Goal || dependency.Binding == nil {
			return c.NativeTaskBinding{}, c.ErrForbidden
		}
	}
	return c.NativeTaskBinding{Version: 3, Goal: task.Goal, TaskURL: task.Reference, TaskScopeSHA: digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{task.Title, task.Body}), PolicySHA: svc.Tasks.Capabilities().PolicySHA, ApprovalSequence: a.ApprovalSequence, BaseBranch: base, AuthorID: a.Actors.Author.GitHubBotID, PeerID: a.Actors.Peer.GitHubBotID, QAID: a.Actors.QA.GitHubBotID, DependsOn: deps}, nil
}
func planner(p c.Principal) bool { return p.Kind == "owner" || slices.Contains(p.Capabilities, "plan") }

type taskCreateIntent struct {
	ProjectUID       string              `json:"project_uid,omitempty"`
	ProviderKey      string              `json:"provider_key,omitempty"`
	ObservedIssueUID string              `json:"observed_issue_uid,omitempty"`
	Binding          c.NativeTaskBinding `json:"binding"`
	Reference        string              `json:"reference,omitempty"`
	ProviderRevision int64               `json:"provider_revision,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
}

func (svc *Service) TaskCreate(ctx context.Context, actor c.Principal, in c.TaskCreateInput) (c.ProviderTask, error) {
	if len(in.DependsOn) == 0 {
		in.DependsOn = nil
	}
	p, a, err := svc.taskAuthority(ctx, actor, in.Goal, true)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !planner(p) {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if in.ExpectedApprovalSequence != a.ApprovalSequence {
		return c.ProviderTask{}, c.ErrConflict
	}
	if !validID(in.RequestID) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 1024 || len(in.Body) > c.MaxText || len(in.DependsOn) > c.MaxPageRecords {
		return c.ProviderTask{}, c.ErrInvalid
	}
	// Authorize scope and dependencies before creating anything in Kata.
	placeholder, _ := c.KataTaskReference(svc.Tasks.Capabilities().ProjectUID, svc.Tasks.Capabilities().ProjectUID, in.Repository)
	intent, err := svc.taskBinding(ctx, p, a, c.ProviderTask{Reference: placeholder, Repository: in.Repository, Goal: in.Goal, Title: in.Title, Body: in.Body}, in.BaseBranch, in.DependsOn)
	if err != nil {
		return c.ProviderTask{}, err
	}
	intent.TaskURL = ""
	// Persist the original approval intent before the external write. A retry after
	// a lost reply must reuse both its original payload and its approved actors.
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.ProviderTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	project := svc.Tasks.Capabilities().ProjectUID
	providerKey := taskstore.CreationKey(in.Goal, in.RequestID)
	saved := taskCreateIntent{Binding: intent, CreatedAt: svc.Store.now().UTC(), ProjectUID: project, ProviderKey: providerKey}
	found, err := request(ctx, tx, p.ID, in.RequestID, "task-create", in, &saved)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if found {
		if !reflect.DeepEqual(saved.Binding, intent) || (saved.ProjectUID != "" && saved.ProjectUID != project) || (saved.ProviderKey != "" && saved.ProviderKey != providerKey) {
			return c.ProviderTask{}, c.ErrConflict
		}
		if saved.ProjectUID == "" || saved.ProviderKey == "" {
			saved.ProjectUID = project
			saved.ProviderKey = providerKey
			if saved.Reference != "" {
				p, uid, _, e := c.ParseKataTaskReference(saved.Reference)
				if e != nil || p != project {
					return c.ProviderTask{}, c.ErrConflict
				}
				saved.ObservedIssueUID = uid
			}
			if _, err = tx.ExecContext(ctx, "UPDATE requests SET result=? WHERE actor=? AND request_id=?", encode(saved), p.ID, in.RequestID); err != nil {
				return c.ProviderTask{}, err
			}
		}
	} else if err = saveRequest(ctx, tx, p.ID, in.RequestID, "task-create", in, saved); err != nil {
		return c.ProviderTask{}, err
	}
	if err = tx.Commit(); err != nil {
		return c.ProviderTask{}, err
	}
	var task c.ProviderTask
	if saved.Reference != "" {
		task, err = svc.Tasks.Read(ctx, saved.Reference)
	} else {
		// Kata retains native create keys for seven days. An unresolved older
		// attempt needs operator reconciliation; reissuing it could create a twin.
		if saved.CreatedAt.IsZero() || svc.Store.now().UTC().Sub(saved.CreatedAt) >= 7*24*time.Hour {
			return c.ProviderTask{}, c.ErrConflict
		}
		task, err = svc.Tasks.Create(ctx, in, p)
	}
	if err != nil {
		return task, err
	}
	if task.Goal != in.Goal || task.Repository != in.Repository || task.Title != in.Title || task.Body != in.Body {
		return c.ProviderTask{}, c.ErrConflict
	}
	if saved.Reference == "" {
		tx, err := svc.Store.db.BeginTx(ctx, nil)
		if err != nil {
			return c.ProviderTask{}, err
		}
		defer func() { _ = tx.Rollback() }()
		var current taskCreateIntent
		found, err := request(ctx, tx, p.ID, in.RequestID, "task-create", in, &current)
		if err != nil {
			return c.ProviderTask{}, err
		}
		if !found || !reflect.DeepEqual(current.Binding, intent) || (current.Reference != "" && current.Reference != task.Reference) {
			return c.ProviderTask{}, c.ErrConflict
		}
		if current.ObservedIssueUID != "" && current.ObservedIssueUID != task.IssueUID {
			return c.ProviderTask{}, c.ErrConflict
		}
		current.ObservedIssueUID = task.IssueUID
		current.Reference = task.Reference
		current.ProviderRevision = task.Revision
		saved = current
		if _, err = tx.ExecContext(ctx, "UPDATE requests SET result=? WHERE actor=? AND request_id=?", encode(current), p.ID, in.RequestID); err != nil {
			return c.ProviderTask{}, err
		}
		if err = tx.Commit(); err != nil {
			return c.ProviderTask{}, err
		}
	}
	binding, err := svc.taskBinding(ctx, p, a, task, in.BaseBranch, in.DependsOn)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if task.Binding != nil && !reflect.DeepEqual(*task.Binding, binding) {
		return c.ProviderTask{}, c.ErrConflict
	}
	return svc.TaskBind(ctx, p, c.TaskBindInput{Binding: binding, ExpectedRevision: saved.ProviderRevision, RequestID: "create-bind-" + digest(struct{ Actor, Request string }{p.ID, in.RequestID})})
}
func taskPublished(ctx context.Context, q queryer, goal c.GoalRef, ref string) (bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT body FROM artifacts WHERE goal_id=? AND revision=? AND kind='pull_request'", goal.GoalID, goal.RevisionSHA256)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var link c.ArtifactLink
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		if err = json.Unmarshal(raw, &link); err != nil {
			return false, err
		}
		if link.TaskURL == ref {
			return true, nil
		}
	}
	return false, rows.Err()
}
func (svc *Service) TaskBind(ctx context.Context, actor c.Principal, in c.TaskBindInput) (c.ProviderTask, error) {
	if len(in.Binding.DependsOn) == 0 {
		in.Binding.DependsOn = nil
	}
	p, a, err := svc.taskAuthority(ctx, actor, in.Binding.Goal, true)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !planner(p) {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if !validID(in.RequestID) || in.ExpectedRevision < 1 {
		return c.ProviderTask{}, c.ErrInvalid
	}
	task, err := svc.Tasks.Read(ctx, in.Binding.TaskURL)
	if err != nil {
		return task, err
	}
	expected, err := svc.taskBinding(ctx, p, a, task, in.Binding.BaseBranch, in.Binding.DependsOn)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !reflect.DeepEqual(expected, in.Binding) {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if err = svc.reconcileTaskMutation(ctx, task); err != nil {
		return c.ProviderTask{}, err
	}
	same := task.Binding != nil && reflect.DeepEqual(*task.Binding, in.Binding)
	if !same {
		old := ""
		if task.Binding != nil {
			old = digest(task.Binding)
		}
		if in.ExpectedRevision != task.Revision || in.ExpectedAssignmentSHA != old {
			return c.ProviderTask{}, c.ErrConflict
		}
	}
	reservation, err := svc.reserveTaskBinding(ctx, p.ID, in, same, task)
	if err != nil {
		return c.ProviderTask{}, err
	}
	sha := digest(in.Binding)
	result, err := svc.Tasks.Bind(ctx, in, p)
	if err != nil {
		observed, readErr := svc.Tasks.Read(ctx, in.Binding.TaskURL)
		if readErr != nil {
			return result, errors.Join(err, readErr)
		}
		return result, errors.Join(err, svc.reconcileTaskMutation(ctx, observed))
	}
	_, err = svc.registerArtifact(ctx, p, c.ArtifactLink{Goal: in.Binding.Goal, Kind: "task", URL: in.Binding.TaskURL, AssignmentSHA: sha, ExpectedAssignmentSHA: in.ExpectedAssignmentSHA, RequestID: "task-register-" + digest(struct{ Actor, Request string }{p.ID, in.RequestID})}, &reservation, func(tx *sql.Tx) error {
		return svc.taskBindingAuthority(ctx, tx, p, in.Binding)
	})
	return result, err
}
func (svc *Service) TaskComplete(ctx context.Context, actor c.Principal, in c.TaskCompleteInput) (c.ProviderTask, error) {
	p, a, err := svc.taskAuthority(ctx, actor, in.Goal, true)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if p.Kind != "owner" && p.ID != a.Actors.Author.PrincipalID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if !validID(in.RequestID) {
		return c.ProviderTask{}, c.ErrInvalid
	}
	task, err := svc.Tasks.Read(ctx, in.Reference)
	if err != nil {
		return task, err
	}
	if task.Goal != in.Goal || task.Binding == nil || digest(task.Binding) != in.AssignmentSHA {
		return c.ProviderTask{}, c.ErrConflict
	}
	expected, err := svc.taskBinding(ctx, p, a, task, task.Binding.BaseBranch, task.Binding.DependsOn)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !reflect.DeepEqual(expected, *task.Binding) {
		return c.ProviderTask{}, c.ErrConflict
	}
	repo, err := artifactRepo(in.PullRequestURL, "pull")
	if err != nil || repo != task.Repository {
		return c.ProviderTask{}, c.ErrInvalid
	}
	var taskSHA string
	if err = svc.Store.db.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", in.Goal.GoalID, in.Goal.RevisionSHA256, in.Reference).Scan(&taskSHA); err != nil || taskSHA != in.AssignmentSHA {
		return c.ProviderTask{}, c.ErrConflict
	}
	var raw []byte
	if err = svc.Store.db.QueryRowContext(ctx, "SELECT body FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='pull_request'", in.Goal.GoalID, in.Goal.RevisionSHA256, in.PullRequestURL).Scan(&raw); err != nil {
		return c.ProviderTask{}, c.ErrConflict
	}
	var link c.ArtifactLink
	if json.Unmarshal(raw, &link) != nil || link.TaskURL != in.Reference || link.AssignmentSHA != in.AssignmentSHA {
		return c.ProviderTask{}, c.ErrConflict
	}
	if err = svc.reserveTaskRequest(ctx, p.ID, in.RequestID, "task-complete", in); err != nil {
		return c.ProviderTask{}, err
	}
	return svc.Tasks.Complete(ctx, in, p)
}
func registerTasks(mux *http.ServeMux, svc *Service, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/tasks/capabilities", wrap(func(_ http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		return svc.TaskCapabilities(r.Context(), p)
	}))
	mux.HandleFunc("POST /api/v1/tasks/read", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskReadInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskRead(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/tasks/list", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskListInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskList(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/tasks/create", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskCreateInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskCreate(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/tasks/adopt", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskAdoptInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskAdopt(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/tasks/bind", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskBindInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskBind(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/tasks/complete", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TaskCompleteInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.TaskComplete(r.Context(), p, in)
	}))
}

// Reserve immutable mutation input before an external effect. Successful or
// ambiguous retries must retain exactly the original revision and payload.
func (svc *Service) reserveTaskRequest(ctx context.Context, actor, id, kind string, input any) error {
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var accepted bool
	found, err := request(ctx, tx, actor, id, kind, input, &accepted)
	if err != nil {
		return err
	}
	if !found {
		if err = saveRequest(ctx, tx, actor, id, kind, input, true); err != nil {
			return err
		}
	}
	return tx.Commit()
}
