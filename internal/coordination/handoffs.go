package coordination

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

const DeliveryPrincipalID = "rook"
const DeliveryGitHubBotID int64 = 345406391

const RetainedDeliveryOverride = "stop-before-merge:exact-retained-delivery"

// RetainedDeliveryValidation rechecks an existing grant without creating authority.
// Both retained-deliver (Rook) and retained-gates (Hawk/Wren) are supported.
type RetainedDeliveryValidation struct {
	Goal      GoalRef `json:"goal"`
	HandoffID string  `json:"handoff_id"`
}

// RetainedDeliveryAuthority is a comparable value; it never replaces source metadata.
type RetainedDeliveryAuthority struct {
	SourcePolicySHA           string `json:"source_policy_sha256"`
	ExecutionPolicySHA        string `json:"execution_policy_sha256"`
	SourceTreeSHA             string `json:"source_tree_sha"`
	PayloadSHA                string `json:"payload_sha256"`
	SourcePeerReviewID        string `json:"source_peer_review_id"`
	SourcePeerBodySHA         string `json:"source_peer_body_sha256"`
	SourceQAReviewID          string `json:"source_qa_review_id"`
	SourceQABodySHA           string `json:"source_qa_body_sha256"`
	SourceReportID            string `json:"source_report_id"`
	SourceReportBodySHA       string `json:"source_report_body_sha256"`
	SourceEnvironment         string `json:"source_environment"`
	SourceEnvironmentRevision string `json:"source_environment_revision"`
	DeliveryAcceptanceSHA     string `json:"delivery_acceptance_sha256"`
	Override                  string `json:"override"`
	EngineeringReportID       string `json:"engineering_report_id,omitempty"`
	EngineeringReportBodySHA  string `json:"engineering_report_body_sha256,omitempty"`
	QAReportID                string `json:"qa_report_id,omitempty"`
	QAReportBodySHA           string `json:"qa_report_body_sha256,omitempty"`
}

// RetainedGateReport binds the assigned gate authority and immutable source
// assignment, as well as both policies. Binding is the exact retained-gates input.
type RetainedGateReport struct {
	GateHandoffID string       `json:"gate_handoff_id"`
	AssignmentSHA string       `json:"assignment_sha256"`
	Binding       HandoffInput `json:"binding"`
	Result        string       `json:"result"`
}

// HandoffInput grants a bounded follow-up, never a replacement task binding.
type HandoffInput struct {
	Kind                string                    `json:"kind"`
	Goal                GoalRef                   `json:"goal"`
	TaskURL             string                    `json:"task_url"`
	PRURL               string                    `json:"pr_url"`
	AssignmentSHA       string                    `json:"assignment_sha256"`
	HeadSHA             string                    `json:"head_sha"`
	ArtifactSHA         string                    `json:"artifact_sha256"`
	ReviewID            string                    `json:"review_id,omitempty"`
	Environment         string                    `json:"environment,omitempty"`
	EnvironmentRevision string                    `json:"environment_revision,omitempty"`
	EvidenceMessageID   string                    `json:"evidence_message_id"`
	Tag                 string                    `json:"tag,omitempty"`
	Workflow            string                    `json:"workflow,omitempty"`
	RequestID           string                    `json:"request_id"`
	ApprovalSequence    int64                     `json:"approval_sequence,omitempty"`
	TaskScopeSHA        string                    `json:"task_scope_sha256,omitempty"`
	BaseBranch          string                    `json:"base_branch,omitempty"`
	BaseSHA             string                    `json:"base_sha,omitempty"`
	PRScopeRevision     string                    `json:"pr_scope_revision,omitempty"`
	AcceptanceSHA       string                    `json:"acceptance_sha256,omitempty"`
	PeerReviewID        string                    `json:"peer_review_id,omitempty"`
	QAReviewID          string                    `json:"qa_review_id,omitempty"`
	QAEvidenceMessageID string                    `json:"qa_evidence_message_id,omitempty"`
	QAEvidenceBodySHA   string                    `json:"qa_evidence_body_sha256,omitempty"`
	Revoked             bool                      `json:"revoked,omitempty"`
	Retained            RetainedDeliveryAuthority `json:"retained,omitempty"`
	TagOnly             TagOnlyAuthority          `json:"tag_only,omitempty"`
}

// Preserve canonical inputs of persisted requests predating these supplements.
// encoding/json does not omit a zero-valued struct with omitempty.
func (in HandoffInput) MarshalJSON() ([]byte, error) {
	type wire HandoffInput
	out := struct {
		*wire
		Retained *RetainedDeliveryAuthority `json:"retained,omitempty"`
		TagOnly  *TagOnlyAuthority          `json:"tag_only,omitempty"`
	}{wire: (*wire)(&in)}
	if in.Retained != (RetainedDeliveryAuthority{}) {
		out.Retained = &in.Retained
	}
	if in.TagOnly != (TagOnlyAuthority{}) {
		out.TagOnly = &in.TagOnly
	}
	return json.Marshal(out)
}

type Handoff struct {
	Sequence  int64        `json:"sequence,omitempty"`
	ID        string       `json:"id"`
	Input     HandoffInput `json:"input"`
	ActorID   string       `json:"actor_id"`
	ActorKind string       `json:"actor_kind,omitempty"`
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
	if in.Kind == "tag-only" {
		return validateTagOnlyHandoff(in)
	}
	if in.TagOnly != (TagOnlyAuthority{}) {
		return ErrInvalid
	}
	retained := in.Kind == "retained-gates" || in.Kind == "retained-deliver"
	if !retained && in.Retained != (RetainedDeliveryAuthority{}) {
		return ErrInvalid
	}
	if retained {
		r := in.Retained
		if strings.TrimSpace(in.Environment) == "" || strings.TrimSpace(in.EnvironmentRevision) == "" || in.QAReviewID == "" || in.QAEvidenceMessageID == "" || in.QAEvidenceBodySHA == "" {
			return ErrInvalid
		}
		if r.Override != RetainedDeliveryOverride || !handoffHead.MatchString(r.SourceTreeSHA) || !handoffID.MatchString(r.SourcePeerReviewID) || !handoffID.MatchString(r.SourceQAReviewID) || !handoffID.MatchString(r.SourceReportID) || strings.TrimSpace(r.SourceEnvironment) == "" || strings.TrimSpace(r.SourceEnvironmentRevision) == "" || len(r.SourceEnvironment) > MaxText || len(r.SourceEnvironmentRevision) > 256 || strings.ContainsAny(r.SourceEnvironmentRevision, "\x00\r\n") {
			return ErrInvalid
		}
		for _, sha := range []string{r.SourcePolicySHA, r.ExecutionPolicySHA, r.PayloadSHA, r.SourcePeerBodySHA, r.SourceQABodySHA, r.SourceReportBodySHA, r.DeliveryAcceptanceSHA} {
			if !handoffSHA.MatchString(sha) {
				return ErrInvalid
			}
		}
		if in.Kind == "retained-deliver" {
			if !handoffID.MatchString(r.EngineeringReportID) || !handoffID.MatchString(r.QAReportID) || !handoffSHA.MatchString(r.EngineeringReportBodySHA) || !handoffSHA.MatchString(r.QAReportBodySHA) {
				return ErrInvalid
			}
		} else if r.EngineeringReportID != "" || r.EngineeringReportBodySHA != "" || r.QAReportID != "" || r.QAReportBodySHA != "" {
			return ErrInvalid
		}
	}
	if in.Kind != "deliver" && !retained && (in.ApprovalSequence != 0 || in.TaskScopeSHA != "" || in.BaseBranch != "" || in.BaseSHA != "" || in.PRScopeRevision != "" || in.AcceptanceSHA != "" || in.PeerReviewID != "" || in.QAReviewID != "" || in.QAEvidenceMessageID != "" || in.QAEvidenceBodySHA != "" || in.Revoked) {
		return ErrInvalid
	}
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
	case "deliver", "retained-gates", "retained-deliver":
		if in.ReviewID != "" || in.Tag != "" || in.Workflow != "" || in.ApprovalSequence < 1 || !handoffSHA.MatchString(in.TaskScopeSHA) || !handoffHead.MatchString(in.BaseSHA) || !handoffSHA.MatchString(in.PRScopeRevision) || !handoffSHA.MatchString(in.AcceptanceSHA) || strings.TrimSpace(in.BaseBranch) == "" || len(in.BaseBranch) > 256 || strings.ContainsAny(in.BaseBranch, "\x00\r\n") || !handoffID.MatchString(in.PeerReviewID) {
			return ErrInvalid
		}
		if in.Environment != "" || in.EnvironmentRevision != "" {
			if strings.TrimSpace(in.Environment) == "" || len(in.Environment) > MaxText || strings.TrimSpace(in.EnvironmentRevision) == "" || len(in.EnvironmentRevision) > 256 || strings.ContainsAny(in.EnvironmentRevision, "\x00\r\n") {
				return ErrInvalid
			}
		}
		if in.QAReviewID != "" || in.QAEvidenceMessageID != "" || in.QAEvidenceBodySHA != "" {
			if !handoffID.MatchString(in.QAReviewID) || !handoffID.MatchString(in.QAEvidenceMessageID) || !handoffSHA.MatchString(in.QAEvidenceBodySHA) {
				return ErrInvalid
			}
		}
	case "first-postmerge-qa":
		if in.ReviewID != "" || in.Tag != "" || in.Workflow != "" || strings.TrimSpace(in.Environment) == "" || strings.TrimSpace(in.EnvironmentRevision) == "" || len(in.EnvironmentRevision) > 256 || strings.ContainsAny(in.EnvironmentRevision, "\x00\r\n") {
			return ErrInvalid
		}
	case "reassess-qa":
		if !handoffID.MatchString(in.ReviewID) || in.Tag != "" || in.Workflow != "" {
			return ErrInvalid
		}
		if in.Environment != "" || in.EnvironmentRevision != "" {
			if strings.TrimSpace(in.Environment) == "" || len(in.Environment) > MaxText || strings.TrimSpace(in.EnvironmentRevision) == "" || len(in.EnvironmentRevision) > 256 || strings.ContainsAny(in.EnvironmentRevision, "\x00\r\n") {
				return ErrInvalid
			}
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
