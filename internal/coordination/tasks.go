package coordination

import (
	"net/url"
	"regexp"
	"strings"
)

var taskUID = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var taskRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// KataTaskReference preserves the immutable project/issue IDs and explicit code
// repository. It is an identity, not an HTTP endpoint or a source of authority.
func KataTaskReference(project, issue, repository string) (string, error) {
	if !taskUID.MatchString(project) || !taskUID.MatchString(issue) || !taskRepository.MatchString(repository) {
		return "", ErrInvalid
	}
	for _, part := range strings.Split(repository, "/") {
		if part == "." || part == ".." {
			return "", ErrInvalid
		}
	}
	return "kata://" + strings.ToLower(project) + "/" + issue + "/" + repository, nil
}
func ParseKataTaskReference(ref string) (project, issue, repository string, err error) {
	u, e := url.Parse(ref)
	if e != nil || u.Scheme != "kata" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", "", "", ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 3 {
		return "", "", "", ErrInvalid
	}
	project, issue, repository = strings.ToUpper(u.Host), parts[0], parts[1]+"/"+parts[2]
	canonical, e := KataTaskReference(project, issue, repository)
	if e != nil || canonical != ref {
		return "", "", "", ErrInvalid
	}
	return project, issue, repository, nil
}

// NativeTaskBinding is shared with the engineering engine. Keep its field order
// and JSON tags stable: existing v3 assignment hashes must remain unchanged.
type NativeTaskBinding struct {
	Version          int      `json:"version"`
	Goal             GoalRef  `json:"goal"`
	TaskURL          string   `json:"task_url"`
	TaskScopeSHA     string   `json:"task_scope_sha256"`
	PolicySHA        string   `json:"policy_sha256"`
	ApprovalSequence int64    `json:"approval_sequence"`
	BaseBranch       string   `json:"base_branch"`
	AuthorID         int64    `json:"author_id"`
	PeerID           int64    `json:"peer_id"`
	QAID             int64    `json:"qa_id,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
}
type TaskCapabilities struct {
	Provider    string `json:"provider"`
	ProjectUID  string `json:"project_uid"`
	ProjectName string `json:"project_name"`
	PolicySHA   string `json:"policy_sha256"`
}
type ProviderTask struct {
	Reference    string             `json:"reference"`
	ProjectUID   string             `json:"project_uid"`
	IssueUID     string             `json:"issue_uid"`
	Repository   string             `json:"repository"`
	Title        string             `json:"title"`
	Body         string             `json:"body"`
	State        string             `json:"state"`
	ClosedReason string             `json:"closed_reason,omitempty"`
	Owner        string             `json:"owner,omitempty"`
	Revision     int64              `json:"revision"`
	Goal         GoalRef            `json:"goal"`
	Binding      *NativeTaskBinding `json:"binding,omitempty"`
	BindingActor Principal          `json:"binding_actor"`
}
type TaskReadInput struct {
	Reference string `json:"reference"`
}
type TaskListInput struct {
	Goal GoalRef `json:"goal"`
}
type ProviderTaskPage struct {
	Tasks []ProviderTask `json:"tasks"`
}
type TaskCreateInput struct {
	ExpectedApprovalSequence int64    `json:"expected_approval_sequence"`
	Goal                     GoalRef  `json:"goal"`
	Repository               string   `json:"repository"`
	BaseBranch               string   `json:"base_branch"`
	Title                    string   `json:"title"`
	Body                     string   `json:"body"`
	DependsOn                []string `json:"depends_on,omitempty"`
	RequestID                string   `json:"request_id"`
}
type TaskBindInput struct {
	Binding               NativeTaskBinding `json:"binding"`
	ExpectedRevision      int64             `json:"expected_revision"`
	ExpectedAssignmentSHA string            `json:"expected_assignment_sha,omitempty"`
	RequestID             string            `json:"request_id"`
}
type TaskCompleteInput struct {
	Reference      string  `json:"reference"`
	Goal           GoalRef `json:"goal"`
	AssignmentSHA  string  `json:"assignment_sha256"`
	PullRequestURL string  `json:"pull_request_url"`
	RequestID      string  `json:"request_id"`
}
