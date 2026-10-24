package coordination

// ConversationSource identifies canonical native text. ClaimedAuthor is data,
// never a Coop principal. Only the authenticated gateway sets VerifiedSenderID.
type ConversationSource struct {
	Provider         string `json:"provider"`
	InstanceUID      string `json:"instance_uid"`
	ProjectUID       string `json:"project_uid"`
	IssueUID         string `json:"issue_uid"`
	CommentUID       string `json:"comment_uid,omitempty"`
	Revision         string `json:"revision,omitempty"`
	ClaimedAuthor    string `json:"claimed_author,omitempty"`
	VerifiedSenderID string `json:"verified_sender_id,omitempty"`
}
type MapConversation struct {
	ThreadID     string             `json:"thread_id"`
	Source       ConversationSource `json:"source"`
	AfterEventID int64              `json:"after_event_id"`
	RequestID    string             `json:"request_id"`
}

// ConversationObservation exposes routing diagnostics without provider credentials
// or treating native authorship as authority.
type ConversationObservation struct {
	EventUID string `json:"event_uid"`
	IssueUID string `json:"issue_uid"`
	ThreadID string `json:"thread_id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}
type ConversationObservationPage struct {
	Observations []ConversationObservation `json:"observations"`
	Next         string                    `json:"next,omitempty"`
}

// ConversationCursor is an exact observation for owner restore reconciliation.
// AcceptedEpoch zero means no initial mapping has established a cursor yet.
type ConversationCursor struct {
	RestoreEpoch  int64  `json:"restore_epoch"`
	AcceptedEpoch int64  `json:"accepted_epoch"`
	InstanceUID   string `json:"instance_uid"`
	ProjectUID    string `json:"project_uid"`
	AfterEventID  int64  `json:"after_event_id"`
	AfterEventUID string `json:"after_event_uid,omitempty"`
}
type ReacceptConversationCursor struct {
	Observed  ConversationCursor `json:"observed"`
	Reason    string             `json:"reason"`
	RequestID string             `json:"request_id"`
	// Only repairs legacy state without an anchor, from independent owner evidence.
	ReconciledEventUID string `json:"reconciled_event_uid,omitempty"`
}
