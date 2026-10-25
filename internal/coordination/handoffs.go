package coordination

import (
	"regexp"
	"time"
)

// HandoffInput grants a bounded follow-up, never a replacement task binding.
type HandoffInput struct {
	Kind              string  `json:"kind"`
	Goal              GoalRef `json:"goal"`
	TaskURL           string  `json:"task_url"`
	PRURL             string  `json:"pr_url"`
	AssignmentSHA     string  `json:"assignment_sha256"`
	HeadSHA           string  `json:"head_sha"`
	ArtifactSHA       string  `json:"artifact_sha256"`
	ReviewID          string  `json:"review_id,omitempty"`
	EvidenceMessageID string  `json:"evidence_message_id"`
	Tag               string  `json:"tag,omitempty"`
	Workflow          string  `json:"workflow,omitempty"`
	RequestID         string  `json:"request_id"`
}
type Handoff struct {
	ID        string       `json:"id"`
	Input     HandoffInput `json:"input"`
	ActorID   string       `json:"actor_id"`
	CreatedAt time.Time    `json:"created_at"`
}
type HandoffQuery struct {
	Goal  GoalRef `json:"goal"`
	After string  `json:"after,omitempty"`
}
type HandoffPage struct {
	Handoffs []Handoff `json:"handoffs"`
	Next     string    `json:"next,omitempty"`
}

var handoffSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var handoffHead = regexp.MustCompile(`^[0-9a-f]{40}$`)
var releaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var releaseWorkflow = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,100}\.ya?ml$`)
var handoffID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func ValidateHandoff(in HandoffInput) error {
	if len(in.Tag) > 128 {
		return ErrInvalid
	}
	if !handoffID.MatchString(in.RequestID) || !handoffID.MatchString(in.EvidenceMessageID) {
		return ErrInvalid
	}
	if !handoffSHA.MatchString(in.AssignmentSHA) || !handoffSHA.MatchString(in.ArtifactSHA) || !handoffHead.MatchString(in.HeadSHA) || in.TaskURL == "" || in.PRURL == "" || in.EvidenceMessageID == "" || in.RequestID == "" || len(in.RequestID) > 128 {
		return ErrInvalid
	}
	switch in.Kind {
	case "reassess-qa":
		if !handoffID.MatchString(in.ReviewID) || in.Tag != "" || in.Workflow != "" {
			return ErrInvalid
		}
	case "release":
		if in.ReviewID != "" || !releaseTag.MatchString(in.Tag) || !releaseWorkflow.MatchString(in.Workflow) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
