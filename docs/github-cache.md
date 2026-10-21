# Shared GitHub observations

Coop can store registered GitHub task and PR observations in its existing
master SQLite database. Workers on different machines use the same master.
Another worker host does not create another database. Independent masters have
independent caches; there is no cross-master replication.

The feature defaults to off. It adds no service, credentials, GitHub App or
webhook receiver. The designated worker collects through her existing App.
Other explicitly authorized readers reuse those observations. Collection is
background work and does not prompt a model or own an engineering slot.

## Private configuration

Add modes and routes to the existing private master configuration. Names and
numeric IDs below are placeholders, not usable registrations.

```toml
[github_cache.modes]
example = "shadow"

[[github_cache.routes]]
id = "example-code"
engagement = "example"
collector = "author"
host = "github.com"
app_id = 123
installation_id = 456
repositories = ["example/project"]
threads = ["th_example"]
readers = ["owner", "author", "peer", "qa", "coordinator"]
```

Principals and threads must exist. Cache reads require current engagement and
goal-thread membership plus an exact repository reader grant. An engagement
alone grants no repository content. App/installation identity is pinned;
repository transfers and identity changes require explicit reconciliation.

Opt participating v3 workers in with this field in their private JSON config:

```json
"github_cache": {"enabled": true, "collect_routes": ["example-code"]}
```

Only the designated collector lists routes. Other engineering workers enable
the cache with no collection routes. Coordinators can read status through their
normal master clients. Existing configurations preserve existing behavior.

| Master mode | Collection | Discovery |
| --- | --- | --- |
| `off` | Disabled | Existing live GitHub catalog |
| `shadow` | Enabled | Live catalog with cached candidate comparison |
| `on` | Enabled | Master observations only |

Enabled workers reject an incompatible or unavailable cache API. They never
silently switch masters or fall back to a full scan. Private configuration
changes follow existing service installation/restart procedures.

## Freshness and actions

Only registered work is indexed. Active headers and pending review data refresh
every minute; other detail and Project data refresh every five minutes.
Referenced terminal headers refresh every 15 minutes. Unchanged closed PRs avoid
repeated deep review/archive reads. Exact-head source verification is reusable
only with the same repository, digest and verifier format. Archives are not
stored in the database.

Rounds have a five-minute deadline and a 256-request limit. Master leases permit
one background round per route, with at least one second between starts.
Collection yields to native action preparation and honors quota backoff. Offline
observations become stale; another agent's credentials are never selected
automatically.

Missing data holds affected tasks while unrelated ready work proceeds. Unknown
registered tasks cannot trigger duplicate planning. Expiry is checked again at
selection. Clients may memoize immutable payload bytes within a 16 MiB memory
bound; they do not create another persistent cache or independent sync process.

Before checkout mutation, model submission and consequential writes, Coop still
checks current master authority and reads GitHub using the acting bird's own
App. This includes planner admission. Cache data and model notes grant no work,
review, QA or merge authority. Existing peer/QA gates remain unchanged.

Publication verifies lease, policy, placement, epoch, dirty generation and
header revision in one transaction. Failed/partial reads preserve prior complete
data without granting freshness. Conditional comment reads require complete
retained bodies. Paginated collections do not use collection validators; 304s
never renew the 30-minute unconditional-read deadline. GraphQL stays unconditional.
Repeated consistency scans within a round still reach GitHub independently;
retained responses never replace the native readers' second verification scan.
Freshness starts at verification, so slow hydration does not publish an already
expired header. Reused Project observations retain their earlier timestamps;
publication rejects future timestamps. Worker and master clocks must be in sync,
as required by the existing lease deadlines.

Project readiness first uses scoped issue memberships. For cross-owner Projects
that omit those memberships, a bounded board lookup reads only item identifiers
and issue URLs. Only registered tasks receive field-value reads. Unreadable or
opaque possible memberships remain unknown. Ordering fetches bounded item
identifiers and memoizes same-repository ranks in the collector. Unrelated board
identifiers and bodies never enter shared payloads. Verified absence from another
Project is valid. Completed dependencies do not require active Project membership.

Identical artifact registrations preserve freshness and in-flight refreshes.
Changed registrations fence old manifests and dirty only their task and PRs;
authority changes still invalidate affected access. Age-priority refresh turns
retain the last observation time when an invalidation clears its deadline.

## Inspection and recovery

```sh
coop cache status --engagement example
```

The read-only MCP tool is `coop_master_github_cache_status`, with an `engagement`
argument. Status reports scope, freshness, failure categories and retry times;
it contains no provider bodies, makes no GitHub requests and wakes no agents.
Team diagnostics distinguish a fresh known author-refresh wait from an admission
stall. Failed cache hints after successful writes never replay those writes;
polling reconciles missed hints. Preserve native journals for uncertain effects.

Use supported master backup/restore commands. A restored master stays quiescent
under its new epoch until reconciliation. Old leases cannot publish and old
mutable observations cannot admit work. Keep additive tables during feature-off
rollback. Do not replace a live database with an older copy to clear the cache.

Payload/staging limits are 512 MiB total, 16 MiB per observation and 10,000 indexed
objects. Chunks fit the existing 256 KiB request limit. Expired staging and
eligible 30-day terminal history are pruned; live dependency facts remain.
Capacity failures are explicit and do not launch an uncontrolled provider scan.

The sync algorithms were informed by
[Forge](https://github.com/kenn-io/forge/tree/410bb174ace7a9c51d3690960c2222bedb7b6209).
Coop does not depend on its UI, daemon or GitHub shim.
