# Release Process

Releases are cut by pushing an annotated version tag. The `release`
workflow builds a darwin/arm64 binary, packages it with the license
files, and publishes a GitHub Release with the tag's annotation as the
release notes.

## Versioning

Tags follow `vMAJOR.MINOR.PATCH` with an optional pre-release suffix,
matching the existing series (`v0.1.0-beta.1`, `v0.1.0-beta.2`). Any tag
containing a `-` is published as a GitHub pre-release. coop is pre-1.0;
breaking command or configuration changes bump the minor version.

The binary's version comes from the tag at build time via
`-ldflags "-X main.version=<tag>"`. Builds without that flag fall back
to the VCS revision (see `resolvedVersion` in `cmd/coop/main.go`), so
`coop --version` distinguishes released builds from source builds.

## Cutting a Release

Hosted CI runs the mocked runtime only — it cannot exercise Apple's
`container` runtime. Steps 1–3 are the real-hardware validation that CI
cannot provide.

1. Confirm `main` is green in CI and `roborev list --open` is clean.
2. On an Apple silicon host running macOS 26+, build from the release
   commit and validate against the real runtime:

   ```sh
   go build -trimpath -o /tmp/coop-rc ./cmd/coop
   /tmp/coop-rc doctor
   ```

3. From a scratch project, smoke-test the paths hosted CI cannot reach:
   `coop rebuild`, `coop up`, an interactive entry, `coop down`, and
   `coop destroy`.
4. Create an annotated tag on the release commit. The tag message
   becomes the release notes, so write it for users: what changed,
   migration notes, known limitations.

   ```sh
   git tag -a v0.1.0-beta.3
   git push origin v0.1.0-beta.3
   ```

5. The `release` workflow runs the same checks as `ci` (gofmt, tests
   with race, vet, govulncheck), cross-compiles darwin/arm64 with
   `CGO_ENABLED=0` (the module is pure Go), and publishes the release.
   Verify the run succeeded and the asset downloads.

If the workflow fails after the tag is pushed, fix the problem, delete
the tag locally and on the remote, and re-tag. Do not reuse a tag that
already produced a published release; cut the next pre-release number
instead.

## Installing from a Release

While the repository is private, downloads require authentication:

```sh
gh release download v0.1.0-beta.3 -R sarcasticbird/coop \
  -p 'coop_*_darwin_arm64.tar.gz' -p checksums.txt
shasum -a 256 -c checksums.txt
tar -xzf coop_v0.1.0-beta.3_darwin_arm64.tar.gz coop
mkdir -p "$HOME/.local/bin"
install -m 0755 coop "$HOME/.local/bin/coop"
```

Note that binaries downloaded through a browser carry the quarantine
attribute and macOS will refuse to run them: the build is ad-hoc signed
by the Go linker, not notarized. Downloads via `gh` or `curl` are not
quarantined. Developer ID signing and notarization are prerequisites
for distributing outside the beta audience (e.g., a Homebrew tap).
