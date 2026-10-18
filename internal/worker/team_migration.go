package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const migrationLimit = 256 << 20

type stateMigration struct {
	Version                                           int
	InstanceID, BeforeSHA, AfterSHA, BackupSHA, Phase string
}

// An unresolved publication is an inspection hold, not permission to replay or
// restore a backup. The old binary independently rejects Version:2 in state.json.
func (s *FileStore) compatible() error {
	if s.version() == 3 {
		return s.masterCompatible()
	}
	if s.version() != 1 && s.version() != 2 {
		return errors.New("unsupported worker store version")
	}
	for _, name := range []string{"master-migration.json", "master-migration-backup.json"} {
		if _, err := os.Lstat(filepath.Join(s.Dir, name)); !os.IsNotExist(err) {
			return errors.New("legacy writer refused master migration evidence")
		}
	}
	raw, err := readPrivate(filepath.Join(s.Dir, "migration.json"), 4096)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m stateMigration
	if strictTeamJSON(raw, &m) != nil || m.Version != 1 || m.Phase != "verified" || s.version() != 2 || !hexSHA.MatchString(m.InstanceID) || !hexSHA.MatchString(m.BeforeSHA) || !hexSHA.MatchString(m.AfterSHA) || !hexSHA.MatchString(m.BackupSHA) {
		return errors.New("state migration requires inspection; no automatic rollback or retry")
	}
	return nil
}

func migrationJob(j Job, wake bool) error {
	return migrationJobEvidence(j, wake, false)
}

func migrationJobEvidence(j Job, wake, legacyCompletedWake bool) error {
	if wake && j.ID == "" {
		if j != (Job{}) {
			return errors.New("wake has job evidence without identity")
		}
		return nil
	}
	if !hexSHA.MatchString(j.ID) || j.Attempts < 0 || j.ReviewCycles < 0 || j.ReconcileFailures < 0 || validateJobPromptFailure(j) != nil {
		return errors.New("invalid migration job")
	}
	if wake {
		if j.Kind != "wake" {
			return errors.New("invalid wake job kind")
		}
	} else {
		switch j.Kind {
		case "plan", "implement", "review", "fix", "deliver":
		default:
			return errors.New("invalid task job kind")
		}
		if JobKey(j) != j.ID {
			return errors.New("migration job key mismatch")
		}
	}
	switch j.Phase {
	case "waiting", "paused", "blocked", "completed":
	default:
		return errors.New("migration requires drained task and wake turns")
	}
	switch j.SubmissionState {
	case "", "completed", "interrupted":
	case "submitting", "unknown", "accepted", "running", "compacting", "blocked":
		// V1 wake completion records TerminalAt without replacing the last
		// submission observation. Preserve that evidence rather than normalizing it.
		if j.Phase != "completed" || (j.TerminalAt.IsZero() && (!wake || !legacyCompletedWake)) {
			return errors.New("migration refuses uncertain submission evidence")
		}
	default:
		return errors.New("migration refuses uncertain submission evidence")
	}
	if j.SessionID != "" && !validSession(j.SessionID) {
		return errors.New("invalid saved session")
	}
	if j.MessageID != "" && (!validMessage(j.MessageID) || j.SessionID == "" || (j.TerminalAt.IsZero() && j.SubmissionState != "interrupted" && (!wake || !legacyCompletedWake))) {
		return errors.New("migration refuses unresolved message evidence")
	}
	return nil
}

// Old wake completion saved a resolved source and reply without replacing the
// running observation or setting TerminalAt. This is structural compatibility
// only; operators must separately reconcile the actual native turn and reply.
func migrationWakeReceipt(c Config, r WakeRequest, v WakeReceipt) error {
	j := v.Job
	if j.SubmissionState != "running" || !j.TerminalAt.IsZero() {
		return migrationJob(j, true)
	}
	if validateWakeRequest(r) != nil || r.Target != c.Name || v.Version != 1 || v.RequestID != r.ID || j.ID != r.ID || v.Phase != "completed" || j.Phase != "completed" || j.Kind != "wake" || !validSession(j.SessionID) || !validMessage(j.MessageID) || j.SubmittedAt.IsZero() || j.SubmittedAt.Before(r.CreatedAt) || v.UpdatedAt.IsZero() || v.UpdatedAt.Before(j.SubmittedAt) {
		return errors.New("invalid legacy completed wake evidence")
	}
	s := v.Source
	if j.GoalURL != s.DiscussionURL || j.TaskURL != s.RootID || !cleanPrivatePath(j.Checkout) {
		return errors.New("invalid legacy completed wake job route")
	}
	for _, id := range []string{s.NodeID, s.DiscussionID, s.RootID, v.ReplyNodeID} {
		if id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n\t ") {
			return errors.New("invalid legacy completed wake node identity")
		}
	}
	if s.NodeID == "" || s.DiscussionID == "" || s.RootID == "" || s.Revision != ConversationRevision(s) || !validWakeText(s.Body, MaxWakeReceipt) || (!validActor(s.Author, "User") && !validActor(s.Author, "Bot")) || s.CreatedAt.IsZero() || sourceTime(s).After(j.SubmittedAt) || s.DiscussionURL != r.Source.DiscussionURL || !strings.Contains(s.DiscussionURL, "/discussions/") || !goalInScope(c, Goal{URL: s.DiscussionURL}) {
		return errors.New("invalid legacy completed wake source")
	}
	base, fragment, comment := strings.Cut(s.URL, "#discussioncomment-")
	if s.NodeID == s.DiscussionID {
		if s.RootID != s.DiscussionID || s.ParentID != "" || s.URL != s.DiscussionURL {
			return errors.New("invalid legacy completed wake root")
		}
	} else if !comment || base != s.DiscussionURL || !numericID.MatchString(fragment) {
		return errors.New("invalid legacy completed wake comment")
	}
	if r.Kind == "discussion" {
		if canonicalConversation(r.Source) != canonicalConversation(s) || r.Source.Revision != s.Revision {
			return errors.New("legacy completed wake source mismatch")
		}
	} else if s.NodeID != s.DiscussionID {
		return errors.New("legacy completed ping requires resolved Discussion root")
	}
	base, fragment, comment = strings.Cut(v.ReplyURL, "#discussioncomment-")
	if !comment || base != s.DiscussionURL || !numericID.MatchString(fragment) {
		return errors.New("invalid legacy completed wake reply")
	}
	return migrationJobEvidence(j, true, true)
}

// MigrateState is deliberately one-way and offline. All wake bytes retain their
// original v1 schema; they are historical input, not new v2 assignments.
func MigrateState(c Config, from, to int, expected string) error {
	if from == 2 && to == 3 {
		return migrateMasterState(c, expected, atomicPrivate)
	}
	return migrateState(c, from, to, expected, atomicPrivate)
}

func migrateState(c Config, from, to int, expected string, write func(string, []byte) error) error {
	if from != 1 || to != 2 || c.Version != 2 || !hexSHA.MatchString(expected) {
		return errors.New("migration requires v2 config, --from 1 --to 2 and exact expected instance; rollback is unsupported")
	}
	s := NewFileStore(c.StateDir)
	if err := ensurePrivateDir(s.Dir, false); err != nil {
		return err
	}
	release, err := s.Lock()
	if err != nil {
		return err
	}
	defer release()
	return migrateStateLocked(c, s, expected, write)
}

// The caller holds the worker lock throughout initial state publication or
// migration. The existing producer fence and migration journal remain shared.
func migrateStateLocked(c Config, s *FileStore, expected string, write func(string, []byte) error) error {
	// Exclude concurrent pings as well as the worker. Do not wait on a producer.
	w := NewWakeStore(filepath.Join(s.Dir, "wake", "v1"))
	if _, err := os.Lstat(filepath.Join(s.Dir, "wake")); err == nil {
		if err := w.ensure(false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Take this lock even when no wake has ever been queued: a first producer
	// must not create an inbox outside the conversion snapshot.
	unlock, err := w.producerLockMode(true)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(filepath.Join(s.Dir, "migration.json")); !os.IsNotExist(err) {
		return errors.New("migration evidence already exists; inspect rather than retry or rollback")
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	if state.InstanceID != expected {
		return errors.New("migration instance mismatch")
	}
	for _, j := range state.Jobs {
		if err := migrationJob(j, false); err != nil {
			return err
		}
	}
	for _, b := range state.Sessions {
		if !validSession(b.SessionID) || !cleanPrivatePath(b.Checkout) {
			return errors.New("invalid saved session binding")
		}
	}
	for _, count := range state.Attempts {
		if count < 0 {
			return errors.New("invalid attempts")
		}
	}
	for _, count := range state.ReviewCycles {
		if count < 0 {
			return errors.New("invalid review cycles")
		}
	}
	if _, err := s.Paused(); err != nil {
		return err
	}
	requests, err := w.Requests()
	if err != nil {
		return err
	}
	for _, r := range requests {
		if r.Target != c.Name {
			return errors.New("foreign wake target")
		}
		receipt, err := w.Receipt(r.ID)
		if err != nil {
			return err
		}
		if receipt.Phase == "running" || receipt.Phase == "recovery" {
			return errors.New("migration requires drained wakes")
		}
		if receipt.Job.ID != "" && receipt.Job.ID != r.ID {
			return errors.New("wake job/request identity mismatch")
		}
		if err := migrationWakeReceipt(c, r, receipt); err != nil {
			return err
		}
	}
	routing, err := w.Routing()
	if err != nil {
		return err
	}
	for _, binding := range routing.Bindings {
		if !validSession(binding.SessionID) || !cleanPrivatePath(binding.Directory) {
			return errors.New("invalid wake session binding")
		}
	}
	files := map[string][]byte{}
	total := 0
	err = filepath.WalkDir(s.Dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(s.Dir, path)
		if err != nil || rel == "." {
			return err
		}
		if entry.IsDir() {
			switch filepath.ToSlash(rel) {
			case "wake", "wake/v1", "wake/v1/inbox", "wake/v1/receipts":
				return ensurePrivateDir(path, false)
			}
			return errors.New("unknown directory in worker store")
		}
		switch filepath.ToSlash(rel) {
		case "worker.lock", "wake/v1/producer.lock":
			_, err := readPrivate(path, 4096)
			return err
		case "state.json", "control.json", "status.json", "wake/v1/routing.json":
		default:
			id := entry.Name()
			if filepath.Ext(id) != ".json" {
				return errors.New("unknown migration record")
			}
			id = id[:len(id)-5]
			if !hexSHA.MatchString(id) || (filepath.Dir(rel) != filepath.Join("wake", "v1", "inbox") && filepath.Dir(rel) != filepath.Join("wake", "v1", "receipts")) {
				return errors.New("unknown migration record")
			}
			if _, err := w.request(id); err != nil {
				return err
			}
		}
		raw, err := readPrivate(path, MaxSnapshot+(4<<20))
		if err != nil {
			return err
		}
		// Decode known records with their schema so opaque map keys, including
		// case-distinct GitHub node IDs, are not mistaken for duplicate fields.
		var doc any
		switch filepath.ToSlash(rel) {
		case "state.json":
			doc = new(State)
		case "control.json":
			doc = new(control)
		case "status.json":
			doc = new(Status)
		case "wake/v1/routing.json":
			doc = new(WakeRoutingState)
		default:
			if filepath.Dir(rel) == filepath.Join("wake", "v1", "inbox") {
				doc = new(WakeRequest)
			} else {
				doc = new(WakeReceipt)
			}
		}
		if err := strictTeamJSON(raw, doc); err != nil {
			return err
		}
		if rel == "status.json" {
			var status Status
			if len(raw) > 64<<10 || strictJSON(raw, &status) != nil || validateStatusPromptFailures(status) != nil {
				return errors.New("invalid migration status record")
			}
		}
		total += len(raw)
		if total > migrationLimit/2 {
			return errors.New("migration input exceeds backup capacity")
		}
		files[rel] = raw
		return nil
	})
	if err != nil {
		return err
	}
	before, ok := files["state.json"]
	if !ok {
		return errors.New("migration requires existing saved state")
	}
	backup, err := json.Marshal(files)
	if err != nil || len(backup) > migrationLimit {
		return errors.New("migration backup exceeds limit")
	}
	backupPath := filepath.Join(s.Dir, "migration-backup.json")
	if err := writeWakeImmutable(backupPath, backup); err != nil {
		return err
	}
	read, err := readPrivate(backupPath, migrationLimit)
	if err != nil || !bytes.Equal(read, backup) {
		return errors.New("migration backup verification failed")
	}
	state.Version = 2
	after, err := json.Marshal(state)
	if err != nil || len(after) > MaxSnapshot+(4<<20) {
		return errors.New("converted state exceeds limit")
	}
	var convertedRouting []byte
	if _, exists := files[filepath.Join("wake", "v1", "routing.json")]; exists && routing.Baselined {
		// A v1 actor pair and a v2 team are different routing authorities. The
		// verified conversion permits one fresh baseline, never a continuity
		// bridge from the old policy. Keep the old history in the backup.
		routing.Baselined = false
		convertedRouting, err = json.Marshal(routing)
		if err != nil || len(convertedRouting) > MaxSnapshot {
			return errors.New("converted routing exceeds limit")
		}
	}
	m := stateMigration{Version: 1, InstanceID: expected, BeforeSHA: BodyHash(string(before)), AfterSHA: BodyHash(string(after)), BackupSHA: BodyHash(string(backup)), Phase: "pending"}
	publish := func() error {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return write(filepath.Join(s.Dir, "migration.json"), raw)
	}
	if err := publish(); err != nil {
		return fmt.Errorf("migration hold publication failed; inspect evidence: %w", err)
	}
	// Old ping binaries never read state.json. Publish an inbox entry they
	// cannot interpret, under the producer lock and before v2 can become live.
	fence, err := json.Marshal(wakeVersionFence{Version: 2, InstanceID: expected})
	if err != nil {
		return err
	}
	fencePath := filepath.Join(w.Dir, "inbox", wakeFenceName)
	if err := write(fencePath, fence); err != nil {
		return fmt.Errorf("migration wake fence uncertain; inspect evidence: %w", err)
	}
	if err := write(filepath.Join(s.Dir, "state.json"), after); err != nil {
		return fmt.Errorf("migration replacement uncertain; inspect evidence: %w", err)
	}
	if convertedRouting != nil {
		if err := write(filepath.Join(w.Dir, "routing.json"), convertedRouting); err != nil {
			return fmt.Errorf("migration routing replacement uncertain; inspect evidence: %w", err)
		}
	}
	for rel, original := range files {
		want := original
		if rel == "state.json" {
			want = after
		} else if rel == filepath.Join("wake", "v1", "routing.json") && convertedRouting != nil {
			want = convertedRouting
		}
		got, err := readPrivate(filepath.Join(s.Dir, rel), MaxSnapshot+(4<<20))
		if err != nil || !bytes.Equal(got, want) {
			return errors.New("migration result verification failed; inspect evidence")
		}
	}
	read, err = readPrivate(backupPath, migrationLimit)
	if err != nil || !bytes.Equal(read, backup) {
		return errors.New("migration backup changed; inspect evidence")
	}
	read, err = readPrivate(fencePath, 4096)
	if err != nil || !bytes.Equal(read, fence) {
		return errors.New("migration wake fence verification failed; inspect evidence")
	}
	m.Phase = "verified"
	if err := publish(); err != nil {
		return fmt.Errorf("migration completion uncertain; inspect evidence: %w", err)
	}
	_, err = NewVersionedFileStore(s.Dir, 2).Load()
	return err
}
