// Package birdmemory imports working knowledge into Coop's durable memory API.
package birdmemory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type Client interface {
	ReadMemory(context.Context, c.MemoryQuery) (c.Memory, error)
	WriteMemory(context.Context, c.MemoryWrite) (c.Memory, error)
}

type ImportResult struct {
	Imported  []c.MemorySummary `json:"imported"`
	Unchanged int               `json:"unchanged"`
}

// ImportMarkdown preflights all files, then creates missing keys. It never
// overwrites existing knowledge or removes the source. Partial imports can be
// rerun safely, including after a committed write with a lost response.
func ImportMarkdown(ctx context.Context, client Client, agent, root string) (ImportResult, error) {
	out := ImportResult{Imported: []c.MemorySummary{}}
	info, err := os.Lstat(root)
	if err != nil {
		return out, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return out, fmt.Errorf("memory import refuses symlinks: %w", c.ErrInvalid)
	}
	var entries []c.MemoryWrite
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory import refuses symlinks: %w", c.ErrInvalid)
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		if len(entries) >= c.MaxMemoryEntries {
			return c.ErrCapacity
		}
		key := filepath.Base(name)
		if info.IsDir() {
			key, err = filepath.Rel(root, name)
			if err != nil {
				return err
			}
		}
		key = filepath.ToSlash(key)
		fileInfo, e := entry.Info()
		if e != nil {
			return e
		}
		if !fileInfo.Mode().IsRegular() {
			return c.ErrInvalid
		}
		if fileInfo.Size() > c.MaxText {
			return c.ErrSize
		}
		f, e := os.Open(name)
		if e != nil {
			return e
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(fileInfo, opened) {
			_ = f.Close()
			return c.ErrConflict
		}
		body, e := io.ReadAll(io.LimitReader(f, c.MaxText+1))
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return errors.Join(e, closeErr)
		}
		in := c.MemoryWrite{MemoryQuery: c.MemoryQuery{AgentID: agent, Key: key}, Body: string(body), Source: "markdown:" + key}
		// Empty Markdown carries no knowledge and is common in memory templates.
		if strings.TrimSpace(in.Body) == "" {
			return nil
		}
		if e = c.ValidateMemoryWrite(in); e != nil {
			return fmt.Errorf("memory %s: %w", key, e)
		}
		entries = append(entries, in)
		return ctx.Err()
	})
	if err != nil {
		return out, err
	}
	for _, in := range entries {
		old, e := client.ReadMemory(ctx, in.MemoryQuery)
		if e == nil {
			if old.Body != in.Body {
				return out, fmt.Errorf("memory %s already exists with different content: %w", in.Key, c.ErrConflict)
			}
			out.Unchanged++
			continue
		}
		if !errors.Is(e, c.ErrNotFound) {
			return out, e
		}
		memory, e := client.WriteMemory(ctx, in)
		if e != nil {
			return out, fmt.Errorf("memory %s: %w", in.Key, e)
		}
		out.Imported = append(out.Imported, memory.MemorySummary)
	}
	return out, nil
}
