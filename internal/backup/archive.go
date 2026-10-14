// Package backup creates checksummed cold archives while preserving sparse disks.
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sarcasticbird/coop/internal/workspace"
	"golang.org/x/sys/unix"
)

type Payload struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}
type Snapshot struct {
	Record         workspace.Record
	RuntimeVersion string
	Payloads       []Payload
	Metadata       json.RawMessage
}
type Storage interface {
	Snapshot(context.Context, workspace.Record) (Snapshot, error)
	Publish(context.Context, workspace.Record, string) error
}
type fileRecord struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	EncodedSize int64  `json:"encoded_size"`
	SHA256      string `json:"sha256"`
}
type Manifest struct {
	Version        int              `json:"version"`
	Record         workspace.Record `json:"record"`
	RuntimeVersion string           `json:"runtime_version"`
	Files          []fileRecord     `json:"files"`
	Metadata       json.RawMessage  `json:"metadata,omitempty"`
}

var payloadName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

const maxDiskSize = int64(1) << 40

type countingWriter struct {
	io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += int64(n)
	return n, err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func writeSparse(ctx context.Context, w io.Writer, f *os.File, size int64) error {
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() != size || size < 0 || size > maxDiskSize {
		return fmt.Errorf("invalid disk payload size")
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(size)); err != nil {
		return err
	}
	for offset := int64(0); offset < size; {
		data, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
		if err == unix.ENXIO {
			break
		}
		if err != nil {
			return fmt.Errorf("sparse disk discovery: %w", err)
		}
		hole, err := unix.Seek(int(f.Fd()), data, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		if hole > size {
			hole = size
		}
		if data < offset || hole <= data {
			return fmt.Errorf("invalid filesystem extent")
		}
		if err := binary.Write(w, binary.LittleEndian, uint64(data)); err != nil {
			return err
		}
		if err := binary.Write(w, binary.LittleEndian, uint64(hole-data)); err != nil {
			return err
		}
		if _, err := f.Seek(data, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(w, contextReader{ctx, f}, hole-data); err != nil {
			return err
		}
		offset = hole
	}
	if err := binary.Write(w, binary.LittleEndian, ^uint64(0)); err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("disk changed during backup")
	}
	return nil
}
func readSparse(ctx context.Context, r io.Reader, f *os.File, want int64) error {
	var size uint64
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return err
	}
	if size > uint64(maxDiskSize) || int64(size) != want {
		return fmt.Errorf("invalid sparse disk size")
	}
	// Establish the sparse logical length before writes. Growing a large
	// APFS file through thousands of separated extents can speculatively
	// allocate the intervening gaps, dramatically inflating restored disks.
	if err := f.Truncate(want); err != nil {
		return err
	}
	var previous uint64
	for {
		var offset, length uint64
		if err := binary.Read(r, binary.LittleEndian, &offset); err != nil {
			return err
		}
		if offset == ^uint64(0) {
			break
		}
		if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
			return err
		}
		if offset < previous || offset > size || length == 0 || length > size-offset {
			return fmt.Errorf("invalid sparse disk extent")
		}
		if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(f, contextReader{ctx, r}, int64(length)); err != nil {
			return err
		}
		previous = offset + length
	}
	var extra [1]byte
	if n, err := r.Read(extra[:]); n != 0 || err != io.EOF {
		return fmt.Errorf("trailing sparse disk data")
	}
	return f.Truncate(want)
}

func Create(ctx context.Context, backend Storage, record workspace.Record, destination string) (retErr error) {
	if err := workspace.ValidateRecord(record); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("backup destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	snapshot, err := backend.Snapshot(ctx, record)
	if err != nil {
		return err
	}
	if len(snapshot.Payloads) == 0 || len(snapshot.Payloads) > 256 {
		return fmt.Errorf("invalid backup payload count")
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".coop-backup-*")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	zw := zip.NewWriter(tmp)
	manifest := Manifest{Version: 1, Record: snapshot.Record, RuntimeVersion: snapshot.RuntimeVersion, Metadata: snapshot.Metadata}
	seen := map[string]bool{}
	for _, p := range snapshot.Payloads {
		if !payloadName.MatchString(p.Name) || p.Name == "manifest.json" || seen[p.Name] {
			return fmt.Errorf("invalid or duplicate payload name %q", p.Name)
		}
		seen[p.Name] = true
		input, err := p.Open()
		if err != nil {
			return err
		}
		f, ok := input.(*os.File)
		if !ok {
			_ = input.Close()
			return fmt.Errorf("disk payload is not a regular file")
		}
		header := &zip.FileHeader{Name: p.Name, Method: zip.Deflate}
		header.SetMode(0600)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			_ = input.Close()
			return err
		}
		hash := sha256.New()
		count := &countingWriter{Writer: io.MultiWriter(writer, hash)}
		writeErr := writeSparse(ctx, count, f, p.Size)
		closeErr := input.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		manifest.Files = append(manifest.Files, fileRecord{Name: p.Name, Size: p.Size, EncodedSize: count.n, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	if _, err := backend.Snapshot(ctx, record); err != nil {
		return fmt.Errorf("workspace changed during backup: %w", err)
	}
	writer, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(writer).Encode(manifest); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link publishes the completed archive atomically without replacing a file.
	return os.Link(tmp.Name(), destination)
}

func ReadManifest(archive string) (Manifest, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = zr.Close() }()
	return readManifest(zr.File)
}
func readManifest(files []*zip.File) (Manifest, error) {
	if len(files) < 2 || len(files) > 257 {
		return Manifest{}, fmt.Errorf("invalid archive entry count")
	}
	seen := map[string]bool{}
	var mf *zip.File
	for _, f := range files {
		if !payloadName.MatchString(f.Name) || seen[f.Name] || !f.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("unsafe archive entry %q", f.Name)
		}
		seen[f.Name] = true
		if f.Name == "manifest.json" {
			mf = f
		}
	}
	if mf == nil || mf.UncompressedSize64 > 1<<20 {
		return Manifest{}, fmt.Errorf("missing or oversized archive manifest")
	}
	r, err := mf.Open()
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = r.Close() }()
	var m Manifest
	decoder := json.NewDecoder(io.LimitReader(r, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return m, fmt.Errorf("trailing manifest data")
	}
	if m.Version != 1 || len(m.Files) != len(files)-1 {
		return m, fmt.Errorf("unsupported backup version or payload count")
	}
	if err := workspace.ValidateRecord(m.Record); err != nil {
		return m, err
	}
	listed := map[string]bool{}
	for _, f := range m.Files {
		if !seen[f.Name] || f.Name == "manifest.json" || listed[f.Name] || f.Size < 0 || f.Size > maxDiskSize || f.EncodedSize < 16 || f.EncodedSize > maxDiskSize {
			return m, fmt.Errorf("invalid payload manifest")
		}
		if digest, err := hex.DecodeString(f.SHA256); err != nil || len(digest) != sha256.Size {
			return m, fmt.Errorf("invalid payload checksum")
		}
		listed[f.Name] = true
	}
	return m, nil
}

func Restore(ctx context.Context, backend Storage, archive, identity string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	m, err := readManifest(zr.File)
	if err != nil {
		return err
	}
	if validator, ok := backend.(interface{ ValidateArchive(Manifest) error }); ok {
		if err := validator.ValidateArchive(m); err != nil {
			return err
		}
	}
	target := m.Record
	target.Identity = identity
	target.RuntimeName = identity
	if target.Mode == workspace.Agent {
		target.RuntimeName = "coop-agent-" + identity
	}
	if err := workspace.ValidateRecord(target); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "coop-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if len(m.Metadata) > 0 {
		if err := os.WriteFile(filepath.Join(dir, ".runtime-metadata.json"), m.Metadata, 0600); err != nil {
			return err
		}
	}
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	for _, file := range m.Files {
		entry := byName[file.Name]
		if entry.UncompressedSize64 != uint64(file.EncodedSize) {
			return fmt.Errorf("payload size mismatch")
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		limited := io.LimitReader(reader, file.EncodedSize+1)
		out, err := os.OpenFile(filepath.Join(dir, file.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = reader.Close()
			return err
		}
		err = readSparse(ctx, io.TeeReader(limited, hash), out, file.Size)
		closeErr := reader.Close()
		syncErr := out.Sync()
		outErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if outErr != nil {
			return outErr
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), file.SHA256) {
			return fmt.Errorf("payload checksum mismatch for %s", file.Name)
		}
	}
	return backend.Publish(ctx, target, dir)
}
