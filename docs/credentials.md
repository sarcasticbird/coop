# Credentials

Coop separates configuration, initialization, persistent application state,
and session secrets. The short version is:

> Configuration describes, seeds initialize non-secrets, agent volumes persist
> provider-owned state, and credential grants lease secrets.

Use seeds for portable configuration, rules, skills, and executables. Let an
agent keep its own interactive login in its project-specific state volume. Use
a credential grant when a tool accepts a documented environment, temporary
file, Git helper, or AWS shared-credentials interface.

## Trust and authorization

Machine-wide configuration and the ignored project `.coop.toml` may define a
credential or select a default credential. Coop does not load a committed
project `coop.toml`.

`[[projects]]` is the authorization boundary. Its canonical path prefix decides
which projects receive a grant. The source URL is also fixed in local
configuration. Coop never derives it from `.git/config` or another repository
file, so changing a remote cannot retarget an authorized grant.

An authorized guest is still trusted with the leased credential. Guest root
can read process environments and lease files and can retain a copy after Coop
cleans up. macOS Keychain protects the host copy at rest; it does not make the
guest unable to copy an intentionally granted secret.

## Lifecycle

For each interactive entry Coop:

1. resolves default, project-scoped, and explicit credential selections;
2. validates all source and exposure claims and rejects collisions;
3. acquires every credential on the host before changing VM state;
4. reconciles the container and applies non-secret seeds;
5. creates a randomized mode-0700 lease under
   `/dev/shm/coop-credentials`;
6. stages mode-0600 files and process environment values atomically;
7. launches exactly one guest command through the lease;
8. removes the lease and revokes renewable material when supported.

Acquisition failure leaves a working container untouched. Cleanup removes the
guest lease, but it cannot revoke a copy retained by guest code or a static
source token.

## Sources, material, and exposures

A source acquires typed material on the host. An exposure projects that
material into a constrained guest interface.

| Source | Material | Purpose |
| --- | --- | --- |
| `file` | opaque | Read one regular host file outside the project |
| `command` | opaque | Run trusted host argv and capture bounded stdout |
| `git-credential` | username/password | Ask host `git credential fill` for one fixed HTTPS URL |
| `aws-profile` | AWS | Export temporary credentials through the host AWS CLI |

| Exposure | Compatible material | Guest result |
| --- | --- | --- |
| `environment` without `field` | opaque | Complete payload in `name` |
| `environment` with `field = "username"` or `"password"` | username/password | Selected field in `name` |
| `file` | opaque | Mode-0600 lease file; its path in `path_env` |
| `git-credential-store` | username/password, or legacy opaque file material | Temporary store plus process-local Git helper override |
| `aws` | AWS | Temporary shared-credentials file and sanitized AWS environment |

New configurations can define several projections with `expose`. Existing
singular `inject` definitions remain supported as a compatibility form. A
grant cannot define both.

## GitHub with macOS Keychain

The recommended GitHub path lets host Git own credential lookup and use
`git-credential-osxkeychain` as its storage backend. Coop's public abstraction
is still `git-credential`: other Git helpers remain possible, and Git helper
configuration is trusted executable host configuration.

First inspect the active helper chain, then configure only what is missing.
`useHttpPath` is necessary when maintaining separate URL-path entries such as
one grant per organization:

```sh
git config --show-origin --get-all credential.helper
git config --show-origin --get-all credential.https://github.com.useHttpPath

# Only when no intended Keychain helper is already active:
git config --global credential.helper osxkeychain
git config --global credential.https://github.com.useHttpPath true
```

Do not replace or duplicate an intentional helper chain without reviewing its
origins. A system-level `osxkeychain` helper is already sufficient; the
path-aware setting may still need to be added at user scope.

Store an organization-scoped GitHub username and token without putting the
token in shell history or a plaintext file. This snippet uses macOS's default
Zsh:

```zsh
read -r "COOP_GH_USER?GitHub username: "
read -r -s "COOP_GH_TOKEN?GitHub token: "
printf '\n'
printf 'protocol=https\nhost=github.com\npath=sarcasticbird\nusername=%s\npassword=%s\n\n' \
  "$COOP_GH_USER" "$COOP_GH_TOKEN" | git credential approve
unset COOP_GH_TOKEN COOP_GH_USER
```

Replace `sarcasticbird` with the fixed URL path used by the grant. The token's
provider-side repository permissions remain the real authority; choose the
narrowest practical token.

Verify operationally that the configured Git helper chain can resolve a
password without printing it or falling back to an interactive prompt:

```sh
printf 'protocol=https\nhost=github.com\npath=sarcasticbird\n\n' |
  GIT_TERMINAL_PROMPT=0 git credential fill |
  awk -F= '$1 == "password" && length($2) { password=1 } END { exit !password }' &&
  printf '%s\n' 'Git credential found'
```

`git credential fill` includes request fields such as `path` in its output even
when the selected helper returned only a username and password. Its output
therefore proves that the fixed request resolves, not which backend record was
selected. Coop deliberately trusts the user's helper chain and cannot use
Git's merged response as backend provenance.

When separate macOS Keychain records enforce organization boundaries, verify
the backend directly and include an unmatched negative control. This still
does not print a password:

```sh
credential_scope=sarcasticbird
printf 'protocol=https\nhost=github.com\npath=%s\n\n' "$credential_scope" |
  git credential-osxkeychain get |
  awk -F= '$1 == "password" && length($2) { password=1 } END { exit !password }' &&
  printf '%s\n' 'Exact Keychain record found'

credential_probe=coop-path-scope-negative-control.invalid
if printf 'protocol=https\nhost=github.com\npath=%s\n\n' "$credential_probe" |
  git credential-osxkeychain get |
  awk -F= '$1 == "password" && length($2) { password=1 } END { exit !password }'
then
  printf '%s\n' 'Unexpected broad Keychain credential' >&2
  exit 1
fi
```

Repeat the positive check for every intended path. The negative control must
return no password; otherwise a host-wide fallback can satisfy a path request.
For another helper backend, use its equivalent exact-record inspection or
accept explicitly that the helper, rather than Coop, owns that scoping rule.

Then declare one acquisition with two projections:

```toml
[credentials.github-sarcasticbird]
source = { type = "git-credential", url = "https://github.com/sarcasticbird" }
expose = [
  { type = "git-credential-store" },
  { type = "environment", name = "GH_TOKEN", field = "password" },
]

[[projects]]
match = "~/Projects/sarcasticbird"
include_credentials = ["github-sarcasticbird"]
```

Inside a matching Coop, Git sees only the temporary helper override and `gh`
uses `GH_TOKEN`; `gh auth login` is unnecessary and must not persist the token
to its guest configuration. Verify without displaying the token:

```sh
gh auth status
GIT_TERMINAL_PROMPT=0 git ls-remote https://github.com/sarcasticbird/coop.git HEAD
```

## Agent authentication

Codex, Claude, OpenCode, and similar clients own their interactive OAuth,
device authorization, refresh tokens, account switching, and storage
migrations. Coop keeps that provider-owned state in the agent's isolated
per-project volume. It does not copy a host `auth.json` or equivalent login
store by default.

Coop should broker an agent credential only when the agent documents a
non-persistent environment or temporary-file interface. Portable rules and
configuration may still be seeded when they contain no secrets.

## Migrating from credential files and secret seeds

The broker replaces four persistent-copy behaviors with explicit ownership and
shorter lifetimes:

| Previous behavior | Broker behavior | Security effect |
| --- | --- | --- |
| PAT stored in a host plaintext file | PAT resolved by the host Git helper, preferably Keychain | Protects the current host copy at rest |
| Matching project seeds a credential file | Trusted `[[projects]]` selects a named grant | Repository configuration cannot grant or retarget it |
| Guest retains a Git store across entries | One command receives a temporary store under tmpfs | Later uncredentialed entries do not inherit it |
| Host agent login state is seeded | Agent performs its own login in its project volume | OAuth and refresh-token lifecycle stays provider-owned |

1. Add and verify the host-native credential backend. For GitHub on macOS, use
   the Keychain recipe above.
2. Add a `git-credential` grant and bind it to the narrowest `[[projects]]`
   path.
3. Remove seeds for `.git-credentials`, custom `.config/git/credentials-*`
   stores, `.netrc`, `.config/gh/hosts.yml`, AWS credentials, kube config,
   Docker config, or agent login files.
4. Replace any `source = { type = "file", ... }` used with
   `git-credential-store`.
5. Run `coop doctor`. Sensitive seeds are failures; legacy plaintext Git store
   sources are migration warnings.
6. Enter a matching project and verify Git and the intended client.
7. Re-run the backend-specific exact-record and negative-control checks. After
   they succeed for every migrated grant, delete the old host plaintext
   credential source files. Name each reviewed file explicitly; do not
   recursively delete a broad config or backup directory.
8. Clean old guest copies after confirming the broker works.

Deleting an untracked plaintext source is permanent: Git cannot recover it.
Keep the host-native backend verified and retain only configuration backups
that do not embed secret values. Run `coop doctor` again after deletion.

Moving an existing token into Keychain does not revoke copies that previously
existed in plaintext files or guests. Rotate it at the provider to invalidate
that prior exposure. Rotation may be deferred operationally, but it remains an
explicit outstanding remediation rather than a property of the migration.

Coop does not automatically delete old secrets. Check both the disposable
container filesystem and persistent agent volumes for prior copies. Also check
the former host seed locations; deleting a seed rule does not delete its source
file. Common locations include:

- `~/.git-credentials` and custom `~/.config/git/credentials-*` files;
- `~/.config/gh/hosts.yml`;
- `~/.codex/auth.json` and corresponding provider login stores;
- `~/.aws/credentials`, `~/.kube/config`, and `~/.docker/config.json`.

Removing a project with `coop destroy` also deletes its agent-state volumes and
history. Use that only when this broader deletion is intended; otherwise remove
confirmed legacy files from the guest explicitly.

An already-running entry is unaffected by a host configuration edit. Preserve
it when necessary, then destroy or explicitly clean that Coop after the command
finishes. New disposable Coops are the clearest proof that no removed seed or
previous agent state influenced the broker test.

## Troubleshooting

- **`git credential fill` fails:** run the non-printing host verification above.
  Check the URL path, `credential.useHttpPath`, helper ordering, and Keychain
  access prompts.
- **Keychain asks for access:** the prompt is host policy. Inspect the requesting
  Git binary and grant only the access you intend.
- **The grant is not selected:** check the canonical project root with
  `coop status`, the `[[projects]].match` component boundary, and the exact
  `include_credentials` name.
- **Exposure collision:** two selected grants claim the same environment name or
  specialized Git/AWS interface. Combine projections from one source or select
  only one owner.
- **Git works but `gh` does not:** ensure the same grant includes the
  `environment` projection with `name = "GH_TOKEN"` and `field = "password"`.
- **`gh` works but Git does not:** ensure `git-credential-store` is present and
  no guest wrapper removes the process-local `GIT_CONFIG_*` variables.

See [configuration.md](configuration.md#credentials) for the exact schema and
[security-model.md](security-model.md#session-credentials) for remaining risk.
