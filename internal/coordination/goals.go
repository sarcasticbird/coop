package coordination

import "time"

type RepositoryScope struct {
	Name         string   `json:"name"`
	BaseBranches []string `json:"base_branches"`
}
type EvidenceDestination struct {
	MasterID   string `json:"master_id"`
	ThreadID   string `json:"thread_id"`
	Engagement string `json:"engagement"`
}
type QAPolicy struct {
	Required            bool                `json:"required"`
	ActorID             string              `json:"actor_id,omitempty"`
	Environment         string              `json:"environment,omitempty"`
	EnvironmentRevision string              `json:"environment_revision,omitempty"`
	EvidenceDestination EvidenceDestination `json:"evidence_destination"`
}
type GoalProposal struct {
	GoalID       string            `json:"goal_id,omitempty"`
	ThreadID     string            `json:"thread_id"`
	Purpose      string            `json:"purpose"`
	Scope        string            `json:"scope"`
	Repositories []RepositoryScope `json:"repositories"`
	Acceptance   []string          `json:"acceptance"`
	AuthorID     string            `json:"author_id"`
	PeerID       string            `json:"peer_id"`
	QA           QAPolicy          `json:"qa"`
	RequestID    string            `json:"request_id"`
}
type GoalRevision struct {
	Ref        GoalRef      `json:"ref"`
	Proposal   GoalProposal `json:"proposal"`
	ProposerID string       `json:"proposer_id"`
	CreatedAt  time.Time    `json:"created_at"`
}
type GoalDecisionInput struct {
	GoalID                   string `json:"goal_id"`
	RevisionSHA256           string `json:"revision_sha256"`
	ExpectedDecisionSequence int64  `json:"expected_decision_sequence"`
	Action                   string `json:"action"`
	RequestID                string `json:"request_id"`
}
type GoalAuthority struct {
	Actors               GoalActors   `json:"actors"`
	Revision             GoalRevision `json:"revision"`
	State                string       `json:"state"`
	ActiveRevisionSHA256 string       `json:"active_revision_sha256"`
	DecisionSequence     int64        `json:"decision_sequence"`
	ApprovalSequence     int64        `json:"approval_sequence"`
	Valid                bool         `json:"valid"`
}
type GoalActor struct {
	PrincipalID string `json:"principal_id"`
	GitHubBotID int64  `json:"github_bot_id"`
}
type GoalActors struct {
	Author GoalActor `json:"author"`
	Peer   GoalActor `json:"peer"`
	QA     GoalActor `json:"qa"`
}
type GoalPage struct {
	Goals []GoalAuthority `json:"goals"`
	Next  string          `json:"next,omitempty"`
}
