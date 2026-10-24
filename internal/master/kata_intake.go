package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// routePendingIntakeLocked commits ACL, mapping, delivery and publication intent
// before acknowledging any observation. It never rewinds the provider cursor.
func (s *Store) routePendingIntakeLocked(ctx context.Context) error {
	if s.conversations.TeamThreadID == "" {
		return nil
	}
	events, err := s.PendingConversationIntake(ctx, s.sourceIntakeAfter, c.MaxPageRecords)
	if err != nil {
		return err
	}
	if len(events) == 0 && s.sourceIntakeAfter != "" {
		s.sourceIntakeAfter = ""
		events, err = s.PendingConversationIntake(ctx, "", c.MaxPageRecords)
		if err != nil {
			return err
		}
	}
	var result error
	for _, e := range events {
		s.sourceIntakeAfter = e.UID
		if e.IssueUID == s.conversations.TeamIssueUID {
			if _, err = s.db.ExecContext(ctx, "UPDATE source_observations SET state='excluded',reason='standing_team_issue' WHERE event_uid=? AND state='pending_intake'", e.UID); err != nil {
				return err
			}
			continue
		}
		if err = s.routeIntakeEvent(ctx, e); err != nil {
			if errors.Is(err, c.ErrInvalid) || errors.Is(err, c.ErrForbidden) || errors.Is(err, c.ErrNotFound) || errors.Is(err, c.ErrSize) || errors.Is(err, c.ErrConflict) {
				if _, err = s.db.ExecContext(ctx, "UPDATE source_observations SET state='rejected',reason=? WHERE event_uid=? AND state='pending_intake'", ErrorCode(err), e.UID); err != nil {
					return err
				}
			} else {
				result = errors.Join(result, err)
			}
		}
	}
	return result
}
func (s *Store) routeIntakeEvent(ctx context.Context, e taskstore.WorkEvent) error {
	issue, err := s.work.InspectIssue(ctx, e.IssueUID)
	if err != nil {
		return err
	}
	if issue.UID != e.IssueUID || issue.ProjectUID != s.conversations.ProjectUID {
		return c.ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = admission(ctx, tx); err != nil {
		return err
	}
	if err = s.sourceEpoch(ctx, tx); err != nil {
		return err
	}
	// Only server registry identity can select the existing task lifecycle.
	// Native metadata never supplies an admission or discussion ACL.
	var registeredLink c.ArtifactLink
	registered := false
	prefix := "kata://" + strings.ToLower(s.conversations.ProjectUID) + "/" + e.IssueUID + "/%"
	rows, err := tx.QueryContext(ctx, "SELECT body FROM artifacts WHERE kind='task' AND url LIKE ? LIMIT 2", prefix)
	if err != nil {
		return err
	}
	for rows.Next() {
		if registered {
			_ = rows.Close()
			return c.ErrConflict
		}
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return err
		}
		if json.Unmarshal(raw, &registeredLink) != nil {
			_ = rows.Close()
			return c.ErrInvalid
		}
		registered = true
	}
	err = rows.Err()
	err = errors.Join(err, rows.Close())
	if err != nil {
		return err
	}
	if !registered {
		if err = s.observeTaskCreation(ctx, tx, e); err != nil {
			return err
		}
		pending, pendingErr := s.taskCreationIntakePending(ctx, tx, issue.UID)
		if pendingErr != nil {
			return pendingErr
		}
		if pending {
			if _, err = tx.ExecContext(ctx, "UPDATE source_observations SET reason='task_creation_pending' WHERE event_uid=? AND state='pending_intake'", e.UID); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	bridge, err := authorize(ctx, tx, c.Principal{ID: s.conversations.BridgePrincipal}, "send")
	if err != nil || bridge.Kind != "service" {
		return c.ErrForbidden
	}
	templateID := s.conversations.TeamThreadID
	if registered {
		authority, e := s.resolveGoal(ctx, tx, bridge, registeredLink.Goal)
		if e != nil {
			return e
		}
		templateID = authority.Revision.Proposal.ThreadID
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", templateID).Scan(&raw); err != nil {
		return err
	}
	var template c.Thread
	if json.Unmarshal(raw, &template) != nil {
		return c.ErrInvalid
	}
	if !registered && (template.Source == nil || template.Source.IssueUID != s.conversations.TeamIssueUID) {
		return c.ErrConflict
	}
	for _, id := range []string{bridge.ID, s.conversations.IntakePrincipal} {
		p, e := principal(ctx, tx, id)
		if e != nil {
			return e
		}
		if e = member(ctx, tx, p, template.ID); e != nil {
			return e
		}
	}
	var thread string
	err = tx.QueryRowContext(ctx, "SELECT thread_id FROM conversation_sources WHERE issue_uid=?", e.IssueUID).Scan(&thread)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return err
	}
	source := c.ConversationSource{Provider: "kata", InstanceUID: s.conversations.InstanceUID, ProjectUID: s.conversations.ProjectUID, IssueUID: e.IssueUID}
	var acknowledgement c.MessageReceipt
	if created {
		// Copy only current configured membership, never native metadata or labels.
		rows, e := tx.QueryContext(ctx, "SELECT principal_id FROM members WHERE thread_id=? ORDER BY principal_id", template.ID)
		if e != nil {
			return e
		}
		var members []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				_ = rows.Close()
				return e
			}
			members = append(members, id)
		}
		e = rows.Err()
		e = errors.Join(e, rows.Close())
		if e != nil {
			return e
		}
		for _, id := range members {
			p, e := principal(ctx, tx, id)
			if e != nil || !slices.Contains(p.Engagements, template.Engagement) {
				return c.ErrForbidden
			}
		}
		title := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, issue.Title)), " ")
		runes := []rune(title)
		if len(runes) > 120 {
			title = string(runes[:120])
		}
		if title == "" {
			title = issue.UID
		}
		if registered {
			title = "Kata task: " + title
		} else {
			title = "Kata intake: " + title
		}
		th := c.Thread{ID: newID("th"), Title: title, Engagement: template.Engagement, Members: members, CreatedAt: s.now().UTC(), Source: &source}
		thread = th.ID
		if _, err = tx.ExecContext(ctx, "INSERT INTO threads(id,engagement,body) VALUES(?,?,?)", th.ID, th.Engagement, encode(th)); err != nil {
			return err
		}
		for _, id := range members {
			if _, err = tx.ExecContext(ctx, "INSERT INTO members(thread_id,principal_id) VALUES(?,?)", thread, id); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_sources(thread_id,issue_uid,after_id,body) VALUES(?,?,0,?)", thread, issue.UID, encode(source)); err != nil {
			return err
		}
		if !registered {
			intake, err := principal(ctx, tx, s.conversations.IntakePrincipal)
			if err != nil {
				return err
			}
			body := fmt.Sprintf("Received for planning by %s. Observed provider revision %d. This native Kata issue is intake and has no engineering admission. Inspect this issue and choose an existing exact approved goal, repository and base, then adopt this same UID through Coop. Planner tools: coop-worker tasks read and coop-worker tasks adopt with private --input JSON; see docs/kata-task-provider.md for the exact revision and approved-goal fields. If a new goal is needed, propose it and wait for an authenticated owner decision.\n\n%s\n%s", intake.Alias, issue.Revision, intakeTextPreview(issue.Title, 1024), intakeTextPreview(issue.Body, c.MaxText/2))
			acknowledgement, err = s.insertMessage(ctx, tx, bridge, c.SendMessage{ThreadID: thread, Recipients: []string{intake.ID}, Body: body, RequestID: "intake-" + digest(issue.UID)})
			if err != nil {
				return err
			}
			if err = s.stageSource(ctx, tx, bridge, &acknowledgement, ""); err != nil {
				return err
			}
		}
	}
	if err = member(ctx, tx, bridge, thread); err != nil {
		return err
	}
	source.Revision = e.UID
	source.ClaimedAuthor = e.Actor
	message, err := s.observeSourceEvent(ctx, tx, bridge, thread, source, e)
	if err != nil {
		return err
	}
	if message == "" && created {
		message = acknowledgement.MessageID
	}
	if _, err = tx.ExecContext(ctx, "UPDATE source_observations SET state='observed',message_id=?,reason=? WHERE event_uid=? AND state='pending_intake'", message, intakeObservationReason(registered), e.UID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if acknowledgement.MessageID != "" {
		_, err = s.publishSourceLocked(ctx, bridge, acknowledgement)
	}
	return err
}

func intakeTextPreview(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	return strings.ToValidUTF8(raw[:limit], "") + "\n[Context shortened; inspect the current native issue before adoption.]"
}

func intakeObservationReason(registered bool) string {
	if registered {
		return "registered_task_lifecycle"
	}
	return ""
}
