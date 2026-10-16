# Host MCP control

Coop exposes owned workspaces through one authenticated MCP service per host. Connect to blackbook and rack-mini as separate servers. Each host owns its runtime, operation ledger and caller credentials. A disconnected client does not stop accepted work or cause it to run again.

```sh
coop mcp serve --config /absolute/private/server.toml
coop mcp stdio --config /absolute/private/client.toml
```

`serve` listens on literal loopback and serves `/mcp`. It does not start Apple Container or any VM when a client connects. `stdio` forwards to one already-running service. Its stdout contains MCP protocol messages only. Remote connections require HTTPS. Plain HTTP is allowed only for literal loopback IP addresses.

## Private configuration

Keep server/client configuration and credentials in a private machine configuration directory, such as `$XDG_CONFIG_HOME/coop/mcp`. Keep these files in private dotfiles if desired. Do not put them in the public Coop repository or project `.coop.toml`.

The configuration directory must belong to the current user and have mode 0700. Files must be regular, user-owned files with no group/other permissions, normally 0600. Symlinks in their paths are refused. The state root must use a physical absolute path, including on macOS where `/var` is a symlink to `/private/var`.

Replace the example absolute paths with the host's paths:

```toml
version = 1
label = "blackbook"
listen = "127.0.0.1:9876"
state_dir = "/Users/you/.local/state/coop/mcp"
verifier_file = "/Users/you/.config/coop/mcp/verifiers.json"
allowed_hosts = ["127.0.0.1"]
allowed_origins = []

[[callers]]
id = "chief"
workspaces = ["agent:robin", "agent:hawk", "agent:wren"]
capabilities = ["read", "ping", "worker_control", "lifecycle", "exec"]
```

The host configuration controls access. Developer agents have no implicit MCP access. Workspace selectors use `agent:<profile>` or `interactive:<owned-runtime-name>`. Tools accept only host-bound references returned by `coop_list`, never host paths. An optional `[[workspaces]]` binding has `mode = "interactive"`, `name`, and an absolute canonical `project` path. It must match the stored ownership record.

The verifier file is a JSON object mapping caller IDs to SHA-256 hashes of their complete bearer tokens. Generate an independent token for each caller on each host. Use 32 random bytes encoded as base64url. Do not send a token in a tool argument, CLI argument, issue or log. To provision a private fixture or an explicitly approved caller, this Python snippet writes files without printing the token:

```python
import hashlib, json, os, pathlib, secrets
root = pathlib.Path(os.environ["COOP_MCP_PRIVATE_DIRECTORY"])
root.mkdir(mode=0o700, parents=True, exist_ok=True)
os.chmod(root, 0o700)
token = secrets.token_urlsafe(32)
for name, content in {
    "chief.token": token + "\n",
    "verifiers.json": json.dumps({"chief": hashlib.sha256(token.encode()).hexdigest()}) + "\n",
}.items():
    fd = os.open(root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(content)
```

This creates new files only. Rotation must preserve other caller verifiers. Replace the private verifier file/config atomically, then send SIGHUP to the MCP service. Reload validates the entire caller policy before replacing it. Invalid reload leaves the old policy active. Changing listeners, state paths, bindings or limits requires an ordinary service restart; these fields do not change during policy reload. Do not reuse a retired caller ID for a different person or agent while its operation records exist.

The stdio client configuration references a token file or environment variable, with exactly one source:

```toml
endpoint = "http://127.0.0.1:9876/mcp"
expected_host_id = "h1_0123456789abcdef0123456789abcdef"
token_file = "/Users/you/.config/coop/mcp/chief.token"
# Alternatively use token_env = "COOP_BLACKBOOK_MCP_TOKEN".
```

Read the actual host ID from the server's structured startup log or authenticated `coop_host_info` response. The adapter rejects another host at that endpoint and refuses redirects. HTTP clients may connect directly with an Authorization bearer header. Test the chosen client's header support before installing ingress. The private token mode does not implement OAuth discovery or cloud-client onboarding.

## Workspaces and tools

Configured agents appear even when absent. Legacy interactive Coops require local owner registration before MCP control. Start the MCP service once to initialize its private identity, then register the existing Coop from another terminal:

```sh
coop mcp register --config /absolute/private/server.toml --project /absolute/canonical/project
```

Registration checks its deterministic name, current project mount/labels, image and specification, then saves ownership. It does not boot, seed or change the runtime. It cannot bypass maintenance on an already-owned workspace. Add its `interactive:<name>` selector to the caller's allowlist and reload policy. Arbitrary inventory adoption is not an MCP tool.

All successful tool results include `host_id`, `host_label` and `data`. Workspace and operation references from a different host are rejected.

| Tool | Required capability | Behavior |
| --- | --- | --- |
| `coop_host_info`, `coop_list`, `coop_status` | `read` | Host health, caller-visible inventory and current/desired configuration |
| `coop_worker_status`, `coop_worker_requests` | `read` | Safe worker status and wake summaries, with private prompts omitted |
| `coop_ping` | `ping` | Queue an optional literal prompt for a running worker |
| `coop_worker_control` | `worker_control` | Request pause/resume; never abort a native turn or clear recovery |
| `coop_lifecycle` | `lifecycle` | Start, stop or restart through existing lifecycle behavior |
| `coop_exec` | `exec` | Run exact argv/cwd/stdin in a running guest, without a PTY |
| `coop_operation_status` | `read` plus original caller | Read state and bounded output pages |
| `coop_operation_cancel` | Target action capability plus original caller | Journal a cancellation request for an exact operation |

No tool exposes host shell execution, raw credentials, goal approval, destroy, rebuild, apply, restore, backup, migration or arbitrary CLI passthrough. Use the existing CLI for those workflows.

## Requests and outcomes

Every mutation requires `request_id` and `created_at`. Reuse the complete input for retries. Generate a new ID for new intent. The request ID embeds its immutable UTC creation time so an expired ID cannot be made new by changing its timestamp:

```python
import datetime, secrets, time
milliseconds = time.time_ns() // 1_000_000
request_id = f"r1.{milliseconds:013d}.{secrets.token_hex(16)}"
created_at = datetime.datetime.fromtimestamp(
    milliseconds / 1000, datetime.timezone.utc
).isoformat(timespec="milliseconds")
```

Requests older than seven days or over five minutes in the future are rejected. Identical retries return the original operation; changed input under the same ID is a conflict. Pruning captured output retains retry evidence through the seven-day window.

`coop_exec` accepts `workspace`, `request_id`, `created_at`, `argv`, optional absolute guest `cwd`, optional `stdin` or `stdin_base64`, and `timeout_seconds` from 1 to 600. Text/base64 stdin are mutually exclusive. Argument boundaries are preserved. A literal `$HOME;...` argument stays literal. An explicit guest `sh -c` is possible for an exec-authorized caller. Commands preserve the Coop's existing guest environment and GitHub routing; they do not acquire credentials, start a VM or provision repositories.

`coop_lifecycle` accepts `action` of `start`, `stop` or `restart` and an explicit `interrupt_active` boolean. Stop/restart refuses known active or unknown worker activity without that flag. Explicit start retains normal configured preparation. Restart saves its stop/start phases and rechecks authorization and ownership before each side effect.

`coop_ping` accepts an optional `prompt`. Its response returns a durable host operation and deterministic guest wake ID. `host_pending` means the host accepted work; `queued` means the guest queue accepted it. Neither means the agent replied. Use `coop_worker_requests` with `wake_id` to observe delivery/reporting. Lost acknowledgements are reconciled by the exact wake ID, without another queue write.

Operation states are `accepted`, `running`, `succeeded`, `failed`, `cancel_requested`, `cancelled` and `indeterminate`. Only an observed guest completion sets an exit code. A nonzero guest exit is a failed command with its real code. Losing the Container connection does not invent a guest exit or prove termination. Killing the Mac-side CLI is not proof that a guest process died.

Cancellation requires `operation_id`, `request_id` and `created_at`. It returns the cancellation request's durable operation ID; inspect the target operation separately. Cancelling queued work prevents dispatch. A running command may remain indeterminate with a workspace hold until an explicitly authorized stop proves its guest execution is gone. Transport cancellation or closing stdio never implicitly cancels accepted work.

After a service restart, dispatched exec is never replayed. Saved lifecycle/wake state is reconciled using current authorization and runtime evidence. A saved restart stopped phase can resume only with unchanged configuration and current authority. Uncertain start/preparation is not declared successful just because a VM is running.

## Bounds and local administration

Defaults are four active operations, one conflicting operation per workspace, a ten-minute ceiling, 64 KiB each for argv/stdin, 256 KiB retained per output stream and 64 KiB maximum output pages. Output continues draining after its retention cap. UTF-8 paging preserves code point boundaries; binary output uses base64 and byte offsets. `total_bytes`, `dropped_bytes` and `truncated` report retention loss. An offset inside a UTF-8 code point is rejected.

Retain at most 256 terminal outputs for seven days, 256 unresolved operations and 4,096 total operation/idempotency records. New work is rejected at capacity rather than discarding unresolved work or unexpired retry evidence. Private state can contain sensitive argv/stdin/prompts/output. Audit metadata excludes those fields and credentials.

The host owner can inspect safe local operation metadata without an MCP caller credential:

```sh
coop mcp inspect --config /absolute/private/server.toml --operation o1.HOST.RANDOM
```

## Staging and acceptance

Build on the development Mac. `scripts/deploy-mcp.sh --stage --host <host> --binary <binary> --config <absolute-private-config> --destination <absolute-empty-stage-directory> --runtime-bin <absolute-container-executable>` creates a private local stage with the binary, launchd plist and activation JSON. It never copies private credentials, runs SSH, bootstraps launchd, starts Container or changes Tailscale. The staged launchd job includes the Container executable directory in its explicit PATH. Verify that same executable path and Container service on the target before activation. Install the reviewed artifacts on the target host only after reviewing the packet. A Git checkout is not required there.

Use a dedicated private Tailscale Serve listener after reading existing Serve/Funnel configuration. Add the exact external hostname to `allowed_hosts`; add browser origins only if needed. Do not share a public Funnel listener, overwrite an existing route or treat Tailscale identity headers as caller authentication. Validate HTTPS plus bearer auth from the intended client, including the future chief-of-staff guest. Host tailnet reachability does not prove guest reachability.

`scripts/uat-mcp.sh --agent mcp-uat-contracts` runs the isolated contract suite and creates no live VM. On macOS set a physical `TMPDIR` if existing worker tests reject `/var` symlinks. Real VM start/stop/restart, persistence, tailnet/guest reachability and two-host acceptance require the separate reviewed disposable-fixture rollout packet. An unavailable rack-mini runtime is an acceptance gap.

Rollback unloads only the new MCP launchd job and removes only its dedicated reviewed Serve route. Preserve its private ledger/config for inspection. It never restores older bird worker state or removes guest disks.

Terminal output and expired retry records are pruned at startup, at least once per minute while running, and before remote status retrieval. Unresolved operations remain available for recovery and are never pruned automatically.
