package coordination

import (
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxMemoryEntries = 512

// Memory belongs to a stable agent, not a container, placement or session.
// It is working knowledge, never task approval or execution authority.
type MemorySummary struct {
	AgentID   string    `json:"agent_id"`
	Key       string    `json:"key"`
	Revision  int64     `json:"revision"`
	Source    string    `json:"source,omitempty"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Memory struct {
	MemorySummary
	Body string `json:"body"`
}

type MemoryQuery struct {
	AgentID string `json:"agent_id,omitempty"`
	Key     string `json:"key"`
}

type MemoryWrite struct {
	MemoryQuery
	Body             string `json:"body"`
	Source           string `json:"source,omitempty"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type MemoryList struct {
	AgentID string `json:"agent_id,omitempty"`
	After   string `json:"after,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
}

type MemoryPage struct {
	Entries []MemorySummary `json:"entries"`
	Next    string          `json:"next,omitempty"`
}

func ValidMemoryKey(key string) bool {
	return key != "" && len(key) <= 256 && utf8.ValidString(key) &&
		strings.TrimSpace(key) == key && path.Clean(key) == key &&
		!strings.HasPrefix(key, "/") && key != "." && key != ".." &&
		!strings.HasPrefix(key, "../") && !strings.Contains(key, "\\") &&
		strings.IndexFunc(key, unicode.IsControl) < 0
}

func ValidateMemoryWrite(in MemoryWrite) error {
	if !ValidMemoryKey(in.Key) || in.ExpectedRevision < 0 || in.ExpectedRevision == 1<<63-1 ||
		!utf8.ValidString(in.Body) || strings.TrimSpace(in.Body) == "" || strings.ContainsRune(in.Body, '\x00') ||
		!utf8.ValidString(in.Source) || strings.IndexFunc(in.Source, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	if len(in.Body) > MaxText || len(in.Source) > 1024 {
		return ErrSize
	}
	return nil
}
