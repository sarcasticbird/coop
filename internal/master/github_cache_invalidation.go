package master

import (
	"context"
	"fmt"
)

const invalidateGitHubLeases = `UPDATE github_cache_leases SET result='invalidated' WHERE result='';
DELETE FROM github_cache_chunks WHERE lease IN (SELECT id FROM github_cache_leases WHERE result='invalidated');`

// Only these code-owned identifiers enter DDL; no caller input is SQL text.
func (s *Store) initializeGitHubCache(ctx context.Context) error {
	for _, table := range []string{"artifacts", "goals", "goal_revisions", "members", "principals", "placements"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			guard := ""
			if table == "placements" && op == "UPDATE" {
				// Presence is updated on every heartbeat. Only the placement body
				// changes identity, generation or host and revokes cache authority.
				guard = " WHEN OLD.body IS NOT NEW.body"
			}
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS cache_revision_%s_%s AFTER %s ON %s%s BEGIN
UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='github_cache_revision';
UPDATE github_cache_objects SET generation=generation+1,due=''; %s END;`, table, op, op, table, guard, invalidateGitHubLeases)
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}
