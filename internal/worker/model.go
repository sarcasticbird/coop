// Package worker coordinates approved GitHub work with a guest-local OpenCode server.
package worker

import "time"

type Actor struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Kind  string `json:"kind"`
}
type ProjectRef struct {
	Owner           string `json:"owner"`
	Number          int    `json:"number"`
	StatusField     string `json:"status_field,omitempty"`
	ReadyValue      string `json:"ready_value,omitempty"`
	InProgressValue string `json:"in_progress_value,omitempty"`
	DoneValue       string `json:"done_value,omitempty"`
	PriorityField   string `json:"priority_field,omitempty"`
}
type BootstrapGoal struct {
	URL          string   `json:"url"`
	BodySHA      string   `json:"body_sha256"`
	Source       string   `json:"source"`
	Repositories []string `json:"repositories"`
	OwnerID      int64    `json:"owner_id"`
}
type Limits struct {
	PollInterval, TurnLimit, RetryBase, RetryCap time.Duration
	MaxAttempts, MaxReviewCycles                 int
}
type OpenCodeConfig struct {
	BaseURL           string        `json:"base_url"`
	PasswordFile      string        `json:"password_file"`
	RequestTimeout    time.Duration `json:"-"`
	ReconcileInterval time.Duration `json:"-"`
}
type Config struct {
	Team                   *TeamPolicy       `json:"team,omitempty"`
	Wake                   *WakeConfig       `json:"wake,omitempty"`
	Version                int               `json:"version"`
	Name                   string            `json:"name"`
	Role                   string            `json:"role"`
	Model                  string            `json:"model"`
	ModelVariant           string            `json:"model_variant,omitempty"`
	StateDir               string            `json:"state_dir"`
	WorkRoot               string            `json:"work_root"`
	OpenCode               OpenCodeConfig    `json:"opencode"`
	Owner                  Actor             `json:"owner"`
	Self                   Actor             `json:"self"`
	Implementer            Actor             `json:"implementer"`
	Reviewer               Actor             `json:"reviewer"`
	CodeOwners             []string          `json:"code_owners"`
	ProjectOwners          []string          `json:"project_owners,omitempty"`
	DiscussionRepositories []string          `json:"discussion_repositories"`
	DiscussionURLs         []string          `json:"discussion_urls,omitempty"`
	Repositories           []string          `json:"repositories,omitempty"`
	Projects               []ProjectRef      `json:"projects,omitempty"`
	BootstrapGoals         []BootstrapGoal   `json:"bootstrap_goals,omitempty"`
	Limits                 Limits            `json:"limits"`
	CheckoutModes          map[string]string `json:"checkout_modes,omitempty"`
}
type Decision struct {
	ID, Command         string
	Author              Actor
	CreatedAt, EditedAt time.Time
}
type Goal struct {
	URL, Body, BodySHA      string
	Repositories            []string
	CreatedAt, LastEditedAt time.Time
	Decisions               []Decision
	Readable                bool
}
type Claim struct {
	Binding                *JobBinding `json:",omitempty"`
	ID, Marker, InstanceID string
	Author                 Actor
}
type Task struct {
	PRHistoryComplete                                                                     bool            `json:",omitempty"`
	AssigneesComplete                                                                     bool            `json:",omitempty"`
	Assignees                                                                             []Actor         `json:",omitempty"`
	TrustedMetadataPresent                                                                bool            `json:",omitempty"`
	QA                                                                                    QAPolicy        `json:",omitzero"`
	ClaimConflict                                                                         bool            `json:",omitempty"`
	Assignment                                                                            *TaskAssignment `json:",omitempty"`
	MetadataUpdatedAt                                                                     time.Time
	URL, Repo, GoalURL, Title, Body, BodySHA, MetadataScopeSHA, Revision                  string
	ProjectID, ProjectOwner, ItemID, StatusFieldID, InProgressID, DoneID, CurrentStatusID string
	Ordinal, Priority                                                                     int
	MetadataAuthor                                                                        Actor
	Ready, Completed, Closed                                                              bool
	DependsOn                                                                             []string
	Claim                                                                                 *Claim
}
type Review struct {
	ID, HeadSHA, State string
	Body               string `json:",omitempty"`
	Author             Actor
	SubmittedAt        time.Time
	LastEditedAt       time.Time `json:",omitzero"`
}
type PR struct {
	QAArtifact                                                   *QAArtifact `json:",omitempty"`
	QAArtifactError                                              string      `json:",omitempty"`
	BaseScopeRevision                                            string      `json:",omitempty"`
	AssignmentSHA                                                string      `json:",omitempty"`
	Contributors                                                 []Actor     `json:",omitempty"`
	ContributorsComplete, ReviewsComplete                        bool        `json:",omitempty"`
	ScopeUpdatedAt                                               time.Time
	ScopeRevision                                                string
	URL, TaskURL, GoalURL, HeadSHA, Branch, Revision, BaseBranch string
	Author                                                       Actor
	Reviews                                                      []Review
	ChecksState, MergeState                                      string
	Merged, Closed                                               bool
}
type Event struct {
	ID, Revision, URL, Kind, Body string
	Author                        Actor
	OccurredAt                    time.Time
}
type Snapshot struct {
	WakeSources []ConversationSource `json:"-"`
	Goals       []Goal
	Tasks       []Task
	PRs         []PR
	Events      []Event
	Cursor      map[string]string
	Complete    bool
}
type Job struct {
	SubmittedModel                                                               *ModelIdentity   `json:",omitempty"`
	ExecutedModel                                                                *ModelIdentity   `json:",omitempty"`
	FirstEligibleAt                                                              time.Time        `json:",omitzero"`
	SchedulePriority                                                             int              `json:",omitempty"`
	ScheduleOrdinal                                                              int              `json:",omitempty"`
	QAArtifact                                                                   *QAArtifact      `json:",omitempty"`
	PRScopeRevision                                                              string           `json:",omitempty"`
	Binding                                                                      *JobBinding      `json:",omitempty"`
	ReconcileFailures                                                            int              `json:",omitempty"`
	ReconcileError                                                               string           `json:",omitempty"`
	PromptReportAttempts                                                         int              `json:",omitempty"`
	PromptFailure                                                                *PromptSizeError `json:",omitempty"`
	BlockReported                                                                bool
	TerminalAt                                                                   time.Time
	ScopeSHA, WaitReason                                                         string
	ID, Kind, GoalURL, TaskURL, PRURL, HeadSHA, InputRevision                    string
	Checkout, Branch, SessionID, MessageID, SubmissionState, Phase, ProgressHash string
	Attempts, ReviewCycles                                                       int
	NextAttemptAt, Deadline, SubmittedAt                                         time.Time
}
type JobBinding struct {
	Version       int
	AssignmentSHA string
	ExecutorID    int64
	Role          string
}
type SessionBinding struct {
	Binding                                                   *JobBinding `json:",omitempty"`
	TaskURL, GoalURL, Role, Kind, Checkout, Branch, SessionID string
}
type State struct {
	Schedule       *TeamScheduleState `json:",omitempty"`
	Attempts       map[string]int
	Version        int
	InstanceID     string
	Sessions       map[string]SessionBinding
	Jobs           map[string]Job
	Seen           map[string]string
	Cursor         map[string]string
	SnapshotCache  Snapshot
	ReviewCycles   map[string]int
	LastProposalAt time.Time
}
type Approval struct{ State, BodySHA, Reason string }
type Artifacts struct {
	Task           *Task
	PlanResponded  bool
	Claim          *Claim
	PR             *PR
	IssueCompleted bool
	ProgressHash   string
}
type Turn struct {
	SubmittedModel                             *ModelIdentity `json:",omitempty"`
	ExecutedModel                              *ModelIdentity `json:",omitempty"`
	SessionID, MessageID, State, ErrorCategory string
	Accepted, Terminal                         bool
}
type ServerHealth struct {
	Healthy bool
	Version string
}

func observedModel(j Job, turn Turn) Job {
	if turn.SubmittedModel != nil {
		j.SubmittedModel = turn.SubmittedModel
	}
	if turn.ExecutedModel != nil {
		j.ExecutedModel = turn.ExecutedModel
	}
	return j
}
