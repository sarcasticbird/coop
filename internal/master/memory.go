package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

const memorySchema = `
CREATE TABLE IF NOT EXISTS agent_memory(
 agent_id TEXT NOT NULL REFERENCES principals(id),
 key TEXT NOT NULL,
 revision INTEGER NOT NULL,
 body BLOB NOT NULL,
 PRIMARY KEY(agent_id,key)
);`

func memoryAgent(ctx context.Context, q queryer, actor c.Principal, id string, write bool) (c.Principal, c.Principal, error) {
	p, err := authorize(ctx, q, actor, "read")
	if err != nil {
		return p, c.Principal{}, err
	}
	if id == "" {
		id = p.ID
	}
	agent, err := principal(ctx, q, id)
	if err != nil {
		return p, agent, err
	}
	if agent.Kind != "agent" {
		return p, agent, c.ErrForbidden
	}
	if p.ID != agent.ID {
		if p.Kind != "owner" || !slices.Contains(p.Capabilities, "manage") {
			return p, agent, c.ErrForbidden
		}
		shared := false
		for _, engagement := range agent.Engagements {
			shared = shared || slices.Contains(p.Engagements, engagement)
		}
		if !shared {
			return p, agent, c.ErrForbidden
		}
	}
	if write {
		capability := "send"
		if p.Kind == "owner" {
			capability = "manage"
		}
		if _, err = authorize(ctx, q, p, capability); err != nil {
			return p, agent, err
		}
	}
	return p, agent, nil
}

func readMemory(ctx context.Context, q queryer, agent, key string) (out c.Memory, err error) {
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT body FROM agent_memory WHERE agent_id=? AND key=?", agent, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return out, c.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return
}

func (s *Store) ReadMemory(ctx context.Context, actor c.Principal, in c.MemoryQuery) (c.Memory, error) {
	if !c.ValidMemoryKey(in.Key) {
		return c.Memory{}, c.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Memory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, agent, err := memoryAgent(ctx, tx, actor, in.AgentID, false)
	if err != nil {
		return c.Memory{}, err
	}
	return readMemory(ctx, tx, agent.ID, in.Key)
}

func (s *Store) WriteMemory(ctx context.Context, actor c.Principal, in c.MemoryWrite) (c.Memory, error) {
	if err := c.ValidateMemoryWrite(in); err != nil {
		return c.Memory{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Memory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, agent, err := memoryAgent(ctx, tx, actor, in.AgentID, true)
	if err != nil {
		return c.Memory{}, err
	}
	old, err := readMemory(ctx, tx, agent.ID, in.Key)
	if err != nil && err != c.ErrNotFound {
		return c.Memory{}, err
	}
	// A lost response may retry the same revision/content. Never roll back a
	// newer edit or adopt a different writer's update as this caller's own.
	if old.Revision == in.ExpectedRevision+1 && old.UpdatedBy == p.ID && old.Body == in.Body && old.Source == in.Source {
		return old, nil
	}
	if old.Revision != in.ExpectedRevision {
		return c.Memory{}, c.ErrConflict
	}
	if old.Revision == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_memory WHERE agent_id=?", agent.ID).Scan(&count); err != nil {
			return c.Memory{}, err
		}
		if count >= c.MaxMemoryEntries {
			return c.Memory{}, c.ErrCapacity
		}
	}
	out := c.Memory{MemorySummary: c.MemorySummary{AgentID: agent.ID, Key: in.Key, Revision: old.Revision + 1, Source: in.Source, UpdatedBy: p.ID, UpdatedAt: s.now().UTC()}, Body: in.Body}
	if _, err = tx.ExecContext(ctx, "INSERT INTO agent_memory(agent_id,key,revision,body) VALUES(?,?,?,?) ON CONFLICT(agent_id,key) DO UPDATE SET revision=excluded.revision,body=excluded.body", agent.ID, in.Key, out.Revision, encode(out)); err != nil {
		return c.Memory{}, err
	}
	return out, tx.Commit()
}

func (s *Store) ListMemory(ctx context.Context, actor c.Principal, in c.MemoryList) (c.MemoryPage, error) {
	if in.After != "" && !c.ValidMemoryKey(in.After) || len(in.Prefix) > 256 || strings.ContainsRune(in.Prefix, '\x00') {
		return c.MemoryPage{}, c.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.MemoryPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, agent, err := memoryAgent(ctx, tx, actor, in.AgentID, false)
	if err != nil {
		return c.MemoryPage{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM agent_memory WHERE agent_id=? AND key>? AND substr(key,1,length(?))=? ORDER BY key LIMIT 51", agent.ID, in.After, in.Prefix, in.Prefix)
	if err != nil {
		return c.MemoryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := c.MemoryPage{Entries: []c.MemorySummary{}}
	for rows.Next() {
		if len(out.Entries) == 50 {
			out.Next = out.Entries[49].Key
			break
		}
		var raw []byte
		var m c.Memory
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, m.MemorySummary)
	}
	return out, rows.Err()
}

func registerMemory(mux *http.ServeMux, s *Store, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/v1/memory/read", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.MemoryQuery
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.ReadMemory(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/memory/write", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.MemoryWrite
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.WriteMemory(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/memory/list", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.MemoryList
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return s.ListMemory(r.Context(), p, in)
	}))
}
