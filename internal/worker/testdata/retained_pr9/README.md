# Retained PR9 Test Inputs

Only the public immutable source archive and this document are distributed in
this directory. Original private QA prose, task records, goal records, and
encoded API captures must not be committed or published here. The fixture
allowlist test rejects additional files. There is no live capture helper.

## Public Coverage

`source.tar.gz` is the actual public GitHub archive for `sarcasticbird/gh-sweep`
at `6a3efb78904ea8e1ff47af2d85b4e772eec190c0`, including its original license.
The default offline suite recomputes `source-bundle-v1` from the real paths,
modes, link targets, and contents and requires digest
`9e4e061ebafbf3d2f94c9d46561dd55f00ad9dc902665a8dc850b616c9fda4c0`.
It does not use a reduced bundle or override hashing.

The public archive was audited against the externally preserved original
records without printing private contents. All 18 files had zero matches for
the complete private report, task body, or goal scope, and zero matches for
private-report lines of at least 160 bytes. The 15 decoded GitHub response
records in the private capture were also checked; no encoded response records
are distributed, regardless of their public origin.

The existing generic retained production-path regressions remain enabled by
default. They use harmless synthetic task, goal, and QA evidence and cover
discovery, preparation, renewed gates, delivery, recovery, drift, revocation,
and cache behavior. They are not represented as exact original PR9 evidence.
The synthetic master dependency regression also covers same-revision successor
creation and cross-revision refusal without requiring private input.

## Private Exact Coverage

The exact-original PR9 master/worker/CLI journey and incremental-task tests
require `COOP_PR9_PRIVATE_FIXTURE` to name an absolute path to the complete
original capture outside the repository. The file must be private and owned
by the user running the tests. No private path is assumed automatically.

The external input contains `Task`, `Authority`, `SourceReport`, `PR`, and
`Reads` records matching the test's typed schema. Preserve original bodies,
authors, identifiers, source policy, and digests exactly. Do not supply a
synthetic report under the original report ID/hash, rewrite original QA
metadata, or substitute a new task/goal and claim exact provenance.

With private input supplied, the tests verify the original assignment
`49d021b8e3bbb8ed034ff3abc8a90d066b7ada6098c64a2f8ef01cfa022f1384`, original
task-scope and source-policy hashes, proposal/acceptance hashes, actual source
archive digest, and original private report body digest. A disposable real
master HTTP service and production worker/CLI helpers then exercise separate
authenticated renewed gates, exact delivery, one original-task completion,
recovery without merge replay, refusal cases, same-revision successor creation,
and cross-revision dependency refusal. GitHub, checkout/model transport, and
provider effects are simulated; no live delivery or QA clearance occurs.

Without the external private input, these exact-original journey tests skip
with an explicit reason. Default CI therefore proves public archive integrity
and generic retained integration, not the exact private-evidence PR9 journey.
Failure diagnostics must not dump private records, candidate goals, reports,
or CLI task output. Keep private captures outside source-control and published
test artifacts.
