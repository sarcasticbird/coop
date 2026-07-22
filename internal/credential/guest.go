package credential

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const credentialRoot = "/dev/shm/coop-credentials"

var (
	leasePathPattern = regexp.MustCompile(`^/dev/shm/coop-credentials/[0-9a-f]{32}$`)
	envNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Executor is the narrow runtime surface needed for guest credential leases.
type Executor interface {
	ExecContext(context.Context, string, []string, io.Reader) error
}

// NewGuestLease creates an unpredictable guest tmpfs lease path.
func NewGuestLease(random io.Reader) (GuestLease, error) {
	var data [16]byte
	if _, err := io.ReadFull(random, data[:]); err != nil {
		return GuestLease{}, fmt.Errorf("generate credential lease id: %w", err)
	}
	id := hex.EncodeToString(data[:])
	return GuestLease{Dir: credentialRoot + "/" + id}, nil
}

const stageScript = `set -eu
umask 077
final=$1
root=${final%/*}
tmp=$final.staging
mkdir -p "$root"
chmod 700 "$root"
test ! -e "$final"
rm -rf -- "$tmp"
mkdir -m 700 "$tmp"
trap 'rm -rf -- "$tmp"' EXIT
tar -xf - -C "$tmp" --no-same-owner --no-same-permissions
mv -T "$tmp" "$final"
trap - EXIT`

// Stage streams a credential bundle through stdin and atomically installs it
// beneath the generated guest tmpfs lease path.
func Stage(ctx context.Context, executor Executor, name string, lease GuestLease, bundle Bundle) error {
	if err := validateLease(lease); err != nil {
		return err
	}
	input, err := bundleTar(bundle)
	if err != nil {
		return err
	}
	argv := []string{"sh", "-c", stageScript, "coop-credential-stage", lease.Dir}
	if err := executor.ExecContext(ctx, name, argv, bytes.NewReader(input)); err != nil {
		return fmt.Errorf("stage guest credentials: %w", err)
	}
	return nil
}

func bundleTar(bundle Bundle) ([]byte, error) {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	writeEntry := func(header *tar.Header, data []byte) error {
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if len(data) > 0 {
			if _, err := writer.Write(data); err != nil {
				return err
			}
		}
		return nil
	}
	for _, directory := range []string{"env/", "files/"} {
		if err := writeEntry(&tar.Header{
			Name: directory, Mode: 0o700, Typeflag: tar.TypeDir,
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
		}, nil); err != nil {
			return nil, fmt.Errorf("build credential tar: %w", err)
		}
	}

	envNames := SortedEnvNames(bundle.env)
	var envList strings.Builder
	for _, name := range envNames {
		if !envNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid credential environment name %q", name)
		}
		value := bundle.env[name]
		if bytes.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("credential environment %s contains a NUL or newline", name)
		}
		envList.WriteString(name)
		envList.WriteByte('\n')
		if err := writeEntry(secretHeader("env/"+name, len(value)), value); err != nil {
			return nil, fmt.Errorf("build credential tar: %w", err)
		}
	}
	if err := writeEntry(secretHeader("env.list", envList.Len()), []byte(envList.String())); err != nil {
		return nil, fmt.Errorf("build credential tar: %w", err)
	}
	unsetNames := slices.Clone(bundle.unsetEnv)
	slices.Sort(unsetNames)
	var unsetList strings.Builder
	for _, name := range unsetNames {
		if !envNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid credential environment name %q", name)
		}
		unsetList.WriteString(name)
		unsetList.WriteByte('\n')
	}
	if err := writeEntry(secretHeader("env.unset", unsetList.Len()), []byte(unsetList.String())); err != nil {
		return nil, fmt.Errorf("build credential tar: %w", err)
	}

	for _, file := range bundle.files {
		if path.IsAbs(file.path) || path.Clean(file.path) != file.path || !strings.HasPrefix(file.path, "files/") {
			return nil, fmt.Errorf("invalid credential bundle path %q", file.path)
		}
		if file.mode != 0o600 {
			return nil, fmt.Errorf("credential bundle file %q has unsafe mode %o", file.path, file.mode)
		}
		if err := writeEntry(secretHeader(file.path, len(file.data)), file.data); err != nil {
			return nil, fmt.Errorf("build credential tar: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close credential tar: %w", err)
	}
	return output.Bytes(), nil
}

func secretHeader(name string, size int) *tar.Header {
	return &tar.Header{
		Name: name, Mode: 0o600, Size: int64(size), Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
	}
}

const guestWrapper = `set -eu
umask 077
lease=$1
shift
command -v rm > /dev/null || { printf '%s\n' 'coop-credential-entry: rm not found on PATH' >&2; exit 127; }
process_start() {
  IFS= read -r proc_stat < "/proc/$1/stat" || return 1
  proc_rest=${proc_stat##*) }
  set -- $proc_rest
  test "$#" -ge 20
  printf '%s\n' "${20}"
}
owner_start=$(process_start "$$")
printf '%s %s\n' "$$" "$owner_start" > "$lease/owner"
cleanup() { rm -rf -- "$lease"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
set +e
(
  set -e
  while IFS= read -r name; do
    unset "$name"
  done < "$lease/env.unset"
  while IFS= read -r name; do
    exec 3< "$lease/env/$name"
    value=
    IFS= read -r value <&3 || :
    exec 3<&-
    export "$name=$value"
  done < "$lease/env.list"
  set +e
  "$@"
  status=$?
  set -e
  exit "$status"
)
status=$?
set -e
exit "$status"`

// Wrap returns a fixed shell wrapper followed by the original argv unchanged.
func Wrap(lease GuestLease, original []string) ([]string, error) {
	if err := validateLease(lease); err != nil {
		return nil, err
	}
	wrapped := []string{"sh", "-c", guestWrapper, "coop-credential-entry", lease.Dir}
	return append(wrapped, original...), nil
}

const scrubScript = `set -eu
root=/dev/shm/coop-credentials
test -d "$root" || exit 0
process_start() {
  IFS= read -r proc_stat < "/proc/$1/stat" || return 1
  proc_rest=${proc_stat##*) }
  set -- $proc_rest
  test "$#" -ge 20
  printf '%s\n' "${20}"
}
now=$(date +%s)
for dir in "$root"/*; do
  test -d "$dir" || continue
  id=${dir##*/}
  staging=0
  case "$id" in
    (*.staging) base=${id%.staging}; staging=1;;
    (*) base=$id;;
  esac
  case "$base" in (*[!0-9a-f]*|'') continue;; esac
  test "${#base}" -eq 32 || continue
  if test "$staging" -eq 1; then
    modified=$(stat -c %Y "$dir") || { test ! -d "$dir" && continue; exit 1; }
    if test $((now - modified)) -gt 60; then rm -rf -- "$dir"; fi
    continue
  fi
  owner=$dir/owner
  if test -f "$owner"; then
    owner_identity=$(cat "$owner") || { test ! -e "$owner" && continue; test ! -d "$dir" && continue; exit 1; }
    set -- $owner_identity
    if test "$#" -eq 2; then
      pid=$1
      owner_start=$2
      case "$pid:$owner_start" in
        (*[!0-9:]*|:*) ;;
        (*)
          if kill -0 "$pid" 2>/dev/null; then
            current_start=$(process_start "$pid") || {
              kill -0 "$pid" 2>/dev/null || { rm -rf -- "$dir"; continue; }
              exit 1
            }
            test "$current_start" = "$owner_start" && continue
          fi
          rm -rf -- "$dir"
          continue;;
      esac
    fi
  fi
  modified=$(stat -c %Y "$dir") || { test ! -d "$dir" && continue; exit 1; }
  if test $((now - modified)) -gt 60; then rm -rf -- "$dir"; fi
done`

// Scrub removes only stale generated leases using guest PID liveness.
func Scrub(ctx context.Context, executor Executor, name string) error {
	argv := []string{"sh", "-c", scrubScript, "coop-credential-scrub"}
	if err := executor.ExecContext(ctx, name, argv, nil); err != nil {
		return fmt.Errorf("scrub guest credential leases: %w", err)
	}
	return nil
}

// Cleanup removes one validated generated lease path.
func Cleanup(ctx context.Context, executor Executor, name string, lease GuestLease) error {
	if err := validateLease(lease); err != nil {
		return err
	}
	if err := executor.ExecContext(ctx, name, []string{"rm", "-rf", "--", lease.Dir, lease.Dir + ".staging"}, nil); err != nil {
		return fmt.Errorf("clean up guest credential lease: %w", err)
	}
	return nil
}

func validateLease(lease GuestLease) error {
	if !leasePathPattern.MatchString(lease.Dir) {
		return errors.New("invalid guest credential lease path")
	}
	return nil
}
