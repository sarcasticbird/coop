package birdmemory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type memoryClient struct {
	entries      map[string]c.Memory
	writes       int
	loseResponse bool
}

func (m *memoryClient) ReadMemory(_ context.Context, in c.MemoryQuery) (c.Memory, error) {
	if v, ok := m.entries[in.Key]; ok {
		return v, nil
	}
	return c.Memory{}, c.ErrNotFound
}

func (m *memoryClient) WriteMemory(_ context.Context, in c.MemoryWrite) (c.Memory, error) {
	m.writes++
	v := c.Memory{MemorySummary: c.MemorySummary{AgentID: in.AgentID, Key: in.Key, Source: in.Source, Revision: 1}, Body: in.Body}
	m.entries[in.Key] = v
	if m.loseResponse {
		m.loseResponse = false
		return c.Memory{}, c.ErrUnavailable
	}
	return v, nil
}

func TestImportMarkdownIsRepeatableAndNonDestructive(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "projects"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"MEMORY.md": "index", "projects/coop.md": "use SQLite", "empty.md": "", "ignored.txt": "not memory"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := &memoryClient{entries: map[string]c.Memory{}, loseResponse: true}
	if _, err := ImportMarkdown(t.Context(), client, "rook", dir); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	out, err := ImportMarkdown(t.Context(), client, "rook", dir)
	if err != nil || out.Unchanged != 1 || len(out.Imported) != 1 || client.writes != 2 {
		t.Fatal(out, err, client.writes)
	}
	out, err = ImportMarkdown(t.Context(), client, "rook", dir)
	if err != nil || out.Unchanged != 2 || len(out.Imported) != 0 || client.writes != 2 {
		t.Fatal(out, err)
	}
	if client.entries["projects/coop.md"].Source != "markdown:projects/coop.md" {
		t.Fatal("missing provenance")
	}
	if raw, e := os.ReadFile(filepath.Join(dir, "MEMORY.md")); e != nil || string(raw) != "index" {
		t.Fatal("source changed", e)
	}
	old := client.entries["MEMORY.md"]
	old.Body = "newer knowledge"
	client.entries["MEMORY.md"] = old
	if _, err = ImportMarkdown(t.Context(), client, "rook", dir); !errors.Is(err, c.ErrConflict) || client.writes != 2 {
		t.Fatal("import overwrote newer knowledge", err)
	}
}

func TestImportPreflightsBeforeWriting(t *testing.T) {
	for _, kind := range []string{"symlink", "oversize", "invalid-utf8", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("valid"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "z.md")); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(filepath.Join(dir, "z.md"), []byte(strings.Repeat("x", c.MaxText+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				if err := os.WriteFile(filepath.Join(dir, "z.md"), []byte{255}, 0600); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(filepath.Join(dir, "z.md"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := &memoryClient{entries: map[string]c.Memory{}}
			if _, err := ImportMarkdown(t.Context(), client, "", dir); err == nil || client.writes != 0 {
				t.Fatal("preflight performed a partial write", err)
			}
		})
	}
}

func TestImportSingleMarkdownFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "MEMORY.md")
	if err := os.WriteFile(file, []byte("knowledge"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &memoryClient{entries: map[string]c.Memory{}}
	out, err := ImportMarkdown(t.Context(), client, "", file)
	if err != nil || len(out.Imported) != 1 || out.Imported[0].Key != "MEMORY.md" {
		t.Fatal(out, err)
	}
}
