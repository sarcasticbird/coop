package coordination

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Recovery evidence is immutable. Resolution records never replace the evidence.
type RecoveryEvidence struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	RecordKey   string          `json:"record_key"`
	MessageID   string          `json:"message_id,omitempty"`
	DeliveryID  string          `json:"delivery_id,omitempty"`
	Intent      *NativeIntent   `json:"native_intent,omitempty"`
	Uncertain   bool            `json:"uncertain,omitempty"`
	Terminal    bool            `json:"terminal,omitempty"`
	Data        json.RawMessage `json:"data"`
	Observation json.RawMessage `json:"observation,omitempty"`
}
type GuestRecovery struct {
	Placement
	RestoreEpoch   int64              `json:"restore_epoch"`
	Records        []RecoveryEvidence `json:"records,omitempty"`
	Complete       bool               `json:"complete"`
	RecordCount    int                `json:"record_count"`
	ManifestSHA256 string             `json:"manifest_sha256"`
}
type RecoveryStatus struct {
	RestoreEpoch   int64  `json:"restore_epoch"`
	Quiescent      bool   `json:"quiescent"`
	PendingGuests  int    `json:"pending_guests"`
	Unresolved     int    `json:"unresolved"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	Reconciled     bool   `json:"reconciled"`
}
type RecoveryResolution struct {
	RestoreEpoch int64  `json:"restore_epoch"`
	AgentID      string `json:"agent_id"`
	EvidenceID   string `json:"evidence_id"`
	ProofID      string `json:"proof_id,omitempty"`
	Reason       string `json:"reason"`
}
type PlacementChange struct {
	Placement
	ExpectedGeneration int64 `json:"expected_generation"`
	RestoreEpoch       int64 `json:"restore_epoch"`
	DryRun             bool  `json:"dry_run"`
}
type HistoryImport struct {
	ThreadID  string `json:"thread_id"`
	SourceURL string `json:"source_url"`
	Cutoff    string `json:"cutoff"`
	Body      string `json:"body"`
	RequestID string `json:"request_id"`
	DryRun    bool   `json:"dry_run"`
}
type HistoryRecord struct {
	HistoryImport
	SHA256 string `json:"sha256"`
}
type SourceCutover struct {
	ThreadID      string `json:"thread_id"`
	SourceURL     string `json:"source_url"`
	HistorySHA256 string `json:"history_sha256"`
	DryRun        bool   `json:"dry_run"`
}

func RecoveryDigest(records []RecoveryEvidence) string {
	items := make([]string, 0, len(records))
	for _, r := range records {
		b, _ := json.Marshal(r)
		h := sha256.Sum256(b)
		items = append(items, r.ID+":"+hex.EncodeToString(h[:]))
	}
	sort.Strings(items)
	b, _ := json.Marshal(items)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type BackupManifest struct {
	Version        int    `json:"version"`
	MasterID       string `json:"master_id"`
	Epoch          int64  `json:"restore_epoch"`
	DatabaseSHA256 string `json:"database_sha256"`
	ConfigSHA256   string `json:"config_sha256"`
}

// PlacementProof binds a generation move to the resolved inventory that made
// the operator's move admissible. It grants no authority to the old placement.
type PlacementProof struct {
	MasterID       string    `json:"master_id"`
	RestoreEpoch   int64     `json:"restore_epoch"`
	From           Placement `json:"from"`
	To             Placement `json:"to"`
	ManifestSHA256 string    `json:"manifest_sha256"`
}
