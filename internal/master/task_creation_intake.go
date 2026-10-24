package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// observeTaskCreation correlates an immutable native creation key with the
// server's validated intent. It grants no scope or admission and never uses
// mutable coop.* metadata. The UID survives lost create responses and restarts.
func (s *Store) observeTaskCreation(ctx context.Context, tx *sql.Tx, e taskstore.WorkEvent) error {
	if e.Type != "issue.created" || e.OriginInstanceUID != s.conversations.InstanceUID || e.ProjectUID != s.conversations.ProjectUID {
		return nil
	}
	var payload struct {
		Key string `json:"idempotency_key"`
	}
	raw := e.Payload
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Key == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE requests SET result=json_set(result,'$.observed_issue_uid',?)
 WHERE kind='task-create' AND json_extract(result,'$.project_uid')=? AND json_extract(result,'$.provider_key')=?
 AND (COALESCE(json_extract(result,'$.observed_issue_uid'),'')='' OR json_extract(result,'$.observed_issue_uid')=?)`, e.IssueUID, e.ProjectUID, payload.Key, e.IssueUID)
	return err
}

func (s *Store) taskCreationIntakePending(ctx context.Context, q queryer, uid string) (bool, error) {
	var pending bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM requests
 WHERE kind='task-create' AND json_extract(result,'$.project_uid')=? AND json_extract(result,'$.observed_issue_uid')=?)`, s.conversations.ProjectUID, uid).Scan(&pending)
	return pending, err
}

// Existing request journals already carry the authoritative exact goal and
// request identity. Add correlation keys once before polling a restored store;
// native data cannot create a request or supply a goal during this migration.
func (s *Store) initializeTaskCreationCorrelation(ctx context.Context, project string) error {
	actor, requestID := "", ""
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT actor,request_id,result FROM requests WHERE kind='task-create'
 AND COALESCE(json_extract(result,'$.provider_key'),'')='' AND (actor,request_id)>(?,?) ORDER BY actor,request_id LIMIT ?`, actor, requestID, c.MaxPageRecords)
		if err != nil {
			return err
		}
		type entry struct {
			actor, id string
			intent    taskCreateIntent
		}
		var entries []entry
		for rows.Next() {
			var entry entry
			var raw []byte
			if err = rows.Scan(&entry.actor, &entry.id, &raw); err != nil {
				_ = rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &entry.intent); err != nil {
				_ = rows.Close()
				return err
			}
			entries = append(entries, entry)
		}
		err = rows.Err()
		err = errors.Join(err, rows.Close())
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			actor, requestID = entry.actor, entry.id
			intent := entry.intent
			if intent.ProjectUID == "" {
				intent.ProjectUID = project
			}
			if intent.Reference != "" {
				p, uid, _, e := c.ParseKataTaskReference(intent.Reference)
				if e != nil {
					_ = tx.Rollback()
					return e
				}
				intent.ProjectUID = p
				intent.ObservedIssueUID = uid
			}
			intent.ProviderKey = taskstore.CreationKey(intent.Binding.Goal, entry.id)
			if _, err = tx.ExecContext(ctx, "UPDATE requests SET result=? WHERE actor=? AND request_id=?", encode(intent), entry.actor, entry.id); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}
