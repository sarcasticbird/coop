package coordination

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// TaskCompletionRefusal describes only this request attempt's definitive
// rejection before entering the provider. It is not completion evidence and
// cannot reconcile a previous unknown attempt.
type TaskCompletionRefusal struct {
	Version      int    `json:"version"`
	MasterID     string `json:"master_id"`
	RestoreEpoch int64  `json:"restore_epoch"`
	RequestID    string `json:"request_id"`
	InputSHA256  string `json:"input_sha256"`
	Effect       string `json:"effect"`
}

func TaskCompletionInputSHA(in TaskCompleteInput) string {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type TaskCompletionNotStartedError struct {
	Refusal TaskCompletionRefusal
	Cause   error
}

func (e *TaskCompletionNotStartedError) Error() string { return e.Cause.Error() }
func (e *TaskCompletionNotStartedError) Unwrap() error { return e.Cause }
