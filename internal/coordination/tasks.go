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
	Provider     string              `json:"provider"`
	ProjectUID   string              `json:"project_uid"`
	ProjectName  string              `json:"project_name"`
	PolicySHA    string              `json:"policy_sha256"`
	Conversation *ConversationSource `json:"conversation,omitempty"`
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
	Goal   GoalRef `json:"goal,omitempty"`
	Status string  `json:"status,omitempty"`
	After  string  `json:"after,omitempty"`
}
type ProviderTaskPage struct {
	Tasks []ProviderTask `json:"tasks"`
	Next  string         `json:"next,omitempty"`
}
type TaskCreateInput struct {
	ExpectedApprovalSequence int64    `json:"expected_approval_sequence,omitempty"`
	Goal                     GoalRef  `json:"goal,omitempty"`
	Repository               string   `json:"repository,omitempty"`
	BaseBranch               string   `json:"base_branch,omitempty"`
	Title                    string   `json:"title"`
	Body                     string   `json:"body"`
	DependsOn                []string `json:"depends_on,omitempty"`
	RequestID                string   `json:"request_id"`
	Owner                    string   `json:"owner,omitempty"`
}

// TaskUpdateInput edits Kata work, not execution grants or GitHub clearance.
// ExpectedRevision prevents overwriting work changed since the bird read it.
type TaskUpdateInput struct {
	Reference        string         `json:"reference"`
	Operation        string         `json:"operation"`
	RequestID        string         `json:"request_id"`
	ExpectedRevision int64          `json:"expected_revision,omitempty"`
	Title            *string        `json:"title,omitempty"`
	Body             *string        `json:"body,omitempty"`
	Owner            *string        `json:"owner,omitempty"`
	Message          string         `json:"message,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	ReplyToUID       string         `json:"reply_to_uid,omitempty"`
	Evidence         []TaskEvidence `json:"evidence,omitempty"`
}
type TaskEvidence struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

func (in TaskUpdateInput) Validate() error {
	if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 256 || strings.ContainsAny(in.RequestID, "\x00\r\n") || len(in.Message) > MaxText || len(in.Evidence) > 20 {
		return ErrInvalid
	}
	if in.Title != nil && (strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 1024) || in.Body != nil && len(*in.Body) > MaxText || in.Owner != nil && (len(*in.Owner) > 128 || strings.ContainsAny(*in.Owner, "\x00\r\n")) {
		return ErrInvalid
	}
	if in.Operation != "comment" && in.ExpectedRevision < 1 {
		return ErrInvalid
	}
	switch in.Operation {
	case "edit":
		if in.Title == nil && in.Body == nil && in.Owner == nil || in.Message != "" || in.Reason != "" || in.ReplyToUID != "" || len(in.Evidence) != 0 {
			return ErrInvalid
		}
	case "assign":
		if in.Owner == nil || in.Title != nil || in.Body != nil || in.Message != "" || in.Reason != "" || in.ReplyToUID != "" || len(in.Evidence) != 0 {
			return ErrInvalid
		}
	case "claim", "close", "reopen":
		if in.Title != nil || in.Body != nil || in.Owner != nil || in.ReplyToUID != "" {
			return ErrInvalid
		}
		if in.Operation != "close" && (in.Reason != "" || len(in.Evidence) != 0) || in.Operation == "claim" && in.Message != "" {
			return ErrInvalid
		}
		if in.Operation == "close" {
			switch in.Reason {
			case "done", "wontfix", "duplicate", "superseded", "audit-no-change":
			default:
				return ErrInvalid
			}
			if strings.TrimSpace(in.Message) == "" {
				return ErrInvalid
			}
		}
	case "comment":
		if strings.TrimSpace(in.Message) == "" || in.Title != nil || in.Body != nil || in.Owner != nil || in.Reason != "" || len(in.Evidence) != 0 || in.ExpectedRevision != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
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

// TaskAdoptInput admits an existing native issue under current server authority.
type TaskAdoptInput struct {
	Reference                string   `json:"reference"`
	Goal                     GoalRef  `json:"goal"`
	Repository               string   `json:"repository"`
	BaseBranch               string   `json:"base_branch"`
	ExpectedRevision         int64    `json:"expected_revision"`
	ExpectedApprovalSequence int64    `json:"expected_approval_sequence"`
	DependsOn                []string `json:"depends_on,omitempty"`
	RequestID                string   `json:"request_id"`
}
