package seed

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type earlyExitRuntime struct {
	*runtime.Mock
	err error
}

func (r *earlyExitRuntime) ExecContext(context.Context, string, []string, io.Reader) error {
	return r.err
}

const (
	hostHome  = "/host/home"
	guestHome = "/Users/u"
)

func writeTemp(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func installTarRequiringCopyfileDisabled(t *testing.T) {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := writeTemp(t, "empty.tar", archive.String(), 0o600)
	binDir := t.TempDir()
	fakeTar := filepath.Join(binDir, "tar")
	script := `#!/bin/sh
if [ "$COPYFILE_DISABLE" != "1" ]; then
  echo "COPYFILE_DISABLE is not enabled" >&2
  exit 42
fi
out=-
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-chf" ]; then out="$2"; break; fi
  shift
done
if [ "$out" = "-" ]; then
  cat "$COOP_TEST_ARCHIVE"
else
  cat "$COOP_TEST_ARCHIVE" > "$out"
fi
`
	if err := os.WriteFile(fakeTar, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_TEST_ARCHIVE", archivePath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldGOOS := goos
	goos = "darwin"
	t.Cleanup(func() { goos = oldGOOS })
}

func TestDarwinDirectoryArchiveDisablesCopyfileMetadata(t *testing.T) {
	installTarRequiringCopyfileDisabled(t)
	src := t.TempDir()

	archive, err := createTarArchive(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinDirectoryOverlayDisablesCopyfileMetadata(t *testing.T) {
	installTarRequiringCopyfileDisabled(t)
	cmd, pipe, err := startTar(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, pipe); err != nil {
		t.Fatal(err)
	}
	if err := finishTar(cmd, pipe, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAlwaysSeedsAndPreservesMode(t *testing.T) {
	m := runtime.NewMock()
	src := writeTemp(t, "wt", "#!/bin/sh\n", 0o755)

	seeds := []config.Seed{{Src: src, Dest: "/usr/local/bin/wt", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if m.GuestFiles["/usr/local/bin/wt"] != "#!/bin/sh\n" {
		t.Errorf("content missing:\n%s", m.ExecString())
	}
	if m.GuestModes["/usr/local/bin/wt"] != "755" {
		t.Errorf("mode not preserved: %v", m.GuestModes)
	}
	// the write must be atomic (exclusive random temp + no-target-dir
	// rename) and refuse symlink/non-regular destinations
	joined := m.ExecString()
	for _, marker := range []string{"mktemp", "mv -T", `-L "$d"`, `! -f "$d"`} {
		if !strings.Contains(joined, marker) {
			t.Errorf("hardened write missing %q:\n%s", marker, joined)
		}
	}
}

func TestIfAbsentFailsClosedOnCheckError(t *testing.T) {
	m := runtime.NewMock()
	m.ExistsErr = errCheck
	src := writeTemp(t, "auth.json", `{"t":"x"}`, 0o600)

	seeds := []config.Seed{{Src: src, Dest: "~/auth.json", Policy: config.PolicyIfAbsent}}
	err := Apply(m, "c", hostHome, guestHome, seeds)
	if err == nil {
		t.Fatal("inconclusive existence check must fail closed")
	}
	if _, wrote := m.GuestFiles["/Users/u/auth.json"]; wrote {
		t.Errorf("wrote despite inconclusive check")
	}
}

var errCheck = os.ErrPermission

func TestIfAbsentSkipsExistingGuestFile(t *testing.T) {
	m := runtime.NewMock()
	m.GuestFiles["/Users/u/.claude/settings.json"] = `{"model":"guest-edited"}`
	src := writeTemp(t, "settings.json", `{"model":"host"}`, 0o644)

	seeds := []config.Seed{{Src: src, Dest: "~/.claude/settings.json", Policy: config.PolicyIfAbsent}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.claude/settings.json"]; got != `{"model":"guest-edited"}` {
		t.Errorf("if-absent clobbered guest file: %q", got)
	}
}

func TestIfAbsentSnapshotsDirectory(t *testing.T) {
	m := runtime.NewMock()
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(src, "nested", "rule.md"),
		[]byte("# rule"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	err := Apply(m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 1 {
		t.Fatalf("directory snapshot calls = %#v", m.ExecCalls)
	}
	call := m.ExecCalls[0]
	script := strings.Join(call.Argv, " ")
	for _, marker := range []string{
		"mktemp -d",
		"mv -T -n",
		`-L "$d"`,
		`trap 'rm -rf "$t"' EXIT`,
		`[ -d "$t" ]`,
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("directory snapshot missing %q: %s", marker, script)
		}
	}
	if !strings.Contains(call.Stdin, "rule.md") {
		t.Fatalf("tar stream omitted nested rule, bytes=%d", len(call.Stdin))
	}
}

func TestIfAbsentDirectoryCleansTrailingSlashDestination(t *testing.T) {
	m := runtime.NewMock()
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	cleanDest := filepath.Join(parent, "tools")

	err := Apply(m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: cleanDest + string(filepath.Separator), Policy: config.PolicyIfAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 1 {
		t.Fatalf("directory snapshot calls = %#v", m.ExecCalls)
	}
	call := m.ExecCalls[0]
	if got := call.Argv[4]; got != cleanDest {
		t.Errorf("destination = %q, want %q", got, cleanDest)
	}
	if got := call.Argv[5]; got != parent {
		t.Errorf("destination parent = %q, want %q", got, parent)
	}
}

func TestIfAbsentSkipsExistingGuestDirectory(t *testing.T) {
	m := runtime.NewMock()
	m.GuestDirs["/Users/u/.codex/rules"] = true
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	err := Apply(m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 0 {
		t.Fatalf("existing directory was touched: %s", m.ExecString())
	}
}

func TestIfAbsentDirectoryFailureIsReturned(t *testing.T) {
	m := runtime.NewMock()
	m.ExecErrors = []error{errors.New("extract failed")}
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	err := Apply(m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
	}})
	if err == nil || !strings.Contains(err.Error(), "extract failed") {
		t.Fatalf("directory snapshot failure = %v", err)
	}
}

func TestIfAbsentDirectoryDoesNotPublishWhenHostArchiveFails(t *testing.T) {
	var partial bytes.Buffer
	tw := tar.NewWriter(&partial)
	content := []byte("partial")
	if err := tw.WriteHeader(&tar.Header{
		Name: "partial.txt",
		Mode: 0o600,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "partial.tar")
	if err := os.WriteFile(archive, partial.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeTar := filepath.Join(binDir, "tar")
	script := `#!/bin/sh
out=-
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-chf" ]; then out="$2"; break; fi
  shift
done
if [ "$out" = "-" ]; then
  cat "$COOP_TEST_ARCHIVE"
else
  cat "$COOP_TEST_ARCHIVE" > "$out"
fi
echo "source changed while reading" >&2
exit 1
`
	if err := os.WriteFile(fakeTar, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_TEST_ARCHIVE", archive)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := runtime.NewMock()
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Apply(m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
	}})
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("host archive failure = %v", err)
	}
	for _, marker := range []string{"archive directory", src, "source changed while reading"} {
		if !strings.Contains(err.Error(), marker) {
			t.Errorf("host archive failure missing %q: %v", marker, err)
		}
	}
	if len(m.ExecCalls) != 0 {
		t.Fatalf("published archive before host tar succeeded: %s", m.ExecString())
	}
}

func TestIfAbsentDirectoryGuestFailureDoesNotBlockTar(t *testing.T) {
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(src, "large.bin"),
		bytes.Repeat([]byte("x"), 4<<20),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	rt := &earlyExitRuntime{
		Mock: runtime.NewMock(),
		err:  errors.New("guest failed before reading archive"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- ApplyContext(ctx, rt, "c", hostHome, guestHome, []config.Seed{{
			Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
		}})
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "guest failed before reading archive") {
			t.Fatalf("directory snapshot failure = %v", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-result
		t.Fatal("directory snapshot blocked waiting for an unread tar stream")
	}
}

func TestIfAbsentDirectoryHonorsCancellation(t *testing.T) {
	m := runtime.NewMock()
	src := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.ExecContextFunc = func(
		callCtx context.Context,
		_ int,
		_ runtime.ExecCall,
	) error {
		cancel()
		<-callCtx.Done()
		return context.Cause(callCtx)
	}

	err := ApplyContext(ctx, m, "c", hostHome, guestHome, []config.Seed{{
		Src: src, Dest: "~/.codex/rules", Policy: config.PolicyIfAbsent,
	}})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("directory snapshot cancellation = %v", err)
	}
}

func TestMissingSourceIsSkippedSilently(t *testing.T) {
	m := runtime.NewMock()
	seeds := []config.Seed{{Src: "/nonexistent", Dest: "/x", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 0 {
		t.Errorf("missing source should not exec:\n%s", m.ExecString())
	}
}

func TestOverlayPipesTar(t *testing.T) {
	m := runtime.NewMock()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "helper", "SKILL.md"), []byte("# helper"), 0o644); err != nil {
		t.Fatal(err)
	}

	seeds := []config.Seed{{Src: filepath.Join(dir, "skills"), Dest: "~/.claude/skills", Policy: config.PolicyOverlay}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}

	var tarCall *runtime.ExecCall
	for i := range m.ExecCalls {
		if strings.HasPrefix(strings.Join(m.ExecCalls[i].Argv, " "), "tar -xf") {
			tarCall = &m.ExecCalls[i]
		}
	}
	if tarCall == nil {
		t.Fatalf("no tar extract call:\n%s", m.ExecString())
	}
	if !strings.Contains(strings.Join(tarCall.Argv, " "), "/Users/u/.claude/skills") {
		t.Errorf("wrong extract dest: %v", tarCall.Argv)
	}
	if !strings.Contains(tarCall.Stdin, "SKILL.md") {
		t.Errorf("tar stream missing content (len=%d)", len(tarCall.Stdin))
	}
}

func TestHomeExpansionUsesRespectiveSides(t *testing.T) {
	m := runtime.NewMock()
	// src ~ expands against HOST home, dest ~ against GUEST home
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "f")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seeds := []config.Seed{{Src: src, Dest: "~/f", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GuestFiles["/Users/u/f"]; !ok {
		t.Errorf("dest not expanded against guest home:\n%s", m.ExecString())
	}
}
