package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/sarcasticbird/coop/internal/worker"
	"golang.org/x/sys/unix"
)

func qaSourceArchive(t *testing.T, root, content string, mode int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	w := tar.NewWriter(z)
	if err := w.WriteHeader(&tar.Header{Name: root + "/file.txt", Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestQASourceDigestOffline(t *testing.T) {
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) {
		t.Fatal("offline command initialized clients")
		return nil, nil, nil
	}
	t.Setenv("COOP_GITHUB_OWNER", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	// Independent source-bundle-v1 vector for a regular 0644 file containing hello.
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte("file.txt\x00420\x0048\x00\x002cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\x00")))
	for _, root := range []string{"generated-root-a", "generated-root-b"} {
		path := filepath.Join(t.TempDir(), "source.tar.gz")
		if err := os.WriteFile(path, qaSourceArchive(t, root, "hello", 0644), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run([]string{"qa-source-digest", "--archive", path}, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("digest = %q, want %q", out.String(), want)
		}
	}
	for _, raw := range [][]byte{qaSourceArchive(t, "root", "changed", 0644), qaSourceArchive(t, "root", "hello", 0755)} {
		digest, err := worker.QASourceDigest(bytes.NewReader(raw))
		if err != nil || digest+"\n" == want {
			t.Fatalf("changed content/mode did not change digest: %q, %v", digest, err)
		}
	}
}

func TestQASourceDigestRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.tar.gz")
	raw := qaSourceArchive(t, "root", "hello", 0644)
	if err := os.WriteFile(valid, raw, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "oversized")
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(worker.MaxAPIPage + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"missing flag":  nil,
		"missing value": {"--archive"},
		"relative":      {"--archive", "source.tar.gz"},
		"config":        {"--archive", valid, "--config", "/missing/config.json"},
		"positional":    {"--archive", valid, "extra"},
		"missing file":  {"--archive", filepath.Join(dir, "missing")},
		"symlink":       {"--archive", link},
		"directory":     {"--archive", dir},
		"fifo":          {"--archive", fifo},
		"oversized":     {"--archive", oversized},
	}
	corrupt := bytes.Clone(raw)
	corrupt[len(corrupt)-8] ^= 1
	for name, content := range map[string][]byte{
		"empty":     nil,
		"not gzip":  []byte("not an archive"),
		"truncated": raw[:len(raw)-1],
		"checksum":  corrupt,
		"traversal": qaSourceArchive(t, "root/..", "hello", 0644),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		cases[name] = []string{"--archive", path}
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(append([]string{"qa-source-digest"}, args...), &out); err == nil {
				t.Fatal("invalid input accepted")
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed a digest: %q", out.String())
			}
		})
	}
}

func TestQASourceDigestReaderBoundsAndErrors(t *testing.T) {
	r := &io.LimitedReader{R: strings.NewReader(strings.Repeat("x", worker.MaxAPIPage+10)), N: worker.MaxAPIPage + 10}
	if digest, err := worker.QASourceDigest(r); err == nil || digest != "" || r.N != 9 {
		t.Fatalf("compressed limit not enforced: digest=%q err=%v remaining=%d", digest, err, r.N)
	}
	want := errors.New("read failure")
	if digest, err := worker.QASourceDigest(iotest.ErrReader(want)); !errors.Is(err, want) || digest != "" {
		t.Fatalf("read error lost: digest=%q err=%v", digest, err)
	}
	path := filepath.Join(t.TempDir(), "source.tar.gz")
	if err := os.WriteFile(path, qaSourceArchive(t, "root", "hello", 0644), 0600); err != nil {
		t.Fatal(err)
	}
	if err := qaSourceDigest([]string{"--archive", path}, qaSourceErrorWriter{want}); !errors.Is(err, want) {
		t.Fatalf("output error lost: %v", err)
	}
}

type qaSourceErrorWriter struct{ err error }

func (w qaSourceErrorWriter) Write([]byte) (int, error) { return 0, w.err }
