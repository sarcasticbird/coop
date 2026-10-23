package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type taskAdoptIntent struct {
	Binding c.NativeTaskBinding `json:"binding"`
}

// TaskAdopt reserves the exact authenticated intent before the provider CAS. Raw
// native metadata cannot supply a goal, actor, policy, or admission permission.
func (svc *Service) TaskAdopt(ctx context.Context, actor c.Principal, in c.TaskAdoptInput) (c.ProviderTask, error) {
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
	if !validID(in.RequestID) || in.ExpectedRevision < 1 {
		return c.ProviderTask{}, c.ErrInvalid
	}
	if in.ExpectedApprovalSequence != a.ApprovalSequence {
		return c.ProviderTask{}, c.ErrConflict
	}
	backend, ok := svc.Tasks.(taskstore.AdoptionBackend)
	if !ok {
		return c.ProviderTask{}, c.ErrUnsupported
	}
	project, uid, repo, err := c.ParseKataTaskReference(in.Reference)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if project != svc.Tasks.Capabilities().ProjectUID || repo != in.Repository {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if svc.Store.conversations.TeamIssueUID == uid {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if err = svc.taskAdoptionConversation(ctx, svc.Store.db, p, a.Revision.Proposal.ThreadID, uid); err != nil {
		return c.ProviderTask{}, err
	}
	issue, err := backend.InspectIssue(ctx, uid)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if issue.UID != uid || issue.ProjectUID != project || issue.Status != "open" {
		return c.ProviderTask{}, c.ErrConflict
	}
	if strings.TrimSpace(issue.Title) == "" || len(issue.Title) > 1024 || len(issue.Body) > c.MaxText {
		return c.ProviderTask{}, c.ErrInvalid
	}
	task := c.ProviderTask{Reference: in.Reference, ProjectUID: project, IssueUID: uid, Repository: repo, Goal: in.Goal, Title: issue.Title, Body: issue.Body, State: issue.Status, Revision: issue.Revision}
	binding, err := svc.taskBinding(ctx, p, a, task, in.BaseBranch, in.DependsOn)
	if err != nil {
		return c.ProviderTask{}, err
	}
	// Dependencies must already be registered and retain their approved scope.
	for _, ref := range in.DependsOn {
		dep, e := svc.TaskRead(ctx, p, c.TaskReadInput{Reference: ref})
		if e != nil {
			return c.ProviderTask{}, e
		}
		expected, e := svc.taskBinding(ctx, p, a, dep, dep.Binding.BaseBranch, dep.Binding.DependsOn)
		if e != nil {
			return c.ProviderTask{}, e
		}
		if !reflect.DeepEqual(expected, *dep.Binding) {
			return c.ProviderTask{}, c.ErrConflict
		}
		var sha string
		if e = svc.Store.db.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", in.Goal.GoalID, in.Goal.RevisionSHA256, ref).Scan(&sha); e != nil || sha != digest(dep.Binding) {
			return c.ProviderTask{}, c.ErrConflict
		}
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.ProviderTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	intent := taskAdoptIntent{Binding: binding}
	found, err := request(ctx, tx, p.ID, in.RequestID, "task-adopt", in, &intent)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if found {
		if !reflect.DeepEqual(intent.Binding, binding) {
			return c.ProviderTask{}, c.ErrConflict
		}
	} else {
		// Even plausible forged reserved metadata is never accepted on first adoption.
		for key := range issue.Metadata {
			if strings.HasPrefix(key, "coop.") {
				return c.ProviderTask{}, c.ErrConflict
			}
		}
		if issue.Revision != in.ExpectedRevision {
			return c.ProviderTask{}, c.ErrConflict
		}
		// A UID cannot be adopted into a second goal or repository registry.
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE kind='task' AND url LIKE ?", "kata://"+strings.ToLower(project)+"/"+uid+"/%").Scan(&count); err != nil {
			return c.ProviderTask{}, err
		}
		if count > 0 {
			return c.ProviderTask{}, c.ErrConflict
		}
		if err = saveRequest(ctx, tx, p.ID, in.RequestID, "task-adopt", in, intent); err != nil {
			return c.ProviderTask{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return c.ProviderTask{}, err
	}
	same := false
	if len(issue.Metadata["coop.native"]) > 0 {
		observed, e := svc.Tasks.Read(ctx, in.Reference)
		if e != nil {
			return c.ProviderTask{}, e
		}
		if observed.Binding == nil || !reflect.DeepEqual(*observed.Binding, binding) || observed.BindingActor.ID != p.ID {
			return c.ProviderTask{}, c.ErrConflict
		}
		same = true
	} else if issue.Revision != in.ExpectedRevision {
		return c.ProviderTask{}, c.ErrConflict
	}
	bind := c.TaskBindInput{Binding: binding, ExpectedRevision: in.ExpectedRevision, RequestID: "adopt-bind-" + digest(struct{ Actor, Request string }{p.ID, in.RequestID})}
	if err = svc.reconcileTaskMutation(ctx, task); err != nil {
		return c.ProviderTask{}, err
	}
	reservation, err := svc.reserveTaskBinding(ctx, p.ID, bind, same)
	if err != nil {
		return c.ProviderTask{}, err
	}
	result, err := backend.Adopt(ctx, in, binding, p)
	if err != nil {
		return result, err
	}
	if result.Reference != in.Reference || result.IssueUID != uid || result.Binding == nil || !reflect.DeepEqual(*result.Binding, binding) || result.Title != issue.Title || result.Body != issue.Body {
		return c.ProviderTask{}, c.ErrConflict
	}
	var receipt c.MessageReceipt
	_, err = svc.registerArtifact(ctx, p, c.ArtifactLink{Goal: in.Goal, Kind: "task", URL: in.Reference, AssignmentSHA: digest(binding), RequestID: "adopt-register-" + digest(struct{ Actor, Request string }{p.ID, in.RequestID})}, &reservation, func(tx *sql.Tx) error {
		if e := svc.taskBindingAuthority(ctx, tx, p, binding); e != nil {
			return e
		}
		var thread string
		e := tx.QueryRowContext(ctx, "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", uid).Scan(&thread)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if e = member(ctx, tx, p, thread); e != nil {
			return e
		}
		receipt, e = svc.Store.stageCanonicalReceipt(ctx, tx, thread, fmt.Sprintf("Authenticated Coop planner %s adopted this existing issue for goal %s at exact revision %s, approval sequence %d. Repository %s, base %s. Assignment %s. Execution remains subject to current Coop approval and admission guards.", p.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, in.ExpectedApprovalSequence, in.Repository, in.BaseBranch, digest(binding)), "adopt-receipt-"+digest(struct{ Actor, Request string }{p.ID, in.RequestID}))
		return e
	})
	if err != nil {
		return result, err
	}
	if err = svc.Store.publishCanonicalReceipt(ctx, receipt); err != nil {
		return result, err
	}
	return result, nil
}

// taskIntakeRead exposes current native CAS state only to a mapped planner.
// No reserved metadata or registry failure may downgrade admitted work to intake.
func (svc *Service) taskIntakeRead(ctx context.Context, actor c.Principal, ref string) (c.ProviderTask, error) {
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !planner(p) {
		return c.ProviderTask{}, c.ErrForbidden
	}
	project, uid, repo, err := c.ParseKataTaskReference(ref)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if project != svc.Tasks.Capabilities().ProjectUID {
		return c.ProviderTask{}, c.ErrForbidden
	}
	var thread string
	var raw []byte
	if err = svc.Store.db.QueryRowContext(ctx, "SELECT thread_id,body FROM conversation_sources WHERE issue_uid=?", uid).Scan(&thread, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.ProviderTask{}, c.ErrNotFound
		}
		return c.ProviderTask{}, err
	}
	if err = member(ctx, svc.Store.db, p, thread); err != nil {
		return c.ProviderTask{}, err
	}
	var source c.ConversationSource
	if json.Unmarshal(raw, &source) != nil || source.Provider != "kata" || source.ProjectUID != project || source.IssueUID != uid {
		return c.ProviderTask{}, c.ErrConflict
	}
	backend, ok := svc.Tasks.(taskstore.AdoptionBackend)
	if !ok {
		return c.ProviderTask{}, c.ErrUnsupported
	}
	var registered int
	if err = svc.Store.db.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE kind='task' AND url LIKE ?", "kata://"+strings.ToLower(project)+"/"+uid+"/%").Scan(&registered); err != nil {
		return c.ProviderTask{}, err
	}
	if registered > 0 {
		return c.ProviderTask{}, c.ErrConflict
	}
	issue, err := backend.InspectIssue(ctx, uid)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if issue.ProjectUID != project || issue.UID != uid {
		return c.ProviderTask{}, c.ErrConflict
	}
	for key := range issue.Metadata {
		if strings.HasPrefix(key, "coop.") {
			return c.ProviderTask{}, c.ErrConflict
		}
	}
	// Provider I/O can overlap revocation, mapping changes, or admission. Read
	// current authority and the intake registry together before exposing text.
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.ProviderTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err = authorize(ctx, tx, actor, "read")
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !planner(p) {
		return c.ProviderTask{}, c.ErrForbidden
	}
	var currentThread string
	var currentRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT thread_id,body FROM conversation_sources WHERE issue_uid=?", uid).Scan(&currentThread, &currentRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.ProviderTask{}, c.ErrConflict
		}
		return c.ProviderTask{}, err
	}
	var currentSource c.ConversationSource
	if json.Unmarshal(currentRaw, &currentSource) != nil || currentThread != thread || currentSource != source || project != svc.Tasks.Capabilities().ProjectUID {
		return c.ProviderTask{}, c.ErrConflict
	}
	if err = member(ctx, tx, p, currentThread); err != nil {
		return c.ProviderTask{}, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE kind='task' AND url LIKE ?", "kata://"+strings.ToLower(project)+"/"+uid+"/%").Scan(&registered); err != nil {
		return c.ProviderTask{}, err
	}
	if registered != 0 {
		return c.ProviderTask{}, c.ErrConflict
	}
	return c.ProviderTask{Reference: ref, IssueUID: uid, ProjectUID: project, Repository: repo, Title: issue.Title, Body: issue.Body, State: issue.Status, Revision: issue.Revision}, nil
}

// Adoption recovery retains the source-thread access and engagement boundary.
func (svc *Service) taskAdoptionConversation(ctx context.Context, q queryer, p c.Principal, goalThread, uid string) error {
	var mappedThread string
	mapErr := q.QueryRowContext(ctx, "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", uid).Scan(&mappedThread)
	if mapErr != nil && !errors.Is(mapErr, sql.ErrNoRows) {
		return mapErr
	}
	if mapErr == nil {
		if err := member(ctx, q, p, mappedThread); err != nil {
			return err
		}
		var sourceEng, goalEng string
		if err := q.QueryRowContext(ctx, "SELECT engagement FROM threads WHERE id=?", mappedThread).Scan(&sourceEng); err != nil {
			return err
		}
		if err := q.QueryRowContext(ctx, "SELECT engagement FROM threads WHERE id=?", goalThread).Scan(&goalEng); err != nil {
			return err
		}
		if sourceEng != goalEng {
			return c.ErrForbidden
		}
	}
	return nil
}
