package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundedCaptureDrains(t *testing.T) {
	b := NewBoundedCapture(4)
	n, e := b.Write([]byte("abcdefgh"))
	if e != nil || n != 8 || string(b.Bytes()) != "abcd" || b.Total() != 8 || b.Dropped() != 4 {
		t.Fatal("capture did not drain and count")
	}
}
func TestMachineMetadataBoundedInjected(t *testing.T) {
	m := AppleMachine{Command: func(_ context.Context, _ []string, _ io.Reader, out, err io.Writer) error {
		_, e := io.WriteString(out, strings.Repeat("x", 2<<20))
		return e
	}}
	if _, e := m.Output(context.Background(), "inspect"); e == nil {
		t.Fatal("oversized metadata accepted")
	}
}
func TestExecStreamsExactArgumentsAndInput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\nfor arg do printf '%s\\n' \"$arg\"; done\ncat\nprintf err >&2\nexit 17\n"
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	a := &Apple{Bin: bin}
	out, errout := NewBoundedCapture(1024), NewBoundedCapture(1024)
	e := a.ExecStreams(context.Background(), "coop-test", "/guest", []string{"cmd", "two words", "$HOME;nope"}, strings.NewReader("input"), out, errout)
	if e == nil || string(out.Bytes()) != "exec\n-i\n--workdir\n/guest\ncoop-test\ncmd\ntwo words\n$HOME;nope\ninput" || string(errout.Bytes()) != "err" {
		t.Fatalf("stream boundary changed: %v %q %q", e, out.Bytes(), errout.Bytes())
	}
}

func TestMachineDiagnosticCaptureBoundedSubprocess(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=3 2>/dev/null >&2\nexit 1\n"
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	m := AppleMachine{Bin: bin}
	e := m.command(context.Background(), []string{"exec"}, nil, io.Discard, io.Discard)
	if e == nil || len(e.Error()) > 66<<10 {
		t.Fatal("diagnostic capture is unbounded")
	}
}

func TestMachineCancellationDoesNotWaitForInheritedPipes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := (AppleMachine{Bin: bin}).command(ctx, nil, nil, io.Discard, io.Discard)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("cancel waited for an inherited output pipe")
	}
}
func TestAppleMetadataUsesOperationContext(t *testing.T) {
	a := NewApple().WithContext(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a = a.WithContext(ctx)
	if _, err := a.output("--version"); err == nil {
		t.Fatal("cancelled metadata request executed")
	}
}
