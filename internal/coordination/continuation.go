package coordination

import (
	"regexp"
	"strings"
)

// TaskContinuationTarget captures one exact native attempt, never a moving alias.
type TaskContinuationTarget struct {
	JobID      string `json:"job_id"`
	TaskURL    string `json:"task"`
	SessionID  string `json:"session_id"`
	MessageID  string `json:"message_id"`
	InstanceID string `json:"worker_instance_id"`
	Generation int64  `json:"generation"`
}
type TaskContinuation struct {
	Target          TaskContinuationTarget `json:"target"`
	SourceMessageID string                 `json:"source_message_id"`
}
type ContinueTaskRequest struct {
	ThreadID        string                 `json:"thread_id"`
	AgentID         string                 `json:"agent_id"`
	Target          TaskContinuationTarget `json:"target"`
	SourceMessageID string                 `json:"source_message_id"`
	Body            string                 `json:"body"`
	RequestID       string                 `json:"request_id"`
}

var continuationNativeID = regexp.MustCompile(`^(ses|msg)_[A-Za-z0-9]+$`)

func ValidContinuationIntent(session, message string) bool {
	return strings.HasPrefix(session, "ses_") && strings.HasPrefix(message, "msg_") && continuationNativeID.MatchString(session) && continuationNativeID.MatchString(message)
}
func ValidateContinuationTarget(t TaskContinuationTarget) error {
	validTask := DiagnosticReference(t.TaskURL) && (strings.HasPrefix(t.TaskURL, "kata://") || strings.Contains(t.TaskURL, "/issues/"))
	if len(t.JobID) != 64 || !diagnosticSHA.MatchString(t.JobID) || !validTask || !ValidContinuationIntent(t.SessionID, t.MessageID) || !diagnosticID.MatchString(t.InstanceID) || t.Generation < 1 {
		return ErrInvalid
	}
	return nil
}
func ContinuationMessage(in ContinueTaskRequest) (SendMessage, error) {
	if ValidateContinuationTarget(in.Target) != nil || !diagnosticID.MatchString(in.SourceMessageID) || !diagnosticID.MatchString(in.AgentID) || strings.TrimSpace(in.Body) == "" || len(in.Body) > 1024 {
		return SendMessage{}, ErrInvalid
	}
	return SendMessage{ThreadID: in.ThreadID, Recipients: []string{in.AgentID}, Body: in.Body, RequestID: in.RequestID, Continuation: &TaskContinuation{Target: in.Target, SourceMessageID: in.SourceMessageID}}, nil
}
