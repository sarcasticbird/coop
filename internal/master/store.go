// Package master owns local durable agent coordination. Guests never open its database.
package master

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	_ "modernc.org/sqlite"
)

type StoreConfig struct {
	Path, MasterID, EpochPath string
	Now                       func() time.Time
}
type Store struct {
	db              *sql.DB
	path, epochPath string
	lock            *os.File
	databaseLock    *os.File
	id              string
	epoch           int64
	now             func() time.Time
}
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func OpenStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if cfg.Path == "" || !filepath.IsAbs(cfg.Path) || !validID(cfg.MasterID) {
		return nil, c.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0700); err != nil {
		return nil, fmt.Errorf("create master directory: %w", err)
	}
	existing, statErr := os.Lstat(cfg.Path)
	fresh := os.IsNotExist(statErr) || (statErr == nil && existing.Mode().IsRegular() && existing.Size() == 0)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open master file: %w", err)
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = privateInfo(cfg.Path, false); err != nil {
		return nil, err
	}
	databaseLock, err := lockFile(cfg.Path + ".lock")
	if err != nil {
		return nil, err
	}
	databaseOpened := false
	defer func() {
		if !databaseOpened {
			_ = databaseLock.Close()
		}
	}()
	epochPath := cfg.EpochPath
	if epochPath == "" {
		epochPath = cfg.Path + ".epoch"
	}
	if !filepath.IsAbs(epochPath) {
		return nil, c.ErrInvalid
	}
	lock, err := lockFile(epochPath + ".lock")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
		}
	}()
	if !fresh {
		if _, err = readLineage(epochPath); err != nil {
			if os.IsNotExist(err) {
				return nil, c.ErrEpoch
			}
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: cfg.Path}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open master database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, id: cfg.MasterID, now: time.Now, path: cfg.Path, epochPath: epochPath, lock: lock, databaseLock: databaseLock}
	if cfg.Now != nil {
		s.now = cfg.Now
	}
	if err = s.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	lineage, err := readLineage(epochPath)
	if os.IsNotExist(err) && s.epoch == 1 {
		err = immutableFile(epochPath, encode(epochLineage{MasterID: s.id, Epoch: s.epoch}))
		lineage = epochLineage{MasterID: s.id, Epoch: s.epoch}
	}
	if err != nil || lineage.MasterID != s.id || lineage.Epoch != s.epoch {
		_ = db.Close()
		if err != nil {
			return nil, err
		}
		return nil, c.ErrEpoch
	}
	if lineage.Principals != nil {
		ps, placements, e := registry(ctx, db)
		if e != nil || digest(ps) != digest(lineage.Principals) || digest(placements) != digest(lineage.Placements) {
			_ = db.Close()
			if e != nil {
				return nil, e
			}
			return nil, c.ErrConflict
		}
	}
	success = true
	databaseOpened = true
	return s, nil
}
func (s *Store) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema+recoverySchema+githubCacheSchema); err != nil {
		return fmt.Errorf("initialize master schema: %w", err)
	}
	if err := s.initializeGitHubCache(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO meta(key,value) VALUES('master_id',?),('epoch','1'),('quiescent','0')", s.id); err != nil {
		return err
	}
	var id, epoch string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='master_id'").Scan(&id); err != nil {
		return err
	}
	if id != s.id {
		return c.ErrConflict
	}
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='epoch'").Scan(&epoch); err != nil {
		return err
	}
	var err error
	s.epoch, err = strconv.ParseInt(epoch, 10, 64)
	return err
}
func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		e := s.lock.Close()
		s.lock = nil
		if err == nil {
			err = e
		}
	}
	if s.databaseLock != nil {
		e := s.databaseLock.Close()
		s.databaseLock = nil
		if err == nil {
			err = e
		}
	}
	return err
}
func (s *Store) Identity() (string, int64) { return s.id, s.epoch }
func validID(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, "\x00\r\n/\\")
}
func newID(prefix string) string { return prefix + "_" + rand.Text() }
func encode(v any) []byte        { b, _ := json.Marshal(v); return b }
func digest(v any) string        { h := sha256.Sum256(encode(v)); return hex.EncodeToString(h[:]) }
func normalized(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (s *Store) Configure(ctx context.Context, ps []c.Principal, placements []c.Placement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	quiescent := admission(ctx, tx) == c.ErrEpoch
	for _, p := range ps {
		if !validID(p.ID) || !validID(p.Alias) || (p.Kind != "owner" && p.Kind != "agent" && p.Kind != "operator" && p.Kind != "service") || len(p.Engagements) == 0 {
			return c.ErrInvalid
		}
		var shadow bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM principals WHERE id<>? AND (id=? OR alias=? OR alias=?))", p.ID, p.Alias, p.ID, p.Alias).Scan(&shadow); err != nil {
			return err
		}
		if shadow {
			return c.ErrConflict
		}
		var old []byte
		err = tx.QueryRowContext(ctx, "SELECT body FROM principals WHERE id=?", p.ID).Scan(&old)
		if err == nil {
			var previous c.Principal
			if quiescent && string(old) != string(encode(p)) {
				return c.ErrEpoch
			}
			if json.Unmarshal(old, &previous) != nil || previous.Kind != p.Kind || previous.GitHubBotID != p.GitHubBotID {
				return c.ErrConflict
			}
		} else if err != sql.ErrNoRows {
			return err
		} else if quiescent {
			return c.ErrEpoch
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO principals(id,kind,alias,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET alias=excluded.alias,body=excluded.body", p.ID, p.Kind, p.Alias, encode(p)); err != nil {
			return err
		}
	}
	for _, p := range placements {
		if !validID(p.AgentID) || !validID(p.HostID) || !validID(p.InstanceID) || p.Generation < 1 {
			return c.ErrInvalid
		}
		var old []byte
		err = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.AgentID).Scan(&old)
		if err == nil {
			if string(old) != string(encode(p)) {
				return c.ErrConflict
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		if quiescent {
			return c.ErrEpoch
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO placements(agent_id,body) VALUES(?,?)", p.AgentID, encode(p)); err != nil {
			return err
		}
	}
	if err = s.saveRegistry(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func principal(ctx context.Context, q queryer, id string) (c.Principal, error) {
	var raw []byte
	var p c.Principal
	err := q.QueryRowContext(ctx, "SELECT body FROM principals WHERE id=? OR alias=?", id, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return p, c.ErrForbidden
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, nil
}
func (s *Store) Principal(ctx context.Context, id string) (c.Principal, error) {
	return principal(ctx, s.db, id)
}
func authorize(ctx context.Context, q queryer, actor c.Principal, cap string) (c.Principal, error) {
	if cap != "read" {
		if err := admission(ctx, q); err != nil {
			return c.Principal{}, err
		}
	}
	p, err := principal(ctx, q, actor.ID)
	if err != nil {
		return p, err
	}
	if !slices.Contains(p.Capabilities, cap) {
		return p, c.ErrForbidden
	}
	return p, nil
}
func member(ctx context.Context, q queryer, actor c.Principal, threadID string) error {
	var engagement string
	err := q.QueryRowContext(ctx, "SELECT t.engagement FROM threads t JOIN members m ON m.thread_id=t.id WHERE t.id=? AND m.principal_id=?", threadID, actor.ID).Scan(&engagement)
	if err == sql.ErrNoRows {
		return c.ErrForbidden
	}
	if err != nil {
		return err
	}
	if !slices.Contains(actor.Engagements, engagement) {
		return c.ErrForbidden
	}
	return nil
}
func placement(ctx context.Context, q queryer, agent, instance string, generation int64) error {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", agent).Scan(&raw); err != nil {
		return c.ErrForbidden
	}
	var p c.Placement
	if json.Unmarshal(raw, &p) != nil || p.InstanceID != instance || p.Generation != generation {
		return c.ErrConflict
	}
	return nil
}

func request(ctx context.Context, q queryer, actor, id, kind string, input any, out any) (bool, error) {
	if !validID(id) {
		return false, c.ErrInvalid
	}
	var hash, k string
	var body []byte
	err := q.QueryRowContext(ctx, "SELECT digest,kind,result FROM requests WHERE actor=? AND request_id=?", actor, id).Scan(&hash, &k, &body)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != digest(input) || k != kind {
		return false, c.ErrConflict
	}
	if err = json.Unmarshal(body, out); err != nil {
		return false, err
	}
	return true, nil
}
func saveRequest(ctx context.Context, q queryer, actor, id, kind string, input, result any) error {
	_, err := q.ExecContext(ctx, "INSERT INTO requests(actor,request_id,digest,kind,result) VALUES(?,?,?,?,?)", actor, id, digest(input), kind, encode(result))
	return err
}
func event(ctx context.Context, q queryer, kind, thread, msg string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO events(kind,thread_id,message_id) VALUES(?,?,?)", kind, thread, msg)
	return err
}

func (s *Store) CreateThread(ctx context.Context, actor c.Principal, in c.CreateThread) (c.Thread, error) {
	var out c.Thread
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "manage")
	if err != nil || p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	in.Members = normalized(append(slices.Clone(in.Members), p.ID))
	if !validID(in.Engagement) || !slices.Contains(p.Engagements, in.Engagement) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 512 || len(in.Members) > 100 {
		return out, c.ErrInvalid
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "thread", in, &out); yes || e != nil {
		return out, e
	}
	for _, id := range in.Members {
		m, e := principal(ctx, tx, id)
		if e != nil || m.ID != id || !slices.Contains(m.Engagements, in.Engagement) {
			return out, c.ErrForbidden
		}
	}
	out = c.Thread{ID: newID("th"), Title: in.Title, Engagement: in.Engagement, Members: in.Members, CreatedAt: s.now().UTC()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO threads(id,engagement,body) VALUES(?,?,?)", out.ID, out.Engagement, encode(out)); err != nil {
		return out, err
	}
	for _, id := range out.Members {
		if _, err = tx.ExecContext(ctx, "INSERT INTO members(thread_id,principal_id) VALUES(?,?)", out.ID, id); err != nil {
			return out, err
		}
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "thread", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "thread", out.ID, ""); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) Threads(ctx context.Context, actor c.Principal, in c.ThreadQuery) (c.ThreadPage, error) {
	out := c.ThreadPage{Threads: []c.Thread{}}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if in.After != "" && !validID(in.After) {
		return out, c.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT t.body FROM threads t JOIN members m ON m.thread_id=t.id WHERE m.principal_id=? AND t.id>? ORDER BY t.id", p.ID, in.After)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	size := 0
	for rows.Next() {
		var raw []byte
		var thread c.Thread
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &thread); err != nil {
			return out, err
		}
		if !slices.Contains(p.Engagements, thread.Engagement) {
			continue
		}
		if len(out.Threads) == c.MaxPageRecords || size+len(raw) > c.MaxPageBytes-4096 {
			if len(out.Threads) == 0 {
				return out, c.ErrSize
			}
			out.Next = out.Threads[len(out.Threads)-1].ID
			break
		}
		out.Threads = append(out.Threads, thread)
		size += len(raw)
	}
	return out, rows.Err()
}
func (s *Store) Thread(ctx context.Context, actor c.Principal, id string) (c.Thread, error) {
	var out c.Thread
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if err = member(ctx, s.db, p, id); err != nil {
		return out, err
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", id).Scan(&raw); err != nil {
		return out, err
	}
	if len(raw) > c.MaxPageBytes-4096 {
		return out, c.ErrSize
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
