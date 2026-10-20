package coordination

import "strings"

// ValidMasterID is the existing master-store identity contract. Presentation
// boundaries may additionally reject non-printable values.
func ValidMasterID(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, "\x00\r\n/\\")
}

type SelfIdentity struct {
	Principal    Principal `json:"principal"`
	Placement    Placement `json:"placement"`
	RestoreEpoch int64     `json:"restore_epoch"`
}
