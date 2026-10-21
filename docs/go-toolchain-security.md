# Go Toolchain Advisory Review

Reviewed on 2026-10-15 for [issue #39](https://github.com/sarcasticbird/coop/issues/39),
against production source at `04e6dff288153f07fd4c44be8cc6dbaec4b8439a`.
`govulncheck@v1.9.0 -show verbose ./...` on Linux arm64 with Go 1.26.6
reproduced nine symbol findings, two imported-package findings and two
module-only findings. All are Go standard-library advisories fixed in
**Go 1.26.9** on the supported 1.26 line. No third-party module finding
was reported. The module now requires 1.26.9; rebuild binaries to receive
the fixes. No scanner exclusions or vulnerability-check changes are needed.

## October 21 CI Follow-Up

PR #127 CI run [39524380602](https://github.com/sarcasticbird/coop/actions/runs/39524380602)
failed only its unchanged `govulncheck@v1.6.0 ./...` gate on source
`9cf583ba31abf418351a7c3f4964dae7f09d6789`. It reported
[GO-2026-6809](https://vuln.go.dev/ID/GO-2026-6809.json), distrust of compromised
intermediate certificates in `crypto/x509.Certificate.Verify`, with a reachable
trace from `internal/hostmcp/client.go`. The upstream advisory identifies Go
1.26.10 as the fix on the 1.26 line. The module now requires **Go 1.26.10**;
the older requirement above records the October 15 assessment, not the current
minimum. Existing CI/release workflows read `go.mod`; no workflow, scanner
exclusion, dependency or trust-store workaround is needed. Rebuilds are required
to receive the standard-library fix; this source repair does not deploy binaries.

The same scanner command on the author's Linux arm64 Go 1.26.9 environment
reported no vulnerabilities, including with a separate XDG cache. Thus the CI
symbol finding was not locally reproduced; the CI log and upstream advisory,
not a claim of local reproduction, establish the required toolchain repair.

Author checks on 2026-10-21 with Flox-selected Go 1.26.10 on Linux arm64 passed:
the unchanged CI scanner command; `go mod verify`; repository-wide build and
vet; scoped race tests for TUI, masterclient, coordination and hostmcp with the
existing documented GCC compiler; ordinary master and CLI tests; Darwin arm64
cross-build; and diff checks. No full-suite rerun or fresh CI pass is claimed
here. New-head CI, independent engineering review and required exact-artifact
runtime QA still precede delivery. No workflow or scanner configuration changed,
and no release or deployment is authorized by this repair.

The dispositions below are static applicability assessments, not exploit
results or reasons to keep an unpatched toolchain. Advisory links identify
the reviewed upstream records; scanner reachability alone does not prove
that Coop satisfies the exploit prerequisites.

| Advisory | Original result | Applicability and disposition |
| --- | --- | --- |
| [GO-2026-6617](https://vuln.go.dev/ID/GO-2026-6617.json) | Symbol | HTTP/2 server HPACK encoder race. No production Go HTTP server exists in Coop; broad client/interface traces do not establish server exposure. Fixed by the toolchain upgrade. |
| [GO-2026-6613](https://vuln.go.dev/ID/GO-2026-6613.json) | Symbol | HTTP/1 server desynchronization after a successful CONNECT response requires a server handler Coop does not have. Outbound proxy use is a different role. Fixed by the upgrade. |
| [GO-2026-6612](https://vuln.go.dev/ID/GO-2026-6612.json) | Symbol | Double flow-control refund affects HTTP/2 server request handling, not Coop's client response handling. No production server path found. Fixed by the upgrade. |
| [GO-2026-6611](https://vuln.go.dev/ID/GO-2026-6611.json) | Symbol | Repeated HTTP/2 initial-window changes can exhaust client CPU. Default-transport HTTPS clients can negotiate HTTP/2, so a malicious or compromised upstream is a relevant conditional risk. Upgrade required; no exploitation demonstrated. |
| [GO-2026-6610](https://vuln.go.dev/ID/GO-2026-6610.json) | Symbol | HTTP/2 client framing-header parser is reachable, but Coop consumes responses locally and does not forward them to HTTP/1 clients as a reverse proxy. The described response-smuggling prerequisite is absent. Upgrade removes the parser defect regardless. |
| [GO-2026-6608](https://vuln.go.dev/ID/GO-2026-6608.json) | Symbol | MIME-header memory-limit bypass requires the finite remaining multipart budget described in the advisory. Coop uses `http.ReadResponse`, not multipart parsing, and caps its in-memory input before parsing. Shared `ReadMIMEHeader` reachability is not that budget-bypass path. Fixed by the upgrade. |
| [GO-2026-6607](https://vuln.go.dev/ID/GO-2026-6607.json) | Symbol | Malformed ECH references affect TLS server inner ClientHello decoding. HTTPS client handshakes alone do not satisfy this prerequisite; no TLS/ECH server is configured. Fixed by the upgrade. |
| [GO-2026-6605](https://vuln.go.dev/ID/GO-2026-6605.json) | Symbol | HTTP/1 CONNECT rejection desynchronization requires a nonempty CONNECT request body and connection reuse. Coop constructs no CONNECT requests. Default HTTPS proxy tunneling sends a bodyless CONNECT; application POST bodies follow tunnel establishment. No matching path found; fixed by the upgrade. |
| [GO-2026-6603](https://vuln.go.dev/ID/GO-2026-6603.json) | Symbol | Trailer memory exhaustion requires an HTTP/2 server receiving requests. No production server path found. Fixed by the upgrade. |
| [GO-2026-6609](https://vuln.go.dev/ID/GO-2026-6609.json) | Package | Range-header parsing in file serving. No production `ServeContent`, `ServeFile`, `FileServer`, or file transport use found; no vulnerable call reported. Fixed by the upgrade. |
| [GO-2026-6604](https://vuln.go.dev/ID/GO-2026-6604.json) | Package | Windows `os.Root.Mkdir(All)` junction escape. Supported hosts/guests are Darwin/Linux, and Coop does not use `os.Root`; ordinary `os.MkdirAll` is not the affected API. Fixed by the upgrade. |
| [GO-2026-6600](https://vuln.go.dev/ID/GO-2026-6600.json) | Module | JavaScript `yield` context in `html/template`. No `html/template` import or execution found; no vulnerable call reported. Fixed by the upgrade. |
| [GO-2026-6599](https://vuln.go.dev/ID/GO-2026-6599.json) | Module | Consecutive JavaScript expressions in `html/template`. No `html/template` import or execution found; no vulnerable call reported. Fixed by the upgrade. |

## Source Evidence

- `internal/githubapp/mint.go` and `internal/releasetool/releasetool.go`
  use timeout-only `http.Client` instances with the default transport for
  GitHub HTTPS requests. That transport supports HTTP/2 and environment
  proxies. Release asset destinations and redirects are restricted to
  allowed GitHub HTTPS hosts, but this is not a substitute for parser fixes.
- `internal/worker/opencode.go` restricts its API to
  `http://127.0.0.1:4096` and uses a custom transport with no proxy and no
  unencrypted HTTP/2 opt-in. The launched OpenCode server is an external
  program, not a Coop Go HTTP server. Test-only HTTP servers do not establish
  production server exposure.
- `internal/worker/github.go` caps captured `gh api --include` output at
  2 MiB + 64 KiB before `http.ReadResponse` parses a `bytes.Reader`.
  The cap is after subprocess capture, not a streaming subprocess-memory
  limit. This is not multipart parsing with a depleted finite header budget.

## Validation

Use the patched toolchain through the supported Flox environment:

```sh
flox activate -- go version
flox activate -- go mod verify
flox activate -- go build ./...
flox activate -- go test -race ./...
flox activate -- go vet ./...
flox activate -- go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
flox activate -- go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -show verbose ./...
flox activate -- env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
```

The first scanner command matches the unchanged CI/release gate; the second
matches the audit tool version and exposes package/module-only findings too.
Cross-compilation checks build compatibility, not execution on macOS or the
real Apple container runtime. Release hardware validation remains required.
