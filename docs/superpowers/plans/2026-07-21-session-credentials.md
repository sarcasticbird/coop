# Session Credentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add trusted, named credential grants that Coop acquires on the host and exposes only for an interactive guest entry, selected by `include_credentials` and `--credentials`.

**Architecture:** Configuration describes acquisition (`file`, `command`, or `aws-profile`) separately from guest injection (`environment`, `file`, `git-credential-store`, or `aws`). A new credential package resolves and acquires grants, builds a bounded secret bundle, stages it through stdin under guest `/dev/shm`, wraps the interactive command, and cleans up afterward. The runtime stops replacing Coop with `unix.Exec` so session orchestration can preserve the guest exit code while performing cleanup and optional revocation.

**Tech Stack:** Go 1.26.5, Cobra/pflag, BurntSushi TOML, Apple `container` CLI 1.x, Go standard-library `archive/tar`, `os/exec`, and `os/signal`.

## Global Constraints

- Support remains macOS 26 or later on Apple silicon.
- `include_credentials` and `[credentials]` take effect only from trusted user configuration; project `coop.toml` cannot grant host access.
- Selected credentials are the ordered, deduplicated union of `include_credentials` and `--credentials`.
- The public flag is `--credentials`; it accepts comma-separated values and repeated uses before the first guest-command token.
- Never place secret values in runtime argv, labels, mounts, persistent container environment, project files, seeds, or named volumes.
- Secret bundle directories live below `/dev/shm/coop-credentials`, use mode `0700`, and contain mode `0600` secret files.
- Preserve user argv as a vector; never join user arguments into shell source.
- A root-capable guest process can read credentials during an active entry; documentation must retain that limitation and unrestricted-egress warning.
- Configure at most 32 grants, select at most 16 grants per entry, limit one acquired payload to 1 MiB, and limit the complete bundle to 8 MiB.
- Host acquisition commands run directly from argv with a ten-minute deadline; stdout is secret payload, while stdin and stderr remain attached to the terminal. They use a dedicated foreground process group, group-wide cancellation, and a bounded `WaitDelay` so descendants cannot outlive cancellation or hold the stdout pipe indefinitely.
- After every Go change, run `gofmt` and `go vet ./...` before any approved commit.
- Do not commit unless the user has explicitly approved commits. Every commit step below is a gated checkpoint, not authorization.

## File Map

- Modify `internal/config/config.go`: credential TOML types, trusted merge, and structural validation.
- Modify `internal/config/config_test.go`: global-only trust and invalid-configuration coverage.
- Create `internal/credential/types.go`: selected-spec, acquired-material, lease, secret-file, metadata, and limits.
- Create `internal/credential/select.go`: ordered selection, unknown-name checks, and injection conflict detection.
- Create `internal/credential/acquire.go`: bounded file/command acquisition and reverse-order rollback.
- Create `internal/credential/aws.go`: AWS process-credential parsing and expiration enforcement.
- Create `internal/credential/bundle.go`: injection adapters and bounded guest bundle construction.
- Create `internal/credential/guest.go`: tar staging, stale-lease scrub, command wrapper, and cleanup.
- Create `internal/jobcontrol/jobcontrol.go`: shared foreground process-group setup and terminal restoration.
- Create focused `internal/credential/*_test.go` files beside each responsibility.
- Modify `internal/runtime/runtime.go`: waiting interactive execution and typed exit status.
- Delete `internal/runtime/exec_unix.go`: obsolete `unix.Exec` wrapper; `x/sys` remains required by `internal/lock`.
- Modify `internal/runtime/mock.go` and `internal/runtime/validate_test.go`: error injection and interactive behavior tests.
- Create `internal/session/entry.go`: acquire → up → stage → enter → cleanup orchestration.
- Modify `internal/session/session.go` and `internal/session/session_test.go`: raw argv preparation and entry integration.
- Modify `cmd/coop/main.go` and `cmd/coop/main_test.go`: flag parsing, TUI propagation, unsupported-command rejection, and exit-code plumbing.
- Modify `internal/doctor/doctor.go` and `internal/doctor/doctor_test.go`: sensitive-seed migration warnings.
- Modify `README.md` and `image/Containerfile`: public configuration, security, and migration guidance.

---

### Task 1: Trusted credential configuration

**Files:**
- Modify: `internal/config/config.go:49-280`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.IncludeCredentials []string`, `Config.Credentials map[string]Credential`, `CredentialSource`, and `CredentialInjection`.
- Consumed by: Tasks 2, 3, 6, and 7.

- [ ] **Step 1: Add failing global-only merge tests**

Add table-driven tests that decode the approved inline-table syntax and prove a project file cannot define or include credentials:

```go
func TestCredentialConfigGlobalOnly(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
include_credentials = ["git"]
[credentials.git]
source = { type = "file", path = "~/.git-credentials" }
inject = { type = "git-credential-store" }
`)
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
include_credentials = ["aws-prod"]
[credentials.aws-prod]
source = { type = "command", argv = ["steal-host-secret"] }
inject = { type = "environment", name = "AWS_SECRET_ACCESS_KEY" }
`)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if diff := strings.Join(cfg.IncludeCredentials, ","); diff != "git" {
		t.Fatalf("project changed included credentials: %q", diff)
	}
	if _, ok := cfg.Credentials["aws-prod"]; ok {
		t.Fatal("project defined a host credential grant")
	}
	if got := cfg.Credentials["git"].Source.Path; got != "~/.git-credentials" {
		t.Fatalf("global grant missing: %q", got)
	}
}
```

Add invalid cases for bad grant names, more than 32 grants, missing source fields, missing injection fields, invalid environment names, `aws` injection without `aws-profile`, `aws-profile` with a non-`aws` injection, `git-credential-store` without a file source, and `require_expiration` on a source that cannot report expiration.

- [ ] **Step 2: Run the config tests and confirm the new schema fails**

Run: `go test ./internal/config -run 'Credential' -v`

Expected: FAIL because credential fields and validation do not exist.

- [ ] **Step 3: Add exact TOML types and constants**

Add these types beside `Seed` and fields to `Config`:

```go
type Credential struct {
	Source            CredentialSource    `toml:"source"`
	Inject            CredentialInjection `toml:"inject"`
	RequireExpiration bool                `toml:"require_expiration"`
}

type CredentialSource struct {
	Type    string   `toml:"type"`
	Path    string   `toml:"path"`
	Argv    []string `toml:"argv"`
	Profile string   `toml:"profile"`
}

type CredentialInjection struct {
	Type    string `toml:"type"`
	Name    string `toml:"name"`
	PathEnv string `toml:"path_env"`
}

type Config struct {
	Image              Image                 `toml:"image"`
	Resources          Resources             `toml:"resources"`
	Agents             map[string]Agent      `toml:"agents"`
	Seeds              []Seed                `toml:"seed"`
	IncludeCredentials []string              `toml:"include_credentials"`
	Credentials        map[string]Credential `toml:"credentials"`
	SSH                bool                  `toml:"ssh"`
}

const (
	MaxCredentialGrants   = 32
	MaxSelectedCredentials = 16
)
```

Use a shared lowercase-name validator and `^[A-Za-z_][A-Za-z0-9_]*$` for environment names. Validate exact source/injection combinations; reject unused fields rather than silently accepting ambiguous configuration.

- [ ] **Step 4: Merge credential capabilities only from trusted config**

In `mergeFile`, copy definitions and inclusion only under `trusted`:

```go
if trusted {
	if cfg.Credentials == nil {
		cfg.Credentials = make(map[string]Credential)
	}
	for name, grant := range layer.Credentials {
		cfg.Credentials[name] = grant
	}
	cfg.IncludeCredentials = append(cfg.IncludeCredentials, layer.IncludeCredentials...)
	cfg.Seeds = append(cfg.Seeds, layer.Seeds...)
}
```

After merge, reject any `include_credentials` entry not present in `cfg.Credentials`; deduplicate included names while preserving their order.

- [ ] **Step 5: Run and format the config package**

Run: `gofmt -w internal/config/config.go internal/config/config_test.go`

Run: `go test ./internal/config -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Run: `git diff --check && git diff -- internal/config`

Only with explicit commit approval:

```sh
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add trusted credential configuration"
```

### Task 2: Selection and host acquisition core

**Files:**
- Create: `internal/credential/types.go`
- Create: `internal/credential/select.go`
- Create: `internal/credential/select_test.go`
- Create: `internal/credential/acquire.go`
- Create: `internal/credential/acquire_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.Credential`, and `config.ExpandHome` from Task 1.
- Produces: `Resolve(config.Config, []string) ([]Selected, error)`, `Manager.AcquireAll(context.Context, string, []Selected) ([]Acquired, error)`, `RevokeAll(context.Context, []Acquired) error`, and the shared `GuestLease` path type.

- [ ] **Step 1: Write selection and conflict tests**

Cover ordered union, comma-token trimming performed by callers, unknown grants, the 16-grant cap, duplicate environment variables, duplicate path environment variables, multiple Git adapters, and AWS collisions with its three standard variables:

```go
func TestResolveIncludesDefaultsThenRequested(t *testing.T) {
	cfg := config.Config{
		IncludeCredentials: []string{"git", "shared"},
		Credentials: map[string]config.Credential{
			"git":    fileGrant("git-credential-store", ""),
			"shared": commandEnvGrant("SHARED_TOKEN"),
			"aws-dev": awsGrant("dev"),
		},
	}
	got, err := Resolve(cfg, []string{"aws-dev", "git"})
	if err != nil {
		t.Fatal(err)
	}
	if names := selectedNames(got); !slices.Equal(names, []string{"git", "shared", "aws-dev"}) {
		t.Fatalf("selection order = %v", names)
	}
}

func fileGrant(injectType, pathEnv string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "file", Path: "~/.secret"},
		Inject: config.CredentialInjection{Type: injectType, PathEnv: pathEnv},
	}
}

func commandEnvGrant(name string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "command", Argv: []string{"secret-tool"}},
		Inject: config.CredentialInjection{Type: "environment", Name: name},
	}
}

func awsGrant(profile string) config.Credential {
	return config.Credential{
		Source: config.CredentialSource{Type: "aws-profile", Profile: profile},
		Inject: config.CredentialInjection{Type: "aws"},
	}
}

func selectedNames(selected []Selected) []string {
	names := make([]string, len(selected))
	for i := range selected {
		names[i] = selected[i].Name
	}
	return names
}
```

- [ ] **Step 2: Define non-serializing credential types**

Use private secret fields and explicit safe metadata accessors:

```go
type Selected struct {
	Name string
	Spec config.Credential
}

type Metadata struct {
	Provider  string
	Profile   string
	AccountID string
	ExpiresAt time.Time
}

type Acquired struct {
	Selected Selected
	payload  []byte
	aws      *AWSCredentials
	metadata Metadata
	revoke   func(context.Context) error
}

type GuestLease struct {
	Dir string
}

func (a *Acquired) Revoke(ctx context.Context) error {
	if a.revoke == nil {
		return nil
	}
	return a.revoke(ctx)
}

func RevokeAll(ctx context.Context, acquired []Acquired) error {
	var errs []error
	for i := len(acquired) - 1; i >= 0; i-- {
		if err := acquired[i].Revoke(ctx); err != nil {
			errs = append(errs, fmt.Errorf("revoke credential %q: %w", acquired[i].Selected.Name, err))
		}
	}
	return errors.Join(errs...)
}
```

Do not implement `String`, `GoString`, JSON, or TOML marshaling on secret-bearing types.

- [ ] **Step 3: Implement deterministic resolution**

`Resolve` appends `cfg.IncludeCredentials` then requested names, deduplicates with a map, looks up every definition, and claims injection resources before returning. Return errors such as `credential "aws-dev": unknown grant` and `credentials "one" and "two" both inject GH_TOKEN` without rendering specs.

- [ ] **Step 4: Write failing bounded-acquisition tests**

Use dependency injection for file opening and command execution:

```go
type Runner func(context.Context, []string) ([]byte, error)

type Manager struct {
	OpenFile func(string) (*os.File, error)
	Run      Runner
	Now      func() time.Time
}
```

Test home expansion, single-open descriptor validation, regular-file enforcement, a 1 MiB payload boundary, command argv preservation with no shell, a ten-minute context deadline, reverse-order revocation after a later failure, and errors that never contain payload bytes.

- [ ] **Step 5: Implement file and command acquisition**

Provide `NewManager()` with production dependencies. File acquisition must open once in nonblocking mode, validate and size-check that descriptor, then read through an `io.LimitReader` so a changing or special source cannot cause an unbounded allocation or a stat/read race. Command acquisition must connect stdout to an explicit OS pipe and read through `io.LimitReader(MaxPayloadBytes + 1)`. On the extra byte it must kill the helper's complete process group immediately rather than continue discarding output. If the direct helper exits while a descendant retains the pipe, wait only the bounded `WaitDelay`, kill the group, and return `exec.ErrWaitDelay`.

`Manager.runAcquisitionCommand` supplies the ten-minute child context. `AcquireAll` must stop when that context is canceled, revoke acquired entries in reverse order with `context.WithoutCancel`, and return an `errors.Join` that retains the acquisition error without secret contents.

- [ ] **Step 6: Run the credential core tests**

Run: `gofmt -w internal/credential/*.go`

Run: `go test ./internal/credential -run 'Resolve|Acquire' -v`

Expected: PASS.

- [ ] **Step 7: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add internal/credential
git commit -m "feat: resolve and acquire credential grants"
```

### Task 3: AWS parsing and injection bundle adapters

**Files:**
- Create: `internal/credential/aws.go`
- Create: `internal/credential/aws_test.go`
- Create: `internal/credential/bundle.go`
- Create: `internal/credential/bundle_test.go`
- Modify: `internal/credential/acquire.go`

**Interfaces:**
- Consumes: `Selected` and `Acquired` from Task 2.
- Produces: `ParseAWSProcess([]byte, time.Time, bool) (AWSCredentials, Metadata, error)`, `BuildBundle([]Acquired, GuestLease) (Bundle, error)`, and `Summaries([]Acquired) []string`.

- [ ] **Step 1: Write AWS parser tests with redaction assertions**

Cover version 1, optional session token, RFC3339 expiration, missing/expired required expiration, malformed JSON, and errors that do not contain `AccessKeyId`, `SecretAccessKey`, or `SessionToken` values:

```go
func TestParseAWSProcessRequiresFutureExpiration(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"Version":1,"AccessKeyId":"AKIASECRET","SecretAccessKey":"hidden","SessionToken":"token","Expiration":"2026-07-21T13:00:00Z"}`)
	creds, meta, err := ParseAWSProcess(raw, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID == "" || !meta.ExpiresAt.After(now) {
		t.Fatalf("parsed credentials or expiration missing")
	}
}
```

- [ ] **Step 2: Implement AWS process parsing and acquisition**

Define:

```go
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}
```

`aws-profile` acquisition must construct exactly:

```go
[]string{"aws", "configure", "export-credentials", "--profile", profile, "--format", "process"}
```

Parse into a private wire struct, validate non-empty key fields, validate expiration when required, copy into `AWSCredentials`, then zero the raw payload slice before returning the structured value.

- [ ] **Step 3: Write bundle adapter tests**

Assert exact environment claims and file modes for:

- command → `environment` (`GH_TOKEN`);
- file → `file` (`KUBECONFIG` points at a lease-local path);
- file → `git-credential-store` (an empty `credential.helper` entry resets
  inherited helpers before a second entry selects the temporary store);
- aws-profile → `aws` (unset inherited access-key, secret-key, and session-token variables, then set `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE=coop`, and `AWS_EC2_METADATA_DISABLED=true`);
- embedded-newline rejection for environment injection; and
- the 8 MiB complete-bundle limit; and
- safe summaries that include grant/provider/profile/expiration or `validity: source-managed` but never payload, keys, tokens, or file contents.

- [ ] **Step 4: Implement the bundle representation**

Use logical relative paths only:

```go
type SecretFile struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

type Bundle struct {
	Files    []SecretFile
	Env      map[string][]byte
	UnsetEnv []string
	Metadata []NamedMetadata
}

type NamedMetadata struct {
	Name string
	Metadata
}
```

`BuildBundle` receives the final guest lease directory so it can generate path environment values. Sort environment names and file paths before serialization. Use indexed internal filenames rather than host basenames. Build the AWS shared file with profile `[coop]`; never reuse a host profile name inside the guest.

`Summaries` must return deterministic safe strings such as `git (file; validity: source-managed)` and `aws-dev (aws-profile dev; expires 2026-07-21T13:00:00Z)`. It consumes only `Selected.Name` and `Metadata`, never `payload` or `aws`.

- [ ] **Step 5: Run adapter tests**

Run: `gofmt -w internal/credential/*.go`

Run: `go test ./internal/credential -run 'AWS|Bundle|Injection' -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add internal/credential
git commit -m "feat: build provider-neutral credential bundles"
```

### Task 4: Guest staging, wrapper, scrub, and cleanup

**Files:**
- Create: `internal/credential/guest.go`
- Create: `internal/credential/guest_test.go`

**Interfaces:**
- Consumes: `Bundle` from Task 3 and the existing `runtime.Runtime.Exec` method.
- Produces: an `Executor` interface containing only `ExecContext(context.Context, string, []string, io.Reader) error`, plus `NewGuestLease(io.Reader) (GuestLease, error)`, context-aware `Stage` and `Scrub`, `Wrap(GuestLease, []string) []string`, and `Cleanup` invoked with a non-canceled cleanup context.

- [ ] **Step 1: Write tar and argv-safety tests**

Use a small fake implementing only `Exec`. Inspect the tar stream with `archive/tar` and assert:

- no secret appears in `Exec` argv;
- the lease directory is `/dev/shm/coop-credentials/<32 lowercase hex chars>`;
- directory/file modes are `0700`/`0600`;
- environment names are stored in a sorted manifest;
- `Wrap` appends the original argv after fixed positional arguments without joining it; and
- cleanup targets only the generated final lease path and its matching `.staging` path.

- [ ] **Step 2: Implement random lease IDs and bounded tar staging**

Generate the shared `GuestLease` type from Task 2 with:

```go
func NewGuestLease(r io.Reader) (GuestLease, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return GuestLease{}, err
	}
	id := hex.EncodeToString(b[:])
	return GuestLease{Dir: "/dev/shm/coop-credentials/" + id}, nil
}
```

Build a tar stream from trusted logical paths. The fixed staging script must create a `.staging` directory, extract from stdin, reject an existing final directory, and atomically `mv -T` into place. Pass only the generated final path as a positional argument. Scrubbing must recognize both generated final IDs and generated `<id>.staging` names while rejecting every other directory name; cleanup removes both paths so a transport error after guest staging cannot strand material.

```go
type Executor interface {
	ExecContext(context.Context, string, []string, io.Reader) error
}

const stageScript = `set -eu
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
```

- [ ] **Step 3: Implement the fixed guest wrapper**

Use a constant script equivalent to:

```sh
set -eu
lease=$1
shift
process_start() {
  proc_stat=$(cat "/proc/$1/stat") || return 1
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
while IFS= read -r name; do
  value=$(cat "$lease/env/$name")
  export "$name=$value"
done < "$lease/env.list"
set +e
"$@"
status=$?
set -e
exit "$status"
```

`Wrap` returns `[]string{"sh", "-c", guestWrapper, "coop-credential-entry", lease.Dir, ...originalArgv}`. Environment names are validated in Task 1, and the wrapper never evaluates their values as shell source.

- [ ] **Step 4: Implement conservative stale scrub**

The fixed scrub script must remove a lease when its recorded numeric owner PID fails `kill -0`, when the live PID's Linux process start-time token differs from the recorded value, or when its owner is missing, empty, or malformed and its GNU `stat -c %Y` age exceeds 60 seconds. It must skip malformed paths and matching live process identities. A lease disappearing between a check and its `cat` or `stat` is a benign concurrent-cleanup race and continues to the next entry; other scrub failures are returned, never silently ignored.

```sh
set -eu
root=/dev/shm/coop-credentials
test -d "$root" || exit 0
process_start() {
  proc_stat=$(cat "/proc/$1/stat") || return 1
  proc_rest=${proc_stat##*) }
  set -- $proc_rest
  test "$#" -ge 20
  printf '%s\n' "${20}"
}
now=$(date +%s)
for dir in "$root"/*; do
  test -d "$dir" || continue
  owner=$dir/owner
  if test -f "$owner"; then
    owner_identity=$(cat "$owner") || { test ! -d "$dir" && continue; exit 1; }
    set -- $owner_identity
    if test "$#" -eq 2; then
      pid=$1
      owner_start=$2
      case "$pid:$owner_start" in
        (*[!0-9:]*|:*) ;;
        (*)
          if kill -0 "$pid" 2>/dev/null; then
            current_start=$(process_start "$pid") || exit 1
            test "$current_start" = "$owner_start" && continue
          fi
          rm -rf -- "$dir"
          continue;;
      esac
    fi
  fi
  modified=$(stat -c %Y "$dir")
  test $((now - modified)) -gt 60 && rm -rf -- "$dir"
done
```

- [ ] **Step 5: Run guest lifecycle tests**

Run: `gofmt -w internal/credential/guest.go internal/credential/guest_test.go`

Run: `go test ./internal/credential -run 'Stage|Wrap|Scrub|Cleanup' -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add internal/credential/guest.go internal/credential/guest_test.go
git commit -m "feat: stage credential leases in guest memory"
```

### Task 5: Waiting interactive runtime and exact exit codes

**Files:**
- Modify: `internal/runtime/runtime.go:64-91,293-317`
- Modify: `internal/runtime/mock.go:12-159`
- Modify: `internal/runtime/validate_test.go`
- Delete: `internal/runtime/exec_unix.go`

**Interfaces:**
- Produces: `runtime.ExitError`, `runtime.SignalError`, `runtime.NotifyContext`, and a returning `Runtime.ExecInteractive(context.Context, name, workdir string, argv []string) error`.
- Consumed by: Tasks 6 and 7.

- [ ] **Step 1: Add failing exit-code and argv tests**

Create fake `container` scripts that capture argv and exit with a chosen status:

```go
func TestExecInteractiveReturnsGuestExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work", []string{"tool", "a b", "$x"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("exit error = %#v", err)
	}
}
```

Add a separate script that records one argument per line and proves spaces and shell metacharacters remain literal.

- [ ] **Step 2: Define a typed guest exit error**

```go
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("guest command exited %d", e.Code) }
func (e *ExitError) ExitCode() int { return e.Code }
```

Infrastructure errors such as `LookPath`, start failures, or signal-forwarding failures must not be wrapped as `ExitError`.

- [ ] **Step 3: Replace `unix.Exec` with a child process**

Use `exec.Command`, attach `os.Stdin`, `os.Stdout`, and `os.Stderr`, and wait. Launch the runtime client in a dedicated process group, transfer terminal foreground ownership to that group, then restore Coop's original foreground group after the child exits only if that child still owns the terminal. When the child stops, restore Coop's group before suspending Coop itself; after the parent shell continues Coop, give the terminal back to the child and continue its process group. If the shell resumes Coop with `bg`, final cleanup must leave the shell's foreground ownership unchanged. A job-control monitoring failure must kill the child group so neither side remains stopped indefinitely. `runtime.NotifyContext` captures `os.Interrupt`, `syscall.SIGTERM`, and `syscall.SIGHUP` across the complete session lifecycle. The first signal starts graceful cancellation; a second restores default handling and re-raises the signal so a wedged cleanup terminates. Terminal-generated signals reach the child foreground group directly; cancellation from a signal directed at Coop is relayed to the child group, followed by group `SIGKILL` after a two-second grace period if the child has not exited. Relay failures other than the expected post-exit `ESRCH` race are joined into the result and trigger a direct-child kill fallback. Convert only `*exec.ExitError` into `*runtime.ExitError`. A bare guest `ExitError` returns its code without redundant stderr output, while joined cleanup or relay errors remain visible.

Delete `internal/runtime/exec_unix.go`. Do not remove `golang.org/x/sys`; `internal/lock/lock.go` still imports it.

- [ ] **Step 4: Extend the runtime mock for failure ordering tests**

Add:

```go
ExecErr        error
InteractiveErr error
```

Return `ExecErr` after recording an `Exec` call and return `InteractiveErr` after recording an interactive call. This lets Task 6 prove cleanup paths.

- [ ] **Step 5: Run runtime tests**

Run: `gofmt -w internal/runtime/runtime.go internal/runtime/mock.go internal/runtime/validate_test.go`

Run: `go test ./internal/runtime -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add internal/runtime/runtime.go internal/runtime/mock.go internal/runtime/validate_test.go internal/runtime/exec_unix.go
git commit -m "refactor: retain control of interactive exec"
```

### Task 6: Session entry orchestration

**Files:**
- Create: `internal/session/entry.go`
- Modify: `internal/session/session.go:218-235`
- Modify: `internal/session/session_test.go`

**Interfaces:**
- Consumes: credential manager and guest functions from Tasks 2-4; returning runtime execution from Task 5.
- Produces: `Session.Run(cwd string, argv []string, requestedCredentials []string) error`.

- [ ] **Step 1: Add end-to-end session-order tests**

Test these exact properties with `runtime.Mock` and temporary file grants:

- credential acquisition occurs before `Up` mutates runtime state;
- an acquisition failure produces no `RunSpec` and no interactive call;
- staging occurs after seeds and before interactive entry;
- Flox wrapping remains inside the credential wrapper;
- guest failure still triggers host cleanup;
- cleanup failure joins a zero-status result but does not replace a typed non-zero `ExitError`; and
- entries with no selected credentials still scrub stale leases but run the original argv without the wrapper.

- [ ] **Step 2: Extract raw argv preparation from `Enter`**

Move cwd clamping, shell defaulting, and Flox wrapping into:

```go
func (s *Session) entryArgv(cwd string, argv []string) (string, []string) {
	cwd = canonicalizeCwd(cwd)
	if !withinProject(s.Project, cwd) {
		cwd = s.Project
	}
	if len(argv) == 0 {
		return cwd, []string{"zsh", "-l"}
	}
	if dir := s.floxDir(cwd); dir != "" {
		return cwd, append([]string{"flox", "activate", "--dir", dir, "--"}, argv...)
	}
	return cwd, slices.Clone(argv)
}
```

- [ ] **Step 3: Implement `Session.Run` as the only public entry path**

`Run` must perform:

```go
ctx, stopSignals := runtime.NotifyContext(context.Background())
defer stopSignals()
selected, err := credential.Resolve(s.Cfg, requestedCredentials)
acquired, err := s.CredentialManager.AcquireAll(ctx, s.HostHome, selected)
defer credential.RevokeAll(context.WithoutCancel(ctx), acquired)
err = s.UpContext(ctx)
err = credential.Scrub(ctx, s.RT, s.Name)
lease, err := credential.NewGuestLease(rand.Reader)
bundle, err := credential.BuildBundle(acquired, lease)
defer credential.Cleanup(context.WithoutCancel(ctx), s.RT, s.Name, lease)
err = credential.Stage(ctx, s.RT, s.Name, lease, bundle)
cwd, command := s.entryArgv(cwd, argv)
err = s.RT.ExecInteractive(ctx, s.Name, cwd, credential.Wrap(lease, command))
```

Implement the function with named-return cleanup so every failure path is explicit:

```go
func (s *Session) Run(cwd string, argv, requestedCredentials []string) (retErr error) {
	ctx, stopSignals := runtime.NotifyContext(context.Background())
	defer stopSignals()
	selected, err := credential.Resolve(s.Cfg, requestedCredentials)
	if err != nil {
		return err
	}
	acquired, err := s.CredentialManager.AcquireAll(ctx, s.HostHome, selected)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, credential.RevokeAll(context.WithoutCancel(ctx), acquired))
	}()
	if err := s.UpContext(ctx); err != nil {
		return err
	}
	if err := credential.Scrub(ctx, s.RT, s.Name); err != nil {
		return fmt.Errorf("scrub credential leases: %w", err)
	}
	for _, summary := range credential.Summaries(acquired) {
		fmt.Fprintf(os.Stderr, "coop: credential %s\n", summary)
	}
	workdir, command := s.entryArgv(cwd, argv)
	if len(acquired) == 0 {
		return operationError(ctx, s.RT.ExecInteractive(ctx, s.Name, workdir, command))
	}
	lease, err := credential.NewGuestLease(rand.Reader)
	if err != nil {
		return fmt.Errorf("create credential lease: %w", err)
	}
	bundle, err := credential.BuildBundle(acquired, lease)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, credential.Cleanup(context.WithoutCancel(ctx), s.RT, s.Name, lease))
	}()
	if err := credential.Stage(ctx, s.RT, s.Name, lease, bundle); err != nil {
		return err
	}
	return operationError(ctx, s.RT.ExecInteractive(ctx, s.Name, workdir, credential.Wrap(lease, command)))
}
```

Pass the lifecycle context through `UpContext` into seed guest transports, and check `context.Cause(ctx)` after every remaining non-context-aware lifecycle operation (`Resolve`, bounded runtime setup, scrub, and bundle construction), joining it with any operation error. Because `errors.Join` preserves `errors.As`, a joined `runtime.ExitError` or `runtime.SignalError` still supplies the correct status to Task 7. Do not shadow the named result in either deferred cleanup.

Add `CredentialManager *credential.Manager` to `Session`, initialize it in `New`, and initialize it in `testSession`.

- [ ] **Step 4: Remove direct external use of `Session.Enter`**

Make raw entry unexported or remove it after root and TUI migrate in Task 7. Until then, keep a short compatibility wrapper only inside the session package tests; no CLI path may bypass `Run` once Task 7 lands.

- [ ] **Step 5: Run session tests**

Run: `gofmt -w internal/session/entry.go internal/session/session.go internal/session/session_test.go`

Run: `go test ./internal/session -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add internal/session
git commit -m "feat: orchestrate credential-aware entries"
```

### Task 7: CLI selection, TUI propagation, and exit status

**Files:**
- Modify: `cmd/coop/main.go:57-243`
- Modify: `cmd/coop/main_test.go`

**Interfaces:**
- Consumes: `Session.Run` from Task 6 and `runtime.ExitError` from Task 5.
- Produces: public `--credentials` behavior and correct process exit codes.

- [ ] **Step 1: Write failing flag behavior tests**

Cover:

```go
func TestCredentialsFlagAcceptsCommaAndRepeat(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	old := runSession
	var got []string
	runSession = func(_ *session.Session, _ string, _ []string, credentials []string) error {
		got = append([]string(nil), credentials...)
		return nil
	}
	t.Cleanup(func() { runSession = old })

	cmd := root()
	cmd.SetArgs([]string{"--credentials", "aws-dev,github", "--credentials", "kubernetes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"aws-dev", "github", "kubernetes"}
	if !slices.Equal(got, want) {
		t.Fatalf("credentials = %v, want %v", got, want)
	}
}
```

Also assert the flag works for `tui`, is rejected for `up`, `down`, `status`, `ls`, `doctor`, `rebuild`, and `destroy`, and remains guest argv when it appears after the first positional token.

- [ ] **Step 2: Add the persistent plural list flag**

Bind a Cobra `StringSlice`:

```go
var requestedCredentials []string
rootCmd.PersistentFlags().StringSliceVar(
	&requestedCredentials,
	"credentials",
	nil,
	"include trusted credential grants for this entry (comma-separated, repeatable)",
)
rootCmd.PersistentFlags().SetInterspersed(false)
rootCmd.Flags().SetInterspersed(false)
```

Add this test seam beside `newRuntime` and `lookPath`, not inside `root`:

```go
var runSession = func(s *session.Session, cwd string, argv, credentials []string) error {
	return s.Run(cwd, argv, credentials)
}
```

Keep interspersed parsing disabled. Add a helper that rejects non-empty `requestedCredentials` for commands that cannot enter a guest.

- [ ] **Step 3: Replace duplicated `Up` + `Enter` calls**

The root and TUI paths must call:

```go
return s.Run(cwd, args, requestedCredentials)
```

and:

```go
return s.Run(res.EnterWorkdir, nil, requestedCredentials)
```

No CLI entry path may call `Up` then raw `Enter`, because that would acquire credentials too late or bypass them.

- [ ] **Step 4: Preserve typed exit status in `main`**

Extract a testable function:

```go
func execute(cmd *cobra.Command) int {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coop:", err)
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			return exitCoder.ExitCode()
		}
		return 1
	}
	return 0
}

func main() { os.Exit(execute(root())) }
```

Test exit code 0, infrastructure error 1, guest error 23, and `errors.Join(guestExit, cleanupError)` still returning 23.

- [ ] **Step 5: Run CLI tests**

Run: `gofmt -w cmd/coop/main.go cmd/coop/main_test.go`

Run: `go test ./cmd/coop -v`

Expected: PASS.

- [ ] **Step 6: Gated commit checkpoint**

Only with explicit commit approval:

```sh
git add cmd/coop/main.go cmd/coop/main_test.go
git commit -m "feat: select credentials per entry"
```

### Task 8: Doctor migration warnings, documentation, and full verification

**Files:**
- Modify: `internal/doctor/doctor.go:72-86`
- Modify: `internal/doctor/doctor_test.go`
- Modify: `README.md`
- Modify: `image/Containerfile:1-7`

**Interfaces:**
- Consumes: final configuration and CLI behavior from Tasks 1-7.
- Produces: migration guidance and release-ready verification evidence.

- [ ] **Step 1: Add failing sensitive-seed warning tests**

Test recognized paths without reading their contents:

```go
func TestSensitiveSeedPathsWarn(t *testing.T) {
	cfg := config.Default()
	cfg.Seeds = []config.Seed{
		{Src: "~/.git-credentials"},
		{Src: "~/.aws/credentials"},
		{Src: "~/.netrc"},
		{Src: "~/.kube", Policy: config.PolicyOverlay},
	}
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(cfg.Image): true}
	c := get(Run(m, cfg, "/Users/u", found), "credential seeds")
	if c == nil || c.Status != Warn || !strings.Contains(c.Detail, "4 sensitive") {
		t.Fatalf("warning = %+v", c)
	}
}
```

Add a negative test for skill/config seeds such as `~/.claude/skills` and `~/.config/opencode/opencode.jsonc`.

- [ ] **Step 2: Implement path-only warning classification**

Normalize the expanded path and match exact basenames/known suffixes. Add a separate `credential seeds` warning after the existing seed-source health check. Never open or inspect a suspected credential file.

- [ ] **Step 3: Update public documentation**

Add README sections with the exact approved examples for:

- `include_credentials = ["git"]`;
- file → Git credential-store;
- command → `GH_TOKEN`;
- file → `KUBECONFIG`;
- AWS profile with `require_expiration = true`;
- `coop --credentials aws-dev,github codex`;
- global-only trust behavior;
- exposure lifetime versus upstream validity;
- root guest and unrestricted-egress limitations; and
- migration from sensitive seeds.

Update the image header comment so it no longer claims all personal material enters through seeds.

- [ ] **Step 4: Run focused and full verification**

Run:

```sh
go fmt ./...
go test ./internal/config ./internal/credential ./internal/runtime ./internal/session ./internal/doctor ./cmd/coop
go test -race ./...
go vet ./...
git diff --check
```

Expected: every command exits 0; all tests report PASS; `git diff --check` prints nothing.

- [ ] **Step 5: Perform macOS manual verification**

With non-production test credentials, verify:

```sh
coop --credentials github -- sh -lc 'test -n "$GH_TOKEN"'
coop --credentials kubernetes -- kubectl config current-context
coop --credentials aws-dev -- aws sts get-caller-identity
coop --credentials aws-dev -- sh -lc 'exit 23'; test "$?" -eq 23
```

Then inspect the guest from a separate credential-free shell and confirm `/dev/shm/coop-credentials` contains no completed lease directories. Interrupt an entry with Ctrl-C and repeat the check. Do not print or inspect secret values.

- [ ] **Step 6: Review the complete diff and open findings**

Run:

```sh
git status --short
git diff --stat
git diff
roborev list --open
```

Expected: only planned files are changed; no unresolved roborev findings exist before any push.

- [ ] **Step 7: Final gated commit checkpoint**

Only with explicit commit approval, commit remaining documentation/test changes:

```sh
git add README.md image/Containerfile internal/doctor/doctor.go internal/doctor/doctor_test.go docs/superpowers/specs/2026-07-21-session-credentials-design.md docs/superpowers/plans/2026-07-21-session-credentials.md
git commit -m "docs: document session credentials"
```

Do not push or open a pull request without separate explicit instruction.
