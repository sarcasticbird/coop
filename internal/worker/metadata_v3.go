package worker

import (
	"encoding/json"
	"errors"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type NativeTaskBinding = c.NativeTaskBinding

func NativeAssignmentSHA(b NativeTaskBinding) string {
	raw, _ := json.Marshal(b)
	return BodyHash(string(raw))
}
func NativeTaskMetadata(b NativeTaskBinding) string {
	raw, _ := json.Marshal(b)
	return "<!-- coop-task:v3 " + string(raw) + " -->"
}
func ParseNativeTaskMetadata(body string) (NativeTaskBinding, error) {
	var out NativeTaskBinding
	if err := nativeMetadata(body, "task", &out); err != nil {
		return out, err
	}
	if out.Version != 3 {
		return out, errors.New("unsupported native task metadata")
	}
	return out, nil
}
func nativeMetadata(body, kind string, out any) error {
	marker := "<!-- coop-" + kind + ":v3 "
	if strings.Count(body, "<!-- coop-"+kind+":") != 1 {
		return errors.New("ambiguous native metadata")
	}
	_, rest, ok := strings.Cut(body, marker)
	if !ok {
		return errors.New("native metadata missing")
	}
	raw, _, ok := strings.Cut(rest, " -->")
	if !ok || len(raw) > 32<<10 {
		return errors.New("invalid native metadata")
	}
	return strictTeamJSON([]byte(raw), out)
}

type NativeQAResult struct {
	Assessment          string    `json:"assessment,omitempty"`
	HandoffID           string    `json:"handoff_id,omitempty"`
	PRScopeRevision     string    `json:"pr_scope_revision"`
	Version             int       `json:"version"`
	Goal                c.GoalRef `json:"goal"`
	AssignmentSHA       string    `json:"assignment_sha256"`
	TaskURL             string    `json:"task_url"`
	HeadSHA             string    `json:"head_sha"`
	ArtifactSHA         string    `json:"artifact_sha256"`
	AcceptanceSHA       string    `json:"acceptance_sha256"`
	Environment         string    `json:"environment"`
	EnvironmentRevision string    `json:"environment_revision"`
	EvidenceMessageID   string    `json:"evidence_message_id"`
	EvidenceBodySHA     string    `json:"evidence_body_sha256"`
	Verdict             string    `json:"verdict"`
}

func NativeQAMetadata(in NativeQAResult) string {
	raw, _ := json.Marshal(in)
	return "<!-- coop-qa:v3 " + string(raw) + " -->"
}

type NativeReviewResult struct {
	Version         int       `json:"version"`
	Goal            c.GoalRef `json:"goal"`
	TaskURL         string    `json:"task_url"`
	AssignmentSHA   string    `json:"assignment_sha256"`
	HeadSHA         string    `json:"head_sha"`
	PRScopeRevision string    `json:"pr_scope_revision"`
	Verdict         string    `json:"verdict"`
}

func NativePeerMetadata(in NativeReviewResult) string {
	raw, _ := json.Marshal(in)
	return "<!-- coop-review:v3 " + string(raw) + " -->"
}
