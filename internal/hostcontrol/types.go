// Package hostcontrol implements host-local, durable control of owned Coops.
package hostcontrol

import "time"

type Host struct {
	ID      string `json:"host_id"`
	Label   string `json:"host_label"`
	Version string `json:"version,omitempty"`
}
type Caller struct {
	ID     string
	digest [32]byte
}
type Workspace struct {
	Ref              string `json:"workspace"`
	Mode             string `json:"mode"`
	Name             string `json:"name"`
	State            string `json:"state"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	Owned            bool   `json:"owned"`
	WorkerConfigured bool   `json:"worker_configured"`
}
type RequestKey struct {
	ID        string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
}
type Mutation struct {
	TargetOperation string        `json:"target_operation,omitempty"`
	Key             RequestKey    `json:"key"`
	Workspace       string        `json:"workspace"`
	Action          string        `json:"action"`
	Argv            []string      `json:"argv,omitempty"`
	Cwd             string        `json:"cwd,omitempty"`
	Stdin           []byte        `json:"stdin,omitempty"`
	Timeout         time.Duration `json:"timeout,omitempty"`
	InterruptActive bool          `json:"interrupt_active,omitempty"`
	Prompt          string        `json:"prompt,omitempty"`
}
type Outcome struct {
	ExitCode          *int   `json:"exit_code,omitempty"`
	Category          string `json:"category,omitempty"`
	Completed         bool   `json:"completed"`
	TerminationProven bool   `json:"termination_proven"`
}
type OutputPage struct {
	Encoding     string `json:"encoding"`
	Data         string `json:"data"`
	Offset       int64  `json:"offset"`
	NextOffset   int64  `json:"next_offset"`
	TotalBytes   int64  `json:"total_bytes"`
	DroppedBytes int64  `json:"dropped_bytes"`
	EOF          bool   `json:"eof"`
	Truncated    bool   `json:"truncated"`
}
type Operation struct {
	Host
	ID            string     `json:"operation_id"`
	Workspace     string     `json:"workspace"`
	Action        string     `json:"action"`
	State         string     `json:"state"`
	Phase         string     `json:"phase,omitempty"`
	ErrorCategory string     `json:"error_category,omitempty"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	WakeID        string     `json:"wake_id,omitempty"`
	WakePhase     string     `json:"wake_phase,omitempty"`
	Stdout        OutputPage `json:"stdout"`
	Stderr        OutputPage `json:"stderr"`
}
type Accepted struct {
	Host
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	WakeID      string `json:"wake_id,omitempty"`
	WakePhase   string `json:"wake_phase,omitempty"`
}
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }
func fail(code string) error   { return &Error{Code: code} }
