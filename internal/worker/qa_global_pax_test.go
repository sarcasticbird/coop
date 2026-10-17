package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
)

func qaPAXRecord(key, value string) string {
	suffix := " " + key + "=" + value + "\n"
	n := len(suffix) + 1
	for {
		record := fmt.Sprintf("%d%s", n, suffix)
		if len(record) == n {
			return record
		}
		n = len(record)
	}
}

// Write the physical global record so malformed PAX payloads are not normalized
// or rejected by tar.Writer before they can exercise the reader boundary.
func qaGlobalBlock(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeReg, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()[:buf.Len()-1024]
	raw[156] = tar.TypeXGlobalHeader
	copy(raw[148:156], "        ")
	sum := 0
	for _, b := range raw[:512] {
		sum += int(b)
	}
	copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return raw
}

func qaSourceTar(t *testing.T, entries ...qaBundleEntry) []byte {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(qaBundle(t, entries...)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return raw[:len(raw)-1024]
}

func qaGlobalArchive(t *testing.T, blocks ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	for _, block := range append(blocks, make([]byte, 1024)) {
		if _, err := z.Write(block); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSourceBundleGlobalCommentCanonical(t *testing.T) {
	file := qaBundleEntry{name: "generated/file.txt", content: "hello", mode: 0644}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("file.txt\x00420\x0048\x00\x002cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\x00")))
	for _, comment := range []string{strings.Repeat("a", 40), "different metadata"} {
		global := qaGlobalBlock(t, qaPAXRecord("comment", comment))
		contents := qaSourceTar(t, file)
		for _, blocks := range [][][]byte{{contents}, {global, contents}, {contents, global}, {global, contents, global}} {
			if got, err := QASourceDigest(bytes.NewReader(qaGlobalArchive(t, blocks...))); err != nil || got != want {
				t.Fatalf("comment changed canonical identity: %q, %v; want %s", got, err, want)
			}
		}
		second := qaSourceTar(t, qaBundleEntry{name: "generated/second", content: "second", mode: 0644})
		without, err := sourceBundleSHA(qaGlobalArchive(t, contents, second))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := sourceBundleSHA(qaGlobalArchive(t, contents, global, second)); err != nil || got != without {
			t.Fatalf("interleaved metadata changed identity: %q, %v", got, err)
		}
	}
	global := qaGlobalBlock(t, qaPAXRecord("comment", "commit"))
	for _, field := range []string{"path", "mode", "type", "link", "content"} {
		changed := file
		switch field {
		case "path":
			changed.name = "generated/other"
		case "mode":
			changed.mode = 0755
		case "type":
			changed.kind, changed.content = tar.TypeSymlink, ""
		case "link":
			changed.kind, changed.content, changed.link = tar.TypeSymlink, "", "elsewhere"
		case "content":
			changed.content = "changed"
		}
		if got, err := sourceBundleSHA(qaGlobalArchive(t, global, qaSourceTar(t, changed))); err != nil || got == want {
			t.Fatalf("changed %s lost source identity: %q, %v", field, got, err)
		}
	}
}

func TestSourceBundleGlobalMetadataRejectsUnsafeRecords(t *testing.T) {
	file := qaSourceTar(t, qaBundleEntry{name: "root/file", content: "source", mode: 0644})
	comment := qaPAXRecord("comment", "commit")
	for name, payload := range map[string]string{
		"empty": "", "empty comment": qaPAXRecord("comment", ""), "malformed syntax": "not a PAX record\n",
		"path": qaPAXRecord("path", "outside"), "linkpath": qaPAXRecord("linkpath", "outside"),
		"size": qaPAXRecord("size", "1"), "malformed size": qaPAXRecord("size", "bad"),
		"sparse": qaPAXRecord("GNU.sparse.size", "1"), "xattr": qaPAXRecord("SCHILY.xattr.user.test", "value"),
		"unknown": qaPAXRecord("unknown", "value"), "mixed path": comment + qaPAXRecord("path", "outside"),
		"mixed xattr":          comment + qaPAXRecord("SCHILY.xattr.user.test", "value"),
		"mixed malformed size": comment + qaPAXRecord("size", "bad"),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := sourceBundleSHA(qaGlobalArchive(t, qaGlobalBlock(t, payload), file)); err == nil || got != "" {
				t.Fatalf("unsafe metadata accepted: %q, %v", got, err)
			}
		})
	}
}

func TestSourceBundleGlobalCommentPreservesValidation(t *testing.T) {
	global := qaGlobalBlock(t, qaPAXRecord("comment", "commit"))
	valid := qaBundleEntry{name: "root/file", content: "source", mode: 0644}
	for name, entries := range map[string][]qaBundleEntry{
		"metadata only": nil, "traversal": {{name: "root/../outside"}}, "no root": {{name: "file"}},
		"mixed roots": {valid, {name: "other/file"}}, "duplicate": {valid, valid},
		"unsupported": {{name: "root/fifo", kind: tar.TypeFifo}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := sourceBundleSHA(qaGlobalArchive(t, global, qaSourceTar(t, entries...))); err == nil || got != "" {
				t.Fatalf("metadata bypassed entry validation: %q, %v", got, err)
			}
		})
	}
	raw := qaGlobalArchive(t, global, qaSourceTar(t, valid))
	corrupt := bytes.Clone(raw)
	corrupt[len(corrupt)-8] ^= 1
	for _, bad := range [][]byte{raw[:len(raw)-6], corrupt} {
		if got, err := sourceBundleSHA(bad); err == nil || got != "" {
			t.Fatalf("metadata bypassed gzip integrity: %q, %v", got, err)
		}
	}
	// Long paths require per-file PAX merging, which must remain unchanged.
	valid.name = "root/" + strings.Repeat("long/", 30) + "file"
	plain, err := sourceBundleSHA(qaBundle(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sourceBundleSHA(qaGlobalArchive(t, global, qaSourceTar(t, valid))); err != nil || got != plain {
		t.Fatalf("global metadata changed per-file PAX: %q, %v", got, err)
	}
}

func TestSourceBundleGlobalCommentHeaderBound(t *testing.T) {
	global := qaGlobalBlock(t, qaPAXRecord("comment", "commit"))
	blocks := make([][]byte, MaxConnection)
	for i := range blocks {
		blocks[i] = global
	}
	blocks[len(blocks)-1] = qaSourceTar(t, qaBundleEntry{name: "root/file", content: "source"})
	if _, err := sourceBundleSHA(qaGlobalArchive(t, blocks...)); err != nil {
		t.Fatalf("header boundary rejected: %v", err)
	}
	if got, err := sourceBundleSHA(qaGlobalArchive(t, append(blocks, global)...)); err == nil || got != "" {
		t.Fatalf("metadata exceeded header bound: %q, %v", got, err)
	}
}

func TestBindQAArtifactGlobalComment(t *testing.T) {
	head := strings.Repeat("a", 40)
	file := qaSourceTar(t, qaBundleEntry{name: "root/file.txt", content: "hello", mode: 0644})
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("file.txt\x00420\x0048\x00\x002cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\x00")))
	for _, unsafe := range []bool{false, true} {
		payload := qaPAXRecord("comment", "informational, not authorization")
		if unsafe {
			payload += qaPAXRecord("size", "bad")
		}
		raw := qaGlobalArchive(t, qaGlobalBlock(t, payload), file)
		g, _ := qaArtifactClient(t, "repos/code/repo/tarball/"+head, response("200 OK", string(raw)), nil)
		pr := PR{HeadSHA: head}
		g.readQAArtifact(context.Background(), Task{Repo: "code/repo", QA: QAPolicy{Required: true}}, &pr)
		if unsafe {
			if pr.QAArtifact != nil || pr.QAArtifactError != "invalid_QA_source_bundle" {
				t.Fatalf("unsafe archive cleared QA gate: %+v", pr)
			}
		} else if pr.QAArtifactError != "" || pr.QAArtifact == nil || pr.QAArtifact.SHA256 != want || pr.QAArtifact.HeadSHA != head || pr.QAArtifact.Revision != "source-bundle-v1" {
			t.Fatalf("native artifact differs from canonical identity: %+v", pr)
		}
	}
}
