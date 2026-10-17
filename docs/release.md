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

Build and validate with Go 1.26.9 or newer, as required by `go.mod`.
In a checkout, use `flox activate -- go ...` for the commands below.
The Flox catalog currently supplies Go 1.26.8 as a bootstrap; the default
`GOTOOLCHAIN=auto` selects and downloads the patched toolchain required by
`go.mod`. First use therefore needs network access. Do not build with the
bootstrap alone or lower the module requirement to avoid that download.
CI and release workflows select their Go version from `go.mod` too.
See [the advisory review](go-toolchain-security.md) for the upgrade rationale.

Hosted CI runs the mocked runtime only — it cannot exercise Apple's
`container` runtime. Steps 1–5 are the real-hardware validation that CI
cannot provide.

1. Choose the release version. Confirm `main` is green in CI and
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
   : > /tmp/coop-rc-project/.coop.toml
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

   Also confirm the interactive first-build paths. They require a project
   whose image does not exist yet, so run them BEFORE the `coop rebuild`
   step above, or use a second scratch project with a different tool set
   (its image name differs, so it is unbuilt): run `/tmp/coop-rc` at a
   terminal and confirm it prompts `Build it now? [Y/n]`, declines cleanly
   on `n`, and on accept builds, prints the helpful-commands block, and
   enters the shell. After any rebuild, `container ls -a` must show the
   `buildkit` container stopped (unless it was running before the build
   started).

4. Exercise configured and project tool layering:

   - Add `[tools] packages = ["hello"]` to the scratch `.coop.toml`, rebuild,
     and confirm `hello` is available to an entered command.
   - Add a sibling scratch directory through top-level `mount`, confirm it is
     present at the identical path, and verify both read-only and read-write
     access. Confirm `/`, the host home, and a path overlapping the selected
     project are rejected.
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
   - In local configuration, add one exact-tag and one `latest`
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

6. Create an annotated tag on the release commit. The tag message becomes the
   release notes, so write it for users: what changed, required user action,
   known limitations, and breaking changes.

   ```sh
   version=vX.Y.Z-beta.N # replace with the version being released
   git tag -a "$version"
   git push origin "$version"
   ```

7. The `release` workflow runs the same checks as `ci` (gofmt, tests
   with race, vet, govulncheck), cross-compiles darwin/arm64 with
   `CGO_ENABLED=0` (the module is pure Go), and publishes the release.
   Verify the run succeeded and the asset downloads.

8. Update the Homebrew tap. Compute the source tarball digest and point
   `Formula/coop.rb` in `sarcasticbird/homebrew-tap` at the new tag:

   ```sh
   curl -sL "https://github.com/sarcasticbird/coop/archive/refs/tags/$version.tar.gz" | shasum -a 256
   ```

   Bump `url` and `sha256`, then verify locally before pushing the tap:

   ```sh
   brew reinstall --build-from-source sarcasticbird/tap/coop
   brew test sarcasticbird/tap/coop
   brew audit --strict sarcasticbird/tap/coop
   ```

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

The Homebrew tap (`brew install sarcasticbird/tap/coop`) builds from the
tagged source on the user's machine, so it does not distribute the unsigned
binary. Browser downloads of the release binary carry quarantine metadata,
and macOS may refuse to run the ad-hoc-signed, non-notarized binary;
downloads via `gh` or `curl` do not normally receive quarantine metadata.
Developer ID signing and notarization remain prerequisites for any channel
that distributes the prebuilt binary to trusted-macOS users.

## Matching named-agent helper installation

The release archive contains the Darwin arm64 Coop CLI. Named agents using the
optional GitHub worker also need Linux arm64 `coop-worker`, the GitHub App guest
helper when configured, and the checked launcher/installer scripts. Use a Git
checkout of the same release tag, not scripts from a moving branch. The source
tarball lacks the Git provenance required by the artifact manifest helper.

On the development Mac, choose the exact published tag and prepare artifacts:

```sh
git clone https://github.com/sarcasticbird/coop.git coop-release-source
cd coop-release-source
version=vX.Y.Z-beta.N # replace with the exact published release tag
git fetch origin tag "$version"
git checkout --detach "$version"
mkdir -p bin/agent-install/artifacts bin/agent-install/host
COOP_BUILD_VERSION="$version" flox activate -c '
  set -eu
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X main.version=$COOP_BUILD_VERSION" \
    -o bin/agent-install/artifacts/coop ./cmd/coop
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
    -o bin/agent-install/artifacts/coop-worker ./cmd/coop-worker
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
    -o bin/agent-install/artifacts/coop-github ./cmd/coop-github
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
    -o bin/agent-install/host/coop-github ./cmd/coop-github
'
```

Build with the toolchain selected by `go.mod`. Keep `bin/` Git-ignored and verify
the checkout remains clean. Record the full tag commit and build provenance,
then create and verify the offline manifest from that source:

```sh
source_head=$(git rev-parse HEAD)
artifacts="$PWD/bin/agent-install/artifacts"
evidence_dir=$(mktemp -d "${TMPDIR:-/tmp}/coop-agent-install.XXXXXX")
manifest="$evidence_dir/manifest.json"
flox activate -- python3 scripts/scheduler_artifacts.py create \
  --source-head "$source_head" --artifacts "$artifacts" --manifest "$manifest"
flox activate -- python3 scripts/scheduler_artifacts.py verify \
  --source-head "$source_head" --artifacts "$artifacts" --manifest "$manifest"
shasum -a 256 "$manifest" bin/agent-install/host/coop-github
```

The manifest binds the supplied binaries, examples and launchers to this exact
checkout. It does not prove that compilation produced those bytes; retain the
build log and toolchain/target settings too. Use the matching built host CLI and
host GitHub helper outside the checkout, as described in
[GitHub App setup](github-apps.md). Keep registration, signing keys, worker
configuration and profiles in private storage. The public examples contain
placeholders and are not usable credentials or a deployment.

After the separately approved backup, drain and service/producer fence, stage
the exact binaries and launchers through the supported installers:

```sh
flox activate -- bash scripts/install-github-auth.sh \
  AGENT_NAME /absolute/agent-install/artifacts/coop-github APP_ID
flox activate -- bash scripts/install-agent-worker.sh \
  AGENT_NAME /absolute/agent-install/artifacts/coop-worker \
  /absolute/private/worker.json
```

These commands require the selected unshared VM to be running for staging and
do not start its services. Keep it fenced. Existing managed installations use
the normal worker installer; a manual OpenCode Coop may need the explicit
matching-launcher adoption mode. Follow [worker installation](workers.md#runtime-and-private-configuration)
and [state compatibility](workers.md#offline-state-compatibility) for first v2
initialization or one-way conversion. Profile application, initialization,
service startup and new admissions remain explicit rollout steps. Source
build/install commands do not authorize them or replace native acceptance.

V2 routing continuity additionally requires one successful unchanged-config
poll before changing the default ping destination. Preserve matching readers
and recovery evidence; older readers reject the new continuity field.
