package coordination

type SelfIdentity struct {
	Principal    Principal `json:"principal"`
	Placement    Placement `json:"placement"`
	RestoreEpoch int64     `json:"restore_epoch"`
}
