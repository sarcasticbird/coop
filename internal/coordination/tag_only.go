package coordination

import "strings"

const TagOnlyPayloadV1 = "coop-go-version-v1"
const TagOnlyGHSweepPayloadV1 = "gh-sweep-version-v1"

func SupportedTagOnlyPayloadSchema(schema string) bool {
	return schema == TagOnlyPayloadV1 || schema == TagOnlyGHSweepPayloadV1
}

// TagOnlyAuthority supplements an immutable source assignment. All fields are
// values so HandoffInput remains comparable for exact grants and withdrawals.
type TagOnlyAuthority struct {
	SourceCompletionRequestID  string  `json:"source_completion_request_id"`
	VersionCompletionRequestID string  `json:"version_completion_request_id,omitempty"`
	SourceDeliveryHandoffID    string  `json:"source_delivery_handoff_id"`
	SourceDeliveryInputSHA     string  `json:"source_delivery_input_sha256"`
	PayloadSchema              string  `json:"payload_schema,omitempty"`
	PayloadSHA                 string  `json:"payload_sha256,omitempty"`
	SourceBundleSHA            string  `json:"source_bundle_sha256,omitempty"`
	VersionBundleSHA           string  `json:"version_bundle_sha256,omitempty"`
	TagAcceptanceSHA           string  `json:"tag_acceptance_sha256"`
	TagEnvironment             string  `json:"tag_environment"`
	TagEnvironmentRevision     string  `json:"tag_environment_revision"`
	ExecutionPolicySHA         string  `json:"execution_policy_sha256"`
	SourcePolicySHA            string  `json:"source_policy_sha256"`
	SourceCommitSHA            string  `json:"source_commit_sha"`
	SourceTreeSHA              string  `json:"source_tree_sha"`
	IntegratedCommitSHA        string  `json:"integrated_commit_sha"`
	IntegratedTreeSHA          string  `json:"integrated_tree_sha"`
	VersionPRURL               string  `json:"version_pr_url,omitempty"`
	VersionHeadSHA             string  `json:"version_head_sha,omitempty"`
	VersionCommitSHA           string  `json:"version_commit_sha,omitempty"`
	VersionTreeSHA             string  `json:"version_tree_sha,omitempty"`
	VersionBaseSHA             string  `json:"version_base_sha,omitempty"`
	VersionGoal                GoalRef `json:"version_goal,omitempty"`
	VersionTaskURL             string  `json:"version_task_url,omitempty"`
	VersionDeliveryHandoffID   string  `json:"version_delivery_handoff_id,omitempty"`
	VersionDeliveryInputSHA    string  `json:"version_delivery_input_sha256,omitempty"`
	OwnerBodySHA               string  `json:"owner_body_sha256"`
	PeerBodySHA                string  `json:"peer_body_sha256"`
	QABodySHA                  string  `json:"qa_body_sha256"`
	EngineeringReportID        string  `json:"engineering_report_id"`
	EngineeringReportBodySHA   string  `json:"engineering_report_body_sha256"`
	QAReportID                 string  `json:"qa_report_id"`
	QAReportBodySHA            string  `json:"qa_report_body_sha256"`
}

// TagOnlySourceMatches preserves the complete original merge decision. The
// supplemental tag owner evidence and renewed reports never rewrite it.
func TagOnlySourceMatches(in, source HandoffInput) bool {
	if source.Kind != "deliver" && source.Kind != "retained-deliver" || source.Revoked {
		return false
	}
	x := source
	x.Kind, x.Tag, x.EvidenceMessageID, x.RequestID, x.Revoked = in.Kind, in.Tag, in.EvidenceMessageID, in.RequestID, in.Revoked
	x.Retained, x.TagOnly = RetainedDeliveryAuthority{}, in.TagOnly
	return x == in
}

func TagOnlyTargetSHA(in HandoffInput) string {
	if in.TagOnly.VersionPRURL != "" {
		return in.TagOnly.VersionCommitSHA
	}
	return in.TagOnly.IntegratedCommitSHA
}

// TagOnlyReportBinding removes only the report references to avoid a digest
// cycle. Every other authority field, including owner evidence, stays exact.
func TagOnlyReportBinding(in HandoffInput) HandoffInput {
	in.RequestID, in.Revoked = "tag-only-report", false
	in.TagOnly.EngineeringReportID, in.TagOnly.EngineeringReportBodySHA = "", ""
	in.TagOnly.QAReportID, in.TagOnly.QAReportBodySHA = "", ""
	return in
}

type TagOnlyReport struct {
	Binding HandoffInput `json:"binding"`
	Result  string       `json:"result"`
}

type TagOnlyValidation struct {
	Goal      GoalRef `json:"goal"`
	HandoffID string  `json:"handoff_id"`
}

func validateTagOnlyHandoff(in HandoffInput) error {
	r := in.TagOnly
	if !handoffID.MatchString(r.SourceCompletionRequestID) {
		return ErrInvalid
	}
	if !handoffID.MatchString(r.SourceDeliveryHandoffID) || !handoffSHA.MatchString(r.SourceDeliveryInputSHA) || !handoffSHA.MatchString(r.TagAcceptanceSHA) || strings.TrimSpace(r.TagEnvironment) == "" || len(r.TagEnvironment) > MaxText || strings.TrimSpace(r.TagEnvironmentRevision) == "" || len(r.TagEnvironmentRevision) > 256 || strings.ContainsAny(r.TagEnvironmentRevision, "\x00\r\n") {
		return ErrInvalid
	}
	if in.Workflow != "" || !releaseTag.MatchString(in.Tag) || in.Retained != (RetainedDeliveryAuthority{}) {
		return ErrInvalid
	}
	base := in
	base.Kind, base.Tag, base.TagOnly = "deliver", "", TagOnlyAuthority{}
	if err := ValidateHandoff(base); err != nil {
		return err
	}
	for _, sha := range []string{r.ExecutionPolicySHA, r.SourcePolicySHA, r.OwnerBodySHA, r.PeerBodySHA, r.QABodySHA, r.EngineeringReportBodySHA, r.QAReportBodySHA} {
		if !handoffSHA.MatchString(sha) {
			return ErrInvalid
		}
	}
	for _, sha := range []string{r.SourceCommitSHA, r.SourceTreeSHA, r.IntegratedCommitSHA, r.IntegratedTreeSHA} {
		if !handoffHead.MatchString(sha) {
			return ErrInvalid
		}
	}
	if !handoffID.MatchString(r.EngineeringReportID) || !handoffID.MatchString(r.QAReportID) || in.QAReviewID == "" || in.QAEvidenceMessageID == "" || in.Environment == "" || in.EnvironmentRevision == "" {
		return ErrInvalid
	}
	if r.VersionPRURL != "" || r.VersionHeadSHA != "" || r.VersionCommitSHA != "" || r.VersionTreeSHA != "" || r.VersionBaseSHA != "" || r.VersionGoal != (GoalRef{}) || r.VersionTaskURL != "" || r.VersionDeliveryHandoffID != "" || r.VersionDeliveryInputSHA != "" || r.PayloadSchema != "" || r.PayloadSHA != "" || r.SourceBundleSHA != "" || r.VersionBundleSHA != "" || r.VersionCompletionRequestID != "" {
		if r.VersionPRURL == "" || !handoffHead.MatchString(r.VersionHeadSHA) || !handoffHead.MatchString(r.VersionCommitSHA) || !handoffHead.MatchString(r.VersionTreeSHA) || r.VersionGoal.Source != "master" || r.VersionGoal.MasterID != in.Goal.MasterID || !handoffSHA.MatchString(r.VersionGoal.RevisionSHA256) || r.VersionGoal.GoalID == "" || r.VersionTaskURL == "" || !handoffID.MatchString(r.VersionDeliveryHandoffID) || !handoffSHA.MatchString(r.VersionDeliveryInputSHA) || !SupportedTagOnlyPayloadSchema(r.PayloadSchema) || !handoffSHA.MatchString(r.PayloadSHA) {
			return ErrInvalid
		}
		if !handoffSHA.MatchString(r.SourceBundleSHA) || !handoffSHA.MatchString(r.VersionBundleSHA) {
			return ErrInvalid
		}
		if !handoffHead.MatchString(r.VersionBaseSHA) {
			return ErrInvalid
		}
		if !handoffID.MatchString(r.VersionCompletionRequestID) {
			return ErrInvalid
		}
	}
	return nil
}
