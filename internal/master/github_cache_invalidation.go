package master

import (
	"context"
	"fmt"
)

const invalidateGitHubLeases = `UPDATE github_cache_leases SET result='invalidated' WHERE result='';
DELETE FROM github_cache_chunks WHERE lease IN (SELECT id FROM github_cache_leases WHERE result='invalidated');`

// Only these code-owned identifiers enter DDL; no caller input is SQL text.
func (s *Store) initializeGitHubCache(ctx context.Context) error {
	// Replacing these code-owned triggers also upgrades databases installed by
	// older versions. Publish the complete trigger set atomically.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"artifacts", "goals", "goal_revisions", "members", "principals", "placements"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			guard := ""
			if (table == "placements" || table == "artifacts") && op == "UPDATE" {
				// Heartbeats and identical artifact registrations do not change
				// identity or scope, even when their request IDs are new.
				guard = " WHEN OLD.body IS NOT NEW.body"
			}
			scope := ""
			if table == "artifacts" {
				matching := func(row string) string {
					return fmt.Sprintf(`(json_extract(ref,'$.goal.goal_id')=%s.goal_id AND json_extract(ref,'$.goal.revision_sha256')=%s.revision AND json_extract(ref,'$.task_url')=CASE WHEN %s.kind='task' THEN %s.url ELSE json_extract(%s.body,'$.task_url') END)`, row, row, row, row, row)
				}
				switch op {
				case "INSERT":
					scope = " WHERE " + matching("NEW")
				case "DELETE":
					scope = " WHERE " + matching("OLD")
				case "UPDATE":
					scope = " WHERE " + matching("OLD") + " OR " + matching("NEW")
				}
			}
			statement := fmt.Sprintf(`DROP TRIGGER IF EXISTS cache_revision_%s_%s;
CREATE TRIGGER cache_revision_%s_%s AFTER %s ON %s%s BEGIN
UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='github_cache_revision';
UPDATE github_cache_objects SET generation=generation+1,due=''%s; %s END;`, table, op, table, op, op, table, guard, scope, invalidateGitHubLeases)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
