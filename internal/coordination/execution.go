package coordination

import (
	"encoding/hex"
	"strings"
)

// ExecutionBinding pins a managed runtime to one authenticated worker placement.
// Unlike diagnostic observations, this pin is checked before runtime replacement.
type ExecutionBinding struct {
	MasterID string `json:"master_id" toml:"master_id"`
	Placement
}

func ValidateExecutionBinding(b ExecutionBinding) error {
	if ValidateDiagnosticBinding(DiagnosticBinding(b)) != nil || len(b.InstanceID) != 64 || b.InstanceID != strings.ToLower(b.InstanceID) {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(b.InstanceID); err != nil {
		return ErrInvalid
	}
	return nil
}
