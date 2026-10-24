package taskstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Adopt initializes creation scope and binding in one provider revision CAS.
// Only the master's previously persisted validated intent may call this method.
func (k *kata) Adopt(ctx context.Context, in c.TaskAdoptInput, binding c.NativeTaskBinding, actor c.Principal) (c.ProviderTask, error) {
	project, uid, repo, err := c.ParseKataTaskReference(in.Reference)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if project != k.cfg.ProjectUID || repo != in.Repository || k.cfg.InstanceUID == "" {
		return c.ProviderTask{}, c.ErrForbidden
	}
	if !validRequest(in.RequestID) || actor.ID == "" || in.ExpectedRevision < 1 {
		return c.ProviderTask{}, c.ErrInvalid
	}
	if binding.TaskURL != in.Reference || binding.Goal != in.Goal || binding.BaseBranch != in.BaseBranch || binding.ApprovalSequence != in.ExpectedApprovalSequence || binding.PolicySHA != k.cfg.PolicySHA || !reflect.DeepEqual(binding.DependsOn, in.DependsOn) {
		return c.ProviderTask{}, c.ErrConflict
	}
	issue, err := k.InspectIssue(ctx, uid)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if issue.Status != "open" || binding.TaskScopeSHA != hash(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{issue.Title, issue.Body}) {
		return c.ProviderTask{}, c.ErrConflict
	}
	cr := creation{Goal: in.Goal, Repository: in.Repository, Input: c.TaskCreateInput{Goal: in.Goal, Repository: in.Repository, BaseBranch: in.BaseBranch, ExpectedApprovalSequence: in.ExpectedApprovalSequence, Title: issue.Title, Body: issue.Body, DependsOn: in.DependsOn, RequestID: in.RequestID}}
	want := native{Binding: binding, Actor: actor}
	if len(issue.Metadata["coop.creation"]) > 0 || len(issue.Metadata["coop.native"]) > 0 {
		var previous creation
		var n native
		if metadata(issue.Metadata["coop.creation"], &previous) != nil || metadata(issue.Metadata["coop.native"], &n) != nil || !reflect.DeepEqual(previous, cr) || !reflect.DeepEqual(n, want) {
			return c.ProviderTask{}, c.ErrConflict
		}
		return k.Read(ctx, in.Reference)
	}
	for key := range issue.Metadata {
		if strings.HasPrefix(key, "coop.") {
			return c.ProviderTask{}, c.ErrConflict
		}
	}
	if issue.Revision != in.ExpectedRevision {
		return c.ProviderTask{}, c.ErrConflict
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return c.ProviderTask{}, err
	}
	input := map[string]any{"actor": actor.ID, "patch": map[string]any{"coop.creation": cr, "coop.native": want}}
	var out struct {
		Issue Issue `json:"issue"`
	}
	err = nativeCallIfMatch(ctx, cl, token, "POST", fmt.Sprintf("/api/v1/projects/%d/issues/%s/metadata", pid, uid), "", input, &out, in.ExpectedRevision)
	// Reconcile an uncertain response only against the exact original payload.
	result, readErr := k.Read(ctx, in.Reference)
	if readErr == nil && result.Binding != nil && reflect.DeepEqual(*result.Binding, binding) && result.BindingActor.ID == actor.ID && result.Title == issue.Title && result.Body == issue.Body {
		observed, e := k.InspectIssue(ctx, uid)
		if e != nil {
			return c.ProviderTask{}, e
		}
		var actual creation
		var n native
		if metadata(observed.Metadata["coop.creation"], &actual) == nil && metadata(observed.Metadata["coop.native"], &n) == nil && reflect.DeepEqual(actual, cr) && reflect.DeepEqual(n, want) {
			return result, nil
		}
	}
	if err != nil {
		return c.ProviderTask{}, errors.Join(err, readErr)
	}
	if readErr != nil {
		return c.ProviderTask{}, readErr
	}
	return c.ProviderTask{}, c.ErrConflict
}

var _ AdoptionBackend = (*kata)(nil)
