package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Project ordering and readiness stay on GitHub. A planner must select one of
// the configured Projects; a single configured Project is unambiguous.
func nativePlaceTask(ctx context.Context, cfg Config, g *GitHubClient, in NativeAction, b NativeTaskBinding, r *nativeActionRecord, effect func(string, func() error) error) error {
	if len(cfg.Projects) == 0 && len(cfg.ProjectOwners) == 0 {
		if in.ProjectOwner != "" || in.ProjectNumber != 0 {
			return errors.New("native project is outside configured scope")
		}
		return nil
	}
	refs, err := g.projectRefs(ctx)
	if err != nil {
		return err
	}
	var selected ProjectRef
	matches := 0
	for _, p := range refs {
		if in.ProjectOwner == "" && len(refs) == 1 || p.Owner == in.ProjectOwner && p.Number == in.ProjectNumber {
			selected = p
			matches++
		}
	}
	if matches != 1 {
		return errors.New("select exactly one configured Project with project_owner and project_number")
	}
	if err = g.verifyOwner(ctx, selected.Owner); err != nil {
		return err
	}
	data, err := g.graphql(ctx, selected.Owner, `query WorkerProject($owner:String!,$number:Int!){`+projectFragment(`projectV2(number:$number){id}`)+`}`, map[string]any{"owner": selected.Owner, "number": selected.Number})
	if err != nil {
		return err
	}
	raw, err := at(data, "repositoryOwner", "projectV2")
	if err != nil {
		return err
	}
	var project struct{ ID string }
	if json.Unmarshal(raw, &project) != nil || project.ID == "" {
		return ghError("invalid_native_project")
	}
	fields, err := g.connection(ctx, selected.Owner, `query WorkerProjectFields($id:ID!,$after:String){node(id:$id){... on ProjectV2{fields(first:100,after:$after){nodes{... on ProjectV2FieldCommon{id name} ... on ProjectV2SingleSelectField{options{id name}}} pageInfo{hasNextPage endCursor}}}}}`, map[string]any{"id": project.ID}, []string{"node", "fields"})
	if err != nil {
		return err
	}
	fieldID, readyID := "", ""
	for _, raw := range fields {
		var f projectField
		if json.Unmarshal(raw, &f) != nil {
			return ghError("invalid_native_project_field")
		}
		if f.Name == selected.StatusField {
			if fieldID != "" {
				return ghError("ambiguous_native_project_field")
			}
			fieldID = f.ID
			for _, option := range f.Options {
				if option.Name == selected.ReadyValue {
					readyID = option.ID
				}
			}
		}
	}
	if fieldID == "" || readyID == "" {
		return ghError("native_project_ready_missing")
	}
	repo, n, err := parseRef(b.TaskURL)
	if err != nil {
		return err
	}
	raw, _, err = g.api(ctx, strings.Split(repo, "/")[0], "GET", "repos/"+repo+"/issues/"+stringID(n), nil)
	if err != nil {
		return err
	}
	var issue struct {
		URL         string `json:"html_url"`
		NodeID      string `json:"node_id"`
		Title, Body string
	}
	if json.Unmarshal(raw, &issue) != nil || issue.URL != b.TaskURL || issue.NodeID == "" || TaskScopeHash(issue.Title, issue.Body) != b.TaskScopeSHA {
		return ghError("native_project_task_changed")
	}
	if r.ProjectItemID == "" {
		if err = effect("project-add", func() error {
			data, e := g.graphql(ctx, selected.Owner, `mutation WorkerNativeProjectAdd($project:ID!,$content:ID!){addProjectV2ItemById(input:{projectId:$project,contentId:$content}){item{id}}}`, map[string]any{"project": project.ID, "content": issue.NodeID})
			if e != nil {
				return e
			}
			raw, e := at(data, "addProjectV2ItemById", "item")
			if e != nil {
				return e
			}
			var item struct{ ID string }
			if json.Unmarshal(raw, &item) != nil || item.ID == "" {
				return ghError("native_project_add_failed")
			}
			r.ProjectItemID = item.ID
			return nil
		}); err != nil {
			return err
		}
	}
	if r.Step != "project-ready" {
		if err = effect("project-ready", func() error {
			_, e := g.graphql(ctx, selected.Owner, `mutation WorkerNativeProjectReady($project:ID!,$item:ID!,$field:ID!,$option:String!){updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}){projectV2Item{id}}}`, map[string]any{"project": project.ID, "item": r.ProjectItemID, "field": fieldID, "option": readyID})
			return e
		}); err != nil {
			return err
		}
	}
	tasks := map[string]Task{b.TaskURL: {URL: b.TaskURL}}
	if err = g.readProject(ctx, selected, tasks, &Snapshot{}); err != nil {
		return err
	}
	if !tasks[b.TaskURL].Ready || tasks[b.TaskURL].BodySHA != b.TaskScopeSHA {
		return ghError("native_project_ready_unconfirmed")
	}
	return nil
}
