// Package coordination defines the master protocol without storage or worker dependencies.
package coordination

import (
	"errors"
	"time"
)

const (
	MaxText           = 32 << 10
	MaxJSON           = 256 << 10
	MaxPageBytes      = 1 << 20
	MaxPageRecords    = 100
	MaxPending        = 128
	MaxMessages       = 100000
	HeartbeatInterval = 15 * time.Second
	PresenceStale     = 45 * time.Second
)

var (
	ErrInvalid     = errors.New("invalid_request")
	ErrForbidden   = errors.New("forbidden")
	ErrNotFound    = errors.New("not_found")
	ErrConflict    = errors.New("conflict")
	ErrCapacity    = errors.New("capacity_exceeded")
	ErrSize        = errors.New("size_exceeded")
	ErrEpoch       = errors.New("restore_reconciliation_required")
	ErrUnavailable = errors.New("master_unavailable")
	ErrUnsupported = errors.New("unsupported_operation")
)

type Principal struct {
	ID           string   `json:"id" toml:"id"`
	Kind         string   `json:"kind" toml:"kind"`
	Alias        string   `json:"alias" toml:"alias"`
	Engagements  []string `json:"engagements" toml:"engagements"`
	Capabilities []string `json:"capabilities" toml:"capabilities"`
	GitHubBotID  int64    `json:"github_bot_id,omitempty" toml:"github_bot_id"`
}
type Placement struct {
	AgentID    string `json:"agent_id" toml:"agent_id"`
	HostID     string `json:"host_id" toml:"host_id"`
	InstanceID string `json:"worker_instance_id" toml:"worker_instance_id"`
	Generation int64  `json:"generation" toml:"generation"`
}
type CreateThread struct {
	Title      string   `json:"title"`
	Engagement string   `json:"engagement"`
	Members    []string `json:"members"`
	RequestID  string   `json:"request_id"`
}
type Thread struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Engagement string    `json:"engagement"`
	Members    []string  `json:"members"`
	CreatedAt  time.Time `json:"created_at"`
}
type SendMessage struct {
	// Nil is legacy/unguarded; an explicit empty set must survive JSON encoding.
	ExpectedMembers []string          `json:"expected_members,omitzero"`
	Continuation    *TaskContinuation `json:"task_continuation,omitempty"`
	ThreadID        string            `json:"thread_id"`
	Recipients      []string          `json:"recipients,omitempty"`
	ParentMessageID string            `json:"parent_message_id,omitempty"`
	Body            string            `json:"body"`
	RequestID       string            `json:"request_id"`
}

type Ping struct {
	AgentID    string `json:"agent_id"`
	Engagement string `json:"engagement"`
	Body       string `json:"body"`
	RequestID  string `json:"request_id"`
}
type Message struct {
	Continuation    *TaskContinuation `json:"task_continuation,omitempty"`
	ID              string            `json:"id"`
	ThreadID        string            `json:"thread_id"`
	AuthorID        string            `json:"author_id"`
	AuthorKind      string            `json:"author_kind"`
	Recipients      []string          `json:"recipients"`
	ParentMessageID string            `json:"parent_message_id,omitempty"`
	Body            string            `json:"body"`
	RequestID       string            `json:"request_id"`
	Sequence        int64             `json:"sequence"`
	ChainID         string            `json:"chain_id"`
	Hop             int               `json:"hop"`
	CreatedAt       time.Time         `json:"created_at"`
	Imported        bool              `json:"imported,omitempty"`
}
type NativeIntent struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
}
type Delivery struct {
	ID             string        `json:"id"`
	MessageID      string        `json:"message_id"`
	AgentID        string        `json:"agent_id"`
	State          string        `json:"state"`
	Reason         string        `json:"reason,omitempty"`
	Intent         *NativeIntent `json:"native_intent,omitempty"`
	ReplyMessageID string        `json:"reply_message_id,omitempty"`
}
type MessageReceipt struct {
	MasterID     string     `json:"master_id"`
	RestoreEpoch int64      `json:"restore_epoch"`
	Backend      string     `json:"backend"`
	MessageID    string     `json:"message_id"`
	Sequence     int64      `json:"sequence"`
	Deliveries   []Delivery `json:"deliveries"`
}
type PublishReply struct {
	RestoreEpoch int64        `json:"restore_epoch"`
	DeliveryID   string       `json:"delivery_id"`
	InstanceID   string       `json:"worker_instance_id"`
	Generation   int64        `json:"generation"`
	Intent       NativeIntent `json:"native_intent"`
	Body         string       `json:"body"`
	Recipients   []string     `json:"recipients,omitempty"`
	RequestID    string       `json:"request_id"`
}
type Cursor struct {
	RestoreEpoch int64 `json:"restore_epoch"`
	Sequence     int64 `json:"sequence"`
}
type PendingQuery struct {
	After string `json:"after,omitempty"`
}
type DeliveryPage struct {
	Quiescent    bool       `json:"quiescent"`
	Deliveries   []Delivery `json:"deliveries"`
	Next         string     `json:"next,omitempty"`
	RestoreEpoch int64      `json:"restore_epoch"`
}
type MessagePage struct {
	Messages []Message `json:"messages"`
	Next     int64     `json:"next,omitempty"`
}
type DeliveryUpdate struct {
	RestoreEpoch int64         `json:"restore_epoch"`
	DeliveryID   string        `json:"delivery_id"`
	InstanceID   string        `json:"worker_instance_id"`
	Generation   int64         `json:"generation"`
	State        string        `json:"state"`
	Reason       string        `json:"reason,omitempty"`
	Intent       *NativeIntent `json:"native_intent,omitempty"`
}
type Heartbeat struct {
	Placement
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}
type Presence struct {
	Placement
	Alias    string    `json:"alias"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}
type Event struct {
	Cursor
	Kind      string `json:"kind"`
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id,omitempty"`
}
type GoalRef struct {
	Source         string `json:"source"`
	MasterID       string `json:"master_id"`
	GoalID         string `json:"goal_id"`
	RevisionSHA256 string `json:"revision_sha256"`
}

type ThreadQuery struct {
	After string `json:"after,omitempty"`
}
type ThreadPage struct {
	Threads []Thread `json:"threads"`
	Next    string   `json:"next,omitempty"`
}
