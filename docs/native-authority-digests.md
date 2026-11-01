# Native Authority Digests

`coop-worker native authority-digests` performs local, read-only calculations.
It never contacts the master or GitHub, writes files, grants authority or publishes
anything. Its flags are exactly `--input`, `--archive`, and `--payload-schema`.
At least one of input/archive is required; an archive requires a supported schema.

Hash a private native `HandoffInput` JSON file, not a whole `Handoff` envelope:

```sh
coop-worker native authority-digests --input /private/source-delivery-input.json
```

The input must satisfy the same private-file protection as worker configuration
and is strictly decoded using the actual comparable native schema. Unknown,
duplicate or trailing JSON is rejected. Semantic handoff validation is deliberately
not required because report fields can still be pending. The result's
`input_sha256` hashes native JSON serialization, including default value fields;
it matches the production source/version delivery-input proof calculation.
Changing pending fields changes the hash; calculate again on the exact final input.

Calculate both canonical source-bundle and version-normalized payload digests:

```sh
coop-worker native authority-digests --archive /private/source.tar.gz --payload-schema gh-sweep-version-v1
```

Or calculate all three digests together:

```sh
coop-worker native authority-digests --input /private/source-delivery-input.json --archive /private/source.tar.gz --payload-schema gh-sweep-version-v1
```

The archive is bounded and structurally validated, never extracted or executed.
Archive results are `payload_sha256` and `source_bundle_sha256`. Archive-only
input may be public; the JSON input must be private. `coop-go-version-v1` remains
supported with the same flags for Coop's fixed Go source contract.

## gh-sweep-version-v1

The fixed version source is the regular executable `gh-sweep` in the archive root.
Actual PR9 at head `6a3efb78904ea8e1ff47af2d85b4e772eec190c0` starts with:

```bash
#!/usr/bin/env bash
set -euo pipefail

VERSION="0.4.0"
```

The parser requires that exact header shape and a strict numeric three-component
version literal. It rejects any other `VERSION` assignment token anywhere in the
executable, conservatively including commented, quoted, heredoc and function
assignments. No preceding code or shell scope can hide the header assignment.

Only the literal's bytes are normalized for the payload digest. Every other
file byte, path, mode and link target, including the rest of `gh-sweep`,
`lib/sweep.sh`, `lib/runs.jq`, scripts, tests and documentation, stays in the
canonical digest. There are no caller-selected exclusions. During native target
verification the version literal must equal the approved tag with its single
leading `v` removed, e.g. `v0.5.0` requires `VERSION="0.5.0"`.

Digest calculation alone does not check an approved tag or claim bundle
equivalence: production still requires exact source/version provenance, full
reviewed archive digests, commit/tree/parent/ancestry checks and equality across
all four source/integration archives before any tag effect.
