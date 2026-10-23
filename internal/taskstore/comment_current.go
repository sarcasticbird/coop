package taskstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
	_ "modernc.org/sqlite"
)

// Kata v0.19.0 has no exact-comment GET: show --thread includes descendant
// bodies and backlinks. Read one current row from its canonical local SQLite
// database, after native authorization. This private adapter never opens a
// writable connection, applies a migration, or copies discussion history.
// Schema/path contracts: kenn-io/kata v0.19.0 internal/config/dsn_resolve.go,
// internal/db/schema_version.go, internal/db/sqlitestore/schema.sql.
func (k *kata) currentComment(ctx context.Context, cl *http.Client, token string, pid int64, issue, uid string) (Comment, error) {
	var out Comment
	var instance struct {
		UID     string `json:"instance_uid"`
		Version string `json:"version"`
		Schema  int    `json:"schema_version"`
	}
	if err := nativeCall(ctx, cl, token, "GET", "/api/v1/instance", "", nil, &instance); err != nil {
		return out, err
	}
	if instance.UID != k.cfg.InstanceUID {
		return out, c.ErrConflict
	}
	if strings.TrimPrefix(instance.Version, "v") != "0.19.0" || instance.Schema != 34 {
		return out, c.ErrUnsupported
	}
	// Keep the daemon's issue ACL check. The local file never grants issue access.
	if _, err := k.InspectIssue(ctx, issue); err != nil {
		return out, err
	}
	path, err := k.commentDatabasePath()
	if err != nil {
		return out, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(5000)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return out, fmt.Errorf("open native comment reader: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var schema, instanceUID string
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT substr(value,1,128) FROM meta WHERE key='schema_version'),(SELECT substr(value,1,128) FROM meta WHERE key='instance_uid')`).Scan(&schema, &instanceUID); err != nil {
		return out, c.ErrUnsupported
	}
	if schema != "34" {
		return out, c.ErrUnsupported
	}
	if instanceUID != k.cfg.InstanceUID {
		return out, c.ErrConflict
	}
	// CASE bounds every projected variable-length value before it crosses the
	// database driver. UID indexes select one row; no body/history scan is needed.
	var body, author, reply, created, edited sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT
	 CASE WHEN length(CAST(c.body AS BLOB))<=? THEN c.body END,
	 CASE WHEN length(CAST(c.author AS BLOB))<=1024 THEN c.author END,
	 CASE WHEN length(coalesce(c.reply_to_uid,''))<=26 THEN coalesce(c.reply_to_uid,'') END,
	 CASE WHEN length(CAST(c.created_at AS TEXT))<=64 THEN CAST(c.created_at AS TEXT) END,
	 CASE WHEN length(coalesce(CAST(c.edited_at AS TEXT),''))<=64 THEN coalesce(CAST(c.edited_at AS TEXT),'') END
	 FROM comments c JOIN issues i ON i.id=c.issue_id JOIN projects p ON p.id=i.project_id
	 WHERE c.uid=? AND i.uid=? AND p.id=? AND p.uid=? AND p.name=? AND i.deleted_at IS NULL AND p.deleted_at IS NULL`, c.MaxText, uid, issue, pid, k.cfg.ProjectUID, k.cfg.ProjectName).Scan(&body, &author, &reply, &created, &edited)
	if errors.Is(err, sql.ErrNoRows) {
		return out, c.ErrNotFound
	}
	if err != nil {
		return out, fmt.Errorf("read native current comment: %w", err)
	}
	if !body.Valid || !author.Valid || !reply.Valid || !created.Valid || !edited.Valid {
		return out, c.ErrSize
	}
	if strings.TrimSpace(body.String) == "" || author.String == "" || (reply.String != "" && !validUID(reply.String)) {
		return out, c.ErrInvalid
	}
	out = Comment{UID: uid, IssueUID: issue, Body: body.String, Author: author.String, ReplyToUID: reply.String}
	if out.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String); err != nil {
		return Comment{}, c.ErrInvalid
	}
	if edited.String != "" {
		at, e := time.Parse(time.RFC3339Nano, edited.String)
		if e != nil {
			return Comment{}, c.ErrInvalid
		}
		out.EditedAt = &at
		out.Revision = at.UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return Comment{}, err
	}
	if len(raw) > c.MaxJSON {
		return Comment{}, c.ErrSize
	}
	return out, nil
}

func privateNativePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 || info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return c.ErrForbidden
	}
	return nil
}

func (k *kata) commentDatabasePath() (string, error) {
	path := filepath.Join(k.cfg.Home, "config.toml")
	if err := privateNativePath(k.cfg.Home, true); err != nil {
		return "", err
	}
	if err := privateNativePath(path, false); err != nil {
		return "", err
	}
	var profile struct {
		Storage struct {
			DSN string `toml:"dsn"`
		} `toml:"storage"`
	}
	if _, err := toml.DecodeFile(path, &profile); err != nil {
		return "", c.ErrInvalid
	}
	// Child CLI calls strip KATA_* variables, so only this private profile's
	// storage.dsn and Kata's documented default participate in path resolution.
	dsn := strings.TrimSpace(profile.Storage.DSN)
	if dsn == "" {
		dsn = filepath.Join(k.cfg.Home, "kata.db")
	}
	path = strings.TrimPrefix(dsn, "sqlite://")
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?\x00\r\n") {
		return "", c.ErrUnsupported
	}
	path = filepath.Clean(path)
	if err := privateNativePath(filepath.Dir(path), true); err != nil {
		return "", err
	}
	if err := privateNativePath(path, false); err != nil {
		return "", err
	}
	return path, nil
}
