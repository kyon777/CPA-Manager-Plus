# Rate-Only Pricing Revision Design

**Status:** Approved by LO on 2026-10-31; implementation plan pending written-spec review.
**Scope:** CPA Manager Plus (CPAM) only.
**Primary outcome:** A change to an existing model's monetary rates updates CPAM's account-level and aggregate displayed costs without replaying archived or live usage events.

## Problem and evidence

The separate derived SQLite design correctly moved materialized usage writes out
of `usage.sqlite`, but its pricing stream still uses
`model.ModelPriceValueRevision(...)` as its generation identity. That value
fingerprint changes for every monetary edit. A cache-read price change such as
the `gpt-6-luna` update therefore gives the pricing stream a new generation and
forces it to scan every retained archive event plus every online event.

On the affected server, the source watermark is approximately 4.086 million
events: roughly 3.149 million online events plus roughly 0.938 million archived
event references. The archive source is deliberately included because the
derived engine requires a contiguous event-ID proof. Removing archive reads or
deleting archive metadata would make the coverage proof incomplete and is not a
valid optimization.

The actual pricing materialization already stores token counters rather than
frozen currency values. Every pricing/account aggregate is keyed by:

```text
account + model/billing model + service tier + context-threshold band
  + input/output/reasoning/cache/cache-read/cache-creation token counters
```

`service/monitoring` applies the current `model_prices` table to those counters
when it builds account-history totals. Thus a rate-only edit needs an
invalidation marker for cached money, not a raw-event rebuild.

## Goals

1. Changing any already-defined monetary rate updates all CPAM price surfaces
   from the existing token aggregates without a historical event replay.
2. This includes base, context-tier, and explicit service-tier values for:
   `prompt`, `completion`, `cache`, `cache_read`, and `cache_creation`.
3. Account list rows, account detail/request-activity totals, dashboard/window
   totals, and other CPAM management pricing consumers must apply the current
   price book consistently.
4. A structural change that changes bucket membership still triggers a safe
   replay. No rate-only shortcut may silently price an event against the wrong
   context band.
5. The pricing worker continues to read the main `usage.sqlite` only through
   short read-only batches and writes only `usage-derived.sqlite`.
6. CPA Core, `cli-proxy-api`, `/home/cpa`, `cpa.service`, source usage facts,
   Credits, quotas, and downstream client billing are not changed.

## Non-goals

- Do not delete archive segments, archive references, or compacted history.
- Do not treat a rate update as a new raw-usage event or rewrite `usage_events`.
- Do not expose historical per-event frozen dollar values where the product
  currently exposes only account/window aggregates.
- Do not make browser requests synchronously launch a rebuild or take a write
  lock on the source database.
- Do not introduce a second currency ledger. The token buckets remain the
  canonical, rebuildable pricing input.

## Two independent revisions

The implementation makes the existing conceptual split explicit everywhere in
the separate-derived path.

| Revision | Contents | Changes on | Effect |
| --- | --- | --- | --- |
| `pricing_structure_revision` | priced model-ID set and sorted context-tier threshold values | model added/removed; a threshold added/removed/changed | New pricing generation; archive+online replay is required. |
| `pricing_value_revision` | all effective price values and configuration flags, including base/context/service-tier rates | any monetary/configuration rate edit | Keep generation and coverage. Invalidate only money caches / status metadata. |

`model.ModelPriceStructureRevision` and `model.ModelPriceValueRevision` already
encode these definitions. The defect is that the separate-derived source and
engine currently use the latter for both jobs.

Service-tier rules are intentionally **value**, not structure. Pricing rows
already preserve `service_tier`; changing or adding an explicit service-tier
override can be evaluated from the same counters. Context thresholds are
structure because they decide which rollup bucket receives an event.

## Data model and invariants

### Source snapshot

Replace the ambiguous singular `PricingRevision` in the separate-derived
contracts with both values:

```go
type SourceSnapshot struct {
    Identity                 SourceIdentity
    TargetEventID            int64
    ArchiveGeneration        string
    PricingStructureRevision string
    PricingValueRevision     string
}
```

The main-database source reads the price book once per snapshot attempt and
derives both hashes from that same price map. It must retry if the price book
changes between snapshot and model-price read, so a snapshot never pairs one
revision with rates from another revision.

### Derived stream state

Extend the Go state/apply contracts with both explicit names, while adding only
the missing structural database column:

```text
pricing_structure_revision  TEXT NOT NULL DEFAULT ''
```

The existing `pricing_revision` column already contains the old value hash. In
Go it is renamed semantically to `PricingValueRevision` and remains the stored
value-revision field, rather than duplicating 4-million-event state with a
second value column. The new `pricing_structure_revision` stores the separate
structure hash. This is an additive migration and preserves the deployed
legacy marker for safe promotion.

The same additive migration records `last_transition TEXT NOT NULL DEFAULT ''`
on the stream state. It is metadata only and records `rate_only_refresh`,
`structural_rebuild`, or `legacy_promotion`; it never participates in coverage
or generation selection.

For the pricing stream only:

- `generation` contains the source epoch, Codex identity revision, and
  `pricing_structure_revision`.
- `pricing_revision` (exposed as `pricing_value_revision` in Go/status) records
  which price book was most recently used to label/refresh any optional
  currency cache.
- Coverage, target, archive generation, and source epoch retain their current
  meanings and remain independent of rate values.

For non-pricing streams, both new fields are harmless empty metadata and their
generation semantics do not change.

### Pricing calculation invariant

For every aggregate, monetary output is calculated as:

```text
current model price book
  times aggregate's model/service-tier/context-band token counters
  = current displayed CPAM cost
```

There is no dependency on the historical rate that existed when an event was
first materialized. This is deliberately the same behavior already used by the
normal cost calculator. It means a rate edit changes account-level totals,
dashboard totals, and visible-page totals immediately after their normal read
path runs; it does not only alter one grand total.

## Runtime behavior

### Rate-only change

When `PricingStructureRevision` matches the active pricing state but
`PricingValueRevision` differs:

1. Keep the existing generation, `coverage_event_id`, `processed_events`, and
   all pricing materialized token rows.
2. Update the state metadata to the new value revision in a short transaction.
3. Invalidate only caches that store a computed currency number keyed by the
   old value revision. Reads recompute from token rollups with the current
   price map or populate a new value-revision cache lazily.
4. Continue normal incremental handling only for events newer than coverage.
   No call may begin at event ID 0 merely because a price changed.
5. Report the stream as coverage-ready when its normal epoch/generation/archive
   proof is ready. A value-revision update is not a `rebuilding` state and must
   not cause list rows to show `syncing` or blank amounts.

This is an O(number of visible/currently-read aggregate rows) price evaluation,
not O(all raw events). Existing cached rows may be refreshed lazily because
their cache key includes `pricing_value_revision`; no broad delete or scan is
needed.

### Structural change

When model membership or a context-tier threshold changes:

1. Create a new pricing generation using the new structure revision.
2. Reset only the pricing stream's materialization/coverage in
   `usage-derived.sqlite` transactionally.
3. Rebuild from contiguous archive-plus-online source events in normal bounded
   batches.
4. Continue serving only coverage-proven data. Do not present an old context
   band as current-rate-accurate.

This is the intentionally expensive path. It is required because a threshold
edit can reclassify an old event into a different token bucket.

### Read-path behavior

`account-history`, `account-window-usage`, dashboard, and price views retain
their existing current-page/tail policy. They obtain price rates from the
current source price map and apply those rates to the existing pricing
aggregates. They do not wait for an archive replay merely because a monetary
value changed.

The visible page can therefore show request count, tokens, and exact current
price while the worker only catches up the genuine new event tail. A failed
price-cache lookup is a cache miss, not a reason to fall back to full raw
history scans.

## Safe legacy transition

Deployed separate-derived databases use a value-based pricing generation. The
transition cannot blindly relabel any old generation after a rate change,
because the old state does not retain proof of which context-tier structure was
in effect when it was built.

The migration has two safe cases:

1. **Current legacy generation's stored value revision equals the current
   value revision.** Equality proves the complete model/threshold/rate book
   represented by its existing rows, even when the worker is still part-way
   through that generation. In one derived-DB transaction, rekey its pricing
   generation to the new structure-based generation and fill both explicit
   revision fields while preserving its committed coverage checkpoint. No raw
   or archive event is read merely to perform that promotion.
2. **The legacy state stores a different value revision.** Preserve the
   in-progress data only as an unread legacy generation and begin one safe
   structure-generation rebuild from event ID 0. The implementation must never
   assert that an unknown old context-bucket layout is compatible just to save
   work. This one-time path is avoided for the deployed in-progress current
   value-generation because case 1 preserves its checkpoint directly.

After promotion, later rate changes of any of the supported price components
are metadata/cache changes only. A server restart does not reset either
coverage or the promoted generation because both remain in
`usage-derived.sqlite`.

## Observability

`database-maintenance` status will expose, for the pricing stream only:

```text
pricing_structure_revision
pricing_value_revision
generation
coverage_event_id
target_event_id
lag
status
last transition = rate_only_refresh | structural_rebuild | legacy_promotion
```

The UI/API should use actual main-source target watermark for `lag`; it must
not report `lag=0` merely because revisions match. No token, credential JSON,
proxy URL, request body, or archive path is returned.

Logs will record a short transition reason and counts (not account identities)
so an operator can distinguish a cheap rate refresh from a deliberate
structural rebuild.

## Test matrix

Focused tests must prove the following with synthetic live and archived events:

1. After complete pricing coverage, changing each of `prompt`, `completion`,
   `cache`, `cache_read`, and `cache_creation` changes computed account cost,
   preserves generation/coverage/processed-event count, and performs no
   `EventsAfter` or `ArchivedEventsAfter` replay.
2. The same holds for a context-tier rate update with the same threshold and
   for an explicit service-tier rule/value update.
3. Account detail and visible-page history calculate the exact new amount from
   the same aggregate counters; they do not show `syncing` solely because the
   value revision differs.
4. Adding/removing a model or changing a context threshold changes the
   structure revision, creates a new generation, and requires replay.
5. Legacy promotion is atomic and idempotent when its stored value revision
   equals the current price book; it does not read raw/archive events.
6. A mismatched or incomplete legacy state is not promoted falsely; it follows
   the one-time verified baseline route.
7. State/status output reports separate structure/value revisions and the real
   source target watermark.

## Deployment and rollback

1. Build and test only from the clean isolated CPAM worktree.
2. Before deployment, record local CPA Core container identity and server
   `cpa.service` identity. They must be unchanged afterward.
3. Preserve `usage.sqlite`, `usage-derived.sqlite`, WAL/SHM files,
   `data.key`, archives, CPAM environment, and secrets. The migration is
   additive and runs only against CPAM's derived database.
4. Deploy the same verified CPAM candidate to the local
   `cpa-manager-plus` Docker service and then to `/home/cpam` on the server;
   restart only `cpam.service` on the server.
5. Confirm post-deploy that a rate-only edit records `rate_only_refresh`, keeps
   pricing coverage stable, and changes a known account's displayed cost.
6. If rollback is required, restore only the prior CPAM release and restart
   `cpam.service`; do not discard the derived database. The added columns are
   backward-compatible and no usage/billing facts were modified.
