// Package seed applies host->guest config propagation rules.
//
// Seeding exists because mounting is wrong for config on this stack:
// host-managed files may be symlinks whose targets do not exist in the
// guest, and `container cp` bypasses volume
// mounts. Host-side reads resolve symlinks; writes go through exec
// stdin into the live mount namespace.
package seed

import (
	"fmt"
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
	for _, s := range seeds {
		src := config.ExpandHome(s.Src, hostHome)
		dest := config.ExpandHome(s.Dest, guestHome)

		switch s.Policy {
		case config.PolicyOverlay:
			if err := overlayDir(rt, name, src, dest); err != nil {
				return fmt.Errorf("seed overlay %s: %w", s.Src, err)
			}
		case config.PolicyIfAbsent:
			// Fail CLOSED: an inconclusive answer must not overwrite a
			// credential the coop refreshed itself.
			exists, err := rt.GuestFileExists(name, dest)
			if err != nil {
				return fmt.Errorf("seed %s: cannot determine guest state: %w", s.Src, err)
			}
			if exists {
				continue
			}
			if err := copyFile(rt, name, src, dest); err != nil {
				return fmt.Errorf("seed %s: %w", s.Src, err)
			}
		case config.PolicyAlways, "":
			if err := copyFile(rt, name, src, dest); err != nil {
				return fmt.Errorf("seed %s: %w", s.Src, err)
			}
		default:
			return fmt.Errorf("seed %s: unknown policy %q", s.Src, s.Policy)
		}
	}
	return nil
}

func copyFile(rt runtime.Runtime, name, src, dest string) error {
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

	if err := rt.Exec(name, []string{"mkdir", "-p", filepath.Dir(dest)}, nil); err != nil {
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
	return rt.Exec(name, []string{"sh", "-c", script, "coop-seed", dest, mode, filepath.Dir(dest)}, f)
}

// overlayDir tars the host tree (dereferencing symlinks, no macOS
// xattrs) and extracts in-guest. Adds/updates only; never deletes.
//
// LIMITATION: extraction follows guest-side symlinks inside the
// destination tree. Use overlay only for non-sensitive trees (skills,
// docs), never credentials.
func overlayDir(rt runtime.Runtime, name, src, dest string) error {
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil // missing or not a dir: skip
	}
	// -h dereferences symlinks in BOTH bsdtar and GNU tar (bsdtar's -L
	// means --tape-length in GNU tar). --no-xattrs is bsdtar-only:
	// suppresses AppleDouble headers that make in-guest GNU tar warn.
	tarArgs := []string{"-C", src, "-chf", "-", "."}
	if goos == "darwin" {
		tarArgs = append([]string{"--no-xattrs"}, tarArgs...)
	}
	tarCmd := exec.Command("tar", tarArgs...)
	pipe, err := tarCmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := tarCmd.Start(); err != nil {
		return err
	}
	if err := rt.Exec(name, []string{"mkdir", "-p", dest}, nil); err != nil {
		return err
	}
	execErr := rt.Exec(name, []string{"tar", "-xf", "-", "-C", dest}, pipe)
	tarErr := tarCmd.Wait()
	if execErr != nil {
		return execErr
	}
	return tarErr
}
