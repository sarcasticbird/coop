# Credentials

Coop is a read-only credential broker:

> GitHub creates and revokes tokens. Git stores them using the host's configured
> credential helper. Coop reads them for an authorized project entry and
> removes the temporary guest copy afterward.

Coop does not create, rotate, update, or revoke source credentials. Rotation is
a provider operation plus an update to the provider's normal host storage.

## Set up GitHub access

This path gives both Git and `gh` a GitHub token inside selected Coops without
putting the token in a project file, shell history, or agent state.

### 1. Create a token on GitHub

Create a narrowly scoped personal access token in GitHub account settings. Give
it only the repository and organization permissions your work requires.

Keep the token available for the next step. Do not paste it into Coop config.

### 2. Confirm host Git uses Keychain

Inspect the active Git credential helpers:

```sh
git config --show-origin --get-all credential.helper
```

If the output already contains the helper you intend to use, keep it. A
standard macOS Keychain setup uses `osxkeychain`; configure it only when no
intentional helper is already active:

```sh
git config --global credential.helper osxkeychain
```

Enable path-aware GitHub credentials so separate organizations or accounts can
have separate records:

```sh
git config --global credential.https://github.com.useHttpPath true
```

Credential-helper configuration is trusted host configuration. If you already
use 1Password, Git Credential Manager, or another deliberate helper chain, keep
that setup and use its normal storage behavior.

### 3. Store the token through Git

Run this in macOS's default Zsh. Enter a GitHub organization or account for the
scope, your GitHub username, and the token when prompted:

```zsh
read -r "COOP_GITHUB_SCOPE?GitHub organization or account: "
read -r "COOP_GITHUB_USER?GitHub username: "
read -r -s "COOP_GITHUB_TOKEN?GitHub token: "
printf '\n'
printf 'protocol=https\nhost=github.com\npath=%s\nusername=%s\npassword=%s\n\n' \
  "$COOP_GITHUB_SCOPE" "$COOP_GITHUB_USER" "$COOP_GITHUB_TOKEN" |
  git credential approve
unset COOP_GITHUB_TOKEN COOP_GITHUB_USER COOP_GITHUB_SCOPE
```

The prompt keeps the token out of shell history. `git credential approve`
passes it to the configured host helper; with `osxkeychain`, the source copy is
stored in macOS Keychain.

Use the same scope in the credential URL and in the project path you authorize.
For example, a scope of `example` becomes `https://github.com/example`.

### 4. Authorize selected projects

Add a named grant to `~/.config/coop/coop.toml`:

```toml
[credentials.github-example]
source = { type = "git-credential", url = "https://github.com/example" }
expose = [
  { type = "git-credential-store" },
  { type = "environment", name = "GH_TOKEN", field = "password" },
]

[[projects]]
match = "~/Projects/example"
include_credentials = ["github-example"]
```

Replace `example` in all three places. The scope URL controls the host Git
lookup; `match` controls which local projects receive the result. A repository
remote cannot change either boundary.

The first exposure gives guest Git a temporary credential store. The second
gives `gh` its documented `GH_TOKEN` interface. You do not need to run
`gh auth login` inside Coop for this grant.

### 5. Verify from a matching project

From a project beneath the configured `match` path:

```sh
coop doctor
coop gh auth status
coop git -c credential.interactive=false ls-remote origin HEAD
```

These commands check configuration, `gh`, and the project's HTTPS Git remote
without displaying the token. If `origin` is not an HTTPS GitHub remote, use
the name of one that is.

## Rotate a GitHub token

Coop has no stored source credential to rotate. Replace the value through Git,
verify a new Coop entry, then revoke the old value at GitHub:

1. Create a replacement token while the old token still works.
2. Run the same Zsh prompt from step 3 with the same scope and username. The
   configured Git helper updates its host record.
3. From a matching project, rerun:

   ```sh
   coop doctor
   coop gh auth status
   coop git -c credential.interactive=false ls-remote origin HEAD
   ```

4. Revoke the old token in GitHub account settings.

Updating the host record changes what future Coop entries acquire. It does not
invalidate the old token; provider-side revocation completes the rotation. An
already-running credentialed command may still hold its temporary copy, so use
a new entry for verification.

## Select a grant for one entry

`[[projects]]` is the preferred default boundary. For a one-off entry, add a
grant explicitly:

```sh
coop --credentials github-example codex
```

Top-level `include_credentials` grants credentials to every project and should
be reserved for credentials that genuinely need that scope. At most 16 unique
grants may be selected for one entry.

## Other credential sources

Every grant has one host `source` and one or more guest `expose` entries.

| Source | Host behavior | Allowed exposure |
| --- | --- | --- |
| `git-credential` | Runs `git credential fill` for a fixed HTTPS URL | `git-credential-store`; `environment` with `username` or `password` field |
| `file` | Reads one regular host file outside the project | `file`; `environment`; `git-credential-store` for an existing Git store |
| `command` | Executes trusted argv on the host and captures stdout | `file`; `environment` |
| `aws-profile` | Asks the host AWS CLI to export profile credentials | `aws` |

Examples:

```toml
[credentials.kubernetes]
source = { type = "file", path = "~/.kube/config" }
expose = [{ type = "file", path_env = "KUBECONFIG" }]

[credentials.internal-api]
source = { type = "command", argv = ["/usr/local/bin/read-api-token"] }
expose = [{ type = "environment", name = "INTERNAL_API_TOKEN" }]

[credentials.aws-dev]
source = { type = "aws-profile", profile = "dev" }
expose = [{ type = "aws" }]
require_expiration = true
```

Host commands run directly, without a shell, from a trusted host directory and
with a restricted environment. Source configuration is powerful: only point it
at files and executables you trust. See the exact schema in
[Configuration](configuration.md#credentials).

## Agent-owned login state

Codex, Claude Code, OpenCode, and similar clients own their OAuth and device
login flows. Coop keeps that native login state in each agent's isolated
project volume. Let the agent log in normally when it does not document an
entry-scoped environment or file interface.

Seed portable rules and non-secret preferences if useful. Do not seed
`auth.json`, `hosts.yml`, or equivalent login stores from the host.

## Moving away from plaintext credentials

Move each secret into its normal host backend, verify the new grant, then
remove only the specific plaintext copies you confirmed. Git cannot recover an
untracked secret file after deletion, so verify first and avoid recursive
cleanup of broad configuration directories.

Moving a token protects the new host copy at rest but does not invalidate old
copies. Rotate the credential at its provider after the move. `coop doctor`
reports recognized sensitive seeds and legacy plaintext Git-store sources that
still need review.

## What cleanup can and cannot do

For a credentialed interactive entry, Coop acquires material before changing
runtime state, stages a randomized lease under guest tmpfs, and removes that
lease when the command exits. It does not put secrets in project config,
container arguments, labels, mounts, seeds, or named volumes.

Every guest-root process can read or retain exposed material while the lease is
active. Cleanup cannot erase a copy made by guest code and cannot revoke a
static provider token. See [Session credentials](security-model.md#session-credentials)
for the complete boundary.

## Troubleshooting

- **Coop cannot acquire the GitHub credential:** confirm the scope in the
  credential URL exactly matches the scope stored through Git. Check
  `credential.https://github.com.useHttpPath` and the active helper origins.
- **Keychain asks for access:** macOS is authorizing the host Git process. Check
  which Git binary requested access before approving it.
- **The grant is not selected:** run `coop status`, then compare the project
  root, `[[projects]].match`, and `include_credentials` name.
- **Git works but `gh` does not:** add the `GH_TOKEN` password exposure shown in
  the GitHub example.
- **`gh` works but Git does not:** add `git-credential-store` and confirm the
  remote is HTTPS.
- **Two grants collide:** selected grants cannot claim the same environment
  name or specialized Git/AWS interface. Select one owner or expose both
  interfaces from one source.

For configuration errors, see [Configuration](configuration.md#credentials).
For the remaining risk, see the [Security model](security-model.md#session-credentials).
