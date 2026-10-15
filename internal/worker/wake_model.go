package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrWakeCapacity = errors.New("wake queue capacity exceeded")

const MaxWakePrompt = 8192
const MaxWakeRecord = 32768

// Pings revalidate a Discussion root after intake. Preserve that lossless source
// even when JSON escaping expands it beyond the inbox limit. Like snapshots,
// receipts remain bounded, including a full API-page source and diagnostics.
const MaxWakeReceipt = MaxSnapshot
const MaxWakeQueued = 128
const MaxWakeRecords = 10000

type WakeConfig struct {
	Enabled         bool   `json:"enabled"`
	CoordinationURL string `json:"coordination_url"`
	PeerHopLimit    int    `json:"peer_hop_limit,omitempty"`
}
type PingInput struct {
	RequestID string `json:"request_id,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}
type ConversationSource struct {
	NodeID, Revision, URL, DiscussionID, DiscussionURL string
	RootID, ParentID, Body                             string
	Author, ParentAuthor                               Actor
	CreatedAt, EditedAt                                time.Time
}
type WakeRequest struct {
	Version                  int
	ID, Target, Kind, Prompt string
	Source                   ConversationSource
	ChainID                  string
	PeerHop                  int
	CreatedAt                time.Time
}
type WakeReceipt struct {
	Version                  int
	RequestID, Phase, Reason string
	Job                      Job
	Source                   ConversationSource
	ReplyNodeID, ReplyURL    string
	UpdatedAt                time.Time
}
type WakeSummary struct {
	ReportAttempts int              `json:"report_attempts,omitempty"`
	PromptFailure  *PromptSizeError `json:"prompt_failure,omitempty"`
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Phase          string           `json:"phase"`
	Reason         string           `json:"reason,omitempty"`
	Coalesced      bool             `json:"coalesced,omitempty"`
}
type WakeBinding struct{ DiscussionID, ThreadID, Directory, SessionID string }
type WakeChain struct {
	ID, RootID, OwnerRevision string
	PeerHop                   int
}
type WakeRoutingState struct {
	Version          int
	Baselined        bool
	ScopeSHA         string
	APINotBefore     time.Time
	IntakeDeferred   bool
	Seen             map[string]string
	Suppressed       map[string]string
	Chains           map[string]WakeChain
	Bindings         map[string]WakeBinding
	UrgentBeforeWake string
}
type WakeStore interface {
	Enqueue(WakeRequest) (WakeSummary, error)
	Requests() ([]WakeRequest, error)
	Receipt(string) (WakeReceipt, error)
	SaveReceipt(WakeReceipt) error
	Routing() (WakeRoutingState, error)
	SaveRouting(WakeRoutingState) error
}

func wakeEnabled(c Config) bool { return c.Wake != nil && c.Wake.Enabled }
func validateWakeConfig(c Config) error {
	if c.Wake == nil {
		return nil
	}
	if c.Wake.PeerHopLimit < 1 || c.Wake.PeerHopLimit > 8 {
		return errors.New("invalid wake peer limit")
	}
	u := c.Wake.CoordinationURL
	if u == "" && !c.Wake.Enabled {
		return nil
	}
	if !validGitHubURL(u) || !strings.Contains(u, "/discussions/") || !goalInScope(c, Goal{URL: u}) {
		return errors.New("invalid wake coordination scope")
	}
	return nil
}
func validWakeText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func validateWakeRequest(r WakeRequest) error {
	if r.Version != 1 || !hexSHA.MatchString(r.ID) || !slug.MatchString(r.Target) || !hexSHA.MatchString(r.ChainID) || (r.Kind != "ping" && r.Kind != "discussion") || r.PeerHop < 0 || r.PeerHop > 8 || r.CreatedAt.IsZero() || !validWakeText(r.Prompt, MaxWakePrompt) || !validWakeText(r.Source.Body, MaxWakeRecord) || !validGitHubURL(r.Source.DiscussionURL) || !strings.Contains(r.Source.DiscussionURL, "/discussions/") {
		return errors.New("invalid wake request")
	}
	if r.Kind == "discussion" && (r.Source.NodeID == "" || r.Source.DiscussionID == "" || r.Source.RootID == "" || !hexSHA.MatchString(r.Source.Revision)) {
		return errors.New("invalid wake source")
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxWakeRecord {
		return errors.New("wake request exceeds size limit")
	}
	return nil
}
func QueuePing(c Config, s WakeStore, in PingInput, now time.Time) (WakeSummary, error) {
	c = Defaults(c)
	if !wakeEnabled(c) {
		return WakeSummary{}, errors.New("wake is not enabled")
	}
	if err := validateWakeConfig(c); err != nil {
		return WakeSummary{}, err
	}
	if in.RequestID != "" && !hexSHA.MatchString(in.RequestID) || !validWakeText(in.Prompt, MaxWakePrompt) {
		return WakeSummary{}, errors.New("invalid ping input")
	}
	return s.Enqueue(WakeRequest{Version: 1, ID: in.RequestID, Target: c.Name, Kind: "ping", Prompt: in.Prompt, CreatedAt: now.UTC(), Source: ConversationSource{URL: c.Wake.CoordinationURL, DiscussionURL: c.Wake.CoordinationURL, Author: c.Owner}})
}
func wakeTerminal(phase string) bool {
	switch phase {
	case "completed", "failed", "cancelled", "superseded", "suppressed":
		return true
	}
	return false
}
func wakePhase(phase string) bool {
	return wakeTerminal(phase) || phase == "queued" || phase == "running" || phase == "blocked"
}

func wakeReceiptTerminal(v WakeReceipt) bool {
	return wakeTerminal(v.Phase) || v.Phase == "blocked" && v.Reason == "prompt_size" && v.Job.PromptFailure != nil
}
