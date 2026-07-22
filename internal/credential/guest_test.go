package credential

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type execCall struct {
	name  string
	argv  []string
	stdin []byte
}

type fakeExecutor struct {
	calls []execCall
	err   error
}

func (f *fakeExecutor) ExecContext(_ context.Context, name string, argv []string, stdin io.Reader) error {
	var input []byte
	if stdin != nil {
		input, _ = io.ReadAll(stdin)
	}
	f.calls = append(f.calls, execCall{name: name, argv: slices.Clone(argv), stdin: input})
	return f.err
}

func TestNewGuestLeaseUsesRandomLowercaseHex(t *testing.T) {
	lease, err := NewGuestLease(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if lease.Dir != "/dev/shm/coop-credentials/abababababababababababababababab" {
		t.Fatalf("lease directory = %q", lease.Dir)
	}
}

func TestStageStreamsSecretsOnlyThroughTarStdin(t *testing.T) {
	lease := GuestLease{Dir: "/dev/shm/coop-credentials/0123456789abcdef0123456789abcdef"}
	bundle := Bundle{
		files: []SecretFile{{path: "files/001", mode: 0o600, data: []byte("file-secret")}},
		unsetEnv: []string{
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_SESSION_TOKEN",
		},
		env: map[string][]byte{
			"Z_TOKEN": []byte("z-secret"),
			"A_TOKEN": []byte("a-secret"),
		},
	}
	fake := &fakeExecutor{}
	if err := Stage(context.Background(), fake, "coop-name", lease, bundle); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("exec calls = %d", len(fake.calls))
	}
	call := fake.calls[0]
	argv := strings.Join(call.argv, " ")
	for _, secret := range []string{"file-secret", "z-secret", "a-secret"} {
		if strings.Contains(argv, secret) {
			t.Fatalf("secret appeared in argv: %s", argv)
		}
	}
	if !slices.Equal(call.argv[len(call.argv)-2:], []string{"coop-credential-stage", lease.Dir}) {
		t.Fatalf("stage argv = %#v", call.argv)
	}

	entries := readTar(t, call.stdin)
	if entries["env/"].mode != 0o700 || entries["files/"].mode != 0o700 {
		t.Fatalf("directory modes: env=%o files=%o", entries["env/"].mode, entries["files/"].mode)
	}
	for _, name := range []string{"env.list", "env.unset", "env/A_TOKEN", "env/Z_TOKEN", "files/001"} {
		if entries[name].mode != 0o600 {
			t.Fatalf("%s mode = %o", name, entries[name].mode)
		}
	}
	if got := string(entries["env.list"].data); got != "A_TOKEN\nZ_TOKEN\n" {
		t.Fatalf("env.list = %q", got)
	}
	if got := string(entries["env.unset"].data); got != "AWS_ACCESS_KEY_ID\nAWS_SECRET_ACCESS_KEY\nAWS_SESSION_TOKEN\n" {
		t.Fatalf("env.unset = %q", got)
	}
	if !strings.Contains(guestWrapper, `unset "$name"`) {
		t.Fatal("guest wrapper does not unset inherited credential environment")
	}
}

func TestWrapPreservesOriginalArgvVector(t *testing.T) {
	lease := GuestLease{Dir: "/dev/shm/coop-credentials/0123456789abcdef0123456789abcdef"}
	original := []string{"agent", "arg with spaces", "$literal", "semi;colon"}
	wrapped, err := Wrap(lease, original)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(wrapped[len(wrapped)-len(original):], original) {
		t.Fatalf("wrapped argv = %#v", wrapped)
	}
	if !slices.Equal(wrapped[:4], []string{"sh", "-c", guestWrapper, "coop-credential-entry"}) {
		t.Fatalf("wrapper prefix = %#v", wrapped[:4])
	}
	if wrapped[4] != lease.Dir {
		t.Fatalf("wrapper lease = %q", wrapped[4])
	}
	if _, err := Wrap(GuestLease{Dir: "/mounted/project"}, original); err == nil {
		t.Fatal("unsafe wrapper cleanup target accepted")
	}
}

func TestWrapperIsolatesCleanupFromInjectedLeaseAndPATH(t *testing.T) {
	for _, name := range []string{"lease", "PATH"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			lease := filepath.Join(root, "lease")
			if err := os.MkdirAll(filepath.Join(lease, "env"), 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(root, "mounted-project")
			if err := os.Mkdir(victim, 0o700); err != nil {
				t.Fatal(err)
			}
			value := victim
			if name == "PATH" {
				value = "/path/controlled/by/credential"
			}
			if err := os.WriteFile(filepath.Join(lease, "env.list"), []byte(name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lease, "env.unset"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lease, "env", name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			procStat := "123 (sh) S" + strings.Repeat(" 0", 18) + " 222\n"
			if err := os.WriteFile(filepath.Join(lease, "proc.stat"), []byte(procStat), 0o600); err != nil {
				t.Fatal(err)
			}

			wrapper := strings.Replace(guestWrapper, `"/proc/$1/stat"`, `"$lease/proc.stat"`, 1)
			marker := filepath.Join(root, "ran")
			cmd := exec.Command("sh", "-c", wrapper, "coop-credential-entry-test", lease,
				"/bin/sh", "-c", `printf ran > "$1"`, "guest-command", marker)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("wrapper with injected %s: %v: %s", name, err, output)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("guest command did not run: %v", err)
			}
			if _, err := os.Stat(victim); err != nil {
				t.Fatalf("injected value was used as cleanup target: %v", err)
			}
			if _, err := os.Stat(lease); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("credential lease was not cleaned: %v", err)
			}
		})
	}
}

func TestScrubAndCleanupUseFixedSafeTargets(t *testing.T) {
	fake := &fakeExecutor{}
	if err := Scrub(context.Background(), fake, "coop-name"); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || !slices.Equal(fake.calls[0].argv, []string{"sh", "-c", scrubScript, "coop-credential-scrub"}) {
		t.Fatalf("scrub call = %#v", fake.calls)
	}
	if !strings.Contains(scrubScript, `id=${dir##*/}`) ||
		!strings.Contains(scrubScript, `base=${id%.staging}`) ||
		!strings.Contains(scrubScript, `test "${#base}" -eq 32`) {
		t.Fatal("scrub does not skip malformed lease directory names")
	}

	lease := GuestLease{Dir: "/dev/shm/coop-credentials/0123456789abcdef0123456789abcdef"}
	if err := Cleanup(context.Background(), fake, "coop-name", lease); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.calls[1].argv, []string{"rm", "-rf", "--", lease.Dir, lease.Dir + ".staging"}) {
		t.Fatalf("cleanup argv = %#v", fake.calls[1].argv)
	}
	before := len(fake.calls)
	if err := Cleanup(context.Background(), fake, "coop-name", GuestLease{Dir: "/dev/shm/coop-credentials/../unsafe"}); err == nil {
		t.Fatal("unsafe cleanup target accepted")
	}
	if len(fake.calls) != before {
		t.Fatal("unsafe cleanup executed")
	}
}

func TestScrubIgnoresLeaseDisappearingDuringInspection(t *testing.T) {
	root := t.TempDir()
	ownerDir := filepath.Join(root, strings.Repeat("0", 32))
	statDir := filepath.Join(root, strings.Repeat("1", 32))
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "owner"), []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(statDir, 0o700); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cat"), []byte("#!/bin/sh\nrm -rf -- \"${1%/*}\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\nrm -rf -- \"$3\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
	cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub aborted when a lease disappeared: %v: %s", err, output)
	}
}

func TestScrubIgnoresOwnerDisappearingDuringInspection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, strings.Repeat("5", 32))
	owner := filepath.Join(dir, "owner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owner, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cat"), []byte("#!/bin/sh\nrm -f -- \"$1\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
	cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub aborted when owner disappeared: %v: %s", err, output)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("lease with concurrently removed owner disappeared: %v", err)
	}
}

func TestScrubReportsInspectionFailureForExistingLease(t *testing.T) {
	for _, command := range []string{"cat", "stat"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, strings.Repeat("2", 32))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if command == "cat" {
				if err := os.WriteFile(filepath.Join(dir, "owner"), []byte("123\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
			cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			if err := cmd.Run(); err == nil {
				t.Fatalf("%s failure for an existing lease was ignored", command)
			}
		})
	}
}

func TestScrubRetainsFreshOwnerlessLeaseWithoutError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, strings.Repeat("4", 32))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\ndate +%s\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
	cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub fresh ownerless lease: %v: %s", err, output)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fresh ownerless lease was removed: %v", err)
	}
}

func TestScrubAgesOutMalformedOwnerLease(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, strings.Repeat("3", 32))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner"), []byte("malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\nprintf '0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
	cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub malformed owner: %v: %s", err, output)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed-owner lease still exists: %v", err)
	}
}

func TestScrubRejectsReusedOwnerPID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, strings.Repeat("6", 32))
	activeDir := filepath.Join(root, strings.Repeat("7", 32))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(activeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner"), []byte(fmt.Sprintf("%d 111\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "owner"), []byte(fmt.Sprintf("%d 222\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	catScript := `#!/bin/sh
case "$1" in
  (/proc/*/stat)
    printf '123 (sh) S'
    i=0
    while test "$i" -lt 18; do printf ' 0'; i=$((i + 1)); done
    printf ' 222\n';;
  (*) exec /bin/cat "$1";;
esac`
	if err := os.WriteFile(filepath.Join(bin, "cat"), []byte(catScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\ndate +%s\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.Replace(scrubScript, "root=/dev/shm/coop-credentials", "root=$1", 1)
	cmd := exec.Command("sh", "-c", script, "coop-credential-scrub-test", root)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub reused owner PID: %v: %s", err, output)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease owned by reused PID still exists: %v", err)
	}
	if _, err := os.Stat(activeDir); err != nil {
		t.Fatalf("lease with matching owner identity disappeared: %v", err)
	}
}

type tarEntry struct {
	mode int64
	data []byte
}

func readTar(t *testing.T, data []byte) map[string]tarEntry {
	t.Helper()
	entries := make(map[string]tarEntry)
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = tarEntry{mode: header.Mode, data: contents}
	}
}
