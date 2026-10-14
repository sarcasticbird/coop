# GitHub Apps for named agents

An App gives each agent its own bot identity without a separate human account.
Install it separately on each code or planning owner. Keep its signing key on
the Mac, outside repositories and guests, in a regular mode-0600 file. A private
registration JSON file records the App ID, slug, bot identity, owner and
installation IDs, expected permissions, and signing-key path.

```json
{
  "app_id": 1234,
  "slug": "example-agent",
  "app_owner": "planning-owner",
  "private_key_path": "/Users/me/Library/Application Support/coop/credentials/example.pem",
  "bot_login": "example-agent[bot]",
  "bot_id": 5678,
  "installations": {
    "code-owner": {
      "id": 9012,
      "repositories": "all",
      "permissions": {"contents": "write", "metadata": "read", "issues": "write", "pull_requests": "write"}
    },
    "planning-owner": {
      "id": 9013,
      "repositories": "all",
      "permissions": {"metadata": "read", "discussions": "write", "organization_projects": "write"}
    }
  }
}
```

The `coop-github` helper uses the existing command credential source. It signs
requests on the host and validates the App, installation owners and granted
permissions before returning short-lived tokens to Coop. The guest receives a
file in tmpfs. No signing key is copied into a VM. Generic command credentials
do not report expiration metadata; the helper validates expiration itself.
Do not set `require_expiration` for this source.

```toml
[credentials.example-github]
source = { type = "command", argv = ["/Users/me/.local/libexec/coop-github", "mint", "--registration", "/Users/me/.config/coop/agents/example/github-app.json"] }
expose = [{ type = "file", path_env = "COOP_GITHUB_APP_BUNDLE_FILE" }]

[profiles.example]
command = ["/usr/local/libexec/coop-github", "env", "--app-id", "1234", "--", "opencode"]
orgs = ["code-owner"]
include_credentials = ["example-github"]
```

Build the host and Linux arm64 helpers from `cmd/coop-github`. Install the host
executable outside project directories. Stage the guest executable with:

```sh
bash scripts/install-github-auth.sh example /absolute/path/to/linux-helper 1234
```

The installer checks architecture, checksum and guest identity. It does not
restart the machine. Add first-boot seeds for the executable at
`/usr/local/libexec/coop-github` and a mode-0600 App-ID text file at
`/var/lib/coop/github-app-id` for future machines. Changing an existing profile
command requires stopped-machine `apply`. Preserve its disk and provider login;
rebuilding a base image does not migrate an existing disk.

Inside the wrapped agent, Git HTTPS uses the URL's owner. `gh` selects an owner
from `--repo`, `--owner`, a REST endpoint, or the current repository's origin.
Opaque GraphQL and global endpoints require explicit context:

```sh
COOP_GITHUB_OWNER=planning-owner gh project list --owner planning-owner
COOP_GITHUB_OWNER=planning-owner gh api graphql -f 'query=query { viewer { login } }'
/usr/local/libexec/coop-github status --json
```

An installation token covers its installed account. Selecting another owner
does not combine two installations' permissions in one API request. In
particular, an organization Project token cannot read a private repository
owned by a personal account. Native cross-owner private issue items require
separate acceptance; do not assume two installations make that workflow work.
An organization Project draft item can hold a link to a personal issue while
repository operations use the personal installation. Keep its summary suitable
for everyone who can access the Project.

Unknown owners, conflicting contexts, unsupported hosts and expired tokens fail
without using a human credential. Each invocation reads the current generation,
even if its parent holds an old credential-file path. Credential-printing/login
commands and inherited network tracing are disabled by the helper. Guest root
can bypass these controls and read its tokens. These are accidental-disclosure
controls, not secret isolation from the agent.

Run `coop credentials watch --profile example --interval 15m --retry 1m` under
a host supervisor for unattended renewal. It refreshes running agents only and
never starts a stopped machine. Failed acquisition leaves the prior generation
intact. On laptop sleep or lost connectivity, expiration produces a clear error.
After wake and renewal, new operations use fresh credentials without restarting
the coding process. Task checkpointing and retry scheduling are separate worker
capabilities.

Do not retire a working PAT until migration, owner routing and renewal pass
acceptance. Revoke an exposed PAT before further use. Deleting a local file does
not revoke provider access. Confirm provider revocation before deleting retired
token files or backups. App registration and renewal do not install a worker or
promise automatic startup after a host reboot.
