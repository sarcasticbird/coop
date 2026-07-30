// Package seed applies host->guest config propagation rules.
//
// Seeding exists because mounting is wrong for config on this stack:
// host-managed files may be symlinks whose targets do not exist in the
// guest, and `container cp` bypasses volume
// mounts. Host-side reads resolve symlinks; writes go through exec
// stdin into the live mount namespace.
package seed

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

// goos is variable for tests; production value is the build platform.
var goos = goruntime.GOOS

// Apply runs every seed rule against a running container. Missing
// sources are skipped silently (rules describe the superset of hosts).
func Apply(rt runtime.Runtime, name, hostHome, guestHome string, seeds []config.Seed) error {
	return ApplyContext(context.Background(), rt, name, hostHome, guestHome, seeds)
}

// ApplyContext runs every seed rule and cancels guest transports with ctx.
func ApplyContext(ctx context.Context, rt runtime.Runtime, name, hostHome, guestHome string, seeds []config.Seed) error {
	for _, s := range seeds {
		src := config.ExpandHome(s.Src, hostHome)
		dest := config.ExpandHome(s.Dest, guestHome)

		switch s.Policy {
		case config.PolicyOverlay:
			if err := overlayDir(ctx, rt, name, src, dest); err != nil {
				return fmt.Errorf("seed overlay %s: %w", s.Src, err)
			}
		case config.PolicyIfAbsent:
			// Fail CLOSED: an inconclusive answer must not overwrite a
			// file the guest may have created or updated itself.
			exists, err := rt.GuestFileExists(name, dest)
			if err != nil {
				return fmt.Errorf("seed %s: cannot determine guest state: %w", s.Src, err)
			}
			if exists {
				continue
			}
			if err := copyIfAbsent(ctx, rt, name, src, dest); err != nil {
				return fmt.Errorf("seed %s: %w", s.Src, err)
			}
		case config.PolicyAlways, "":
			if err := copyFile(ctx, rt, name, src, dest); err != nil {
				return fmt.Errorf("seed %s: %w", s.Src, err)
			}
		default:
			return fmt.Errorf("seed %s: unknown policy %q", s.Src, s.Policy)
		}
	}
	return nil
}

func copyIfAbsent(ctx context.Context, rt runtime.Runtime, name, src, dest string) error {
	fi, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDirIfAbsent(ctx, rt, name, src, dest)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("source is neither a regular file nor directory")
	}
	return copyFile(ctx, rt, name, src, dest)
}

func copyFile(ctx context.Context, rt runtime.Runtime, name, src, dest string) error {
	f, err := os.Open(src) // resolves symlinks
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	mode := fmt.Sprintf("%o", fi.Mode().Perm())

	if err := rt.ExecContext(ctx, name, []string{"mkdir", "-p", filepath.Dir(dest)}, nil); err != nil {
		return err
	}
	// Atomic, symlink-refusing write. dest and mode pass positionally —
	// never interpolated into shell text. Defenses, in order:
	//   - dest must not be a symlink or non-regular file (a guest-
	//     planted link could redirect seeded data into the project
	//     mount, which is host-visible)
	//   - mktemp creates the temp exclusively at a random name — a
	//     predictable temp path could itself be pre-planted
	//   - mode is set before content is written
	//   - mv -T refuses to descend into a directory at dest
	//   - the physical parent directory must match the expected literal
	//     parent: a guest-planted symlink in ANY path component would
	//     resolve elsewhere (e.g. into the host-visible project mount)
	//     and is refused. Residual TOCTOU between check and write is
	//     acknowledged; fully closing it requires an openat2/no-follow
	//     guest helper.
	script := `set -e
d="$1"; m="$2"; p="$3"
rp=$(cd -P "$p" 2>/dev/null && pwd) || { echo "dest parent missing: $p" >&2; exit 1; }
if [ "$rp" != "$p" ]; then echo "refusing symlinked parent: $p -> $rp" >&2; exit 1; fi
if [ -L "$d" ]; then echo "refusing symlink dest: $d" >&2; exit 1; fi
if [ -e "$d" ] && [ ! -f "$d" ]; then echo "refusing non-regular dest: $d" >&2; exit 1; fi
t=$(mktemp "$d.coop-seed.XXXXXX")
trap 'rm -f "$t"' EXIT
chmod "$m" "$t"
cat > "$t"
mv -T "$t" "$d"
trap - EXIT`
	return rt.ExecContext(ctx, name, []string{"sh", "-c", script, "coop-seed", dest, mode, filepath.Dir(dest)}, f)
}

func copyDirIfAbsent(ctx context.Context, rt runtime.Runtime, name, src, dest string) error {
	// Keep the parent distinct from a trailing-slash destination so mkdir
	// cannot create the destination before the existence guard runs.
	dest = filepath.Clean(dest)
	archive, err := createTarArchive(ctx, src)
	if err != nil {
		return err
	}
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archive.Name())
	}()
	script := `set -e
d="$1"; p="$2"
mkdir -p "$p"
rp=$(cd -P "$p" 2>/dev/null && pwd) || { echo "dest parent missing: $p" >&2; exit 1; }
if [ "$rp" != "$p" ]; then echo "refusing symlinked parent: $p -> $rp" >&2; exit 1; fi
if [ -L "$d" ] || [ -e "$d" ]; then cat >/dev/null; exit 0; fi
t=$(mktemp -d "$p/.coop-seed.XXXXXX")
trap 'rm -rf "$t"' EXIT
tar -xf - -C "$t"
mv -T -n "$t" "$d"
if [ -d "$t" ]; then
  echo "destination appeared during seed: $d" >&2
  exit 1
fi
trap - EXIT`
	execErr := rt.ExecContext(
		ctx,
		name,
		[]string{"sh", "-c", script, "coop-seed-dir", dest, filepath.Dir(dest)},
		archive,
	)
	return execErr
}

func createTarArchive(ctx context.Context, src string) (*os.File, error) {
	archive, err := os.CreateTemp("", "coop-seed-*.tar")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = archive.Close()
		_ = os.Remove(archive.Name())
	}

	// Finish the host archive before the guest can publish it. A late tar
	// failure must not leave a partial destination that if-absent then skips.
	tarArgs := []string{"-C", src, "-chf", archive.Name(), "."}
	if goos == "darwin" {
		tarArgs = append([]string{"--no-xattrs"}, tarArgs...)
	}
	tarCmd := hostTarCommand(ctx, tarArgs...)
	var stderr bytes.Buffer
	tarCmd.Stderr = &stderr
	if err := tarCmd.Run(); err != nil {
		cleanup()
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) == 0 {
			return nil, fmt.Errorf("archive directory %s: %w", src, err)
		}
		return nil, fmt.Errorf("archive directory %s: %w: %s", src, err, detail)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return archive, nil
}

func startTar(ctx context.Context, src string) (*exec.Cmd, io.ReadCloser, error) {
	// -h dereferences symlinks in BOTH bsdtar and GNU tar (bsdtar's -L
	// means --tape-length in GNU tar). --no-xattrs is bsdtar-only:
	// suppresses AppleDouble headers that make in-guest GNU tar warn.
	tarArgs := []string{"-C", src, "-chf", "-", "."}
	if goos == "darwin" {
		tarArgs = append([]string{"--no-xattrs"}, tarArgs...)
	}
	tarCmd := hostTarCommand(ctx, tarArgs...)
	pipe, err := tarCmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := tarCmd.Start(); err != nil {
		return nil, nil, err
	}
	return tarCmd, pipe, nil
}

func hostTarCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "tar", args...)
	if goos == "darwin" {
		cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	}
	return cmd
}

// overlayDir tars the host tree (dereferencing symlinks, no macOS
// xattrs) and extracts in-guest. Adds/updates only; never deletes.
//
// LIMITATION: extraction follows guest-side symlinks inside the
// destination tree. Use overlay only for non-sensitive trees (skills,
// docs), never credentials.
func overlayDir(ctx context.Context, rt runtime.Runtime, name, src, dest string) error {
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil // missing or not a dir: skip
	}
	tarCmd, pipe, err := startTar(ctx, src)
	if err != nil {
		return err
	}
	if err := rt.ExecContext(ctx, name, []string{"mkdir", "-p", dest}, nil); err != nil {
		_ = pipe.Close()
		_ = tarCmd.Wait()
		return err
	}
	execErr := rt.ExecContext(ctx, name, []string{"tar", "-xf", "-", "-C", dest}, pipe)
	return finishTar(tarCmd, pipe, execErr)
}

func finishTar(tarCmd *exec.Cmd, pipe io.ReadCloser, execErr error) error {
	// ExecContext is synchronous: once it returns, nobody should read from the
	// stream again. Close our read end before Wait so a guest-side failure
	// cannot leave the host tar process blocked on a full pipe.
	_ = pipe.Close()
	tarErr := tarCmd.Wait()
	if execErr != nil {
		return execErr
	}
	return tarErr
}
