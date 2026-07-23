# Release Process

Releases are cut by pushing an annotated version tag. The `release`
workflow builds a darwin/arm64 binary, packages it with the license
files, and publishes a GitHub Release with the tag's annotation as the
release notes.

## Versioning

Tags follow `vMAJOR.MINOR.PATCH` with an optional pre-release suffix such as
`-beta.1`. Any tag containing a `-` is published as a GitHub pre-release. coop
is pre-1.0; breaking command or configuration changes bump the minor version.

The binary's version comes from the tag at build time via
`-ldflags "-X main.version=<tag>"`. Builds without that flag fall back
to the VCS revision (see `resolvedVersion` in `cmd/coop/main.go`), so
`coop --version` distinguishes released builds from source builds.

## Cutting a Release

Hosted CI runs the mocked runtime only — it cannot exercise Apple's
`container` runtime. Steps 1–5 are the real-hardware validation that CI
cannot provide.

1. Choose the release version, update the README install example to that exact
   version, and commit the change. Confirm `main` is green in CI and
   `roborev list --open` is clean.
2. On an Apple silicon host running macOS 26+, build from the release
   commit and validate against the real runtime:

   ```sh
   go build -trimpath -o /tmp/coop-rc ./cmd/coop
   /tmp/coop-rc doctor
   ```

3. From a scratch project without `.flox`, smoke-test the paths hosted CI
   cannot reach:

   ```sh
   mkdir -p /tmp/coop-rc-project
   : > /tmp/coop-rc-project/coop.toml
   cd /tmp/coop-rc-project
   /tmp/coop-rc rebuild
   /tmp/coop-rc status
   /tmp/coop-rc up
   /tmp/coop-rc sh -c '
     set -eu
     for c in bash zsh ls grep sed find awk tar gzip git gh ssh curl rg jq \
       diff patch file less ps tmux unzip codex claude opencode flox nix
     do
       command -v "$c" >/dev/null
     done
     test -r "$SSL_CERT_FILE"
   '
   /tmp/coop-rc down
   /tmp/coop-rc destroy
   ```

4. Exercise configured and project tool layering:

   - Add `[tools] packages = ["hello"]` to the scratch `coop.toml`, rebuild,
     and confirm `hello` is available to an entered command.
   - Add a configured package that also provides a core command and confirm
     the command still resolves from the locked core path.
   - Confirm Coop maintenance still succeeds and the configured profile is
     not on its maintenance PATH.
   - Add an `aarch64-linux` project `.flox`, then confirm its executable path
     precedes `/opt/coop-tools/profile/bin` while configured tools remain
     available.
   - Pass arguments containing spaces, quotes, dollar signs, and semicolons
     through both Flox and non-Flox entry paths and confirm they remain
     separate argv values.
   - Request one nonexistent package and confirm the failed rebuild leaves the
     previous image and container usable.
   - In trusted user configuration, add one exact-tag and one `latest`
     `[[tools.github_release]]` entry whose releases publish API SHA-256
     digests. Rebuild and confirm both Linux arm64 commands run.
   - Run `coop status` without network access and confirm it reports the locked
     release tags. Rebuild again and confirm cached assets are accepted.
   - Try an absent asset, digest mismatch, and unsafe archive fixture; confirm
     each rebuild fails without replacing the previous release lock or
     disturbing the working container.

5. Verify recreation semantics. Create a marker in the guest root filesystem
   and a marker under one agent's named state directory, change the effective
   tool set, rebuild, and enter again. Confirm `coop status` reports pending
   recreation before entry, the rootfs marker disappears, and the named-volume
   marker survives.

6. Create an annotated tag on the release commit. The tag message
   becomes the release notes, so write it for users: what changed,
   migration notes, known limitations, and any removed bundled tools or other
   breaking changes. For example, the Flox-backed tooling release called out
   that Node.js was no longer bundled and had to be declared through project
   Flox or `[tools].packages`.

   ```sh
   version=vX.Y.Z-beta.N # replace with the version being released
   git tag -a "$version"
   git push origin "$version"
   ```

7. The `release` workflow runs the same checks as `ci` (gofmt, tests
   with race, vet, govulncheck), cross-compiles darwin/arm64 with
   `CGO_ENABLED=0` (the module is pure Go), and publishes the release.
   Verify the run succeeded and the asset downloads.

If the workflow fails after the tag is pushed, fix the problem, delete
the tag locally and on the remote, and re-tag. Do not reuse a tag that
already produced a published release; cut the next pre-release number
instead.

## Installing from a Release

Public release assets can be downloaded without authentication using a current
GitHub CLI:

```sh
version=vX.Y.Z-beta.N # replace with the version to install
archive="coop_${version}_darwin_arm64.tar.gz"
gh release download "$version" -R sarcasticbird/coop \
  -p "$archive" -p checksums.txt
shasum -a 256 -c checksums.txt
tar -xzf "$archive" coop
mkdir -p "$HOME/.local/bin"
install -m 0755 coop "$HOME/.local/bin/coop"
```

Browser downloads carry quarantine metadata, and macOS may refuse to run the
ad-hoc-signed, non-notarized binary. Downloads via `gh` or `curl` do not
normally receive browser quarantine metadata. Developer ID signing and
notarization are prerequisites for distribution channels that require trusted
macOS binaries, such as a Homebrew tap.
